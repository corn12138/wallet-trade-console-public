package bridge

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/ethtx"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// selFulfill is fulfill(bytes32,address,address,uint256,uint256).
const selFulfill = "0x7b4c4595"

// ChainClient is everything the relayer needs from one chain. *rpc.Client
// satisfies it; tests supply a fake.
type ChainClient interface {
	EthCall(ctx context.Context, to, dataHex, block string) ([]byte, error)
	BlockNumber(ctx context.Context) (*big.Int, error)
	GetLogs(ctx context.Context, filter rpc.LogFilter) ([]rpc.Log, error)
	GetTransactionCount(ctx context.Context, address, block string) (*big.Int, error)
	GetGasPrice(ctx context.Context) (*big.Int, error)
	EstimateGas(ctx context.Context, from, to, value, dataHex string) (*big.Int, error)
	SendRawTransaction(ctx context.Context, rawHex string) (string, error)
	ChainID(ctx context.Context) (*big.Int, error)
	// GetBalance funds the heartbeat's gas report: a relayer that runs out of
	// gas keeps looping and delivers nothing, which looks like health.
	GetBalance(ctx context.Context, address, block string) (*big.Int, error)
}

var _ ChainClient = (*rpc.Client)(nil)

// RelayerConfig tunes the scan/deliver loops.
type RelayerConfig struct {
	// ConfirmationDepth keeps the scanner behind the head so a reorg cannot
	// promote a deposit that never finalized.
	ConfirmationDepth uint64
	// MaxBlockRange bounds a single eth_getLogs window.
	MaxBlockRange uint64
	// StartBlocks seeds the first scan per chain when the store is empty, so a
	// fresh relayer does not walk the chain from genesis.
	StartBlocks map[int]uint64
	// GasLimitPadPercent pads the estimate; 0 means use the estimate as-is.
	GasLimitPadPercent uint64
	// GasPricePadPercent pads eth_gasPrice. The base fee can rise between the
	// read and the broadcast — on Arbitrum Sepolia a 0.3% move was enough to
	// get a fulfil rejected with "max fee per gas less than block base fee".
	// The gas LIMIT was already padded; the price was not, and a relayer that
	// bids exactly the last observed price loses that race intermittently on
	// any chain with a moving base fee. 0 means bid the observed price.
	GasPricePadPercent uint64
	// DeliverBatch bounds how many pending transfers one cycle attempts.
	DeliverBatch int
}

// DefaultRelayerConfig is conservative: 3 confirmations and 2k-block windows.
func DefaultRelayerConfig() RelayerConfig {
	return RelayerConfig{
		ConfirmationDepth:  3,
		MaxBlockRange:      2_000,
		StartBlocks:        map[int]uint64{},
		GasLimitPadPercent: 25,
		GasPricePadPercent: 25,
		DeliverBatch:       20,
	}
}

// Relayer observes gateway events and delivers pending transfers.
//
// The one invariant that shapes everything here: a transfer's status is only
// ever advanced by an OBSERVED on-chain event. Sending a fulfil transaction
// does NOT mark the transfer fulfilled — the delivery is recorded when the
// BridgeFulfilled log is later scanned. A relayer that wrote its own optimistic
// success would be reinventing the fake progress bar this package replaced.
type Relayer struct {
	registry *Registry
	store    *Store
	signer   *ethtx.Signer
	clients  map[int]ChainClient
	cfg      RelayerConfig
	log      *slog.Logger

	// cursors tracks the last scanned block per chain within this process.
	cursors map[int]uint64
}

// NewRelayer builds a relayer. A nil signer makes it observe-only: it will keep
// the transfer projection current but never deliver, which is a legitimate
// read-only deployment.
func NewRelayer(registry *Registry, store *Store, signer *ethtx.Signer, clients map[int]ChainClient, cfg RelayerConfig, log *slog.Logger) *Relayer {
	if log == nil {
		log = slog.Default()
	}
	if cfg.MaxBlockRange == 0 {
		cfg.MaxBlockRange = 2_000
	}
	if cfg.DeliverBatch <= 0 {
		cfg.DeliverBatch = 20
	}
	return &Relayer{
		registry: registry, store: store, signer: signer,
		clients: clients, cfg: cfg, log: log, cursors: map[int]uint64{},
	}
}

