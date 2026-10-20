package executionregistry

import (
	"context"
	"sort"
	"strconv"
)

// ---- review (§5.3) ----

type ReviewBinding struct {
	Operation     string `json:"operation"`
	Market        string `json:"market"`
	BindingDigest string `json:"bindingDigest"`
}

// ReviewContext is advisory: it never upgrades a review.
type ReviewContext struct {
	State            string          `json:"state"` // bound | unbound | unavailable
	Reason           string          `json:"reason,omitempty"`
	RegistryRevision string          `json:"registryRevision,omitempty"`
	Bindings         []ReviewBinding `json:"bindings,omitempty"`
	addresses        map[string]string
	interfaces       map[string][]string
	chainID          int
}

// ContractAddress serves the review decoder from the same snapshot read.
func (c ReviewContext) ContractAddress(chainID int, name string) (string, bool) {
	if chainID != c.chainID {
		return "", false
	}
	address, ok := c.addresses[name]
	return address, ok
}

// ResolveReview reads the active revision once and reports which enabled
// bindings the exact transaction bytes satisfy. Identical approve bytes are
// valid for every market sharing the collateral token, so all matches are
// listed rather than one being guessed.
func (s *Service) ResolveReview(ctx context.Context, chainID int, to, data string) ReviewContext {
	if s == nil || s.store == nil {
		return ReviewContext{State: "unavailable", Reason: ReasonAuthorityUnavailable}
	}
	active, err := s.store.ActiveRevision(ctx)
	if err != nil {
		return ReviewContext{State: "unavailable", Reason: unavailableReason(err)}
	}
	ctxOut := ReviewContext{RegistryRevision: strconv.FormatInt(active.Revision, 10), addresses: map[string]string{}, interfaces: map[string][]string{}, chainID: chainID}
	entries := active.Snapshot.entriesByChain(chainID)
	for name, e := range entries {
		ctxOut.addresses[name] = e.Address
		for _, id := range e.Interfaces {
			if _, supported := s.manifest.Interfaces[id]; supported {
				ctxOut.interfaces[name] = append(ctxOut.interfaces[name], id)
			}
		}
	}
	registered, disabled, incompatible := false, false, false
	for _, b := range active.Snapshot.Bindings {
		if b.ChainID != chainID || entries[b.Target].Address != lower(to) {
			continue
		}
		registered = true
		iface, ok := s.manifest.Interfaces[b.InterfaceID]
		if !ok || !contains(entries[b.Target].Interfaces, b.InterfaceID) {
			incompatible = true
			continue
		}
		resolved, err := resolve(active.Snapshot, active.Revision, chainID, b.BindingDigest)
		if err != nil || !bytesMatch(iface, resolved, to, data) {
			continue
		}
		if b.Status != "enabled" {
			disabled = true
			continue
		}
		ctxOut.Bindings = append(ctxOut.Bindings, ReviewBinding{Operation: b.Operation, Market: b.Market, BindingDigest: b.BindingDigest})
	}
	switch {
	case len(ctxOut.Bindings) > 0:
		ctxOut.State = "bound"
	case disabled:
		ctxOut.State, ctxOut.Reason = "unbound", "operationDisabled"
	case incompatible:
		ctxOut.State, ctxOut.Reason = "unbound", "consumerIncompatible"
	case registered:
		ctxOut.State, ctxOut.Reason = "unbound", "operationWithdrawn"
	default:
		ctxOut.State, ctxOut.Reason = "unbound", "notRegistered"
	}
	return ctxOut
}

// ContractSupports keeps semantic authorization specific to the declared
// interface; registering an address alone never authorizes every selector.
func (c ReviewContext) ContractSupports(chainID int, name, interfaceID string) bool {
	return chainID == c.chainID && contains(c.interfaces[name], interfaceID)
}

// KnownSpenders uses the same read as the semantic decoder, avoiding a live
// deployment-file change between review rules and execution context lookup.
func (c ReviewContext) KnownSpenders(chainID int) []string {
	if c.chainID != chainID {
		return nil
	}
	out := make([]string, 0, len(c.addresses))
	for _, address := range c.addresses {
		out = append(out, address)
	}
	sort.Strings(out)
	return out
}
