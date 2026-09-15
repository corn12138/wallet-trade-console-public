package web3events

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

type stubChainRPC struct {
	chainID      *big.Int
	tx           rpc.Transaction
	txOK         bool
	receipt      rpc.Receipt
	receiptOK    bool
	head         *big.Int
	accountNonce *big.Int
	chainErr     error
	txErr        error
	receiptErr   error
	headErr      error
	nonceErr     error
}

func (s stubChainRPC) ChainID(context.Context) (*big.Int, error) {
	return s.chainID, s.chainErr
}

func (s stubChainRPC) BlockNumber(context.Context) (*big.Int, error) {
	return s.head, s.headErr
}

func (s stubChainRPC) GetTransactionCount(context.Context, string, string) (*big.Int, error) {
	return s.accountNonce, s.nonceErr
}

func (s stubChainRPC) GetTransactionByHash(context.Context, string) (rpc.Transaction, bool, error) {
	return s.tx, s.txOK, s.txErr
}

func (s stubChainRPC) GetTransactionReceipt(context.Context, string) (rpc.Receipt, bool, error) {
	return s.receipt, s.receiptOK, s.receiptErr
}

func TestRPCTransactionVerifier_UsesChainSenderAndReceiptProof(t *testing.T) {
	const chainID = 11155111
	hash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	owner := "0x000000000000000000000000000000000000dead"
	to := "0x000000000000000000000000000000000000cafe"
	gasUsed := big.NewInt(21000)
	gasPrice := big.NewInt(1_000_000_000)
	verifier := NewRPCTransactionVerifier(map[int]ChainRPC{
		chainID: stubChainRPC{
			chainID: big.NewInt(chainID),
			tx: rpc.Transaction{
				TxHash: hash, From: owner, To: &to, Value: big.NewInt(42),
			},
			txOK: true,
			receipt: rpc.Receipt{
				TxHash: hash, From: owner, To: &to, BlockNumber: 123,
				GasUsed: gasUsed, EffectiveGasPrice: gasPrice, Success: true,
			},
			receiptOK: true,
		},
	})

	submitted, err := verifier.VerifySubmitted(context.Background(), chainID, hash, owner)
	if err != nil {
		t.Fatalf("VerifySubmitted: %v", err)
	}
	if submitted.ToAddress == nil || *submitted.ToAddress != to || submitted.Value == nil || *submitted.Value != "42" {
		t.Errorf("submitted = %+v", submitted)
	}
	receipt, err := verifier.VerifyReceipt(context.Background(), chainID, hash, owner)
	if err != nil {
		t.Fatalf("VerifyReceipt: %v", err)
	}
	if receipt.Status != "confirmed" || receipt.BlockNumber != 123 || receipt.GasUsed == nil || *receipt.GasUsed != 21000 {
		t.Errorf("receipt = %+v", receipt)
	}
}

