package txreview

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

// smokeAddr is the authenticated wallet the handler owner-pins fromAddress to.
const smokeAddr = "0x000000000000000000000000000000000000aaaa"

// authedReview wraps a POST /tx-review request with the given authenticated
// wallet on its context, mirroring what the web3 middleware injects. The
// handler owner-pins fromAddress to this address (auth.ResolveOwner).
func authedReview(body string, authAddr string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/tx-review", strings.NewReader(body))
	if authAddr != "" {
		req = req.WithContext(auth.WithAddress(req.Context(), authAddr))
	}
	return req
}

// fakeSwap stubs SwapPreparer.
type fakeSwap struct {
	quote      SwapQuote
	quoteErr   error
	swapTx     TxShape
	swapErr    error
	approveTx  TxShape
	approveErr error

	gotApprove SwapApproveRequest
	gotSwap    SwapBuildRequest
	gotQuote   SwapQuoteRequest
}

func (f *fakeSwap) GetQuote(req SwapQuoteRequest) (SwapQuote, error) {
	f.gotQuote = req
	return f.quote, f.quoteErr
}
func (f *fakeSwap) BuildSwapTx(req SwapBuildRequest) (TxShape, error) {
	f.gotSwap = req
	return f.swapTx, f.swapErr
}
func (f *fakeSwap) BuildApproveTx(req SwapApproveRequest) (TxShape, error) {
	f.gotApprove = req
	return f.approveTx, f.approveErr
}

// fakeEarn stubs EarnPreparer.
type fakeEarn struct {
	depositTx, withdrawTx   TxShape
	depositErr, withdrawErr error
	tokenAddress            string
}

func (f *fakeEarn) BuildDepositTx(_ context.Context, _, _ string, _ int) (TxShape, error) {
	return f.depositTx, f.depositErr
}
func (f *fakeEarn) BuildWithdrawTx(_ context.Context, _, _ string, _ int) (TxShape, error) {
	return f.withdrawTx, f.withdrawErr
}
func (f *fakeEarn) FindProductTokenAddress(_ context.Context, _ string, _ int) (string, error) {
	return f.tokenAddress, nil
}

// fakeTokens stubs TokenIntelLookup.
type fakeTokens struct {
	rows map[string]TokenIntelRow
	err  error
}

func (f *fakeTokens) GetTokenIntel(_ context.Context, _ int, _ []string) (map[string]TokenIntelRow, error) {
	return f.rows, f.err
}

// fakeSites stubs ConnectedSiteLookup.
type fakeSites struct {
	site *ConnectedSiteDetail
}

func (f *fakeSites) FindConnectedSite(_ context.Context, _ string, _ int, _ string) (*ConnectedSiteDetail, error) {
	return f.site, nil
}

// fakeKnownSpenders stubs KnownSpendersLookup.
type fakeKnown struct{ spenders []string }

func (f fakeKnown) KnownSpenders(_ int) []string { return f.spenders }

// stubRPCSimple provides successful estimateGas + gasPrice + balance for orchestration tests.
type stubRPCSimple struct {
	estimateGas, gasPrice, balance *big.Int
	allowanceResult                []byte
}

func (s *stubRPCSimple) EstimateGas(_ context.Context, _, _, _, _ string) (*big.Int, error) {
	return s.estimateGas, nil
}
func (s *stubRPCSimple) GetGasPrice(_ context.Context) (*big.Int, error) { return s.gasPrice, nil }
func (s *stubRPCSimple) GetBalance(_ context.Context, _, _ string) (*big.Int, error) {
	return s.balance, nil
}
func (s *stubRPCSimple) EthCall(_ context.Context, _, _, _ string) ([]byte, error) {
	if s.allowanceResult != nil {
		return s.allowanceResult, nil
	}
	return []byte{0x01}, nil
}

func ptrStr(s string) *string { return &s }
func ptrInt(n int) *int       { return &n }

