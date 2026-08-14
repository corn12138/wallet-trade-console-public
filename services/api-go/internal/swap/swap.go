// Package swap is the Go port of legacy NestJS swap/swap.service.ts.
// Three POST endpoints:
//
//	POST /api/swap/quote          (live Router quote via eth_call when an
//	                                RPC client is wired; degraded fallback
//	                                estimate otherwise)
//	POST /api/swap/build-approve  (ERC-20 approve, ABI-encoded)
//	POST /api/swap/build-swap     (Router.swapExactTokensForTokens —
//	                                exercises dynamic address[] encoding)
//
// Quote gating: every quote carries a machine-readable `quoteStatus`
// ("live" | "fallback") and `executable` flag. A fallback estimate is
// NEVER executable — the FE must not submit a wallet swap from it. The
// fallback shape otherwise matches NestJS buildFallbackQuote (same
// `routeSource`, `routerAddress`, `warnings` slots) so the panel keeps
// rendering an estimate even when the router/RPC isn't available.
package swap

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"github.com/go-chi/chi/v5"
)

// defaultChainID mirrors `input.chainId || 11155111` (Sepolia) in the
// NestJS service. The port keeps the same fallback so requests that
// omit chainId hit the same deployment lookups.
const defaultChainID = 11155111

// defaultDeadlineSeconds matches `input.deadlineSeconds || 60 * 20`.
const defaultDeadlineSeconds = 60 * 20

// defaultSlippageBps matches `input.slippageBps ?? 50`.
const defaultSlippageBps = 50

// DeploymentsLoader abstracts the deployments registry. Mirrors
// markets.DeploymentsLoader so cmd/api can pass the same instance.
type DeploymentsLoader interface {
	Load() (map[int]deployments.ChainConfig, error)
}

// Sentinels for the handler error mapping. ErrRouterNotConfigured maps
// to 404 (matches NestJS NotFoundException), the rest map to 400.
var (
	ErrInvalidAddress      = errors.New("swap: invalid token address")
	ErrSameToken           = errors.New("swap: tokenIn and tokenOut must differ")
	ErrInvalidAmount       = errors.New("swap: invalid amount")
	ErrRouterNotConfigured = errors.New("swap: router address is not configured")
)

// EthCaller is the minimal RPC surface swap needs to produce live
// quotes. *rpc.Client satisfies it; tests pass a fake.
type EthCaller interface {
	EthCall(ctx context.Context, to, dataHex, block string) ([]byte, error)
}

// Service holds the deployments loader + optional RPC client. No DB
// dependency — every read is fully deterministic from request body,
// deployments registry, and (when configured) eth_call results.
type Service struct {
	loader    DeploymentsLoader
	rpcClient EthCaller
}

// NewService constructs a Service. A nil loader is tolerated; lookups
// then always degrade to the fallback quote / 404 on build-* (same
// shape as if no Router were configured on the requested chain).
// RPC client is wired separately via SetRPCClient so cmd/api can keep
// constructing the service before reading env.
func NewService(loader DeploymentsLoader) *Service {
	return &Service{loader: loader}
}

// SetRPCClient enables the live-quote path. Calling with nil disables
// it (callers can opt out per-environment).
func (s *Service) SetRPCClient(client EthCaller) {
	if s == nil {
		return
	}
	s.rpcClient = client
}

// QuoteRequest mirrors the @Body shape of POST /swap/quote.
type QuoteRequest struct {
	ChainID          *int   `json:"chainId,omitempty"`
	TokenIn          string `json:"tokenIn"`
	TokenOut         string `json:"tokenOut"`
	AmountIn         string `json:"amountIn"`
	TokenInDecimals  int    `json:"tokenInDecimals"`
	TokenOutDecimals int    `json:"tokenOutDecimals"`
	SlippageBps      *int   `json:"slippageBps,omitempty"`
}

// Quote status values for QuoteResponse.QuoteStatus.
const (
	QuoteStatusLive     = "live"
	QuoteStatusFallback = "fallback"
)

