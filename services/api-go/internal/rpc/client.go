// Package rpc provides a minimal Ethereum JSON-RPC client.
// Phase 5d.1: just enough to support eth_call for the swap router
// (getAmountsOut + getReserves) so swap.GetQuote can produce live
// quotes instead of always falling back. Other RPC methods will land
// when the indexer and tx-review ports need them.
package rpc

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultTimeout matches viem's default request timeout (10s).
const DefaultTimeout = 10 * time.Second

// Client is a minimal JSON-RPC client for an Ethereum node.
type Client struct {
	url    string
	http   *http.Client
	nextID uint64
}

// NewClient constructs a client. timeout <= 0 falls back to DefaultTimeout.
func NewClient(url string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{url: url, http: &http.Client{Timeout: timeout}}
}

// URL returns the configured endpoint (useful for diagnostics).
func (c *Client) URL() string {
	if c == nil {
		return ""
	}
	return c.url
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      uint64 `json:"id"`
}

// RPCError carries a JSON-RPC error frame back to the caller.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

// ErrEmpty is returned when the node responds with "0x" (empty result).
var ErrEmpty = errors.New("rpc: empty result")

// redactTransportErr strips the endpoint URL from a transport error. Go's
// http.Client.Do wraps failures in *url.Error, whose Error() prints the full URL
// — which for our RPC endpoints embeds the provider API key (e.g. Infura
// .../v3/<key>). We surface the inner cause without the URL so the key never
// reaches logs. Both cmd/api and cmd/indexer ship these errors at WARN/ERROR.
func redactTransportErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("rpc: %s request failed: %w", ue.Op, ue.Err)
	}
	return err
}

// RedactURL returns the endpoint with its path/query masked, keeping only
// scheme+host for diagnostics. Provider RPC endpoints carry the API key in the
// path (.../v3/<key>) or query, so logging the raw URL leaks the key. Use this
// for any deliberate INFO log of an RPC endpoint (cmd/api swap, indexer worker).
func RedactURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<redacted-url>"
	}
	return u.Scheme + "://" + u.Host + "/<redacted>"
}

// callRaw posts a JSON-RPC request and returns the raw `result` field.
// Shared by EthCall and the *Hex helpers below.
func (c *Client) callRaw(ctx context.Context, method string, params []any) (json.RawMessage, error) {
	if c == nil {
		return nil, errors.New("rpc: client nil")
	}
	id := atomic.AddUint64(&c.nextID, 1)
	body, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
	})
	if err != nil {
		return nil, fmt.Errorf("rpc: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, redactTransportErr(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, redactTransportErr(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("rpc: http %d: %s", resp.StatusCode, string(raw))
	}
	var out rpcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("rpc: response decode: %w (body=%s)", err, string(raw))
	}
	if out.Error != nil {
		return nil, out.Error
	}
	return out.Result, nil
}

// EthCall issues an `eth_call` at the given block tag (defaults to
// "latest"). The data field is sent as a hex string with 0x prefix.
// The decoded result bytes (without leading 0x) are returned.
func (c *Client) EthCall(ctx context.Context, to, dataHex, block string) ([]byte, error) {
	if block == "" {
		block = "latest"
	}
	raw, err := c.callRaw(ctx, "eth_call", []any{
		map[string]string{"to": to, "data": dataHex},
		block,
	})
	if err != nil {
		return nil, err
	}
	var hexStr string
	if err := json.Unmarshal(raw, &hexStr); err != nil {
		return nil, fmt.Errorf("rpc: result not hex string: %w", err)
	}
	hexStr = strings.TrimPrefix(hexStr, "0x")
	if hexStr == "" {
		return nil, ErrEmpty
	}
	bs, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("rpc: hex decode: %w", err)
	}
	return bs, nil
}

// callHex shared by EstimateGas / GetGasPrice / GetBalance: decode a
// hex-string result as a *big.Int.
func (c *Client) callHex(ctx context.Context, method string, params []any) (*big.Int, error) {
	raw, err := c.callRaw(ctx, method, params)
	if err != nil {
		return nil, err
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("rpc: hex result decode: %w", err)
	}
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return big.NewInt(0), nil
	}
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return nil, fmt.Errorf("rpc: hex result invalid: %q", s)
	}
	return v, nil
}

// EstimateGas issues `eth_estimateGas`. value="" omits the field;
// dataHex should already carry its 0x prefix.
func (c *Client) EstimateGas(ctx context.Context, from, to, value, dataHex string) (*big.Int, error) {
	tx := map[string]string{"to": to, "data": dataHex}
	if from != "" {
		tx["from"] = from
	}
	if value != "" {
		tx["value"] = value
	}
	return c.callHex(ctx, "eth_estimateGas", []any{tx})
}