func TestReview_CustomOp_Happy(t *testing.T) {
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18)},
		&fakeSwap{}, &fakeEarn{}, nil, nil, fakeKnown{}, nil)
	in := ReviewInput{
		OperationType: OpCustom,
		FromAddress:   "0x000000000000000000000000000000000000aaaa",
		Tx:            &TxShape{To: "0x000000000000000000000000000000000000beef", Data: "0xdeadbeef"},
	}
	out, err := svc.ReviewTransaction(context.Background(), in)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// Custom op sets spender = tx.to. With no KnownSpenders configured
	// the spender-rule fires unknown-spender (warn) → status is warning
	// (matches the NestJS behavior for an unknown to address).
	if out.ReviewStatus != ReviewWarning {
		t.Errorf("status = %s, want warning (unknown-spender)", out.ReviewStatus)
	}
	if out.GeneratedTx.To != "0x000000000000000000000000000000000000beef" {
		t.Errorf("tx.to = %q", out.GeneratedTx.To)
	}
	if out.OperationType != OpCustom {
		t.Errorf("op = %s", out.OperationType)
	}
	if out.AllowanceChange != nil {
		t.Errorf("allowanceChange should be nil for custom op")
	}
}

func TestReview_CustomOp_KnownSpenderApproves(t *testing.T) {
	known := fakeKnown{spenders: []string{"0x000000000000000000000000000000000000beef"}}
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18)},
		&fakeSwap{}, &fakeEarn{}, nil, nil, known, nil)
	out, err := svc.ReviewTransaction(context.Background(), ReviewInput{
		OperationType: OpCustom,
		FromAddress:   "0x000000000000000000000000000000000000aaaa",
		Tx:            &TxShape{To: "0x000000000000000000000000000000000000beef", Data: "0xdead"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.ReviewStatus != ReviewApproved {
		t.Errorf("status = %s, want approved", out.ReviewStatus)
	}
}

func TestReview_CustomOp_RequiresTx(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	_, err := svc.ReviewTransaction(context.Background(), ReviewInput{
		OperationType: OpCustom,
		FromAddress:   "0x000000000000000000000000000000000000aaaa",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestReview_ApproveOp_BuildsAllowanceChange(t *testing.T) {
	// fake approve tx with spender 0x...beef + amount 5e18
	spenderArg, _ := abi.EncodeAddress("0x000000000000000000000000000000000000beef")
	amountArg, _ := abi.EncodeUint256(new(big.Int).SetUint64(5_000_000_000_000_000_000))
	data := abi.EncodeCall("approve(address,uint256)", spenderArg, amountArg)

	swap := &fakeSwap{approveTx: TxShape{ChainID: 11155111, To: "0x000000000000000000000000000000000000feed", Value: "0", Data: data}}
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18), allowanceResult: make([]byte, 32)},
		swap, &fakeEarn{}, nil, nil, fakeKnown{spenders: []string{"0x000000000000000000000000000000000000beef"}}, nil)

	out, err := svc.ReviewTransaction(context.Background(), ReviewInput{
		OperationType: OpApprove,
		FromAddress:   "0x000000000000000000000000000000000000aaaa",
		TokenAddress:  ptrStr("0x000000000000000000000000000000000000feed"),
		Amount:        ptrStr("5"),
		TokenDecimals: ptrInt(18),
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.AllowanceChange == nil || out.AllowanceChange.Spender != "0x000000000000000000000000000000000000beef" {
		t.Errorf("allowanceChange = %+v", out.AllowanceChange)
	}
	if out.AllowanceChange.RequestedAllowance == nil || *out.AllowanceChange.RequestedAllowance != "5000000000000000000" {
		t.Errorf("requestedAllowance = %v", out.AllowanceChange.RequestedAllowance)
	}
	// Known spender → spender-known pass; unlimited check should NOT trip
	for _, c := range out.Checks {
		if c.ID == "max-approval" {
			t.Errorf("5e18 should not trip max-approval: %+v", c)
		}
	}
}

func TestReview_RevokeOp_EncodesZeroApprove(t *testing.T) {
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18), allowanceResult: make([]byte, 32)},
		&fakeSwap{}, &fakeEarn{}, nil, nil, fakeKnown{}, nil)
	out, err := svc.ReviewTransaction(context.Background(), ReviewInput{
		OperationType: OpRevokeApproval,
		FromAddress:   "0x000000000000000000000000000000000000aaaa",
		TokenAddress:  ptrStr("0x000000000000000000000000000000000000feed"),
		Spender:       ptrStr("0x000000000000000000000000000000000000beef"),
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// Data should start with the approve selector and end with all-zero amount.
	if !strings.HasPrefix(out.GeneratedTx.Data, "0x095ea7b3") {
		t.Errorf("data prefix = %q, want approve selector", out.GeneratedTx.Data[:10])
	}
	zeroTail := strings.ToLower(out.GeneratedTx.Data[len(out.GeneratedTx.Data)-64:])
	if zeroTail != strings.Repeat("0", 64) {
		t.Errorf("amount slot not zero: %q", zeroTail)
	}
}

func TestReview_SwapOp_DelegatesToPreparer(t *testing.T) {
	bps := 50
	swap := &fakeSwap{
		quote:  SwapQuote{RouterAddress: "0x000000000000000000000000000000000000feed", MinimumReceived: "990", SlippageBps: 50, Warnings: nil},
		swapTx: TxShape{ChainID: 11155111, To: "0x000000000000000000000000000000000000feed", Value: "0", Data: "0xdead"},
	}
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18)},
		swap, &fakeEarn{}, nil, nil, fakeKnown{}, nil)
	out, err := svc.ReviewTransaction(context.Background(), ReviewInput{
		OperationType:    OpSwap,
		FromAddress:      "0x000000000000000000000000000000000000aaaa",
		TokenIn:          ptrStr("0x000000000000000000000000000000000000aaaa"),
		TokenOut:         ptrStr("0x000000000000000000000000000000000000bbbb"),
		Amount:           ptrStr("1"),
		TokenInDecimals:  ptrInt(18),
		TokenOutDecimals: ptrInt(18),
		SlippageBps:      &bps,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if swap.gotQuote.AmountIn != "1" {
		t.Errorf("quote not called: %+v", swap.gotQuote)
	}
	if swap.gotSwap.AmountOutMin != "990" {
		t.Errorf("amountOutMin = %q, want 990 from quote", swap.gotSwap.AmountOutMin)
	}
	if out.GeneratedTx.Data != "0xdead" {
		t.Errorf("tx.data = %q", out.GeneratedTx.Data)
	}
}

func TestReview_EarnDeposit_DelegatesToPreparer(t *testing.T) {
	earn := &fakeEarn{depositTx: TxShape{ChainID: 11155111, To: "0x000000000000000000000000000000000000feed", Data: "0xa694fc3a"}, tokenAddress: "0x000000000000000000000000000000000000aaaa"}
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18)},
		&fakeSwap{}, earn, nil, nil, fakeKnown{}, nil)
	out, err := svc.ReviewTransaction(context.Background(), ReviewInput{
		OperationType: OpEarnDeposit,
		FromAddress:   "0x000000000000000000000000000000000000aaaa",
		ProductID:     ptrStr("pool-1"),
		Amount:        ptrStr("100"),
		TokenDecimals: ptrInt(18),
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if out.GeneratedTx.To != "0x000000000000000000000000000000000000feed" {
		t.Errorf("tx.to = %q", out.GeneratedTx.To)
	}
}

func TestReview_TokenIntel_AppendsWarnings(t *testing.T) {
	tokens := &fakeTokens{rows: map[string]TokenIntelRow{
		"0x000000000000000000000000000000000000feed": {Address: "0x000000000000000000000000000000000000feed", Status: "ACTIVE", IsOfficial: false, Tags: []string{"scam"}},
	}}
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18), allowanceResult: make([]byte, 32)},
		&fakeSwap{approveTx: TxShape{ChainID: 11155111, To: "0x000000000000000000000000000000000000feed", Value: "0", Data: "0x" + hex.EncodeToString(append(abi.Selector("approve(address,uint256)"), make([]byte, 64)...))}},
		&fakeEarn{}, tokens, nil, fakeKnown{}, nil)
	out, _ := svc.ReviewTransaction(context.Background(), ReviewInput{
		OperationType: OpApprove,
		FromAddress:   "0x000000000000000000000000000000000000aaaa",
		TokenAddress:  ptrStr("0x000000000000000000000000000000000000feed"),
		Amount:        ptrStr("1"),
		TokenDecimals: ptrInt(18),
	})
	// Token-rule short-circuits on isSupportedToken=false (matches NestJS) —
	// only unsupported-token fires, not token-warning.
	hasUnsupported := false
	for _, c := range out.Checks {
		if c.ID == "unsupported-token" {
			hasUnsupported = true
		}
	}
	if !hasUnsupported {
		t.Errorf("checks missing unsupported-token: %+v", out.Checks)
	}
	if out.ReviewStatus != ReviewBlocked {
		t.Errorf("status = %s, want blocked (unsupported-token fail)", out.ReviewStatus)
	}
}