// CanDeliver reports whether a signer is configured.
func (r *Relayer) CanDeliver() bool { return r.signer != nil }

// RelayerAddress returns the delivering address, or "" when observe-only.
func (r *Relayer) RelayerAddress() string {
	if r.signer == nil {
		return ""
	}
	return r.signer.Address()
}

// RunOnce performs one full cycle: scan every chain, then deliver what is
// pending. Exported so a test and a one-shot CLI can drive a single pass.
func (r *Relayer) RunOnce(ctx context.Context) error {
	var firstErr error
	for _, chainID := range r.registry.Chains() {
		err := r.ScanChain(ctx, chainID)
		if err == nil {
			continue
		}
		// A chain in the registry with no RPC configured is a deployment fact,
		// not a transient fault: it is known at startup and will not change
		// within the process. Letting it set firstErr would mark every cycle
		// failed forever, and in a one-shot/cron deployment that turns "one
		// chain is unconfigured" into "the relayer is broken" — hiding real
		// failures behind a permanent one. Still logged every cycle, because
		// deposits on that chain genuinely are not being observed.
		if errors.Is(err, ErrNoRPC) {
			r.log.Warn("bridge relayer: chain not scanned (no RPC configured; deposits there are NOT observed)",
				"chainId", chainID, "env", fmt.Sprintf("BRIDGE_RPC_URL_%d", chainID))
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
		r.log.Warn("bridge relayer: scan failed", "chainId", chainID, "err", err)
	}
	if r.CanDeliver() {
		if err := r.DeliverPending(ctx); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			r.log.Warn("bridge relayer: deliver failed", "err", err)
		}
	}
	r.writeHeartbeat(ctx, firstErr)
	return firstErr
}

// writeHeartbeat records that a cycle ran. Without it a stopped relayer and a
// healthy idle one are indistinguishable — both leave bridge_transfers
// unchanged — so a bridge can go silently deposit-only.
//
// It records the cycle's error too, because "up but failing every 15 seconds"
// is the failure mode that actually happened in production (a missing table)
// and it looked identical to health from the outside.
//
// Best-effort by design: a heartbeat problem must never be the reason a
// transfer isn't delivered.
func (r *Relayer) writeHeartbeat(ctx context.Context, cycleErr error) {
	if r.store == nil {
		return
	}
	hb := RelayerHeartbeat{
		RelayerAddress: r.RelayerAddress(),
		LastSeenAt:     time.Now().UTC(),
		Chains:         r.registry.Chains(),
		CanDeliver:     r.CanDeliver(),
		GasBalances:    r.gasBalances(ctx),
	}
	if cycleErr != nil {
		hb.LastCycleError = cycleErr.Error()
	}
	if err := r.store.WriteHeartbeat(ctx, hb); err != nil && !errors.Is(err, ErrStoreUnavailable) {
		r.log.Warn("bridge relayer: heartbeat write failed", "err", err)
	}
}

// gasBalances reports the relayer's native balance per chain it could deliver
// on. A relayer that has run out of gas keeps looping and delivers nothing,
// which from the outside looks exactly like health.
func (r *Relayer) gasBalances(ctx context.Context) map[string]string {
	addr := r.RelayerAddress()
	if addr == "" {
		return nil
	}
	out := map[string]string{}
	for chainID, client := range r.clients {
		if client == nil {
			continue
		}
		bal, err := client.GetBalance(ctx, addr, "latest")
		if err != nil || bal == nil {
			// Unreadable is not zero: omit the chain rather than report a
			// balance we did not observe.
			continue
		}
		out[strconv.Itoa(chainID)] = bal.String()
	}
	return out
}

