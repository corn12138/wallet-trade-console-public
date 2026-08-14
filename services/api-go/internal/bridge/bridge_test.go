package bridge

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
)

// The predecessor of this file tested a simulated route engine that invented
// amounts, fees and ETAs. Those tests are gone with their subject. What is
// pinned here is the property that replaced them: the API may only call a route
// executable when the CHAIN says it is, and every refusal must name its reason.

const (
	sepolia   = 11155111
	baseSep   = 84532
	srcGW     = "0x2d68a51fb4c3f3ac26fa48b8a457d150132f185d"
	dstGW     = "0x1111111111111111111111111111111111111111"
	srcToken  = "0x57e554d795a18f3ca0a0e9e03a17ac3c509c3bf8"
	dstToken  = "0x2222222222222222222222222222222222222222"
	recipient = "0x3333333333333333333333333333333333333333"
)

// fakeCaller answers eth_call by selector prefix.
type fakeCaller struct {
	route     []byte
	paused    []byte
	liquidity []byte
	err       error
}

func (f *fakeCaller) EthCall(_ context.Context, _, data, _ string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	switch {
	case strings.HasPrefix(data, selRoute):
		return f.route, nil
	case strings.HasPrefix(data, selPaused):
		return f.paused, nil
	case strings.HasPrefix(data, selAvailableLiquidity):
		return f.liquidity, nil
	}
	return nil, errors.New("fakeCaller: unexpected selector")
}

func word(v *big.Int) []byte {
	b := make([]byte, 32)
	v.FillBytes(b)
	return b
}

func boolWord(b bool) []byte {
	if b {
		return word(big.NewInt(1))
	}
	return word(big.NewInt(0))
}

func addrWord(addr string) []byte {
	raw, _ := hex.DecodeString(strings.TrimPrefix(addr, "0x"))
	b := make([]byte, 32)
	copy(b[32-len(raw):], raw)
	return b
}

func routeResponse(supported bool, dst string, minAmount *big.Int) []byte {
	out := append([]byte{}, boolWord(supported)...)
	out = append(out, addrWord(dst)...)
	return append(out, word(minAmount)...)
}

// twoChainRegistry models a fully-deployed route: gateways AND rpc on both ends.
func twoChainRegistry(src, dst *fakeCaller) *Registry {
	chains := map[int]deployments.ChainConfig{
		sepolia: {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: srcGW}}},
		baseSep: {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: dstGW}}},
	}
	return NewRegistry(chains, map[int]EthCaller{sepolia: src, baseSep: dst})
}

func healthyCallers() (*fakeCaller, *fakeCaller) {
	src := &fakeCaller{
		route:  routeResponse(true, dstToken, big.NewInt(1)),
		paused: boolWord(false),
	}
	dst := &fakeCaller{liquidity: word(big.NewInt(1_000_000))}
	return src, dst
}

func fixedNow() time.Time { return time.Date(2026, 8, 8, 13, 0, 0, 0, time.UTC) }

func newSvc(reg *Registry) *Service {
	return NewService(reg, NewStore(nil), fixedNow)
}

func routeReq(amount string) RoutesRequest {
	return RoutesRequest{
		FromChainID: sepolia, ToChainID: baseSep,
		TokenSymbol: "mUSDC", SrcToken: srcToken, Amount: amount,
	}
}

// ─── Executable only when the chain says so ─────────────────────────────────