// QuoteResponse mirrors NestJS getQuote's return, extended with the
// executable-gating fields. QuoteStatus/Executable are the machine-
// readable contract: a "fallback" quote has Executable=false and the
// FE must keep the swap CTA disabled (warnings alone are not a guard).
type QuoteResponse struct {
	ChainID            int      `json:"chainId"`
	RouteSource        string   `json:"routeSource"`
	QuoteStatus        string   `json:"quoteStatus"`
	Executable         bool     `json:"executable"`
	RouterAddress      *string  `json:"routerAddress"`
	AmountIn           string   `json:"amountIn"`
	AmountOut          string   `json:"amountOut"`
	AmountOutRaw       string   `json:"amountOutRaw"`
	MinimumReceived    string   `json:"minimumReceived"`
	MinimumReceivedRaw string   `json:"minimumReceivedRaw"`
	PriceImpactPct     float64  `json:"priceImpactPct"`
	SlippageBps        int      `json:"slippageBps"`
	Path               []string `json:"path"`
	Warnings           []string `json:"warnings"`
}

// BuildApproveRequest mirrors NestJS buildApproveTx @Body.
type BuildApproveRequest struct {
	ChainID       *int    `json:"chainId,omitempty"`
	TokenAddress  string  `json:"tokenAddress"`
	Amount        string  `json:"amount"`
	TokenDecimals int     `json:"tokenDecimals"`
	Spender       *string `json:"spender,omitempty"`
}

// BuildSwapRequest mirrors NestJS buildSwapTx @Body.
type BuildSwapRequest struct {
	ChainID          *int   `json:"chainId,omitempty"`
	TokenIn          string `json:"tokenIn"`
	TokenOut         string `json:"tokenOut"`
	AmountIn         string `json:"amountIn"`
	AmountOutMin     string `json:"amountOutMin"`
	TokenInDecimals  int    `json:"tokenInDecimals"`
	TokenOutDecimals int    `json:"tokenOutDecimals"`
	Recipient        string `json:"recipient"`
	DeadlineSeconds  *int   `json:"deadlineSeconds,omitempty"`
}

// TxResponse is the shared {chainId, to, value, data} shape used by
// both build-approve and build-swap. Matches the NestJS literal return.
type TxResponse struct {
	ChainID int    `json:"chainId"`
	To      string `json:"to"`
	Value   string `json:"value"`
	Data    string `json:"data"`
}

// GetQuote returns a live quote when both an RPC client and a Router
// deployment are available; otherwise it falls back to the same shape
// with routeSource/quoteStatus "fallback" AND executable=false. A
// degraded quote is display-only: the FE renders the estimate but the
// swap CTA must stay disabled, so a live-quote failure (RPC error,
// decode error) can never lead to an executable swap built from a
// made-up number.
func (s *Service) GetQuote(req QuoteRequest) (QuoteResponse, error) {
	if err := validateAddressPair(req.TokenIn, req.TokenOut); err != nil {
		return QuoteResponse{}, err
	}
	chainID := req.ChainID
	if chainID == nil {
		v := defaultChainID
		chainID = &v
	}
	amountInRaw, err := abi.ParseUnits(req.AmountIn, req.TokenInDecimals)
	if err != nil {
		return QuoteResponse{}, ErrInvalidAmount
	}

	router := s.routerAddress(*chainID)
	if router == "" {
		return buildFallbackQuote(req, *chainID, []string{"Router not configured for this chain"}), nil
	}

	if s.rpcClient != nil {
		if live, ok := s.fetchLiveQuote(req, *chainID, router, amountInRaw); ok {
			return live, nil
		}
	}
	return buildFallbackQuote(req, *chainID, []string{"Live quote unavailable, using fallback estimate"}), nil
}

