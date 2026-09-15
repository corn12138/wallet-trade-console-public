package swap

import (
	"context"
	"errors"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"math"
	"math/big"
	"time"
)

// fetchLiveQuote calls Router.getAmountsOut + Router.getReserves via
// eth_call and assembles the live quote payload. Returns a safe diagnostic on
// failure; provider messages can contain endpoints and must not reach clients.
func (s *Service) fetchLiveQuote(req QuoteRequest, chainID int, router string, amountInRaw *big.Int) (QuoteResponse, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	slippageBps := defaultSlippageBps
	if req.SlippageBps != nil {
		slippageBps = *req.SlippageBps
	}

	// getAmountsOut(uint256, address[]) — dynamic encoding.
	amountInArg, err := abi.EncodeUint256(amountInRaw)
	if err != nil {
		return QuoteResponse{}, "Live quote unavailable: invalid quote data"
	}
	pathArg, err := abi.EncodeAddressArrayArg([]string{req.TokenIn, req.TokenOut})
	if err != nil {
		return QuoteResponse{}, "Live quote unavailable: invalid quote data"
	}
	amountsData, err := abi.EncodeCallArgs(
		"getAmountsOut(uint256,address[])",
		abi.Static(amountInArg),
		pathArg,
	)
	if err != nil {
		return QuoteResponse{}, "Live quote unavailable: invalid quote data"
	}
	amountsRaw, err := s.rpcClient.EthCall(ctx, router, amountsData, "")
	if err != nil {
		return QuoteResponse{}, quoteReadFailure("amounts", err)
	}
	amounts, err := rpc.DecodeDynamicUint256Array(amountsRaw)
	if err != nil || len(amounts) != 2 || amounts[1].Sign() <= 0 {
		return QuoteResponse{}, "Live quote unavailable: invalid quote data"
	}
	amountOutRaw := amounts[len(amounts)-1]

	// getReserves(address, address) — both static, just two addresses.
	tokenInArg, err := abi.EncodeAddress(req.TokenIn)
	if err != nil {
		return QuoteResponse{}, "Live quote unavailable: invalid quote data"
	}
	tokenOutArg, err := abi.EncodeAddress(req.TokenOut)
	if err != nil {
		return QuoteResponse{}, "Live quote unavailable: invalid quote data"
	}
	reservesData := abi.EncodeCall("getReserves(address,address)", tokenInArg, tokenOutArg)
	reservesRaw, err := s.rpcClient.EthCall(ctx, router, reservesData, "")
	if err != nil {
		return QuoteResponse{}, quoteReadFailure("reserves", err)
	}
	if len(reservesRaw) < 64 {
		return QuoteResponse{}, "Live quote unavailable: invalid quote data"
	}
	reserveIn, err := rpc.DecodeUint256At(reservesRaw, 0)
	if err != nil {
		return QuoteResponse{}, "Live quote unavailable: invalid quote data"
	}
	reserveOut, err := rpc.DecodeUint256At(reservesRaw, 32)
	if err != nil {
		return QuoteResponse{}, "Live quote unavailable: invalid quote data"
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
	}, ""
}

// Price impact is display-only; minimum received uses integer arithmetic.
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

// Only the failed stage and error category leave the service. Never serialize
// provider error text: it may include credentials embedded in an RPC URL.
func quoteReadFailure(stage string, err error) string {
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "Live quote unavailable: " + stage + " timeout"
	}
	var rpcErr *rpc.RPCError
	if errors.As(err, &rpcErr) {
		return "Live quote unavailable: " + stage + " RPC rejected"
	}
	if errors.Is(err, rpc.ErrEmpty) {
		return "Live quote unavailable: invalid quote data"
	}
	return "Live quote unavailable: " + stage + " RPC failed"
}