func TestRouteIsExecutableWhenChainStateAllowsIt(t *testing.T) {
	svc := newSvc(twoChainRegistry(healthyCallers()))

	resp, err := svc.GetRoutes(context.Background(), routeReq("1000"))
	if err != nil {
		t.Fatalf("GetRoutes: %v", err)
	}
	if !resp.Executable || resp.ExecutionMode != "live" {
		t.Fatalf("want live/executable, got mode=%q executable=%v routes=%+v", resp.ExecutionMode, resp.Executable, resp.Routes)
	}
	r := resp.Routes[0]
	if len(r.Blockers) != 0 {
		t.Errorf("executable route must have no blockers, got %v", r.Blockers)
	}
	if r.DstToken != dstToken {
		t.Errorf("dstToken: want %s, got %s", dstToken, r.DstToken)
	}
	if r.DstLiquidity != "1000000" {
		t.Errorf("dstLiquidity: want the real balance, got %q", r.DstLiquidity)
	}
	// Lock-and-release takes no protocol fee: out must equal in, not a
	// fabricated 99.5% like the simulated engine used to return.
	if r.AmountOut != r.AmountIn {
		t.Errorf("amountOut %s != amountIn %s — the gateway takes no fee", r.AmountOut, r.AmountIn)
	}
	if r.TrustModel != TrustModel || resp.TrustModel != TrustModel {
		t.Error("every response must carry the trust model")
	}
}

// ─── Every refusal names its reason ─────────────────────────────────────────

func TestBlockersNameEveryReason(t *testing.T) {
	cases := []struct {
		name    string
		reg     func() *Registry
		amount  string
		blocker string
	}{
		{
			name: "no counterpart gateway deployed",
			reg: func() *Registry {
				src, _ := healthyCallers()
				chains := map[int]deployments.ChainConfig{
					sepolia: {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: srcGW}}},
				}
				return NewRegistry(chains, map[int]EthCaller{sepolia: src})
			},
			amount:  "1000",
			blocker: "NO_DESTINATION_GATEWAY",
		},
		{
			name: "route not configured on-chain",
			reg: func() *Registry {
				src, dst := healthyCallers()
				src.route = routeResponse(false, "0x0000000000000000000000000000000000000000", big.NewInt(0))
				return twoChainRegistry(src, dst)
			},
			amount:  "1000",
			blocker: "ROUTE_NOT_CONFIGURED",
		},
		{
			name: "gateway paused",
			reg: func() *Registry {
				src, dst := healthyCallers()
				src.paused = boolWord(true)
				return twoChainRegistry(src, dst)
			},
			amount:  "1000",
			blocker: "GATEWAY_PAUSED",
		},
		{
			name: "below the on-chain minimum",
			reg: func() *Registry {
				src, dst := healthyCallers()
				src.route = routeResponse(true, dstToken, big.NewInt(5000))
				return twoChainRegistry(src, dst)
			},
			amount:  "1000",
			blocker: "BELOW_MIN_AMOUNT",
		},
		{
			name: "destination cannot cover the transfer",
			reg: func() *Registry {
				src, dst := healthyCallers()
				dst.liquidity = word(big.NewInt(10))
				return twoChainRegistry(src, dst)
			},
			amount:  "1000",
			blocker: "INSUFFICIENT_DESTINATION_LIQUIDITY",
		},
		{
			name: "source rpc missing",
			reg: func() *Registry {
				chains := map[int]deployments.ChainConfig{
					sepolia: {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: srcGW}}},
					baseSep: {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: dstGW}}},
				}
				return NewRegistry(chains, map[int]EthCaller{})
			},
			amount:  "1000",
			blocker: "SOURCE_RPC_UNAVAILABLE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := newSvc(tc.reg()).GetRoutes(context.Background(), routeReq(tc.amount))
			if err != nil {
				t.Fatalf("GetRoutes: %v", err)
			}
			if resp.Executable {
				t.Fatal("route must not be executable")
			}
			if resp.ExecutionMode != "unavailable" {
				t.Errorf("mode: want unavailable, got %q", resp.ExecutionMode)
			}
			if !hasBlocker(resp.Routes[0].Blockers, tc.blocker) {
				t.Errorf("want blocker %q, got %v", tc.blocker, resp.Routes[0].Blockers)
			}
		})
	}
}