// Run loops until ctx is cancelled.
func (r *Relayer) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	r.log.Info("bridge relayer: starting",
		"chains", r.registry.Chains(),
		"deliver", r.CanDeliver(),
		"relayer", r.RelayerAddress(),
		"intervalMs", interval.Milliseconds(),
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
			r.log.Warn("bridge relayer: cycle had errors", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// ScanChain reads new gateway logs for one chain and projects them.
func (r *Relayer) ScanChain(ctx context.Context, chainID int) error {
	gw, ok := r.registry.Gateway(chainID)
	if !ok {
		return nil
	}
	client, ok := r.clients[chainID]
	if !ok || client == nil {
		return fmt.Errorf("%w: chain %d", ErrNoRPC, chainID)
	}

	head, err := client.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("bridge relayer: head: %w", err)
	}
	if !head.IsUint64() {
		return errors.New("bridge relayer: implausible head block")
	}
	safeHead := head.Uint64()
	if safeHead <= r.cfg.ConfirmationDepth {
		return nil
	}
	safeHead -= r.cfg.ConfirmationDepth

	from, err := r.resumeFrom(ctx, chainID)
	if err != nil {
		return err
	}
	if from > safeHead {
		return nil
	}
	to := min(from+r.cfg.MaxBlockRange-1, safeHead)

	logs, err := client.GetLogs(ctx, rpc.LogFilter{
		FromBlock: from,
		ToBlock:   to,
		Address:   gw.Address,
		Topics:    nil, // all three gateway events; filtered by topic0 below
	})
	if err != nil {
		return fmt.Errorf("bridge relayer: getLogs [%d,%d]: %w", from, to, err)
	}

	for _, lg := range logs {
		if err := r.handleLog(ctx, chainID, lg); err != nil {
			// One bad log must not stall the cursor for the whole range —
			// but it also must not be silently dropped.
			r.log.Warn("bridge relayer: log handling failed",
				"chainId", chainID, "tx", lg.TxHash, "err", err)
		}
	}

	r.cursors[chainID] = to + 1
	if len(logs) > 0 {
		r.log.Info("bridge relayer: scanned", "chainId", chainID, "from", from, "to", to, "logs", len(logs))
	}
	return nil
}

// resumeFrom picks the next block to scan: the in-process cursor, else one past
// the highest deposit already stored, else the configured start block.
func (r *Relayer) resumeFrom(ctx context.Context, chainID int) (uint64, error) {
	if c, ok := r.cursors[chainID]; ok {
		return c, nil
	}
	highest, err := r.store.HighestScannedBlock(ctx, chainID)
	if err != nil && !errors.Is(err, ErrStoreUnavailable) {
		return 0, err
	}
	if highest > 0 {
		return uint64(highest) + 1, nil
	}
	if start, ok := r.cfg.StartBlocks[chainID]; ok {
		return start, nil
	}
	return 0, nil
}

func (r *Relayer) handleLog(ctx context.Context, chainID int, lg rpc.Log) error {
	if len(lg.Topics) == 0 {
		return nil
	}
	switch strings.ToLower(lg.Topics[0]) {
	case TopicBridgeInitiated:
		return r.handleInitiated(ctx, chainID, lg)
	case TopicBridgeFulfilled:
		return r.handleFulfilled(ctx, lg)
	case TopicBridgeRefunded:
		return r.handleRefunded(ctx, chainID, lg)
	default:
		return nil
	}
}

// handleInitiated projects a deposit.
//
//	BridgeInitiated(bytes32 indexed transferId, address indexed sender,
//	  address indexed recipient, address srcToken, uint256 amount,
//	  uint256 srcChainId, uint256 dstChainId, uint256 depositNonce)
func (r *Relayer) handleInitiated(ctx context.Context, chainID int, lg rpc.Log) error {
	if len(lg.Topics) < 4 {
		return fmt.Errorf("bridge relayer: BridgeInitiated needs 4 topics, got %d", len(lg.Topics))
	}
	data, err := decodeData(lg.Data)
	if err != nil {
		return err
	}
	if len(data) < 5*32 {
		return fmt.Errorf("bridge relayer: BridgeInitiated data too short (%d bytes)", len(data))
	}

	srcToken, err := wordAddressAt(data, 0)
	if err != nil {
		return err
	}
	amount, err := rpc.DecodeUint256At(data, 32)
	if err != nil {
		return err
	}
	dstChainID, err := rpc.DecodeUint256At(data, 96)
	if err != nil {
		return err
	}
	dstChain, err := bridgeChainID("destination chain ID", dstChainID)
	if err != nil {
		return err
	}
	logIndex, err := bridgeLogIndex(lg.LogIndex)
	if err != nil {
		return err
	}
	blockNumber, err := bridgeBlockNumber(lg.BlockNumber)
	if err != nil {
		return err
	}

	gw, _ := r.registry.Gateway(chainID)
	transfer := Transfer{
		TransferID:      strings.ToLower(lg.Topics[1]),
		SrcChainID:      chainID,
		DstChainID:      dstChain,
		SrcGateway:      gw.Address,
		Sender:          topicAddress(lg.Topics[2]),
		Recipient:       topicAddress(lg.Topics[3]),
		SrcToken:        srcToken,
		Amount:          amount.String(),
		DepositTxHash:   lg.TxHash,
		DepositLogIndex: logIndex,
		DepositBlock:    blockNumber,
		DepositedAt:     time.Now().UTC(),
	}

	inserted, err := r.store.RecordInitiated(ctx, transfer)
	if err != nil {
		return err
	}
	if inserted {
		r.log.Info("bridge relayer: deposit observed",
			"transferId", transfer.TransferID, "srcChain", chainID,
			"dstChain", transfer.DstChainID, "amount", transfer.Amount)
	}
	return nil
}

// handleFulfilled records a delivery.
//
//	BridgeFulfilled(bytes32 indexed transferId, address indexed recipient,
//	  address dstToken, uint256 amount, uint256 srcChainId)
func (r *Relayer) handleFulfilled(ctx context.Context, lg rpc.Log) error {
	if len(lg.Topics) < 2 {
		return fmt.Errorf("bridge relayer: BridgeFulfilled needs 2 topics, got %d", len(lg.Topics))
	}
	data, err := decodeData(lg.Data)
	if err != nil {
		return err
	}
	if len(data) < 3*32 {
		return fmt.Errorf("bridge relayer: BridgeFulfilled data too short (%d bytes)", len(data))
	}
	srcChainID, err := rpc.DecodeUint256At(data, 64)
	if err != nil {
		return err
	}
	srcChain, err := bridgeChainID("source chain ID", srcChainID)
	if err != nil {
		return err
	}
	blockNumber, err := bridgeBlockNumber(lg.BlockNumber)
	if err != nil {
		return err
	}

	updated, err := r.store.MarkFulfilled(ctx,
		srcChain, lg.Topics[1], lg.TxHash, blockNumber, time.Now().UTC())
	if err != nil {
		return err
	}
	if updated {
		r.log.Info("bridge relayer: delivery confirmed",
			"transferId", strings.ToLower(lg.Topics[1]), "tx", lg.TxHash)
	}
	return nil
}

// handleRefunded records a refund observed on the source chain.
func (r *Relayer) handleRefunded(ctx context.Context, chainID int, lg rpc.Log) error {
	if len(lg.Topics) < 2 {
		return fmt.Errorf("bridge relayer: BridgeRefunded needs 2 topics, got %d", len(lg.Topics))
	}
	updated, err := r.store.MarkRefunded(ctx, chainID, lg.Topics[1], lg.TxHash, time.Now().UTC())
	if err != nil {
		return err
	}
	if updated {
		r.log.Info("bridge relayer: refund observed", "transferId", strings.ToLower(lg.Topics[1]))
	}
	return nil
}

// DeliverPending attempts to fulfil every pending transfer.
func (r *Relayer) DeliverPending(ctx context.Context) error {
	pending, err := r.store.ListPending(ctx, r.cfg.DeliverBatch)
	if err != nil {
		return err
	}
	var firstErr error
	for _, t := range pending {
		if err := r.deliver(ctx, t); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			// Persist WHY so the status endpoint can explain a stuck transfer
			// instead of showing an indefinite "pending".
			if recErr := r.store.RecordAttempt(ctx, t.SrcChainID, t.TransferID, err.Error()); recErr != nil {
				r.log.Warn("bridge relayer: could not record attempt", "err", recErr)
			}
			r.log.Warn("bridge relayer: delivery attempt failed",
				"transferId", t.TransferID, "err", err)
		}
	}
	return firstErr
}

