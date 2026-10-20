package txreview

import (
	"context"
	"errors"
	"math/big"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

func simulateReview(ctx context.Context, client RPCCaller, from string, tx TxShape, allowance *AllowanceRequest) SimulationResult {
	full, ok := client.(transactionSimulator)
	if !ok {
		result := simulateLegacy(ctx, client, from, tx, "")
		if allowance != nil {
			result.CurrentAllowance = FetchAllowance(ctx, client, allowance.TokenAddress, from, allowance.Spender)
			result.Observations.Allowance = observation("unavailable", "legacy-read-call")
			result.Observations.Allowance.TokenAddress = allowance.TokenAddress
			result.Observations.Allowance.Spender = allowance.Spender
			if result.CurrentAllowance != nil {
				result.Observations.Allowance.ObservationStatus = "succeeded"
				result.Observations.Allowance.Pinning = "latest-only"
			}
		}
		return result
	}
	return simulateEnvelope(ctx, full, from, tx, allowance)
}

func simulateEnvelope(ctx context.Context, client transactionSimulator, from string, tx TxShape, allowance *AllowanceRequest) SimulationResult {
	result := SimulationResult{Mode: SimulationModeFallback, SimulationScope: "unavailable", Observations: emptyObservations("anchor-unavailable")}
	if allowance != nil {
		result.Observations.Allowance = observation("unavailable", "anchor-unavailable")
		result.Observations.Allowance.TokenAddress = allowance.TokenAddress
		result.Observations.Allowance.Spender = allowance.Spender
	}
	chain, err := client.VerifiedChainID(ctx)
	if err != nil || chain == nil || chain.Cmp(big.NewInt(int64(tx.ChainID))) != 0 {
		return result
	}
	anchor, err := client.BlockReferenceAt(ctx, "latest")
	if err != nil {
		return result
	}
	value := ParseOptionalBigInt(tx.Value)
	if value == nil || value.Sign() < 0 || value.BitLen() > 256 {
		return result
	}
	call := rpc.TransactionCall{From: from, To: tx.To, Value: "0x" + value.Text(16), Data: tx.Data}
	result.SimulationScope = "full-envelope"
	_, result.Observations.Call = observePinned(anchor, func(block rpc.BlockReference) ([]byte, error) { return client.EthCallTransaction(ctx, call, block) })
	gas, gasObs := observePinned(anchor, func(block rpc.BlockReference) (*big.Int, error) {
		value, err := client.EstimateGasTransaction(ctx, call, block)
		if err == nil && (value == nil || value.Sign() <= 0) {
			return nil, errors.New("invalid gas result")
		}
		return value, err
	})
	result.Observations.Gas = gasObs
	balance, balanceObs := observePinned(anchor, func(block rpc.BlockReference) (*big.Int, error) {
		value, err := client.GetBalanceAt(ctx, from, block)
		if err == nil && (value == nil || value.Sign() < 0) {
			return nil, errors.New("invalid balance result")
		}
		return value, err
	})
	result.Observations.Balance = balanceObs
	if allowance != nil {
		current, obs := observePinned(anchor, func(block rpc.BlockReference) (*big.Int, error) {
			return allowanceAt(ctx, client, from, allowance, block)
		})
		obs.TokenAddress = allowance.TokenAddress
		obs.Spender = allowance.Spender
		result.Observations.Allowance = obs
		if obs.ObservationStatus == "succeeded" {
			result.CurrentAllowance = current
		}
	}
	// Gas price is a live quote, never a state read at the historical anchor.
	price, err := client.GasPriceQuote(ctx)
	result.Observations.FeeQuote = observation("succeeded", "")
	result.Observations.FeeQuote.Pinning = "latest-only"
	if err != nil || price == nil || price.Sign() < 0 {
		price = nil
		result.Observations.FeeQuote.ObservationStatus = "unavailable"
		result.Observations.FeeQuote.Reason = "fee-quote-unavailable"
	}
	end, err := client.BlockReferenceAt(ctx, anchor.Number)
	endChain, chainErr := client.VerifiedChainID(ctx)
	if err != nil || end.Number != anchor.Number || !strings.EqualFold(end.Hash, anchor.Hash) || chainErr != nil || endChain == nil || endChain.Cmp(chain) != 0 {
		invalidateSnapshot(&result)
		return result
	}
	if gasObs.ObservationStatus != "succeeded" {
		gas = nil
	}
	if balanceObs.ObservationStatus != "succeeded" {
		balance = nil
	}
	var callOK *bool
	if state := result.Observations.Call.ObservationStatus; state == "succeeded" || state == "reverted" {
		ok := state == "succeeded"
		callOK = &ok
	}
	computed := finalizeSimulation(gas, price, "", callOK, balance, "")
	computed.SimulationScope = result.SimulationScope
	computed.Observations = result.Observations
	computed.CurrentAllowance = result.CurrentAllowance
	computed.ConsistentSnapshot = observationsConsistent(result.Observations)
	if !computed.ConsistentSnapshot {
		reason := "Complete canonical simulation evidence unavailable"
		computed.ErrorMessage = &reason
	}
	return computed
}

func allowanceAt(ctx context.Context, client transactionSimulator, owner string, req *AllowanceRequest, block rpc.BlockReference) (*big.Int, error) {
	ownerArg, err := abi.EncodeAddress(owner)
	if err != nil {
		return nil, err
	}
	spenderArg, err := abi.EncodeAddress(req.Spender)
	if err != nil {
		return nil, err
	}
	call := rpc.TransactionCall{From: owner, To: req.TokenAddress, Value: "0x0", Data: abi.EncodeCall("allowance(address,address)", ownerArg, spenderArg)}
	data, err := client.EthCallTransaction(ctx, call, block)
	if err != nil {
		return nil, err
	}
	if len(data) != 32 {
		return nil, errors.New("invalid allowance response")
	}
	return new(big.Int).SetBytes(data), nil
}

func invalidateSnapshot(result *SimulationResult) {
	for _, obs := range []*SimulationObservation{&result.Observations.Call, &result.Observations.Gas, &result.Observations.Balance, &result.Observations.Allowance} {
		if obs.ObservationStatus != "not-required" {
			obs.ObservationStatus = "unavailable"
			obs.Reason = "canonical-anchor-changed-or-unverifiable"
		}
	}
	result.CurrentAllowance = nil
	reason := "Canonical block or chain changed during observation; state evidence was discarded"
	result.ErrorMessage = &reason
}
