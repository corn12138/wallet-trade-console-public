package ai

import (
	"fmt"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/txreview"
)

// Only server-computed observations reach the model. Provider error text and
// endpoints are excluded because they may contain secrets or instructions.
func appendSimulationEvidence(b *strings.Builder, sim txreview.SimulationResult, callResult string) {
	if sim.SimulationScope != "full-envelope" {
		fmt.Fprintf(b, "RPC read-call result: %s\n", callResult)
		b.WriteString("Read-call scope: this call omits the complete sender/value envelope. It is not a full wallet transaction simulation, and its outcome does not prove wallet execution success or failure.\n")
		return
	}
	b.WriteString("Simulation scope: full-envelope (from, to, value and calldata); outcome describes only the observed block and does not guarantee future execution.\n")
	fmt.Fprintf(b, "Required observations form one consistent snapshot: %t\n", sim.ConsistentSnapshot)
	items := []struct {
		name        string
		observation txreview.SimulationObservation
	}{
		{"call", sim.Observations.Call}, {"gas", sim.Observations.Gas},
		{"balance", sim.Observations.Balance}, {"allowance", sim.Observations.Allowance},
		{"feeQuote", sim.Observations.FeeQuote},
	}
	for _, item := range items {
		o := item.observation
		fmt.Fprintf(b, "Observation %s: status=%s; source=%s; pinning=%s; observedAt=%s", item.name, o.ObservationStatus, o.Source, o.Pinning, o.ObservedAt)
		if o.BlockReference != nil {
			fmt.Fprintf(b, "; blockNumber=%s; blockHash=%s", o.BlockReference.Number, o.BlockReference.Hash)
		}
		b.WriteByte('\n')
	}
	b.WriteString("Fee quote is latest-only and excluded from snapshot consistency. Unavailable or unsupported evidence is not an execution revert.\n")
}