// GetGasPrice issues `eth_gasPrice`.
func (c *Client) GetGasPrice(ctx context.Context) (*big.Int, error) {
	return c.callHex(ctx, "eth_gasPrice", []any{})
}

// BlockNumber issues `eth_blockNumber`. Used by the indexer to drive
// the catch-up loop's head pointer.
func (c *Client) BlockNumber(ctx context.Context) (*big.Int, error) {
	return c.callHex(ctx, "eth_blockNumber", []any{})
}

// GetBalance issues `eth_getBalance` at the given block tag (defaults
// to "latest").
func (c *Client) GetBalance(ctx context.Context, address, block string) (*big.Int, error) {
	if block == "" {
		block = "latest"
	}
	return c.callHex(ctx, "eth_getBalance", []any{address, block})
}

// GetTransactionCount issues `eth_getTransactionCount` — the sender's next
// nonce. The relayer uses the "pending" tag so back-to-back sends within one
// block do not collide on the same nonce.
func (c *Client) GetTransactionCount(ctx context.Context, address, block string) (*big.Int, error) {
	if block == "" {
		block = "pending"
	}
	return c.callHex(ctx, "eth_getTransactionCount", []any{address, block})
}

// ChainID issues `eth_chainId`. The relayer verifies this against its configured
// chain before signing: an EIP-155 signature is bound to a chain id, and signing
// for the wrong one would produce a transaction valid on a chain the operator
// did not intend.
func (c *Client) ChainID(ctx context.Context) (*big.Int, error) {
	return c.callHex(ctx, "eth_chainId", []any{})
}

// SendRawTransaction issues `eth_sendRawTransaction` and returns the tx hash.
func (c *Client) SendRawTransaction(ctx context.Context, rawHex string) (string, error) {
	if !strings.HasPrefix(rawHex, "0x") {
		rawHex = "0x" + rawHex
	}
	raw, err := c.callRaw(ctx, "eth_sendRawTransaction", []any{rawHex})
	if err != nil {
		return "", err
	}
	var hash string
	if err := json.Unmarshal(raw, &hash); err != nil {
		return "", fmt.Errorf("rpc: sendRawTransaction result not a string: %w", err)
	}
	return hash, nil
}

// Transaction is the authoritative sender/destination/value projection returned
// by eth_getTransactionByHash.
//
// Nonce and BlockHash are what make a replacement observable: a speed-up or
// cancel reuses the sender's nonce under a new hash, so two attempts sharing
// (from, nonce) where one is mined identifies the other as replaced. BlockHash
// is nil while the transaction is still in the mempool.
//
// Nonce is a POINTER because zero is a real nonce (a wallet's first
// transaction) and parseHexQuantity maps "" to 0. A node that omits the field
// must leave this nil so the caller declines to compare, rather than silently
// treating every first transaction as sharing nonce 0.
type Transaction struct {
	TxHash    string
	From      string
	To        *string
	Value     *big.Int
	Nonce     *uint64
	BlockHash *string
}

