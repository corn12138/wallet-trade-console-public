package web3events

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/golang-jwt/jwt/v5"
)

const writeTestSecret = "test-web3-secret"

type stubWriter struct {
	submitted *SubmitTxInput
	receipt   *ReceiptTxInput
	row       TransactionRow
	err       error
}

func (s *stubWriter) UpsertSubmittedTransaction(_ context.Context, in SubmitTxInput) (TransactionRow, error) {
	s.submitted = &in
	return s.row, s.err
}

func (s *stubWriter) UpsertTransactionReceipt(_ context.Context, in ReceiptTxInput) (TransactionRow, error) {
	s.receipt = &in
	return s.row, s.err
}

func signWeb3(t *testing.T, sub string) string {
	t.Helper()
	claims := auth.Claims{
		Sub: sub, ChainID: 11155111, Type: "web3",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(writeTestSecret))
	if err != nil {
		t.Fatalf("sign web3 token: %v", err)
	}
	return tok
}

// TestWriteHandlers covers the SIWE-guarded tx-reporting writes: the web3 guard
// (401 without a token), the owner-pin (403 on a mismatched fromAddress), input
// validation (400 on a bad txHash), and the happy paths (201 submit / 200
// receipt) with normalized input reaching the Writer.
func TestWriteHandlers(t *testing.T) {
	t.Setenv("NODE_ENV", "production") // strict web3 guard, no header fallback
	mw := auth.Middleware(auth.NewVerifier(writeTestSecret))
	validHash := "0x" + strings.Repeat("a", 64)
	dead := "0x000000000000000000000000000000000000dead"
	wr := &stubWriter{row: TransactionRow{ID: 7, ChainID: 11155111, TxHash: validHash, FromAddress: dead, Status: "pending", BlockNumber: "0"}}
	r := Router(NewService(stubReader{}).WithWrites(wr, mw))
	tok := signWeb3(t, dead)

	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(http.MethodPost, "/transactions", "", `{"txHash":"`+validHash+`"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /transactions without token = %d, want 401", rec.Code)
	}
	if rec := do(http.MethodPost, "/transactions", tok, `{"chainId":11155111,"txHash":"`+validHash+`"}`); rec.Code != http.StatusCreated {
		t.Errorf("valid submit = %d, want 201", rec.Code)
	}
	if rec := do(http.MethodPost, "/transactions", tok, `{"txHash":"0xnothex"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad txHash = %d, want 400", rec.Code)
	}
	beef := "0x000000000000000000000000000000000000beef"
	if rec := do(http.MethodPost, "/transactions", tok, `{"txHash":"`+validHash+`","fromAddress":"`+beef+`"}`); rec.Code != http.StatusForbidden {
		t.Errorf("mismatched fromAddress = %d, want 403", rec.Code)
	}
	if rec := do(http.MethodPost, "/transactions/"+validHash+"/receipt", tok, `{"status":"confirmed","blockNumber":"123","gasUsed":21000}`); rec.Code != http.StatusOK {
		t.Errorf("valid receipt = %d, want 200", rec.Code)
	}
	if wr.receipt == nil || wr.receipt.Status != "confirmed" || wr.receipt.BlockNumber != 123 || wr.receipt.GasUsed == nil || *wr.receipt.GasUsed != 21000 {
		t.Errorf("receipt input not normalized: %+v", wr.receipt)
	}
	// status normalization: success → confirmed; bad status → 400.
	if rec := do(http.MethodPost, "/transactions/"+validHash+"/receipt", tok, `{"status":"bogus"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad status = %d, want 400", rec.Code)
	}
}