// fetchLiveQuote calls Router.getAmountsOut + Router.getReserves via
// eth_call and assembles the live quote payload. Returns ok=false on
// any failure so GetQuote can fall back transparently.
func (s *Service) fetchLiveQuote(req QuoteRequest, chainID int, router string, amountInRaw *big.Int) (QuoteResponse, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	slippageBps := defaultSlippageBps
	if req.SlippageBps != nil {
		slippageBps = *req.SlippageBps
	}

	// getAmountsOut(uint256, address[]) — dynamic encoding.
	amountInArg, err := abi.EncodeUint256(amountInRaw)
	if err != nil {
		return QuoteResponse{}, false
	}
	pathArg, err := abi.EncodeAddressArrayArg([]string{req.TokenIn, req.TokenOut})
	if err != nil {
		return QuoteResponse{}, false
	}
	amountsData, err := abi.EncodeCallArgs(
		"getAmountsOut(uint256,address[])",
		abi.Static(amountInArg),
		pathArg,
	)
	if err != nil {
		return QuoteResponse{}, false
	}
	amountsRaw, err := s.rpcClient.EthCall(ctx, router, amountsData, "")
	if err != nil {
		return QuoteResponse{}, false
	}
	amounts, err := rpc.DecodeDynamicUint256Array(amountsRaw)
	if err != nil || len(amounts) < 2 {
		return QuoteResponse{}, false
	}
	amountOutRaw := amounts[len(amounts)-1]

	// getReserves(address, address) — both static, just two addresses.
	tokenInArg, err := abi.EncodeAddress(req.TokenIn)
	if err != nil {
		return QuoteResponse{}, false
	}
	tokenOutArg, err := abi.EncodeAddress(req.TokenOut)
	if err != nil {
		return QuoteResponse{}, false
	}
	reservesData := abi.EncodeCall("getReserves(address,address)", tokenInArg, tokenOutArg)
	reservesRaw, err := s.rpcClient.EthCall(ctx, router, reservesData, "")
	if err != nil || len(reservesRaw) < 64 {
		return QuoteResponse{}, false
	}
	reserveIn, err := rpc.DecodeUint256At(reservesRaw, 0)
	if err != nil {
		return QuoteResponse{}, false
	}
	reserveOut, err := rpc.DecodeUint256At(reservesRaw, 32)
	if err != nil {
		return QuoteResponse{}, false
	}

	// minimumReceivedRaw = amountOutRaw × (10000 - slippageBps) / 10000.
	minimumReceivedRaw := new(big.Int).Mul(amountOutRaw, big.NewInt(int64(10000-slippageBps)))
	minimumReceivedRaw.Quo(minimumReceivedRaw, big.NewInt(10000))

	routerCopy := router
	return QuoteResponse{
		ChainID:            chainID,
		RouteSource:        "router",
		QuoteStatus:        QuoteStatusLive,
		Executable:         true,
		RouterAddress:      &routerCopy,
		AmountIn:           req.AmountIn,
		AmountOut:          abi.FormatUnits(amountOutRaw, req.TokenOutDecimals),
		AmountOutRaw:       amountOutRaw.String(),
		MinimumReceived:    abi.FormatUnits(minimumReceivedRaw, req.TokenOutDecimals),
		MinimumReceivedRaw: minimumReceivedRaw.String(),
		PriceImpactPct:     computePriceImpact(reserveIn, reserveOut, amountInRaw, amountOutRaw),
		SlippageBps:        slippageBps,
		Path:               []string{req.TokenIn, req.TokenOut},
		Warnings:           []string{},
	}, true
}

// computePriceImpact mirrors the NestJS computePriceImpact helper. All
// math is done in float64 — NestJS narrows reserves and amounts via
// `Number(...)` and rounds to 3 decimals.
func computePriceImpact(reserveIn, reserveOut, amountIn, amountOut *big.Int) float64 {
	if reserveIn == nil || reserveOut == nil || amountIn == nil || amountOut == nil {
		return 0
	}
	if reserveIn.Sign() == 0 || reserveOut.Sign() == 0 || amountIn.Sign() == 0 {
		return 0
	}
	rIn, _ := new(big.Float).SetInt(reserveIn).Float64()
	rOut, _ := new(big.Float).SetInt(reserveOut).Float64()
	aIn, _ := new(big.Float).SetInt(amountIn).Float64()
	aOut, _ := new(big.Float).SetInt(amountOut).Float64()
	if rIn == 0 || aIn == 0 {
		return 0
	}
	current := rOut / rIn
	execution := aOut / aIn
	if math.IsNaN(current) || math.IsInf(current, 0) || current == 0 {
		return 0
	}
	pct := ((current - execution) / current) * 100
	return math.Round(pct*1000) / 1000
}