func TestRPCTransactionVerifier_RejectsUntrustedCoordinates(t *testing.T) {
	const chainID = 11155111
	hash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	owner := "0x000000000000000000000000000000000000dead"
	other := "0x000000000000000000000000000000000000beef"

	tests := []struct {
		name   string
		client stubChainRPC
		want   error
	}{
		{
			name: "wrong rpc chain",
			client: stubChainRPC{chainID: big.NewInt(1), tx: rpc.Transaction{
				TxHash: hash, From: owner,
			}, txOK: true},
			want: ErrRPCChainMismatch,
		},
		{
			name:   "unknown transaction",
			client: stubChainRPC{chainID: big.NewInt(chainID)},
			want:   ErrTransactionNotFound,
		},
		{
			name: "different sender",
			client: stubChainRPC{chainID: big.NewInt(chainID), tx: rpc.Transaction{
				TxHash: hash, From: other,
			}, txOK: true},
			want: ErrSenderMismatch,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verifier := NewRPCTransactionVerifier(map[int]ChainRPC{chainID: tc.client})
			_, err := verifier.VerifySubmitted(context.Background(), chainID, hash, owner)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestRPCTransactionVerifier_PendingReceiptIsNotAccepted(t *testing.T) {
	const chainID = 11155111
	hash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	owner := "0x000000000000000000000000000000000000dead"
	verifier := NewRPCTransactionVerifier(map[int]ChainRPC{
		chainID: stubChainRPC{
			chainID: big.NewInt(chainID),
			tx:      rpc.Transaction{TxHash: hash, From: owner},
			txOK:    true,
		},
	})
	_, err := verifier.VerifyReceipt(context.Background(), chainID, hash, owner)
	if !errors.Is(err, ErrReceiptPending) {
		t.Fatalf("err=%v, want ErrReceiptPending", err)
	}
}

func TestRPCTransactionVerifier_RequiresReceiptSenderAndDestinationConsistency(t *testing.T) {
	const chainID = 11155111
	hash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	owner := "0x000000000000000000000000000000000000dead"
	to := "0x000000000000000000000000000000000000cafe"
	base := stubChainRPC{
		chainID:   big.NewInt(chainID),
		tx:        rpc.Transaction{TxHash: hash, From: owner, To: &to},
		txOK:      true,
		receipt:   rpc.Receipt{TxHash: hash, BlockNumber: 1, Success: true},
		receiptOK: true,
	}

	verifier := NewRPCTransactionVerifier(map[int]ChainRPC{chainID: base})
	if _, err := verifier.VerifyReceipt(context.Background(), chainID, hash, owner); !errors.Is(err, ErrSenderMismatch) {
		t.Fatalf("missing receipt sender err=%v, want ErrSenderMismatch", err)
	}

	base.receipt.From = owner
	verifier = NewRPCTransactionVerifier(map[int]ChainRPC{chainID: base})
	if _, err := verifier.VerifyReceipt(context.Background(), chainID, hash, owner); !errors.Is(err, ErrRPCEvidenceMismatch) {
		t.Fatalf("missing receipt destination err=%v, want ErrRPCEvidenceMismatch", err)
	}
}

// The three attempt fields WP1.2 requires must actually arrive from the chain,
// not ship permanently NULL.
func TestVerifierCarriesSenderNonceBlockHashAndConfirmations(t *testing.T) {
	const chainID = 11155111
	hash := "0x" + strings.Repeat("a", 64)
	owner := "0x000000000000000000000000000000000000dead"
	to := "0x000000000000000000000000000000000000cafe"
	nonce := uint64(9)
	txBlock := "0xBBB"
	receiptBlock := "0xCcC"

	verifier := NewRPCTransactionVerifier(map[int]ChainRPC{chainID: stubChainRPC{
		chainID: big.NewInt(chainID),
		tx: rpc.Transaction{
			TxHash: hash, From: owner, To: &to, Value: big.NewInt(5),
			Nonce: &nonce, BlockHash: &txBlock,
		},
		txOK: true,
		receipt: rpc.Receipt{
			TxHash: hash, From: owner, To: &to, BlockNumber: 100,
			BlockHash: &receiptBlock, Success: true,
		},
		receiptOK: true,
		head:      big.NewInt(104),
	}})

	submitted, err := verifier.VerifySubmitted(context.Background(), chainID, hash, owner)
	if err != nil {
		t.Fatalf("VerifySubmitted: %v", err)
	}
	if submitted.SenderNonce == nil || *submitted.SenderNonce != 9 {
		t.Errorf("sender nonce = %v, want 9", submitted.SenderNonce)
	}
	if submitted.BlockHash == nil || *submitted.BlockHash != "0xbbb" {
		t.Errorf("block hash = %v, want lowercased 0xbbb", submitted.BlockHash)
	}

	receipt, err := verifier.VerifyReceipt(context.Background(), chainID, hash, owner)
	if err != nil {
		t.Fatalf("VerifyReceipt: %v", err)
	}
	if receipt.BlockHash == nil || *receipt.BlockHash != "0xccc" {
		t.Errorf("receipt block hash = %v, want lowercased 0xccc", receipt.BlockHash)
	}
	// head 104, mined at 100 → the mined block counts, so 5.
	if receipt.Confirmations != 5 {
		t.Errorf("confirmations = %d, want 5 (head 104, mined 100, inclusive)", receipt.Confirmations)
	}
	if receipt.SenderNonce == nil || *receipt.SenderNonce != 9 {
		t.Errorf("receipt sender nonce = %v, want 9", receipt.SenderNonce)
	}
}

// An unreadable head must not fabricate a confirmation depth. The receipt is
// still authoritative; the depth is simply not known yet.
func TestConfirmationsAreZeroRatherThanGuessedWhenTheHeadIsUnreadable(t *testing.T) {
	const chainID = 11155111
	hash := "0x" + strings.Repeat("a", 64)
	owner := "0x000000000000000000000000000000000000dead"
	nonce := uint64(1)

	verifier := NewRPCTransactionVerifier(map[int]ChainRPC{chainID: stubChainRPC{
		chainID:   big.NewInt(chainID),
		tx:        rpc.Transaction{TxHash: hash, From: owner, Nonce: &nonce},
		txOK:      true,
		receipt:   rpc.Receipt{TxHash: hash, From: owner, BlockNumber: 100, Success: true},
		receiptOK: true,
		headErr:   errors.New("node unreachable"),
	}})

	receipt, err := verifier.VerifyReceipt(context.Background(), chainID, hash, owner)
	if err != nil {
		t.Fatalf("VerifyReceipt must still succeed on a known receipt: %v", err)
	}
	if receipt.Confirmations != 0 {
		t.Errorf("confirmations = %d, want 0 when the head is unreadable", receipt.Confirmations)
	}
	// A head BEHIND the receipt (a lagging replica) must also not go negative.
	behind := NewRPCTransactionVerifier(map[int]ChainRPC{chainID: stubChainRPC{
		chainID:   big.NewInt(chainID),
		tx:        rpc.Transaction{TxHash: hash, From: owner, Nonce: &nonce},
		txOK:      true,
		receipt:   rpc.Receipt{TxHash: hash, From: owner, BlockNumber: 100, Success: true},
		receiptOK: true,
		head:      big.NewInt(50),
	}})
	lagging, err := behind.VerifyReceipt(context.Background(), chainID, hash, owner)
	if err != nil {
		t.Fatalf("VerifyReceipt: %v", err)
	}
	if lagging.Confirmations != 0 {
		t.Errorf("confirmations = %d, want 0 for a lagging head", lagging.Confirmations)
	}
}

func TestAccountNonceAndPresenceFailClosedOnAnUnsupportedChain(t *testing.T) {
	verifier := NewRPCTransactionVerifier(map[int]ChainRPC{11155111: stubChainRPC{}})
	if _, err := verifier.AccountNonce(context.Background(), 1, "0xdead"); !errors.Is(err, ErrUnsupportedChain) {
		t.Errorf("AccountNonce on an unconfigured chain = %v, want ErrUnsupportedChain", err)
	}
	if _, err := verifier.TransactionPresent(context.Background(), 1, "0xabc"); !errors.Is(err, ErrUnsupportedChain) {
		t.Errorf("TransactionPresent on an unconfigured chain = %v, want ErrUnsupportedChain", err)
	}
}