func (r *Relayer) deliver(ctx context.Context, t Transfer) error {
	if r.signer == nil {
		return errors.New("bridge relayer: observe-only (no signer configured)")
	}
	dstGW, ok := r.registry.Gateway(t.DstChainID)
	if !ok {
		return fmt.Errorf("no gateway deployed on destination chain %d", t.DstChainID)
	}
	client, ok := r.clients[t.DstChainID]
	if !ok || client == nil {
		return fmt.Errorf("no RPC client for destination chain %d", t.DstChainID)
	}

	// Reconcile before re-delivering. A BridgeFulfilled event can be missed —
	// a restart with a start block past it, a scan gap — and the projection
	// then says INITIATED forever while the recipient already holds the funds.
	// That is the worst possible lie for this product: the UI shows a user's
	// money in limbo when it has actually arrived, and every cycle retries a
	// delivery the contract will reject.
	//
	// This is still not optimism: it reads the destination gateway's own
	// `fulfilled` mapping. Chain state remains the only thing that advances a
	// status; this just asks the chain directly instead of waiting for a log
	// window that may never come back.
	if done, err := r.alreadyFulfilled(ctx, client, dstGW.Address, t.TransferID); err == nil && done {
		if _, mErr := r.store.MarkFulfilled(ctx, t.SrcChainID, t.TransferID, "", 0, time.Now().UTC()); mErr != nil {
			return fmt.Errorf("reconcile fulfilled: %w", mErr)
		}
		r.log.Info("bridge relayer: transfer already fulfilled on-chain, projection reconciled",
			"transferId", t.TransferID, "dstChain", t.DstChainID)
		return nil
	}

	// Resolve the destination token from the SOURCE gateway's route table: the
	// contract, not the stored row, decides what gets delivered.
	route, err := r.registry.Route(ctx, t.SrcChainID, t.SrcToken, t.DstChainID)
	if err != nil {
		return fmt.Errorf("route lookup: %w", err)
	}
	if !route.Supported || route.DstToken == "" {
		return errors.New("route is no longer configured on-chain")
	}

	amount, ok := new(big.Int).SetString(t.Amount, 10)
	if !ok {
		return fmt.Errorf("stored amount %q is not an integer", t.Amount)
	}

	// Refuse to send a transaction that would revert on insufficient liquidity:
	// it would burn gas and produce a confusing failed tx on the explorer.
	liquidity, err := r.registry.Liquidity(ctx, t.DstChainID, route.DstToken)
	if err != nil {
		return fmt.Errorf("liquidity read: %w", err)
	}
	if liquidity.Cmp(amount) < 0 {
		return fmt.Errorf("insufficient destination liquidity: have %s, need %s", liquidity, amount)
	}

	data := selFulfill +
		strings.TrimPrefix(strings.ToLower(t.TransferID), "0x") +
		padAddress(route.DstToken) +
		padAddress(t.Recipient) +
		padUint(amount) +
		padUint(big.NewInt(int64(t.SrcChainID)))

	txHash, err := r.send(ctx, client, dstGW.Address, data)
	if err != nil {
		return err
	}
	// Deliberately NOT marking the transfer fulfilled here. The status advances
	// only when the BridgeFulfilled log is observed on a later scan.
	r.log.Info("bridge relayer: fulfil submitted (awaiting on-chain confirmation)",
		"transferId", t.TransferID, "dstChain", t.DstChainID, "tx", txHash)
	return nil
}

