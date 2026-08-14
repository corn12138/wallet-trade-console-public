package txreview

import (
	"context"
	"math/big"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
)

// RPCCaller is the subset of *rpc.Client the simulator needs. Satisfied
// by the real client; tests pass a fake.
type RPCCaller interface {
	EstimateGas(ctx context.Context, from, to, value, dataHex string) (*big.Int, error)
	GetGasPrice(ctx context.Context) (*big.Int, error)
	GetBalance(ctx context.Context, address, block string) (*big.Int, error)
	EthCall(ctx context.Context, to, dataHex, block string) ([]byte, error)
}

// Simulate runs the simulation pipeline. When `client` is nil — or all
// estimateGas/call attempts fail — the result Mode degrades to
// "fallback" so the rules engine can flag gas-unavailable. The
// `nativeBalanceWei` parameter is a pre-provided override; "" → fetch
// via getBalance.
func Simulate(ctx context.Context, client RPCCaller, fromAddress string, tx TxShape, nativeBalanceWei string) SimulationResult {
	var (
		gasEstimate   *big.Int
		callOK        *bool
		errMsg        string
		gasPrice      *big.Int
		nativeBalance = ParseOptionalBigInt(nativeBalanceWei)
	)

	if client == nil {
		errMsg = "RPC client not configured"
		return finalizeSimulation(nil, nil, "", nil, nil, errMsg)
	}

	valueHex := normalizeValueToHex(tx.Value)

	if v, err := client.EstimateGas(ctx, fromAddress, tx.To, valueHex, tx.Data); err == nil {
		gasEstimate = v
	} else {
		errMsg = err.Error()
	}

	if _, err := client.EthCall(ctx, tx.To, tx.Data, ""); err == nil {
		t := true
		callOK = &t
	} else {
		f := false
		callOK = &f
		if errMsg == "" {
			errMsg = err.Error()
		}
	}

	if v, err := client.GetGasPrice(ctx); err == nil {
		gasPrice = v
	}

	if nativeBalance == nil {
		if v, err := client.GetBalance(ctx, fromAddress, ""); err == nil {
			nativeBalance = v
		}
	}

	return finalizeSimulation(gasEstimate, gasPrice, "", callOK, nativeBalance, errMsg)
}

// finalizeSimulation builds the SimulationResult from the four IO
// slots. value parameter (placeholder; unused for now) keeps the
// signature open for a future "raw RPC response" extension without
// re-shuffling callers.
func finalizeSimulation(gasEstimate, gasPrice *big.Int, _ string, callOK *bool, nativeBalance *big.Int, errMsg string) SimulationResult {
	mode := SimulationModeFallback
	if gasEstimate != nil || callOK != nil {
		mode = SimulationModeEstimate
	}
	r := SimulationResult{Mode: mode, GasEstimate: gasEstimate, CallSucceeded: callOK, NativeBalanceWei: nativeBalance}
	if gasEstimate != nil {
		gs := gasEstimate.String()
		r.GasEstimateString = &gs
		gls := ApplyGasPadding(gasEstimate).String()
		r.GasLimitSuggestion = &gls
	}
	if gasEstimate != nil && gasPrice != nil {
		fee := new(big.Int).Mul(gasEstimate, gasPrice)
		feeStr := fee.String()
		r.EstimatedFeeWei = fee
		r.EstimatedFeeString = &feeStr
	}
	if errMsg != "" {
		em := errMsg
		r.ErrorMessage = &em
	}
	return r
}

// normalizeValueToHex converts a decimal-string tx.value into "0x..."
// form for eth_estimateGas. "" or "0" → "".
func normalizeValueToHex(value string) string {
	v := ParseOptionalBigInt(value)
	if v == nil || v.Sign() == 0 {
		return ""
	}
	return "0x" + v.Text(16)
}

// erc20AllowanceSelector is the ABI selector for allowance(address,address).
var erc20AllowanceSelector = abi.Selector("allowance(address,address)")

// FetchAllowance reads ERC-20 allowance(owner, spender) via eth_call.
// Returns nil on any error so the orchestrator can omit the
// currentAllowance field (matches NestJS — null on failure).
func FetchAllowance(ctx context.Context, client RPCCaller, tokenAddress, owner, spender string) *big.Int {
	if client == nil {
		return nil
	}
	ownerArg, err := abi.EncodeAddress(owner)
	if err != nil {
		return nil
	}
	spenderArg, err := abi.EncodeAddress(spender)
	if err != nil {
		return nil
	}
	data := abi.EncodeCall("allowance(address,address)", ownerArg, spenderArg)
	_ = erc20AllowanceSelector // keep the symbol referenced (sanity)
	raw, err := client.EthCall(ctx, tokenAddress, data, "")
	if err != nil || len(raw) < 32 {
		return nil
	}
	return new(big.Int).SetBytes(raw[:32])
}

// NormalizeTokenWarnings dedupes a slice while preserving order.
// Phase 5d.2b orchestrator uses this to merge seed warnings with
// tag-derived warnings.
func NormalizeTokenWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return warnings
	}
	seen := map[string]bool{}
	out := warnings[:0]
	for _, w := range warnings {
		w = strings.TrimSpace(w)
		if w == "" || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}