// An unreadable destination balance must NOT be reported as zero liquidity.
func TestUnknownLiquidityIsNotReportedAsZero(t *testing.T) {
	src, _ := healthyCallers()
	chains := map[int]deployments.ChainConfig{
		sepolia: {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: srcGW}}},
		baseSep: {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: dstGW}}},
	}
	// Destination gateway is deployed but has no RPC configured.
	reg := NewRegistry(chains, map[int]EthCaller{sepolia: src})

	resp, err := newSvc(reg).GetRoutes(context.Background(), routeReq("1000"))
	if err != nil {
		t.Fatalf("GetRoutes: %v", err)
	}
	r := resp.Routes[0]
	if r.DstLiquidity != "" {
		t.Errorf("unknown liquidity must stay empty, got %q", r.DstLiquidity)
	}
	if !hasBlocker(r.Blockers, "DESTINATION_LIQUIDITY_UNKNOWN") {
		t.Errorf("want DESTINATION_LIQUIDITY_UNKNOWN, got %v", r.Blockers)
	}
}

func TestSameChainAndBadAmountRejected(t *testing.T) {
	svc := newSvc(twoChainRegistry(healthyCallers()))

	same := routeReq("1000")
	same.ToChainID = same.FromChainID
	resp, err := svc.GetRoutes(context.Background(), same)
	if err != nil {
		t.Fatalf("GetRoutes: %v", err)
	}
	if !hasBlocker(resp.Routes[0].Blockers, "SAME_CHAIN") {
		t.Errorf("want SAME_CHAIN, got %v", resp.Routes[0].Blockers)
	}

	for _, bad := range []string{"", "0", "-5", "abc", "1.5"} {
		if _, err := svc.GetRoutes(context.Background(), routeReq(bad)); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("amount %q: want ErrInvalidAmount, got %v", bad, err)
		}
	}
}

// ─── Calldata builder ───────────────────────────────────────────────────────

func TestBuildDepositRefusesNonExecutableRoute(t *testing.T) {
	src, dst := healthyCallers()
	dst.liquidity = word(big.NewInt(1)) // cannot cover the transfer
	svc := newSvc(twoChainRegistry(src, dst))

	_, err := svc.BuildDeposit(context.Background(), BuildDepositRequest{
		FromChainID: sepolia, ToChainID: baseSep,
		SrcToken: srcToken, Amount: "1000", Recipient: recipient,
	})
	// Handing the user calldata that would revert or strand funds is exactly
	// what the old "executable: false but here's a CTA" shape did.
	if !errors.Is(err, ErrRouteUnavailable) {
		t.Fatalf("want ErrRouteUnavailable, got %v", err)
	}
}

func TestBuildDepositProducesCorrectCalldata(t *testing.T) {
	svc := newSvc(twoChainRegistry(healthyCallers()))

	resp, err := svc.BuildDeposit(context.Background(), BuildDepositRequest{
		FromChainID: sepolia, ToChainID: baseSep,
		SrcToken: srcToken, Amount: "1000", Recipient: recipient,
	})
	if err != nil {
		t.Fatalf("BuildDeposit: %v", err)
	}
	if resp.To != strings.ToLower(srcGW) || resp.ApprovalTarget != strings.ToLower(srcGW) {
		t.Errorf("must target the source gateway, got to=%s approve=%s", resp.To, resp.ApprovalTarget)
	}
	if resp.Value != "0" {
		t.Errorf("ERC20 deposit must carry no ETH value, got %q", resp.Value)
	}
	// selector + 4 words
	if want := 10 + 4*64; len(resp.Data) != want {
		t.Fatalf("calldata length: want %d, got %d (%s)", want, len(resp.Data), resp.Data)
	}
	if !strings.HasPrefix(resp.Data, selDeposit) {
		t.Errorf("wrong selector: %s", resp.Data[:10])
	}
	// Args decode back to exactly what was requested.
	args := resp.Data[10:]
	if got := "0x" + strings.TrimLeft(args[0:64], "0"); got != srcToken {
		t.Errorf("arg0 token: want %s, got %s", srcToken, got)
	}
	if got := new(big.Int).SetBytes(mustDecode(t, args[64:128])); got.Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("arg1 amount: want 1000, got %s", got)
	}
	if got := new(big.Int).SetBytes(mustDecode(t, args[128:192])); got.Cmp(big.NewInt(baseSep)) != 0 {
		t.Errorf("arg2 dstChainId: want %d, got %s", baseSep, got)
	}
	if got := "0x" + strings.TrimLeft(args[192:256], "0"); got != recipient {
		t.Errorf("arg3 recipient: want %s, got %s", recipient, got)
	}
}