func TestReview_ConnectedSiteRiskAppendsCheck(t *testing.T) {
	site := &ConnectedSiteDetail{ID: "s1", Origin: "https://x.com", SiteName: "X", RiskLevel: "Critical", Permissions: []string{"transfer"}}
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18)},
		&fakeSwap{}, &fakeEarn{}, nil, &fakeSites{site: site}, fakeKnown{}, nil)
	out, _ := svc.ReviewTransaction(context.Background(), ReviewInput{
		OperationType: OpCustom,
		FromAddress:   "0x000000000000000000000000000000000000aaaa",
		SiteOrigin:    ptrStr("x.com"),
		Tx:            &TxShape{To: "0x000000000000000000000000000000000000beef", Data: "0xdead"},
	})
	if out.ConnectedSite == nil || out.ConnectedSite.SiteName != "X" {
		t.Errorf("connectedSite = %+v", out.ConnectedSite)
	}
	hasSiteCheck := false
	for _, c := range out.Checks {
		if c.ID == "connected-site-risk" {
			hasSiteCheck = true
		}
	}
	if !hasSiteCheck {
		t.Errorf("missing connected-site-risk check")
	}
	if out.ReviewStatus != ReviewWarning {
		t.Errorf("status = %s, want warning", out.ReviewStatus)
	}
	// Permissions appended to recommended actions
	hasPerms := false
	for _, a := range out.RecommendedActions {
		if strings.Contains(a, "Connected site permissions") {
			hasPerms = true
		}
	}
	if !hasPerms {
		t.Errorf("missing permissions action: %+v", out.RecommendedActions)
	}
}