// GetTransactionByHash returns ok=false until the configured node knows the
// transaction.
func (c *Client) GetTransactionByHash(ctx context.Context, txHash string) (Transaction, bool, error) {
	raw, err := c.callRaw(ctx, "eth_getTransactionByHash", []any{txHash})
	if err != nil {
		return Transaction{}, false, err
	}
	var body *struct {
		Hash      string  `json:"hash"`
		From      string  `json:"from"`
		To        *string `json:"to"`
		Value     string  `json:"value"`
		Nonce     string  `json:"nonce"`
		BlockHash *string `json:"blockHash"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Transaction{}, false, fmt.Errorf("rpc: transaction decode: %w", err)
	}
	if body == nil {
		return Transaction{}, false, nil
	}
	value, err := parseHexBigQuantity(body.Value)
	if err != nil {
		return Transaction{}, false, fmt.Errorf("rpc: transaction value: %w", err)
	}
	// A malformed nonce is an error; an absent one is left nil. Only the first
	// is evidence of a broken response — omission just means this node did not
	// tell us, and the caller must not infer nonce 0 from silence.
	var nonce *uint64
	if strings.TrimSpace(body.Nonce) != "" {
		parsed, err := parseHexQuantity(body.Nonce)
		if err != nil {
			return Transaction{}, false, fmt.Errorf("rpc: transaction nonce: %w", err)
		}
		nonce = &parsed
	}
	return Transaction{
		TxHash:    body.Hash,
		From:      body.From,
		To:        body.To,
		Value:     value,
		Nonce:     nonce,
		BlockHash: emptyToNil(body.BlockHash),
	}, true, nil
}

// emptyToNil normalises a JSON field that a node may send as null, as "" or as
// a real value. A pending transaction's blockHash is null on some nodes and an
// empty string on others; both mean "not mined".
func emptyToNil(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return value
}

// Receipt is the mined result needed by the relayer and transaction reporter.
type Receipt struct {
	TxHash            string
	From              string
	To                *string
	ContractAddress   *string
	BlockNumber       uint64
	BlockHash         *string
	GasUsed           *big.Int
	EffectiveGasPrice *big.Int
	Success           bool
}

// GetTransactionReceipt returns the receipt, or ok=false when the transaction
// is not yet mined. A reverted transaction returns ok=true with Success=false —
// the caller must not treat "mined" as "worked".
func (c *Client) GetTransactionReceipt(ctx context.Context, txHash string) (Receipt, bool, error) {
	raw, err := c.callRaw(ctx, "eth_getTransactionReceipt", []any{txHash})
	if err != nil {
		return Receipt{}, false, err
	}
	var body *struct {
		TransactionHash   string  `json:"transactionHash"`
		From              string  `json:"from"`
		To                *string `json:"to"`
		ContractAddress   *string `json:"contractAddress"`
		BlockNumber       string  `json:"blockNumber"`
		BlockHash         *string `json:"blockHash"`
		GasUsed           string  `json:"gasUsed"`
		EffectiveGasPrice string  `json:"effectiveGasPrice"`
		Status            string  `json:"status"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return Receipt{}, false, fmt.Errorf("rpc: receipt decode: %w", err)
	}
	if body == nil {
		return Receipt{}, false, nil
	}
	if strings.TrimSpace(body.BlockNumber) == "" {
		return Receipt{}, false, errors.New("rpc: receipt blockNumber missing")
	}
	blockNumber, err := parseHexQuantity(body.BlockNumber)
	if err != nil {
		return Receipt{}, false, fmt.Errorf("rpc: receipt blockNumber: %w", err)
	}
	if strings.TrimSpace(body.Status) == "" {
		return Receipt{}, false, errors.New("rpc: receipt status missing")
	}
	status, err := parseHexQuantity(body.Status)
	if err != nil || status > 1 {
		return Receipt{}, false, fmt.Errorf("rpc: receipt status invalid: %q", body.Status)
	}
	gasUsed, err := parseOptionalHexBigQuantity(body.GasUsed)
	if err != nil {
		return Receipt{}, false, fmt.Errorf("rpc: receipt gasUsed: %w", err)
	}
	gasPrice, err := parseOptionalHexBigQuantity(body.EffectiveGasPrice)
	if err != nil {
		return Receipt{}, false, fmt.Errorf("rpc: receipt effectiveGasPrice: %w", err)
	}
	return Receipt{
		TxHash:            body.TransactionHash,
		From:              body.From,
		To:                body.To,
		ContractAddress:   body.ContractAddress,
		BlockNumber:       blockNumber,
		BlockHash:         emptyToNil(body.BlockHash),
		GasUsed:           gasUsed,
		EffectiveGasPrice: gasPrice,
		Success:           status == 1,
	}, true, nil
}

func parseHexBigQuantity(raw string) (*big.Int, error) {
	value := strings.TrimPrefix(strings.TrimSpace(raw), "0x")
	if value == "" {
		return big.NewInt(0), nil
	}
	out, ok := new(big.Int).SetString(value, 16)
	if !ok || out.Sign() < 0 {
		return nil, fmt.Errorf("invalid hex quantity %q", raw)
	}
	return out, nil
}

func parseOptionalHexBigQuantity(raw string) (*big.Int, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	return parseHexBigQuantity(raw)
}

// LogFilter is the eth_getLogs query the indexer's backfill loop issues.
// FromBlock/ToBlock are inclusive block numbers, encoded as hex
// quantities on the wire. Address narrows the filter to one contract;
// Topics (optional) further narrows by topic0..topicN — nil means no
// topic constraint (every event the address emits in the range).
type LogFilter struct {
	Address   string
	FromBlock uint64
	ToBlock   uint64
	Topics    []string
}