// BuildApproveTx mirrors NestJS buildApproveTx: validate address,
// default chainId/spender, encode approve(spender, amount). Returns
// ErrRouterNotConfigured when no explicit spender is supplied and
// no Router is deployed on the chain (NestJS NotFoundException).
func (s *Service) BuildApproveTx(req BuildApproveRequest) (TxResponse, error) {
	if !isValidAddress(req.TokenAddress) {
		return TxResponse{}, ErrInvalidAddress
	}
	chainID := defaultChainID
	if req.ChainID != nil {
		chainID = *req.ChainID
	}
	spender := ""
	if req.Spender != nil && *req.Spender != "" {
		spender = *req.Spender
	} else {
		spender = s.routerAddress(chainID)
	}
	if spender == "" {
		return TxResponse{}, ErrRouterNotConfigured
	}
	if !isValidAddress(spender) {
		return TxResponse{}, ErrInvalidAddress
	}
	amountBig, err := abi.ParseUnits(req.Amount, req.TokenDecimals)
	if err != nil {
		return TxResponse{}, ErrInvalidAmount
	}
	spenderArg, err := abi.EncodeAddress(spender)
	if err != nil {
		return TxResponse{}, ErrInvalidAddress
	}
	amountArg, err := abi.EncodeUint256(amountBig)
	if err != nil {
		return TxResponse{}, ErrInvalidAmount
	}
	return TxResponse{
		ChainID: chainID,
		To:      req.TokenAddress,
		Value:   "0",
		Data:    abi.EncodeCall("approve(address,uint256)", spenderArg, amountArg),
	}, nil
}

// BuildSwapTx mirrors NestJS buildSwapTx. Encodes the V2-style
// swapExactTokensForTokens(uint256, uint256, address[], address, uint256)
// — the only call in the ported surface that exercises dynamic
// address[] encoding (handled via abi.EncodeCallArgs).
//
// `now` is injected so tests can pin the deadline computation; production
// callers pass time.Now.
func (s *Service) BuildSwapTx(req BuildSwapRequest, now time.Time) (TxResponse, error) {
	if err := validateAddressPair(req.TokenIn, req.TokenOut); err != nil {
		return TxResponse{}, err
	}
	if !isValidAddress(req.Recipient) {
		return TxResponse{}, ErrInvalidAddress
	}
	chainID := defaultChainID
	if req.ChainID != nil {
		chainID = *req.ChainID
	}
	router := s.routerAddress(chainID)
	if router == "" {
		return TxResponse{}, ErrRouterNotConfigured
	}
	deadlineSecs := defaultDeadlineSeconds
	if req.DeadlineSeconds != nil && *req.DeadlineSeconds > 0 {
		deadlineSecs = *req.DeadlineSeconds
	}
	amountIn, err := abi.ParseUnits(req.AmountIn, req.TokenInDecimals)
	if err != nil {
		return TxResponse{}, ErrInvalidAmount
	}
	amountOutMin, err := abi.ParseUnits(req.AmountOutMin, req.TokenOutDecimals)
	if err != nil {
		return TxResponse{}, ErrInvalidAmount
	}
	deadline := big.NewInt(now.Unix() + int64(deadlineSecs))
	amountInArg, err := abi.EncodeUint256(amountIn)
	if err != nil {
		return TxResponse{}, ErrInvalidAmount
	}
	amountOutMinArg, err := abi.EncodeUint256(amountOutMin)
	if err != nil {
		return TxResponse{}, ErrInvalidAmount
	}
	pathArg, err := abi.EncodeAddressArrayArg([]string{req.TokenIn, req.TokenOut})
	if err != nil {
		return TxResponse{}, ErrInvalidAddress
	}
	recipientArg, err := abi.EncodeAddress(req.Recipient)
	if err != nil {
		return TxResponse{}, ErrInvalidAddress
	}
	deadlineArg, err := abi.EncodeUint256(deadline)
	if err != nil {
		return TxResponse{}, ErrInvalidAmount
	}
	data, err := abi.EncodeCallArgs(
		"swapExactTokensForTokens(uint256,uint256,address[],address,uint256)",
		abi.Static(amountInArg),
		abi.Static(amountOutMinArg),
		pathArg,
		abi.Static(recipientArg),
		abi.Static(deadlineArg),
	)
	if err != nil {
		return TxResponse{}, err
	}
	return TxResponse{
		ChainID: chainID,
		To:      router,
		Value:   "0",
		Data:    data,
	}, nil
}