func TestHandler_BadJSON400(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	Router(svc, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tx-review", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d", rec.Code)
	}
}

func TestHandler_InvalidInput400(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(ReviewInput{OperationType: OpCustom, FromAddress: smokeAddr})
	Router(svc, nil).ServeHTTP(rec, authedReview(string(body), smokeAddr))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("code = %d", rec.Code)
	}
}

// TestHandler_OwnerPin proves the handler owner-pins fromAddress to the
// authenticated wallet (parity with NestJS resolveAuthenticatedOwnerAddress):
// no auth context → 401; a fromAddress that mismatches the JWT wallet → 403.
func TestHandler_OwnerPin(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	body, _ := json.Marshal(ReviewInput{
		OperationType: OpCustom,
		FromAddress:   smokeAddr,
		Tx:            &TxShape{To: "0x000000000000000000000000000000000000beef", Data: "0xdead"},
	})

	// No authenticated address → 401 (must not review unauthenticated).
	rec := httptest.NewRecorder()
	Router(svc, nil).ServeHTTP(rec, authedReview(string(body), ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth = %d, want 401", rec.Code)
	}

	// Authenticated as a different wallet than fromAddress → 403 (no IDOR).
	rec = httptest.NewRecorder()
	Router(svc, nil).ServeHTTP(rec, authedReview(string(body), "0x000000000000000000000000000000000000bEEF"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("owner-mismatch = %d, want 403", rec.Code)
	}
}

func TestHandler_HappyPath200(t *testing.T) {
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18)},
		&fakeSwap{}, &fakeEarn{}, nil, nil, fakeKnown{}, nil)
	body, _ := json.Marshal(ReviewInput{
		OperationType: OpCustom,
		FromAddress:   smokeAddr,
		Tx:            &TxShape{To: "0x000000000000000000000000000000000000beef", Data: "0xdead"},
	})
	rec := httptest.NewRecorder()
	Router(svc, nil).ServeHTTP(rec, authedReview(string(body), smokeAddr))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out Result
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OperationType != OpCustom {
		t.Errorf("op = %s", out.OperationType)
	}
}
