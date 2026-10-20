package executionregistry

import (
	"fmt"
	"math"
	"sort"
	"strconv"
)

// ValidateSnapshot re-derives identities rather than trusting stored digests.
// Publication and activation are separate trust boundaries: a publisher may
// write rows directly, and a newer CLI may encounter an older staged snapshot.
func ValidateSnapshot(s Snapshot, manifest Manifest) error {
	if s.SchemaVersion != 1 || s.ManifestDigest != manifest.Digest || !digestPattern.MatchString(s.DeclarationsDigest) {
		return refuse("unsupported snapshot schema or manifest")
	}
	entries := map[int]map[string]Entry{}
	for _, e := range s.Entries {
		if entries[e.ChainID] == nil {
			entries[e.ChainID] = map[string]Entry{}
		}
		if _, exists := entries[e.ChainID][e.Name]; exists {
			return refuse("duplicate entry")
		}
		if e.ChainID <= 0 || e.CodeBlockNumber > math.MaxInt64 || !namePattern.MatchString(e.Name) || !addressPattern.MatchString(e.Address) || lower(e.Address) != e.Address || e.Address == zeroAddress ||
			!digestPattern.MatchString(e.ABIHash) || !codeHashPattern.MatchString(e.CodeHash) || e.CodeHash == emptyCodeHash || !codeHashPattern.MatchString(e.CodeBlockHash) ||
			e.DeploymentFile == "" || e.DeploymentTimestamp == "" || !sortedUnique(e.Interfaces) {
			return refuse("invalid entry %s", e.Name)
		}
		for _, id := range e.Interfaces {
			if _, ok := manifest.Interfaces[id]; !ok {
				return refuse("unsupported interface %s", id)
			}
		}
		if e.EntryDigest != EntryDigest(e) {
			return refuse("entry digest mismatch: %s", e.Name)
		}
		entries[e.ChainID][e.Name] = e
	}
	markets := map[string]Market{}
	for _, m := range s.Markets {
		key := strconv.Itoa(m.ChainID) + "/" + m.Symbol
		if _, exists := markets[key]; exists {
			return refuse("duplicate market")
		}
		if !marketPattern.MatchString(m.Symbol) || m.IndexDecimals < 0 || m.IndexDecimals > 36 || m.CollateralDecimals < 0 || m.CollateralDecimals > 30 || m.PricePrecision != 30 || !sortedUnique(m.Components) {
			return refuse("invalid market %s", key)
		}
		for _, name := range append([]string{m.PositionManager, m.IndexToken, m.CollateralToken}, m.Components...) {
			if _, exists := entries[m.ChainID][name]; !exists {
				return refuse("market %s has missing entry %s", key, name)
			}
		}
		if m.MarketDigest != MarketDigest(m, entries[m.ChainID]) {
			return refuse("market digest mismatch: %s", key)
		}
		markets[key] = m
	}
	seen := map[string]bool{}
	for _, b := range s.Bindings {
		key := strconv.Itoa(b.ChainID) + "/" + b.Market
		m, exists := markets[key]
		shape, known := operationShapes[b.Operation]
		if !exists || !known || seen[key+"/"+b.Operation] {
			return refuse("unknown or duplicate binding")
		}
		seen[key+"/"+b.Operation] = true
		fields := map[string]string{"positionManager": m.PositionManager, "indexToken": m.IndexToken, "collateralToken": m.CollateralToken}
		if b.Status != "enabled" && b.Status != "disabled" || b.Operation == OpClosePosition && b.Status != "enabled" || b.InterfaceID != shape.interfaceID || b.Target != fields[shape.target] || len(b.Roles) != len(shape.roles) {
			return refuse("invalid binding shape: %s", b.Operation)
		}
		if !contains(entries[b.ChainID][b.Target].Interfaces, b.InterfaceID) {
			return refuse("binding interface not declared by target")
		}
		for role, field := range shape.roles {
			if b.Roles[role] != fields[field] {
				return refuse("binding role mismatch: %s", role)
			}
		}
		if b.BindingDigest != BindingDigest(b, entries[b.ChainID], m) {
			return refuse("binding digest mismatch: %s", b.Operation)
		}
	}
	for key := range markets {
		if !seen[key+"/"+OpClosePosition] {
			return refuse("market %s has no enabled exit", key)
		}
	}
	if s.ContentDigest != ContentDigest(s) {
		return refuse("content digest mismatch")
	}
	return nil
}

func sortedUnique(values []string) bool {
	if !sort.StringsAreSorted(values) {
		return false
	}
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return false
		}
	}
	return true
}

func (s *Service) consumerCompatible(id, digest, interfaceID string) bool {
	// A self-reported interface list is not a manifest. Unknown builds must be
	// negotiated explicitly before they can claim the semantics of this build.
	_, known := s.manifest.Interfaces[interfaceID]
	return id == "web-trade" && digest == s.manifest.Digest && known
}

func snapshotValidationError(err error) error { return fmt.Errorf("%w: %v", ErrNotSealed, err) }
