package portfolio

import (
	"regexp"
	"sort"
	"strings"
)

// RawAsset is the pre-visibility-filter shape produced by
// collectRawAssets. Metadata is a discriminated union by Origin; the
// spam-filter rules inspect it but it's stripped before the JSON
// payload is emitted.
type RawAsset struct {
	ID            string        `json:"id"`
	AssetType     string        `json:"assetType"`
	Symbol        string        `json:"symbol"`
	Name          string        `json:"name"`
	ChainID       int           `json:"chainId"`
	RawBalance    string        `json:"rawBalance"`
	ValueUSD      float64       `json:"valueUsd"`
	Status        string        `json:"status"`
	Source        string        `json:"source"`
	Address       *string       `json:"address"`
	WalletAddress *string       `json:"walletAddress"`
	UpdatedAt     string        `json:"updatedAt"`
	Metadata      *RawAssetMeta `json:"-"`
}

// RawAssetMeta carries the inputs spam-filter rules look at. Origin
// determines whether spam rules apply at all — only "wallet-discovered"
// items are candidates for spam filtering.
type RawAssetMeta struct {
	Origin         string // protocol-position | created-by-owner | wallet-discovered
	IsOfficial     bool
	CreatorAddress *string
	Tags           []string
	MarketCap      *float64
}

// VisibilityItem is the post-filter, JSON-emitted asset row (metadata
// stripped — matches the NestJS stripPortfolioVisibilityMetadata).
type VisibilityItem struct {
	ID            string  `json:"id"`
	AssetType     string  `json:"assetType"`
	Symbol        string  `json:"symbol"`
	Name          string  `json:"name"`
	ChainID       int     `json:"chainId"`
	RawBalance    string  `json:"rawBalance"`
	ValueUSD      float64 `json:"valueUsd"`
	Status        string  `json:"status"`
	Source        string  `json:"source"`
	Address       *string `json:"address"`
	WalletAddress *string `json:"walletAddress"`
	UpdatedAt     string  `json:"updatedAt"`
}

// FilterSummary mirrors AtlasPortfolioAssetFilters on the FE.
type FilterSummary struct {
	SpamFilterLevel       string `json:"spamFilterLevel"`
	TotalCount            int    `json:"totalCount"`
	VisibleCount          int    `json:"visibleCount"`
	FilteredCount         int    `json:"filteredCount"`
	HiddenConfiguredCount int    `json:"hiddenConfiguredCount"`
	HiddenMatchedCount    int    `json:"hiddenMatchedCount"`
	SpamFilteredCount     int    `json:"spamFilteredCount"`
}

// AssetInventory is the {items, filterSummary} envelope GET
// /portfolio/assets returns.
type AssetInventory struct {
	Items         []VisibilityItem `json:"items"`
	FilterSummary FilterSummary    `json:"filterSummary"`
}

// emptyInventory mirrors NestJS getEmptyAssetInventory().
func emptyInventory() AssetInventory {
	return AssetInventory{
		Items: []VisibilityItem{},
		FilterSummary: FilterSummary{
			SpamFilterLevel:       "standard",
			TotalCount:            0,
			VisibleCount:          0,
			FilteredCount:         0,
			HiddenConfiguredCount: 0,
			HiddenMatchedCount:    0,
			SpamFilteredCount:     0,
		},
	}
}

// applyVisibility filters + sorts the raw asset list. Mirrors
// portfolio-asset-visibility.ts byte-for-byte: hidden-key match
// removes the asset (any candidate key match against the configured
// hidden-assets set), spam rules remove only wallet-discovered items
// matching the level-specific signal set, then sort by valueUsd DESC
// with updatedAt DESC as tiebreaker.
func applyVisibility(assets []RawAsset, hiddenKeys []string, spamFilterLevel string) AssetInventory {
	hiddenSet := make(map[string]struct{}, len(hiddenKeys))
	for _, k := range hiddenKeys {
		hiddenSet[k] = struct{}{}
	}
	level := normalizeSpamFilterLevel(spamFilterLevel)

	hiddenMatched := 0
	spamFiltered := 0
	visible := make([]RawAsset, 0, len(assets))

	for _, a := range assets {
		candidates := buildHiddenAssetCandidateKeys(a.ChainID, a.Address, a.Symbol, a.WalletAddress)
		matched := false
		for _, c := range candidates {
			if _, ok := hiddenSet[c]; ok {
				matched = true
				break
			}
		}
		if matched {
			hiddenMatched++
			continue
		}
		if shouldFilterAsSpam(a, level) {
			spamFiltered++
			continue
		}
		visible = append(visible, a)
	}

	sort.SliceStable(visible, func(i, j int) bool {
		if visible[i].ValueUSD != visible[j].ValueUSD {
			return visible[i].ValueUSD > visible[j].ValueUSD
		}
		return visible[i].UpdatedAt > visible[j].UpdatedAt
	})

	items := make([]VisibilityItem, 0, len(visible))
	for _, a := range visible {
		items = append(items, VisibilityItem{
			ID:            a.ID,
			AssetType:     a.AssetType,
			Symbol:        a.Symbol,
			Name:          a.Name,
			ChainID:       a.ChainID,
			RawBalance:    a.RawBalance,
			ValueUSD:      a.ValueUSD,
			Status:        a.Status,
			Source:        a.Source,
			Address:       a.Address,
			WalletAddress: a.WalletAddress,
			UpdatedAt:     a.UpdatedAt,
		})
	}

	return AssetInventory{
		Items: items,
		FilterSummary: FilterSummary{
			SpamFilterLevel:       level,
			TotalCount:            len(assets),
			VisibleCount:          len(items),
			FilteredCount:         hiddenMatched + spamFiltered,
			HiddenConfiguredCount: len(hiddenKeys),
			HiddenMatchedCount:    hiddenMatched,
			SpamFilteredCount:     spamFiltered,
		},
	}
}