// send builds, signs and broadcasts a transaction from the relayer key.
func (r *Relayer) send(ctx context.Context, client ChainClient, to, data string) (string, error) {
	from := r.signer.Address()

	chainID, err := client.ChainID(ctx)
	if err != nil {
		return "", fmt.Errorf("chainId: %w", err)
	}
	nonce, err := client.GetTransactionCount(ctx, from, "pending")
	if err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	gasPrice, err := client.GetGasPrice(ctx)
	if err != nil {
		return "", fmt.Errorf("gasPrice: %w", err)
	}
	gasPrice = padGasPrice(gasPrice, r.cfg.GasPricePadPercent)
	gas, err := client.EstimateGas(ctx, from, to, "0x0", data)
	if err != nil {
		// A failing estimate means the call would revert — surface that rather
		// than sending with a guessed limit and burning gas on a failure.
		return "", fmt.Errorf("gas estimate (the call would revert): %w", err)
	}
	gasLimit := gas.Uint64()
	if r.cfg.GasLimitPadPercent > 0 {
		gasLimit += gasLimit * r.cfg.GasLimitPadPercent / 100
	}

	rawBytes, err := hex.DecodeString(strings.TrimPrefix(data, "0x"))
	if err != nil {
		return "", fmt.Errorf("calldata: %w", err)
	}

	signed, err := r.signer.SignTx(ethtx.Tx{
		Nonce:    nonce.Uint64(),
		GasPrice: gasPrice,
		Gas:      gasLimit,
		To:       to,
		Value:    big.NewInt(0),
		Data:     rawBytes,
		ChainID:  chainID,
	})
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	txHash, err := client.SendRawTransaction(ctx, signed)
	if err != nil {
		return "", fmt.Errorf("broadcast: %w", err)
	}
	return txHash, nil
}

