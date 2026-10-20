package portfolio

import "math"

const (
	ValuationVersion                 = 1
	SourceReady                      = "ready"
	SourceUnavailable                = "unavailable"
	BalanceToken                     = "token"
	BalanceBaseUnit                  = "base-unit"
	BalanceUSD30                     = "usd30"
	ValuationKnown                   = "known"
	ValuationUnavailable             = "unavailable"
	ReasonMissingUSDPrice            = "missing-usd-price"
	ReasonPositionEquityUnavailable  = "position-equity-unavailable"
	ReasonHolderValuationUnavailable = "holder-valuation-unavailable"
	createdTokensLimit               = 20
	holdingsLimit                    = 40
)

type Valuation struct {
	State            string   `json:"state"`
	KnownSubtotalUSD *float64 `json:"knownSubtotalUsd"`
	ValuedCount      int      `json:"valuedCount"`
	UnpricedCount    int      `json:"unpricedCount"`
}

type Coverage struct {
	Scope   string         `json:"scope"`
	Sources SourceCoverage `json:"sources"`
	Limits  CoverageLimits `json:"limits"`
}

type SourceCoverage struct {
	Staking       string `json:"staking"`
	Perp          string `json:"perp"`
	CreatedTokens string `json:"createdTokens"`
	Holdings      string `json:"holdings"`
}

type CoverageLimits struct {
	CreatedTokens int `json:"createdTokens"`
	Holdings      int `json:"holdings"`
}

func newCoverage() Coverage {
	return Coverage{
		Scope:   "indexed-protocol-assets",
		Sources: SourceCoverage{SourceUnavailable, SourceUnavailable, SourceUnavailable, SourceUnavailable},
		Limits:  CoverageLimits{CreatedTokens: createdTokensLimit, Holdings: holdingsLimit},
	}
}

func (s SourceCoverage) readyCount() int {
	count := 0
	for _, state := range []string{s.Staking, s.Perp, s.CreatedTokens, s.Holdings} {
		if state == SourceReady {
			count++
		}
	}
	return count
}

func knownValue(value *float64, status string) bool {
	return status == ValuationKnown && value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0
}

func normalizeAssetValuation(asset RawAsset) RawAsset {
	if knownValue(asset.ValueUSD, asset.ValuationStatus) {
		asset.ValuationReason = nil
		return asset
	}
	asset.ValueUSD = nil
	asset.ValuationStatus = ValuationUnavailable
	reason := ReasonHolderValuationUnavailable
	switch asset.AssetType {
	case "earn":
		reason = ReasonMissingUSDPrice
	case "perp":
		reason = ReasonPositionEquityUnavailable
	}
	asset.ValuationReason = &reason
	return asset
}

func normalizeDisplayValuations(assets []RawAsset) ([]RawAsset, bool) {
	subtotal := 0.0
	for _, asset := range assets {
		if knownValue(asset.ValueUSD, asset.ValuationStatus) {
			subtotal += *asset.ValueUSD
		}
	}
	if !math.IsInf(subtotal, 0) {
		return assets, false
	}
	// A non-finite subtotal cannot be represented in the display contract.
	// Keep row status and counts consistent instead of clipping an invented total.
	normalized := make([]RawAsset, len(assets))
	for i, asset := range assets {
		asset.ValuationStatus = ValuationUnavailable
		normalized[i] = normalizeAssetValuation(asset)
	}
	return normalized, true
}

// Counts and subtotal cover only returned, visible rows; they never prove that
// the indexed protocol sources cover every asset in the wallet.
func summarizeValuation(items []VisibilityItem) Valuation {
	out := Valuation{State: "empty"}
	subtotal := 0.0
	for _, item := range items {
		if knownValue(item.ValueUSD, item.ValuationStatus) {
			out.ValuedCount++
			subtotal += *item.ValueUSD
		} else {
			out.UnpricedCount++
		}
	}
	if out.ValuedCount > 0 {
		out.KnownSubtotalUSD = &subtotal
		out.State = "complete"
		if out.UnpricedCount > 0 {
			out.State = "partial"
		}
	} else if out.UnpricedCount > 0 {
		out.State = "unavailable"
	}
	return out
}

func (a AssetInventory) compatibleValue() *float64 {
	if a.Coverage.Sources.readyCount() != 4 || a.truncated || a.Valuation.State != "complete" {
		return nil
	}
	return a.Valuation.KnownSubtotalUSD
}

func (a AssetInventory) dataCompleteness() string {
	if a.Coverage.Sources.readyCount() == 0 {
		return "unavailable"
	}
	if a.FilterSummary.FilteredCount > 0 {
		return "partial-live-filtered"
	}
	return "partial-live"
}
