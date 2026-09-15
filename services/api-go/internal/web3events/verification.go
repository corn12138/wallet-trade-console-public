package web3events

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

var (
	ErrVerificationUnavailable = errors.New("transaction verification unavailable")
	ErrUnsupportedChain        = errors.New("transaction verification unsupported for chain")
	ErrRPCChainMismatch        = errors.New("transaction RPC chain mismatch")
	ErrTransactionNotFound     = errors.New("transaction not found by RPC")
	ErrReceiptPending          = errors.New("transaction receipt is not mined")
	ErrSenderMismatch          = errors.New("transaction sender does not match authenticated wallet")
	ErrRPCEvidenceMismatch     = errors.New("transaction RPC evidence is inconsistent")
)

// ChainRPC is the narrow RPC surface used to validate transaction reports.
//
// BlockNumber and GetTransactionCount exist for the attempt model: confirmations
// need the chain head, and deciding that a transaction was DROPPED needs the
// sender's account nonce to have moved past it. *rpc.Client already satisfies
// both, so the concrete wiring is unchanged.
type ChainRPC interface {
	ChainID(ctx context.Context) (*big.Int, error)
	BlockNumber(ctx context.Context) (*big.Int, error)
	GetTransactionCount(ctx context.Context, address, block string) (*big.Int, error)
	GetTransactionByHash(ctx context.Context, txHash string) (rpc.Transaction, bool, error)
	GetTransactionReceipt(ctx context.Context, txHash string) (rpc.Receipt, bool, error)
}

type VerifiedTransaction struct {
	ToAddress *string
	Value     *string
	// SenderNonce is the replacement key. It is a pointer because zero is a
	// real nonce and a node that omits the field must read as "unknown" — a
	// nil nonce makes replacement detection and DROPPED unreachable for the
	// attempt rather than wrong.
	SenderNonce *uint64
	BlockHash   *string
}

type VerifiedReceipt struct {
	ToAddress       *string
	ContractAddress *string
	Value           *string
	Status          string
	BlockNumber     int64
	BlockHash       *string
	SenderNonce     *uint64
	// Confirmations counts the mined block itself, so a just-mined transaction
	// is 1. It is 0 when the head could not be read — never a guess.
	Confirmations int
	GasUsed       *int64
	GasPrice      *int64
}

type TransactionVerifier interface {
	VerifySubmitted(ctx context.Context, chainID int, txHash, owner string) (VerifiedTransaction, error)
	VerifyReceipt(ctx context.Context, chainID int, txHash, owner string) (VerifiedReceipt, error)
}

type RPCTransactionVerifier struct {
	clients map[int]ChainRPC
}

func NewRPCTransactionVerifier(clients map[int]ChainRPC) *RPCTransactionVerifier {
	copyOfClients := make(map[int]ChainRPC, len(clients))
	for chainID, client := range clients {
		if client != nil {
			copyOfClients[chainID] = client
		}
	}
	return &RPCTransactionVerifier{clients: copyOfClients}
}

func (v *RPCTransactionVerifier) VerifySubmitted(
	ctx context.Context,
	chainID int,
	txHash, owner string,
) (VerifiedTransaction, error) {
	_, tx, err := v.verifiedTransaction(ctx, chainID, txHash, owner)
	if err != nil {
		return VerifiedTransaction{}, err
	}
	return VerifiedTransaction{
		ToAddress:   normalizeRPCAddress(tx.To),
		Value:       decimalValue(tx.Value),
		SenderNonce: tx.Nonce,
		BlockHash:   normalizeRPCHash(tx.BlockHash),
	}, nil
}

// AccountNonce reports the sender's next nonce. A transaction whose own nonce is
// below this has been superseded on chain — the evidence that separates
// "dropped" from "still in somebody's mempool".
func (v *RPCTransactionVerifier) AccountNonce(ctx context.Context, chainID int, address string) (uint64, error) {
	if v == nil {
		return 0, ErrVerificationUnavailable
	}
	client := v.clients[chainID]
	if client == nil {
		return 0, ErrUnsupportedChain
	}
	count, err := client.GetTransactionCount(ctx, address, "latest")
	if err != nil {
		return 0, fmt.Errorf("%w: account nonce: %v", ErrVerificationUnavailable, err)
	}
	if count == nil || !count.IsUint64() {
		return 0, fmt.Errorf("%w: account nonce range", ErrRPCEvidenceMismatch)
	}
	return count.Uint64(), nil
}

// TransactionPresent reports whether the node still knows the transaction at
// all. Absence is evidence only when the caller also has a nonce that has moved
// past it; on its own a flaky or load-balanced node produces it routinely.
func (v *RPCTransactionVerifier) TransactionPresent(ctx context.Context, chainID int, txHash string) (bool, error) {
	if v == nil {
		return false, ErrVerificationUnavailable
	}
	client := v.clients[chainID]
	if client == nil {
		return false, ErrUnsupportedChain
	}
	_, ok, err := client.GetTransactionByHash(ctx, txHash)
	if err != nil {
		return false, fmt.Errorf("%w: transaction lookup: %v", ErrVerificationUnavailable, err)
	}
	return ok, nil
}

