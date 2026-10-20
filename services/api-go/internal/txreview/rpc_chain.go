package txreview

import (
	"context"
	"math/big"
)

type rpcChainReader interface {
	ChainID(context.Context) (*big.Int, error)
}

func rpcForReviewChain(ctx context.Context, caller RPCCaller, chainID int) (RPCCaller, *Check) {
	unavailable := func() (RPCCaller, *Check) {
		return nil, &Check{ID: "simulation-chain-unverified", Status: StatusWarn, Severity: SeverityMedium,
			Title: "Simulation chain not verified", Summary: "The configured RPC could not be verified as the requested chain. No simulation, gas estimate, balance or allowance from that RPC is used for this review."}
	}
	reader, ok := caller.(rpcChainReader)
	if !ok || chainID <= 0 {
		return unavailable()
	}
	actual, err := reader.ChainID(ctx)
	if err != nil || actual == nil || actual.Cmp(big.NewInt(int64(chainID))) != 0 {
		return unavailable()
	}
	// The same client is used for identity and subsequent reads; the requested
	// chain ID alone cannot turn a single configured RPC into multi-chain evidence.
	return caller, nil
}