// Log is a decoded eth_getLogs result entry. It carries the raw
// (Address, Topics, Data) the ABI decoder reads, plus the (BlockNumber,
// TxHash, LogIndex) coordinates used for checkpointing and the persist
// layer's idempotency key (chainId, contractAddress, txHash, logIndex).
type Log struct {
	Address     string
	Topics      []string
	Data        string
	BlockNumber uint64
	TxHash      string
	LogIndex    uint64
	Removed     bool
}

// jsonLog is the wire shape: quantities arrive as hex strings.
type jsonLog struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	BlockNumber     string   `json:"blockNumber"`
	TransactionHash string   `json:"transactionHash"`
	LogIndex        string   `json:"logIndex"`
	Removed         bool     `json:"removed"`
}

// GetLogs issues `eth_getLogs` for the given filter and decodes the
// result array. fromBlock/toBlock are sent as hex quantities; an empty
// Address omits the address constraint.
func (c *Client) GetLogs(ctx context.Context, filter LogFilter) ([]Log, error) {
	params := map[string]any{
		"fromBlock": hexQuantity(filter.FromBlock),
		"toBlock":   hexQuantity(filter.ToBlock),
	}
	if filter.Address != "" {
		params["address"] = filter.Address
	}
	if len(filter.Topics) > 0 {
		params["topics"] = filter.Topics
	}
	raw, err := c.callRaw(ctx, "eth_getLogs", []any{params})
	if err != nil {
		return nil, err
	}
	var entries []jsonLog
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("rpc: getLogs decode: %w", err)
	}
	out := make([]Log, 0, len(entries))
	for i, e := range entries {
		blockNumber, err := parseHexQuantity(e.BlockNumber)
		if err != nil {
			return nil, fmt.Errorf("rpc: getLogs[%d] blockNumber: %w", i, err)
		}
		logIndex, err := parseHexQuantity(e.LogIndex)
		if err != nil {
			return nil, fmt.Errorf("rpc: getLogs[%d] logIndex: %w", i, err)
		}
		out = append(out, Log{
			Address:     e.Address,
			Topics:      e.Topics,
			Data:        e.Data,
			BlockNumber: blockNumber,
			TxHash:      e.TransactionHash,
			LogIndex:    logIndex,
			Removed:     e.Removed,
		})
	}
	return out, nil
}

// hexQuantity encodes a uint64 as a minimal 0x hex quantity (no leading
// zeros), per the Ethereum JSON-RPC QUANTITY convention.
func hexQuantity(n uint64) string {
	return "0x" + strconv.FormatUint(n, 16)
}

// parseHexQuantity decodes a 0x hex quantity. An empty string (e.g. a
// null blockNumber on a pending log) decodes to 0 without error.
func parseHexQuantity(s string) (uint64, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid hex quantity %q: %w", s, err)
	}
	return v, nil
}

// DecodeUint256At reads a 32-byte big-endian uint256 at byte `offset`.
func DecodeUint256At(data []byte, offset int) (*big.Int, error) {
	if offset < 0 || offset+32 > len(data) {
		return nil, fmt.Errorf("rpc: uint256 out of range (offset=%d len=%d)", offset, len(data))
	}
	return new(big.Int).SetBytes(data[offset : offset+32]), nil
}

// DecodeDynamicUint256Array reads a uint256[] from `data`. The standard
// dynamic-array encoding is: 32-byte offset → 32-byte length → length
// × 32-byte values.
func DecodeDynamicUint256Array(data []byte) ([]*big.Int, error) {
	if len(data) < 32 {
		return nil, fmt.Errorf("rpc: array offset missing (len=%d)", len(data))
	}
	off := new(big.Int).SetBytes(data[:32])
	if !off.IsInt64() {
		return nil, fmt.Errorf("rpc: array offset overflow")
	}
	o := int(off.Int64())
	if o < 0 || o+32 > len(data) {
		return nil, fmt.Errorf("rpc: array offset out of range (o=%d len=%d)", o, len(data))
	}
	nb := new(big.Int).SetBytes(data[o : o+32])
	if !nb.IsInt64() {
		return nil, fmt.Errorf("rpc: array length overflow")
	}
	n := int(nb.Int64())
	if n < 0 {
		return nil, fmt.Errorf("rpc: array length negative")
	}
	start := o + 32
	if start+n*32 > len(data) {
		return nil, fmt.Errorf("rpc: array data truncated (need=%d have=%d)", start+n*32, len(data))
	}
	out := make([]*big.Int, n)
	for i := 0; i < n; i++ {
		out[i] = new(big.Int).SetBytes(data[start+i*32 : start+i*32+32])
	}
	return out, nil
}