func (s *Service) routerAddress(chainID int) string {
	if s == nil || s.loader == nil {
		return ""
	}
	registry, err := s.loader.Load()
	if err != nil {
		return ""
	}
	return deployments.LookupAddress(registry[chainID], "router", "Router")
}

// buildFallbackQuote mirrors the NestJS buildFallbackQuote helper.
// amountOut = amountIn × 0.997 (the 0.3% fee assumption baked into the
// estimate), formatted to min(decimals, 6) trailing digits — same as
// the Number(...).toFixed(...) calls in TS.
func buildFallbackQuote(input QuoteRequest, chainID int, warnings []string) QuoteResponse {
	amountIn, err := strconv.ParseFloat(input.AmountIn, 64)
	if err != nil || math.IsNaN(amountIn) || math.IsInf(amountIn, 0) {
		amountIn = 0
	}
	amountOut := amountIn * 0.997
	slippageBps := defaultSlippageBps
	if input.SlippageBps != nil {
		slippageBps = *input.SlippageBps
	}
	minimumReceived := amountOut * (float64(10000-slippageBps) / 10000.0)
	precision := input.TokenOutDecimals
	if precision > 6 {
		precision = 6
	}
	if precision < 0 {
		precision = 0
	}
	return QuoteResponse{
		ChainID:            chainID,
		RouteSource:        "fallback",
		QuoteStatus:        QuoteStatusFallback,
		Executable:         false,
		RouterAddress:      nil,
		AmountIn:           input.AmountIn,
		AmountOut:          strconv.FormatFloat(amountOut, 'f', precision, 64),
		AmountOutRaw:       "0",
		MinimumReceived:    strconv.FormatFloat(minimumReceived, 'f', precision, 64),
		MinimumReceivedRaw: "0",
		PriceImpactPct:     0,
		SlippageBps:        slippageBps,
		Path:               []string{input.TokenIn, input.TokenOut},
		Warnings:           warnings,
	}
}

func validateAddressPair(tokenIn, tokenOut string) error {
	if !isValidAddress(tokenIn) || !isValidAddress(tokenOut) {
		return ErrInvalidAddress
	}
	if strings.EqualFold(tokenIn, tokenOut) {
		return ErrSameToken
	}
	return nil
}

func isValidAddress(addr string) bool {
	if len(addr) != 42 {
		return false
	}
	if addr[0] != '0' || (addr[1] != 'x' && addr[1] != 'X') {
		return false
	}
	for _, c := range addr[2:] {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// Router mounts /quote, /build-approve, /build-swap on a chi sub-router.
// Mounted at /api/swap from httpx.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Post("/quote", makeQuoteHandler(svc))
	r.Post("/build-approve", makeBuildApproveHandler(svc))
	r.Post("/build-swap", makeBuildSwapHandler(svc, time.Now))
	return r
}

func makeQuoteHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req QuoteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		out, err := svc.GetQuote(req)
		if errors.Is(err, ErrInvalidAddress) || errors.Is(err, ErrSameToken) || errors.Is(err, ErrInvalidAmount) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "swap quote failed", "err", err)
			http.Error(w, "failed to build quote", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeBuildApproveHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req BuildApproveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		out, err := svc.BuildApproveTx(req)
		switch {
		case errors.Is(err, ErrInvalidAddress) || errors.Is(err, ErrInvalidAmount):
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		case errors.Is(err, ErrRouterNotConfigured):
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		case err != nil:
			slog.ErrorContext(r.Context(), "swap build-approve failed", "err", err)
			http.Error(w, "failed to build approve tx", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeBuildSwapHandler(svc *Service, nowFn func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req BuildSwapRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		out, err := svc.BuildSwapTx(req, nowFn())
		switch {
		case errors.Is(err, ErrInvalidAddress) || errors.Is(err, ErrSameToken) || errors.Is(err, ErrInvalidAmount):
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		case errors.Is(err, ErrRouterNotConfigured):
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		case err != nil:
			slog.ErrorContext(r.Context(), "swap build-swap failed", "err", err)
			http.Error(w, "failed to build swap tx", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("swap response encode failed", "err", err)
	}
}