// alreadyFulfilled reads the destination gateway's `fulfilled(bytes32)` mapping.
// A read failure returns false so the caller falls through to the normal
// delivery path — the gas estimate there is the real guard against sending a
// transaction that would revert.
func (r *Relayer) alreadyFulfilled(ctx context.Context, client ChainClient, gateway, transferID string) (bool, error) {
	id := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(transferID)), "0x")
	if len(id) != 64 {
		return false, fmt.Errorf("bridge relayer: transferId is not 32 bytes: %q", transferID)
	}
	raw, err := client.EthCall(ctx, gateway, selFulfilled+id, "latest")
	if err != nil {
		return false, err
	}
	if len(raw) < 32 {
		return false, fmt.Errorf("bridge relayer: short fulfilled() response (%d bytes)", len(raw))
	}
	v, err := rpc.DecodeUint256At(raw, 0)
	if err != nil {
		return false, err
	}
	return v.Sign() != 0, nil
}

// padGasPrice bids above the last observed price so a base fee that rises
// between the read and the broadcast doesn't reject the fulfil. Overbidding
// costs a little gas; underbidding strands a user's deposit until the next
// cycle, which is the worse of the two.
func padGasPrice(gasPrice *big.Int, padPercent uint64) *big.Int {
	if gasPrice == nil || padPercent == 0 {
		return gasPrice
	}
	padded := new(big.Int).Mul(gasPrice, big.NewInt(int64(100+padPercent)))
	return padded.Div(padded, big.NewInt(100))
}

func decodeData(dataHex string) ([]byte, error) {
	clean := strings.TrimPrefix(strings.TrimSpace(dataHex), "0x")
	if clean == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("bridge relayer: log data is not hex: %w", err)
	}
	return b, nil
}

func wordAddressAt(data []byte, offset int) (string, error) {
	word, err := rpc.DecodeUint256At(data, offset)
	if err != nil {
		return "", err
	}
	return wordToAddress(word), nil
}

// topicAddress renders an indexed address topic as a 0x address.
func topicAddress(topic string) string {
	clean := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(topic)), "0x")
	if len(clean) < 40 {
		return ""
	}
	return "0x" + clean[len(clean)-40:]
}
