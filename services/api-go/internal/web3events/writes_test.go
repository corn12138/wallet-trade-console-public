package web3events

import (
	"context"
	"encoding/json"
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

type stubTransactionVerifier struct {
	submitted    VerifiedTransaction
	receipt      VerifiedReceipt
	submittedErr error
	receiptErr   error
	chainID      int
	txHash       string
	owner        string
}

func (s *stubTransactionVerifier) VerifySubmitted(
	_ context.Context,
	chainID int,
	txHash, owner string,
) (VerifiedTransaction, error) {
	s.chainID, s.txHash, s.owner = chainID, txHash, owner
	return s.submitted, s.submittedErr
}

func (s *stubTransactionVerifier) VerifyReceipt(
	_ context.Context,
	chainID int,
	txHash, owner string,
) (VerifiedReceipt, error) {
	s.chainID, s.txHash, s.owner = chainID, txHash, owner
	return s.receipt, s.receiptErr
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

// TestWriteHandlers covers SIWE owner pinning and RPC-authoritative transaction
// and receipt fields reaching the Writer.
func TestWriteHandlers(t *testing.T) {
	t.Setenv("NODE_ENV", "production") // strict web3 guard, no header fallback
	mw := auth.Middleware(auth.NewVerifier(writeTestSecret))
	validHash := "0x" + strings.Repeat("a", 64)
	dead := "0x000000000000000000000000000000000000dead"
	to := "0x000000000000000000000000000000000000cafe"
	value := "42"
	gasUsed := int64(21000)
	gasPrice := int64(1_000_000_000)
	wr := &stubWriter{row: TransactionRow{ID: 7, ChainID: 11155111, TxHash: validHash, FromAddress: dead, Status: "pending", BlockNumber: "0"}}
	verifier := &stubTransactionVerifier{
		submitted: VerifiedTransaction{ToAddress: &to, Value: &value},
		receipt: VerifiedReceipt{
			ToAddress: &to, Value: &value, Status: "confirmed", BlockNumber: 123,
			GasUsed: &gasUsed, GasPrice: &gasPrice,
		},
	}
	r := Router(NewService(stubReader{}).WithWrites(wr, mw, verifier))
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
	if wr.submitted == nil || wr.submitted.FromAddress != dead || wr.submitted.ToAddress == nil || *wr.submitted.ToAddress != to || wr.submitted.Value == nil || *wr.submitted.Value != value {
		t.Errorf("submitted input is not RPC-authoritative: %+v", wr.submitted)
	}
	if rec := do(http.MethodPost, "/transactions", tok, `{"txHash":"0xnothex"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad txHash = %d, want 400", rec.Code)
	}
	beef := "0x000000000000000000000000000000000000beef"
	if rec := do(http.MethodPost, "/transactions", tok, `{"txHash":"`+validHash+`","fromAddress":"`+beef+`"}`); rec.Code != http.StatusForbidden {
		t.Errorf("mismatched fromAddress = %d, want 403", rec.Code)
	}
	if rec := do(http.MethodPost, "/transactions/"+validHash+"/receipt", tok, `{"status":"failed","blockNumber":"1","gasUsed":1}`); rec.Code != http.StatusOK {
		t.Errorf("valid receipt = %d, want 200", rec.Code)
	}
	if wr.receipt == nil || wr.receipt.Status != "confirmed" || wr.receipt.BlockNumber != 123 || wr.receipt.GasUsed == nil || *wr.receipt.GasUsed != 21000 || wr.receipt.GasPrice == nil || *wr.receipt.GasPrice != gasPrice {
		t.Errorf("receipt input is not RPC-authoritative: %+v", wr.receipt)
	}
	if verifier.chainID != 11155111 || verifier.txHash != validHash || verifier.owner != dead {
		t.Errorf("verification coordinates = chain=%d hash=%q owner=%q", verifier.chainID, verifier.txHash, verifier.owner)
	}
	// A client-provided status is a hint only; the RPC receipt remains authoritative.
	if rec := do(http.MethodPost, "/transactions/"+validHash+"/receipt", tok, `{"status":"bogus"}`); rec.Code != http.StatusOK {
		t.Errorf("untrusted status hint = %d, want 200 with RPC proof", rec.Code)
	}
}

func TestWriteHandlers_VerificationAndOwnerConflicts(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	mw := auth.Middleware(auth.NewVerifier(writeTestSecret))
	validHash := "0x" + strings.Repeat("b", 64)
	owner := "0x000000000000000000000000000000000000dead"
	token := signWeb3(t, owner)

	do := func(writer *stubWriter, verifier TransactionVerifier, path string) int {
		router := Router(NewService(stubReader{}).WithWrites(writer, mw, verifier))
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"chainId":11155111,"txHash":"`+validHash+`"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := do(&stubWriter{}, nil, "/transactions"); got != http.StatusServiceUnavailable {
		t.Errorf("nil verifier = %d, want 503", got)
	}
	if got := do(&stubWriter{}, &stubTransactionVerifier{submittedErr: ErrSenderMismatch}, "/transactions"); got != http.StatusForbidden {
		t.Errorf("sender mismatch = %d, want 403", got)
	}
	if got := do(&stubWriter{}, &stubTransactionVerifier{submittedErr: ErrTransactionNotFound}, "/transactions"); got != http.StatusConflict {
		t.Errorf("missing tx = %d, want 409", got)
	}
	if got := do(&stubWriter{}, &stubTransactionVerifier{submittedErr: ErrVerificationUnavailable}, "/transactions"); got != http.StatusServiceUnavailable {
		t.Errorf("RPC unavailable = %d, want 503", got)
	}
	if got := do(&stubWriter{err: ErrTransactionOwnerConflict}, &stubTransactionVerifier{}, "/transactions"); got != http.StatusConflict {
		t.Errorf("immutable owner conflict = %d, want 409", got)
	}
}

// GET /web3-events/transactions is mounted OUTSIDE the web3 guard and returns
// every wallet's rows to anonymous callers. The attempt columns therefore must
// never reach TransactionRow — failure detail and internal chain bookkeeping
// would be published cross-user.
func TestPublicTransactionRowKeySetIsFrozen(t *testing.T) {
	row := TransactionRow{ID: 1, ChainID: 11155111, TxHash: "0xabc", FromAddress: "0xdead"}
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := map[string]struct{}{
		"id": {}, "chainId": {}, "txHash": {}, "fromAddress": {}, "toAddress": {},
		"contractAddress": {}, "value": {}, "gasUsed": {}, "gasPrice": {},
		"blockNumber": {}, "status": {}, "txType": {}, "metadata": {}, "createdAt": {},
	}
	for key := range decoded {
		if _, ok := want[key]; !ok {
			t.Errorf("TransactionRow gained the public key %q — this route is unauthenticated", key)
		}
	}
	for key := range want {
		if _, ok := decoded[key]; !ok {
			t.Errorf("TransactionRow lost the public key %q", key)
		}
	}
	for _, forbidden := range []string{"failureDetail", "failureCode", "actionId", "senderNonce", "blockHash", "checkAttempts"} {
		if _, leaked := decoded[forbidden]; leaked {
			t.Errorf("%q reached the unauthenticated response", forbidden)
		}
	}
}

// A caller that supplies no clientActionId must never be able to reach the 409.
// Today's frontend supplies none, and it resets its only dedup refs on any
// non-2xx, so a reachable 409 would become a re-POST loop.
func TestSubmitWithoutAClientActionIDCannotReachTheReuseConflict(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	mw := auth.Middleware(auth.NewVerifier(writeTestSecret))
	hash := "0x" + strings.Repeat("a", 64)
	dead := "0x000000000000000000000000000000000000dead"
	to := "0x000000000000000000000000000000000000cafe"
	value := "42"

	wr := &stubWriter{row: TransactionRow{ID: 1, ChainID: 11155111, TxHash: hash, FromAddress: dead}}
	router := Router(NewService(stubReader{}).WithWrites(wr, mw, &stubTransactionVerifier{
		submitted: VerifiedTransaction{ToAddress: &to, Value: &value},
	}))

	req := httptest.NewRequest(http.MethodPost, "/transactions",
		strings.NewReader(`{"chainId":11155111,"txHash":"`+hash+`"}`))
	req.Header.Set("Authorization", "Bearer "+signWeb3(t, dead))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if wr.submitted.ClientSupplied {
		t.Error("a request with no clientActionId must not be marked client-supplied")
	}
	if wr.submitted.ClientActionID != "" {
		t.Errorf("clientActionId = %q, want empty so the server derives it", wr.submitted.ClientActionID)
	}
	if wr.submitted.RequestFingerprint != nil {
		t.Error("no client key means no fingerprint, so a reuse conflict is unreachable")
	}
}

func TestSubmitRejectsAnActionIDThatSquatsTheServerNamespaceOrIsOversized(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	mw := auth.Middleware(auth.NewVerifier(writeTestSecret))
	hash := "0x" + strings.Repeat("a", 64)
	dead := "0x000000000000000000000000000000000000dead"
	to := "0x000000000000000000000000000000000000cafe"
	value := "42"
	token := signWeb3(t, dead)

	post := func(body string) *httptest.ResponseRecorder {
		router := Router(NewService(stubReader{}).WithWrites(
			&stubWriter{}, mw, &stubTransactionVerifier{submitted: VerifiedTransaction{ToAddress: &to, Value: &value}}))
		req := httptest.NewRequest(http.MethodPost, "/transactions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	squat := post(`{"chainId":11155111,"txHash":"` + hash + `","clientActionId":"srv:nonce:11155111:7"}`)
	if squat.Code != http.StatusBadRequest {
		t.Errorf("squatting the server namespace = %d, want 400", squat.Code)
	}
	if !strings.Contains(squat.Body.String(), "CLIENT_ACTION_ID_INVALID") {
		t.Errorf("expected a stable code, got %s", squat.Body.String())
	}

	oversize := post(`{"chainId":11155111,"txHash":"` + hash + `","clientActionId":"` + strings.Repeat("a", 200) + `"}`)
	if oversize.Code != http.StatusBadRequest {
		t.Errorf("oversize clientActionId = %d, want 400", oversize.Code)
	}

	bigFingerprint := post(`{"chainId":11155111,"txHash":"` + hash +
		`","clientActionId":"valid-key-0001","clientFingerprint":"` + strings.Repeat("f", 300) + `"}`)
	if bigFingerprint.Code != http.StatusBadRequest {
		t.Errorf("oversize clientFingerprint = %d, want 400", bigFingerprint.Code)
	}
}

func TestActionConflictsSurfaceAsStableCodes(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	mw := auth.Middleware(auth.NewVerifier(writeTestSecret))
	hash := "0x" + strings.Repeat("a", 64)
	dead := "0x000000000000000000000000000000000000dead"
	to := "0x000000000000000000000000000000000000cafe"
	value := "42"
	token := signWeb3(t, dead)

	for _, tc := range []struct {
		err  error
		code string
	}{
		{ErrClientActionIDReused, "CLIENT_ACTION_ID_REUSED"},
		{ErrActionAttemptBound, "ACTION_ATTEMPT_BOUND"},
		{ErrTransactionOwnerConflict, "TRANSACTION_OWNER_CONFLICT"},
	} {
		router := Router(NewService(stubReader{}).WithWrites(
			&stubWriter{err: tc.err}, mw,
			&stubTransactionVerifier{submitted: VerifiedTransaction{ToAddress: &to, Value: &value}}))
		req := httptest.NewRequest(http.MethodPost, "/transactions",
			strings.NewReader(`{"chainId":11155111,"txHash":"`+hash+`","clientActionId":"a-real-key-01"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict {
			t.Errorf("%v = %d, want 409", tc.err, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.code) {
			t.Errorf("%v body = %s, want code %s", tc.err, rec.Body.String(), tc.code)
		}
	}
}

// The chain-observed facts must reach the writer, or the attempt columns ship
// permanently empty.
func TestChainObservedNonceAndBlockHashReachTheWriter(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	mw := auth.Middleware(auth.NewVerifier(writeTestSecret))
	hash := "0x" + strings.Repeat("a", 64)
	dead := "0x000000000000000000000000000000000000dead"
	to := "0x000000000000000000000000000000000000cafe"
	value := "42"
	nonce := uint64(19)
	blockHash := "0xblockhash"

	wr := &stubWriter{row: TransactionRow{ID: 1}}
	router := Router(NewService(stubReader{}).WithWrites(wr, mw, &stubTransactionVerifier{
		submitted: VerifiedTransaction{ToAddress: &to, Value: &value, SenderNonce: &nonce, BlockHash: &blockHash},
		receipt: VerifiedReceipt{
			ToAddress: &to, Value: &value, Status: "confirmed", BlockNumber: 5,
			SenderNonce: &nonce, BlockHash: &blockHash, Confirmations: 12,
		},
	}))
	token := signWeb3(t, dead)

	req := httptest.NewRequest(http.MethodPost, "/transactions",
		strings.NewReader(`{"chainId":11155111,"txHash":"`+hash+`"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(httptest.NewRecorder(), req)
	if wr.submitted == nil || wr.submitted.SenderNonce == nil || *wr.submitted.SenderNonce != 19 {
		t.Errorf("submit sender nonce = %v, want 19", wr.submitted)
	}

	req = httptest.NewRequest(http.MethodPost, "/transactions/"+hash+"/receipt",
		strings.NewReader(`{"chainId":11155111}`))
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(httptest.NewRecorder(), req)
	if wr.receipt == nil || wr.receipt.Confirmations != 12 || wr.receipt.BlockHash == nil {
		t.Errorf("receipt chain facts did not reach the writer: %+v", wr.receipt)
	}
}