func (v *RPCTransactionVerifier) VerifyReceipt(
	ctx context.Context,
	chainID int,
	txHash, owner string,
) (VerifiedReceipt, error) {
	client, tx, err := v.verifiedTransaction(ctx, chainID, txHash, owner)
	if err != nil {
		return VerifiedReceipt{}, err
	}
	receipt, ok, err := client.GetTransactionReceipt(ctx, txHash)
	if err != nil {
		return VerifiedReceipt{}, fmt.Errorf("%w: receipt lookup: %v", ErrVerificationUnavailable, err)
	}
	if !ok {
		return VerifiedReceipt{}, ErrReceiptPending
	}
	if !strings.EqualFold(receipt.TxHash, txHash) {
		return VerifiedReceipt{}, fmt.Errorf("%w: receipt hash", ErrRPCEvidenceMismatch)
	}
	if !strings.EqualFold(receipt.From, owner) {
		return VerifiedReceipt{}, ErrSenderMismatch
	}
	if !sameOptionalAddress(receipt.To, tx.To) {
		return VerifiedReceipt{}, fmt.Errorf("%w: receipt destination", ErrRPCEvidenceMismatch)
	}
	if receipt.BlockNumber > math.MaxInt64 {
		return VerifiedReceipt{}, fmt.Errorf("%w: block number overflow", ErrRPCEvidenceMismatch)
	}
	gasUsed, err := checkedInt64(receipt.GasUsed)
	if err != nil {
		return VerifiedReceipt{}, err
	}
	gasPrice, err := checkedInt64(receipt.EffectiveGasPrice)
	if err != nil {
		return VerifiedReceipt{}, err
	}
	status := "failed"
	if receipt.Success {
		status = "confirmed"
	}
	return VerifiedReceipt{
		ToAddress:       normalizeRPCAddress(receipt.To),
		ContractAddress: normalizeRPCAddress(receipt.ContractAddress),
		Value:           decimalValue(tx.Value),
		Status:          status,
		BlockNumber:     int64(receipt.BlockNumber),
		BlockHash:       normalizeRPCHash(receipt.BlockHash),
		SenderNonce:     tx.Nonce,
		Confirmations:   confirmationsFor(ctx, client, receipt.BlockNumber),
		GasUsed:         gasUsed,
		GasPrice:        gasPrice,
	}, nil
}

// confirmationsFor counts the mined block itself, so a just-mined transaction is
// 1. An unreadable head returns 0 rather than a guess: the receipt is still
// authoritative evidence, and the depth is simply not yet known.
func confirmationsFor(ctx context.Context, client ChainRPC, blockNumber uint64) int {
	head, err := client.BlockNumber(ctx)
	if err != nil || head == nil || !head.IsUint64() {
		return 0
	}
	if head.Uint64() < blockNumber {
		return 0
	}
	depth := head.Uint64() - blockNumber + 1
	if depth > uint64(math.MaxInt32) {
		return math.MaxInt32
	}
	return int(depth)
}

// normalizeRPCHash lowercases a 0x hash and maps an absent or empty one to nil.
func normalizeRPCHash(hash *string) *string {
	if hash == nil || strings.TrimSpace(*hash) == "" {
		return nil
	}
	value := strings.ToLower(strings.TrimSpace(*hash))
	return &value
}

func sameOptionalAddress(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.EqualFold(*left, *right)
}

func (v *RPCTransactionVerifier) verifiedTransaction(
	ctx context.Context,
	chainID int,
	txHash, owner string,
) (ChainRPC, rpc.Transaction, error) {
	if v == nil {
		return nil, rpc.Transaction{}, ErrVerificationUnavailable
	}
	client := v.clients[chainID]
	if client == nil {
		return nil, rpc.Transaction{}, ErrUnsupportedChain
	}
	actualChainID, err := client.ChainID(ctx)
	if err != nil {
		return nil, rpc.Transaction{}, fmt.Errorf("%w: chain id: %v", ErrVerificationUnavailable, err)
	}
	if actualChainID == nil || !actualChainID.IsInt64() || actualChainID.Int64() != int64(chainID) {
		return nil, rpc.Transaction{}, ErrRPCChainMismatch
	}
	tx, ok, err := client.GetTransactionByHash(ctx, txHash)
	if err != nil {
		return nil, rpc.Transaction{}, fmt.Errorf("%w: transaction lookup: %v", ErrVerificationUnavailable, err)
	}
	if !ok {
		return nil, rpc.Transaction{}, ErrTransactionNotFound
	}
	if !strings.EqualFold(tx.TxHash, txHash) {
		return nil, rpc.Transaction{}, fmt.Errorf("%w: transaction hash", ErrRPCEvidenceMismatch)
	}
	if !strings.EqualFold(tx.From, owner) {
		return nil, rpc.Transaction{}, ErrSenderMismatch
	}
	return client, tx, nil
}

func normalizeRPCAddress(address *string) *string {
	if address == nil || strings.TrimSpace(*address) == "" {
		return nil
	}
	value := strings.ToLower(strings.TrimSpace(*address))
	return &value
}

func decimalValue(value *big.Int) *string {
	if value == nil {
		return nil
	}
	text := value.String()
	return &text
}

func checkedInt64(value *big.Int) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	if !value.IsInt64() || value.Sign() < 0 {
		return nil, fmt.Errorf("%w: numeric field overflow", ErrRPCEvidenceMismatch)
	}
	out := value.Int64()
	return &out, nil
}