func TestBuildDepositRejectsBadRecipient(t *testing.T) {
	svc := newSvc(twoChainRegistry(healthyCallers()))
	for _, bad := range []string{"", "0xnope", "not-an-address"} {
		_, err := svc.BuildDeposit(context.Background(), BuildDepositRequest{
			FromChainID: sepolia, ToChainID: baseSep,
			SrcToken: srcToken, Amount: "1000", Recipient: bad,
		})
		if err == nil {
			t.Errorf("recipient %q should be rejected", bad)
		}
	}
}

// ─── Status without a store ─────────────────────────────────────────────────

// "We cannot look" must never render as "it does not exist".
func TestStatusWithoutStoreIs503Not404(t *testing.T) {
	router := Router(newSvc(twoChainRegistry(healthyCallers())))

	rec := httptest.NewRecorder()
	id := "0x" + strings.Repeat("ab", 32)
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status/"+id, nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestStatusRejectsMalformedId(t *testing.T) {
	router := Router(newSvc(twoChainRegistry(healthyCallers())))
	for _, bad := range []string{"abc", "0x1234", "not-hex"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status/"+bad, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("id %q: want 400, got %d", bad, rec.Code)
		}
	}
}

func TestRoutesEndpointShape(t *testing.T) {
	router := Router(newSvc(twoChainRegistry(healthyCallers())))

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"fromChainId":11155111,"toChainId":84532,"srcToken":"` + srcToken + `","amount":"1000"}`)
	req := httptest.NewRequest(http.MethodPost, "/routes", body)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var resp RoutesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TrustModel != TrustModel {
		t.Errorf("trustModel missing from the wire shape: %+v", resp)
	}
	if len(resp.Chains) != 2 {
		t.Errorf("want both deployed chains listed, got %v", resp.Chains)
	}
}

// ─── Registry ───────────────────────────────────────────────────────────────

func TestRegistrySkipsChainsWithoutAGateway(t *testing.T) {
	chains := map[int]deployments.ChainConfig{
		sepolia: {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: srcGW}}},
		31337:   {Contracts: map[string]deployments.ContractInfo{"router": {Address: dstGW}}},
	}
	reg := NewRegistry(chains, nil)

	if got := reg.Chains(); len(got) != 1 || got[0] != sepolia {
		t.Errorf("only gateway chains belong in the registry, got %v", got)
	}
	if _, ok := reg.Gateway(31337); ok {
		t.Error("a chain without a bridgeGateway entry must not appear")
	}
}

func TestCalldataPaddingIsExact(t *testing.T) {
	data := BuildDepositCalldata(srcToken, big.NewInt(1), baseSep, recipient)
	if len(data) != 10+4*64 {
		t.Fatalf("unexpected calldata length %d", len(data))
	}
	// A value that fills the whole word must not be truncated.
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	full := BuildDepositCalldata(srcToken, max, baseSep, recipient)
	if !strings.Contains(full, strings.Repeat("f", 64)) {
		t.Error("uint256 max was not encoded as a full word")
	}
}

func hasBlocker(list []string, want string) bool {
	return slices.Contains(list, want)
}

func mustDecode(t *testing.T, h string) []byte {
	t.Helper()
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("decode %q: %v", h, err)
	}
	return b
}
