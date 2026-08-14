// Package markets ports the NestJS Markets module's public read endpoints
// to Go as part of the Phase 1 strangler-fig migration. The static market
// catalog mirrors legacy NestJS trading/trading-market.config.ts —
// kept hand-in-sync deliberately until both languages can read from a
// shared source-of-truth (Phase 5).
package markets

// SupportedChainIDs enumerates the L1/L2 networks the perp markets are
// deployed to. Order is significant for tests; the NestJS source uses
// `[11155111, 31337]` and we preserve it.
var SupportedChainIDs = []int{11155111, 31337}

// DefinitionTemplate is a chain-agnostic market spec. Cross-product with
// SupportedChainIDs in Definitions().
type DefinitionTemplate struct {
	Symbol                  string
	FundingRate             string
	IndexContractNames      []string
	CollateralContractNames []string
}

// Definition is a per-chain market spec (template × chainId).
type Definition struct {
	DefinitionTemplate
	ChainID int
}

var marketTemplates = []DefinitionTemplate{
	{
		Symbol:                  "ETH-USD",
		FundingRate:             "0",
		IndexContractNames:      []string{"MockWETH", "weth"},
		CollateralContractNames: []string{"MockUSDC", "usdc"},
	},
	{
		Symbol:                  "BTC-USD",
		FundingRate:             "0",
		IndexContractNames:      []string{"MockWBTC", "wbtc"},
		CollateralContractNames: []string{"MockUSDC", "usdc"},
	},
}

// Definitions returns the cross-product of marketTemplates × SupportedChainIDs,
// optionally filtered to one chain. Pass nil to disable filtering — matches
// the `chainId?: number` optional semantic on the NestJS side without
// reserving 0 as a sentinel (chainId 0 is technically valid in Web3).
func Definitions(chainIDFilter *int) []Definition {
	out := make([]Definition, 0, len(marketTemplates)*len(SupportedChainIDs))
	for _, chainID := range SupportedChainIDs {
		if chainIDFilter != nil && chainID != *chainIDFilter {
			continue
		}
		for _, template := range marketTemplates {
			out = append(out, Definition{
				DefinitionTemplate: template,
				ChainID:            chainID,
			})
		}
	}
	return out
}
