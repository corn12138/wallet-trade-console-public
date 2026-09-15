package swap

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
)

// fakeLoader is the test-double for DeploymentsLoader. registry maps
// chainID → contracts; a missing chain or missing contract surfaces as
// empty string via deployments.LookupAddress.
type fakeLoader struct {
	registry map[int]deployments.ChainConfig
	err      error
}

func (f fakeLoader) Load() (map[int]deployments.ChainConfig, error) {
	return f.registry, f.err
}

func newLoader(routerAddr string) fakeLoader {
	contracts := map[string]deployments.ContractInfo{}
	if routerAddr != "" {
		contracts["Router"] = deployments.ContractInfo{Address: routerAddr}
	}
	return fakeLoader{registry: map[int]deployments.ChainConfig{
		11155111: {Contracts: contracts},
	}}
}

func TestGetQuote_FallbackWhenRouterMissing(t *testing.T) {
	svc := NewService(newLoader(""))
	got, err := svc.GetQuote(QuoteRequest{
		TokenIn:          "0x000000000000000000000000000000000000aAaA",
		TokenOut:         "0x000000000000000000000000000000000000BbBb",
		AmountIn:         "100",
		TokenInDecimals:  18,
		TokenOutDecimals: 6,
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.RouteSource != "fallback" {
		t.Errorf("routeSource = %q, want fallback", got.RouteSource)
	}
	if got.RouterAddress != nil {
		t.Errorf("routerAddress = %v, want nil", *got.RouterAddress)
	}
	if got.AmountOut != "" || got.MinimumReceived != "" {
		t.Errorf("unavailable quote fabricated display amounts: %+v", got)
	}
	if got.SlippageBps != 50 {
		t.Errorf("slippageBps = %d, want 50", got.SlippageBps)
	}
	if got.PriceImpactPct != 0 {
		t.Errorf("priceImpactPct = %v, want 0", got.PriceImpactPct)
	}
	if !strings.Contains(strings.Join(got.Warnings, "|"), "Router not configured") {
		t.Errorf("warnings = %v, want Router-not-configured", got.Warnings)
	}
	if got.Executable {
		t.Errorf("executable = true, want false (fallback quotes must not be executable)")
	}
	if got.QuoteStatus != QuoteStatusFallback {
		t.Errorf("quoteStatus = %q, want %q", got.QuoteStatus, QuoteStatusFallback)
	}
}

func TestGetQuote_FallbackWhenRouterPresentSaysLiveUnavailable(t *testing.T) {
	// Router is deployed, but live RPC isn't wired (Phase 4n). The
	// warning text differs from the no-router branch so the FE can
	// distinguish "no pool" from "RPC down".
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	got, _ := svc.GetQuote(QuoteRequest{
		TokenIn:          "0x000000000000000000000000000000000000aAaA",
		TokenOut:         "0x000000000000000000000000000000000000BbBb",
		AmountIn:         "1",
		TokenInDecimals:  18,
		TokenOutDecimals: 18,
	})
	if got.RouteSource != "fallback" {
		t.Errorf("routeSource = %q, want fallback", got.RouteSource)
	}
	if !strings.Contains(strings.Join(got.Warnings, "|"), "Live quote unavailable") {
		t.Errorf("warnings = %v, want Live-quote-unavailable", got.Warnings)
	}
}

// stubCaller is the fake EthCaller. For each call it picks the next
// response by selector prefix of dataHex.
type stubCaller struct {
	calls       int
	getAmounts  []byte // raw bytes returned for getAmountsOut
	getReserves []byte // raw bytes returned for getReserves
	failOn      string // selector prefix ("getAmountsOut" / "getReserves") that should error
	failErr     error
}

func (s *stubCaller) EthCall(_ context.Context, _, dataHex, _ string) ([]byte, error) {
	s.calls++
	sel := abi.Selector("getAmountsOut(uint256,address[])")
	getAmountsPrefix := "0x" + hex.EncodeToString(sel[:4])
	if strings.HasPrefix(dataHex, getAmountsPrefix) {
		if s.failOn == "getAmountsOut" {
			return nil, s.failErr
		}
		return s.getAmounts, nil
	}
	if s.failOn == "getReserves" {
		return nil, s.failErr
	}
	return s.getReserves, nil
}

// buildUint256Array encodes [v...] as the raw dynamic-array layout
// (offset 0x20 | len | values).
func buildUint256Array(values []uint64) []byte {
	const wordHex = "0000000000000000000000000000000000000000000000000000000000000000"
	out := "0000000000000000000000000000000000000000000000000000000000000020" // offset 32
	// len
	lenStr := (wordHex)[:64-len(itoa64(uint64(len(values))))] + itoa64(uint64(len(values)))
	out += lenStr
	for _, v := range values {
		h := itoa64(v)
		out += wordHex[:64-len(h)] + h
	}
	b, _ := hex.DecodeString(out)
	return b
}

// itoa64 is base-16 (no "0x"), for embedding in the synthetic encoder.
func itoa64(v uint64) string {
	if v == 0 {
		return "0"
	}
	const digits = "0123456789abcdef"
	buf := [16]byte{}
	idx := len(buf)
	for v > 0 {
		idx--
		buf[idx] = digits[v%16]
		v /= 16
	}
	return string(buf[idx:])
}

// buildTwoUint256s lays out two uint256 reserves back-to-back.
func buildTwoUint256s(a, b uint64) []byte {
	const wordHex = "0000000000000000000000000000000000000000000000000000000000000000"
	ah := itoa64(a)
	bh := itoa64(b)
	out := wordHex[:64-len(ah)] + ah + wordHex[:64-len(bh)] + bh
	r, _ := hex.DecodeString(out)
	return r
}

func TestGetQuote_LiveQuoteWhenRPCClientWired(t *testing.T) {
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	stub := &stubCaller{
		// amountIn=1e18, path=[a,b]. amounts=[1e18, 950]. amountOut last = 950.
		getAmounts:  buildUint256Array([]uint64{1_000_000_000_000_000_000, 950}),
		getReserves: buildTwoUint256s(1_000_000, 2_000_000),
	}
	svc.SetRPCClient(stub)
	got, err := svc.GetQuote(QuoteRequest{
		TokenIn:          "0x000000000000000000000000000000000000aAaA",
		TokenOut:         "0x000000000000000000000000000000000000BbBb",
		AmountIn:         "1",
		TokenInDecimals:  18,
		TokenOutDecimals: 0,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.RouteSource != "router" {
		t.Errorf("routeSource = %q, want router", got.RouteSource)
	}
	if got.AmountOutRaw != "950" {
		t.Errorf("amountOutRaw = %q, want 950", got.AmountOutRaw)
	}
	// 950 × (10000 - 50) / 10000 = 945 (50bps default slippage).
	if got.MinimumReceivedRaw != "945" {
		t.Errorf("minimumReceivedRaw = %q, want 945", got.MinimumReceivedRaw)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("warnings = %v, want empty", got.Warnings)
	}
	if stub.calls != 2 {
		t.Errorf("EthCall calls = %d, want 2", stub.calls)
	}
	if !got.Executable {
		t.Errorf("executable = false, want true (live router quote)")
	}
	if got.QuoteStatus != QuoteStatusLive {
		t.Errorf("quoteStatus = %q, want %q", got.QuoteStatus, QuoteStatusLive)
	}
}

func TestGetQuote_LiveQuoteRPCErrorFallsBack(t *testing.T) {
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	stub := &stubCaller{failOn: "getAmountsOut", failErr: errors.New("boom")}
	svc.SetRPCClient(stub)
	got, _ := svc.GetQuote(QuoteRequest{
		TokenIn:          "0x000000000000000000000000000000000000aAaA",
		TokenOut:         "0x000000000000000000000000000000000000BbBb",
		AmountIn:         "1",
		TokenInDecimals:  18,
		TokenOutDecimals: 18,
	})
	if got.RouteSource != "fallback" {
		t.Errorf("routeSource = %q, want fallback (RPC failed)", got.RouteSource)
	}
	// The degraded quote must be machine-readably non-executable — an
	// RPC outage silently turning into an executable made-up estimate is
	// exactly the failure mode this field exists to prevent.
	if got.Executable {
		t.Errorf("executable = true after RPC failure, want false")
	}
	if got.QuoteStatus != QuoteStatusFallback {
		t.Errorf("quoteStatus = %q, want %q", got.QuoteStatus, QuoteStatusFallback)
	}
}

func TestGetQuote_NoRPCClientNeverExecutable(t *testing.T) {
	// Missing RPC env (SetRPCClient never called) + deployed router: the
	// quote must degrade to a non-executable fallback, proving there is
	// no configuration where an un-verified estimate can drive a swap.
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	got, err := svc.GetQuote(QuoteRequest{
		TokenIn:          "0x000000000000000000000000000000000000aAaA",
		TokenOut:         "0x000000000000000000000000000000000000BbBb",
		AmountIn:         "5",
		TokenInDecimals:  18,
		TokenOutDecimals: 18,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Executable || got.QuoteStatus != QuoteStatusFallback {
		t.Errorf("got executable=%v status=%q, want non-executable fallback", got.Executable, got.QuoteStatus)
	}
}

func TestGetQuote_RejectsSameToken(t *testing.T) {
	svc := NewService(newLoader(""))
	_, err := svc.GetQuote(QuoteRequest{
		TokenIn:          "0x000000000000000000000000000000000000aAaA",
		TokenOut:         "0x000000000000000000000000000000000000AAAA", // same address, different case
		AmountIn:         "1",
		TokenInDecimals:  18,
		TokenOutDecimals: 18,
	})
	if err != ErrSameToken {
		t.Errorf("err = %v, want ErrSameToken", err)
	}
}

func TestGetQuote_RejectsBadAddress(t *testing.T) {
	svc := NewService(newLoader(""))
	_, err := svc.GetQuote(QuoteRequest{
		TokenIn:  "0xbadaddress",
		TokenOut: "0x000000000000000000000000000000000000BbBb",
		AmountIn: "1",
	})
	if err != ErrInvalidAddress {
		t.Errorf("err = %v, want ErrInvalidAddress", err)
	}
}

func TestBuildApproveTx_DefaultsToRouterSpender(t *testing.T) {
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	got, err := svc.BuildApproveTx(BuildApproveRequest{
		TokenAddress:  "0x000000000000000000000000000000000000aAaA",
		Amount:        "5",
		TokenDecimals: 18,
	})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.ChainID != 11155111 {
		t.Errorf("chainId = %d, want 11155111", got.ChainID)
	}
	if got.To != "0x000000000000000000000000000000000000aAaA" {
		t.Errorf("to = %q, want token address", got.To)
	}
	if got.Value != "0" {
		t.Errorf("value = %q, want 0", got.Value)
	}
	// approve(0x...1111, 5e18) — selector 0x095ea7b3
	if !strings.HasPrefix(got.Data, "0x095ea7b3") {
		t.Errorf("data prefix = %q, want 0x095ea7b3...", got.Data[:10])
	}
	// 5e18 = 0x4563918244F40000 — must appear in the trailing slot.
	if !strings.Contains(strings.ToLower(got.Data), "4563918244f40000") {
		t.Errorf("data missing 5e18 encoding: %s", got.Data)
	}
	// spender 0x...1111 must be left-padded in the first slot.
	if !strings.Contains(got.Data, "0000000000000000000000000000000000001111") {
		t.Errorf("data missing spender padding: %s", got.Data)
	}
}

func TestBuildApproveTx_ExplicitSpenderOverridesRouter(t *testing.T) {
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	custom := "0x0000000000000000000000000000000000002222"
	got, err := svc.BuildApproveTx(BuildApproveRequest{
		TokenAddress:  "0x000000000000000000000000000000000000aAaA",
		Amount:        "1",
		TokenDecimals: 6,
		Spender:       &custom,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(got.Data, "0000000000000000000000000000000000002222") {
		t.Errorf("data missing custom spender: %s", got.Data)
	}
}

func TestBuildApproveTx_NoRouterAndNoSpenderIs404(t *testing.T) {
	svc := NewService(newLoader(""))
	_, err := svc.BuildApproveTx(BuildApproveRequest{
		TokenAddress:  "0x000000000000000000000000000000000000aAaA",
		Amount:        "1",
		TokenDecimals: 6,
	})
	if err != ErrRouterNotConfigured {
		t.Errorf("err = %v, want ErrRouterNotConfigured", err)
	}
}

func TestBuildApproveTx_BadTokenAddress(t *testing.T) {
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	_, err := svc.BuildApproveTx(BuildApproveRequest{
		TokenAddress:  "0xbad",
		Amount:        "1",
		TokenDecimals: 6,
	})
	if err != ErrInvalidAddress {
		t.Errorf("err = %v, want ErrInvalidAddress", err)
	}
}

func TestBuildSwapTx_EncodesDynamicPathArg(t *testing.T) {
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	pinnedNow := time.Unix(1700000000, 0)
	got, err := svc.BuildSwapTx(BuildSwapRequest{
		TokenIn:          "0x000000000000000000000000000000000000aAaA",
		TokenOut:         "0x000000000000000000000000000000000000BbBb",
		AmountIn:         "1",
		AmountOutMin:     "0",
		TokenInDecimals:  18,
		TokenOutDecimals: 18,
		Recipient:        "0x000000000000000000000000000000000000bEEF",
	}, pinnedNow)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.To != "0x0000000000000000000000000000000000001111" {
		t.Errorf("to = %q, want router address", got.To)
	}
	// selector for swapExactTokensForTokens(uint256,uint256,address[],address,uint256)
	if !strings.HasPrefix(got.Data, "0x38ed1739") {
		t.Errorf("data prefix = %q, want 0x38ed1739...", got.Data[:10])
	}
	// path offset = 160 (0xa0). 5 args * 32 bytes head section.
	if !strings.Contains(got.Data, "00000000000000000000000000000000000000000000000000000000000000a0") {
		t.Errorf("data missing 0xa0 offset slot for path: %s", got.Data)
	}
	// path length = 2
	if !strings.Contains(got.Data, "0000000000000000000000000000000000000000000000000000000000000002") {
		t.Errorf("data missing path length=2: %s", got.Data)
	}
	// recipient (right-most 20 bytes of one head slot must end in beef)
	if !strings.Contains(got.Data, "000000000000000000000000000000000000beef") {
		t.Errorf("data missing recipient padding: %s", got.Data)
	}
}

func TestBuildSwapTx_DeadlineUsesNowPlusOffset(t *testing.T) {
	// Default 20-minute deadline: now + 1200s.
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	pinnedNow := time.Unix(1_700_000_000, 0)
	expectedDeadline := pinnedNow.Unix() + 1200 // 1_700_001_200 = 0x6553F5B0
	got, err := svc.BuildSwapTx(BuildSwapRequest{
		TokenIn:          "0x000000000000000000000000000000000000aAaA",
		TokenOut:         "0x000000000000000000000000000000000000BbBb",
		AmountIn:         "1",
		AmountOutMin:     "0",
		TokenInDecimals:  18,
		TokenOutDecimals: 18,
		Recipient:        "0x000000000000000000000000000000000000bEEF",
	}, pinnedNow)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	wantHex := strings.ToLower(strings.TrimPrefix(formatUint256Hex(expectedDeadline), "0x"))
	if !strings.Contains(strings.ToLower(got.Data), wantHex) {
		t.Errorf("data missing deadline %d (hex %s): %s", expectedDeadline, wantHex, got.Data)
	}
}

func TestBuildSwapTx_NoRouterReturns404(t *testing.T) {
	svc := NewService(newLoader(""))
	_, err := svc.BuildSwapTx(BuildSwapRequest{
		TokenIn:          "0x000000000000000000000000000000000000aAaA",
		TokenOut:         "0x000000000000000000000000000000000000BbBb",
		AmountIn:         "1",
		AmountOutMin:     "0",
		TokenInDecimals:  18,
		TokenOutDecimals: 18,
		Recipient:        "0x000000000000000000000000000000000000bEEF",
	}, time.Now())
	if err != ErrRouterNotConfigured {
		t.Errorf("err = %v, want ErrRouterNotConfigured", err)
	}
}

func TestHandler_BuildApproveReturnsJSON(t *testing.T) {
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	mux := Router(svc)
	body := `{"tokenAddress":"0x000000000000000000000000000000000000aAaA","amount":"5","tokenDecimals":18}`
	req := httptest.NewRequest(http.MethodPost, "/build-approve", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp TxResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(resp.Data, "0x095ea7b3") {
		t.Errorf("data prefix = %q, want 0x095ea7b3...", resp.Data[:10])
	}
}

func TestHandler_QuoteReturnsFallbackJSON(t *testing.T) {
	svc := NewService(newLoader(""))
	mux := Router(svc)
	body := `{"tokenIn":"0x000000000000000000000000000000000000aAaA","tokenOut":"0x000000000000000000000000000000000000BbBb","amountIn":"1","tokenInDecimals":18,"tokenOutDecimals":6}`
	req := httptest.NewRequest(http.MethodPost, "/quote", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp QuoteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RouteSource != "fallback" {
		t.Errorf("routeSource = %q, want fallback", resp.RouteSource)
	}
}

func TestHandler_QuoteBadInputReturns400(t *testing.T) {
	svc := NewService(newLoader(""))
	mux := Router(svc)
	body := `{"tokenIn":"0xbad","tokenOut":"0xalso-bad","amountIn":"1"}`
	req := httptest.NewRequest(http.MethodPost, "/quote", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_BuildApprove404WhenRouterMissing(t *testing.T) {
	svc := NewService(newLoader(""))
	mux := Router(svc)
	body := `{"tokenAddress":"0x000000000000000000000000000000000000aAaA","amount":"1","tokenDecimals":18}`
	req := httptest.NewRequest(http.MethodPost, "/build-approve", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_BuildSwapMalformedBodyReturns400(t *testing.T) {
	svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
	mux := Router(svc)
	req := httptest.NewRequest(http.MethodPost, "/build-swap", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// formatUint256Hex produces a 64-hex string representation of n,
// equivalent to abi.EncodeUint256 → hex without the 0x prefix. Used
// to assert presence of specific slot values inside the encoded call.
func formatUint256Hex(n int64) string {
	const hexits = "0123456789abcdef"
	out := make([]byte, 64)
	for i := 63; i >= 0; i-- {
		out[i] = hexits[n&0xf]
		n >>= 4
	}
	return string(out)
}