// buildHiddenAssetCandidateKeys mirrors common/utils/web3-hidden-asset.ts.
// The full grid: {all-wallets, walletAddress} × {compound, addressOnly,
// symbolOnly}. Order matches the JS flatMap exactly so the resulting
// key set ID-equates with what NestJS stores.
func buildHiddenAssetCandidateKeys(chainID int, assetAddress *string, symbol string, walletAddress *string) []string {
	// LinkedHashSet semantics: insertion order preserved, duplicates
	// dropped on second insert.
	scopes := []string{"all-wallets"}
	scopeSeen := map[string]struct{}{"all-wallets": {}}
	if walletAddress != nil {
		w := strings.ToLower(*walletAddress)
		if _, dup := scopeSeen[w]; !dup {
			scopes = append(scopes, w)
			scopeSeen[w] = struct{}{}
		}
	}

	identities := []string{resolveAssetIdentity(assetAddress, symbol)}
	idSeen := map[string]struct{}{identities[0]: {}}
	if assetAddress != nil && *assetAddress != "" {
		v := resolveAssetIdentity(assetAddress, "")
		if _, dup := idSeen[v]; !dup {
			identities = append(identities, v)
			idSeen[v] = struct{}{}
		}
	}
	if symbol != "" {
		v := resolveAssetIdentity(nil, symbol)
		if _, dup := idSeen[v]; !dup {
			identities = append(identities, v)
			idSeen[v] = struct{}{}
		}
	}

	out := make([]string, 0, len(scopes)*len(identities))
	for _, scope := range scopes {
		for _, identity := range identities {
			out = append(out, formatHiddenKey(chainID, scope, identity))
		}
	}
	return out
}

func resolveAssetIdentity(assetAddress *string, symbol string) string {
	if assetAddress != nil && *assetAddress != "" {
		return strings.ToLower(*assetAddress)
	}
	s := strings.ToUpper(strings.TrimSpace(symbol))
	if s == "" {
		s = "UNKNOWN"
	}
	return "symbol:" + s
}

func formatHiddenKey(chainID int, scope, identity string) string {
	return strings.Join([]string{intStr(chainID), scope, identity}, ":")
}

// intStr avoids strconv import just for one digit-formatter call.
func intStr(n int) string {
	// Fast path for common positive small ints.
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	digits := make([]byte, 0, 8)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

var spamPhraseRE = regexp.MustCompile(`(?i)airdrop|claim|reward|bonus|free|visit|http|www\.|t\.me`)
var spamTagRE = regexp.MustCompile(`(?i)spam|scam|phishing|airdrop`)

// shouldFilterAsSpam reproduces the multi-signal logic from
// portfolio-asset-visibility.ts. Only wallet-discovered items can ever
// be filtered as spam — protocol-position / created-by-owner pass
// through unconditionally.
func shouldFilterAsSpam(a RawAsset, level string) bool {
	if a.Metadata == nil || a.Metadata.Origin != "wallet-discovered" {
		return false
	}
	signals := detectSpamSignals(a)
	if len(signals) == 0 {
		return false
	}
	switch level {
	case "relaxed":
		return containsAny(signals, "suspicious-label", "spam-tag")
	case "strict":
		return true
	default: // "standard"
		for _, s := range signals {
			if s != "thin-market" {
				return true
			}
		}
		return false
	}
}

// detectSpamSignals returns the unique set of signals applicable to a.
// Matches the JS Set<string> + the regex patterns / threshold checks.
func detectSpamSignals(a RawAsset) []string {
	signalSet := map[string]struct{}{}
	add := func(s string) { signalSet[s] = struct{}{} }

	search := strings.ToLower(a.Symbol + " " + a.Name)
	if spamPhraseRE.MatchString(search) {
		add("suspicious-label")
	}
	if a.Metadata != nil {
		for _, tag := range a.Metadata.Tags {
			if spamTagRE.MatchString(tag) {
				add("spam-tag")
				break
			}
		}
	}
	if a.Address == nil || *a.Address == "" {
		add("missing-contract-address")
	}
	isOfficial := a.Metadata != nil && a.Metadata.IsOfficial
	statusLC := strings.ToLower(a.Status)
	if !isOfficial && statusLC != "active" && statusLC != "launched" {
		add("unverified-status")
	}
	marketCap := 0.0
	if a.Metadata != nil && a.Metadata.MarketCap != nil {
		marketCap = *a.Metadata.MarketCap
	}
	if !isOfficial && marketCap <= 10_000 && a.ValueUSD <= 0 {
		add("thin-market")
	}

	out := make([]string, 0, len(signalSet))
	for s := range signalSet {
		out = append(out, s)
	}
	return out
}

func containsAny(haystack []string, needles ...string) bool {
	for _, n := range needles {
		for _, h := range haystack {
			if h == n {
				return true
			}
		}
	}
	return false
}

func normalizeSpamFilterLevel(level string) string {
	switch level {
	case "relaxed", "strict":
		return level
	default:
		return "standard"
	}
}
