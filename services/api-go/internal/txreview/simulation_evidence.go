package txreview

import (
	"context"
	"math/big"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

type ObservationBlockReference struct {
	Number  string `json:"number"`
	Hash    string `json:"hash"`
	Pinning string `json:"pinning"`
}

type SimulationObservation struct {
	ObservationStatus string                     `json:"observationStatus"`
	Source            string                     `json:"source"`
	BlockReference    *ObservationBlockReference `json:"blockReference,omitempty"`
	ObservedAt        string                     `json:"observedAt"`
	Pinning           string                     `json:"pinning"`
	Reason            string                     `json:"reason,omitempty"`
	TokenAddress      string                     `json:"tokenAddress,omitempty"`
	Spender           string                     `json:"spender,omitempty"`
}

type SimulationObservations struct {
	Call      SimulationObservation `json:"call"`
	Gas       SimulationObservation `json:"gas"`
	Balance   SimulationObservation `json:"balance"`
	Allowance SimulationObservation `json:"allowance"`
	FeeQuote  SimulationObservation `json:"feeQuote"`
}

type transactionSimulator interface {
	VerifiedChainID(context.Context) (*big.Int, error)
	BlockReferenceAt(context.Context, string) (rpc.BlockReference, error)
	EthCallTransaction(context.Context, rpc.TransactionCall, rpc.BlockReference) ([]byte, error)
	EstimateGasTransaction(context.Context, rpc.TransactionCall, rpc.BlockReference) (*big.Int, error)
	GetBalanceAt(context.Context, string, rpc.BlockReference) (*big.Int, error)
	GasPriceQuote(context.Context) (*big.Int, error)
}

func observation(status, reason string) SimulationObservation {
	return SimulationObservation{ObservationStatus: status, Source: "rpc", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Pinning: "unavailable", Reason: reason}
}

func emptyObservations(reason string) SimulationObservations {
	missing := observation("unavailable", reason)
	unused := observation("not-required", "")
	unused.Source = "not-requested"
	return SimulationObservations{missing, missing, missing, unused, missing}
}

// A hash capability failure may fall back only to the same height. The caller
// rechecks its canonical hash after all observations, including allowance.
func observePinned[T any](anchor rpc.BlockReference, read func(rpc.BlockReference) (T, error)) (T, SimulationObservation) {
	value, err := read(anchor)
	if err != nil && rpc.IsUnsupportedBlock(err) {
		anchor.Pinning = "number-pinned"
		value, err = read(anchor)
	}
	obs := observation("succeeded", "")
	obs.Pinning = anchor.Pinning
	number, _ := new(big.Int).SetString(strings.TrimPrefix(anchor.Number, "0x"), 16)
	if number == nil {
		obs.ObservationStatus = "unavailable"
		obs.Reason = "invalid-block-number"
		return value, obs
	}
	obs.BlockReference = &ObservationBlockReference{Number: number.String(), Hash: anchor.Hash, Pinning: anchor.Pinning}
	if err != nil {
		obs.ObservationStatus = "unavailable"
		obs.Reason = "rpc-unavailable"
		if rpc.IsExecutionRevert(err) {
			obs.ObservationStatus = "reverted"
			obs.Reason = "execution-reverted"
		}
		if rpc.IsUnsupportedBlock(err) {
			obs.ObservationStatus = "unsupported"
			obs.Reason = "pinned-block-unsupported"
		}
	}
	return value, obs
}

func observationsConsistent(obs SimulationObservations) bool {
	var anchor *ObservationBlockReference
	for _, item := range []SimulationObservation{obs.Call, obs.Gas, obs.Balance, obs.Allowance} {
		if item.ObservationStatus == "not-required" {
			continue
		}
		if item.ObservationStatus != "succeeded" || item.BlockReference == nil {
			return false
		}
		if anchor == nil {
			anchor = item.BlockReference
			continue
		}
		if anchor.Number != item.BlockReference.Number || anchor.Hash != item.BlockReference.Hash {
			return false
		}
	}
	return anchor != nil
}

func simulationChecks(sim SimulationResult) []Check {
	if sim.SimulationScope == "full-envelope" {
		if sim.Observations.Call.ObservationStatus == "reverted" || sim.Observations.Gas.ObservationStatus == "reverted" {
			return []Check{{ID: "simulation-reverted", Status: StatusFail, Severity: SeverityHigh, Title: "Transaction simulation reverted", Summary: "The complete sender/value transaction reverted at the observed canonical block. Future execution remains state dependent."}}
		}
		if !sim.ConsistentSnapshot {
			return []Check{{ID: "simulation-snapshot-unavailable", Status: StatusWarn, Severity: SeverityMedium, Title: "Complete simulation evidence unavailable", Summary: "One or more required observations are unavailable, unsupported, or cannot be tied to one canonical block. This does not prove transaction failure."}}
		}
		return nil
	}
	checks := []Check{{ID: "simulation-scope-limited", Status: StatusWarn, Severity: SeverityMedium, Title: "Complete transaction simulation unavailable", Summary: "Only legacy read-call evidence or unavailable RPC evidence is present; sender/value execution and a consistent block snapshot are not verified."}}
	if sim.CallSucceeded != nil && !*sim.CallSucceeded {
		checks = append(checks, Check{ID: "simulation-call-failed", Status: StatusWarn, Severity: SeverityMedium, Title: "RPC read-call failed", Summary: "The legacy read-call omits sender/value and cannot establish that the wallet transaction will revert."})
	}
	return checks
}
