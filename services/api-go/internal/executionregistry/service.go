package executionregistry

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Store is the read side the service needs; Repository implements it.
type Store interface {
	ActiveRevision(ctx context.Context) (Active, error)
	Revision(ctx context.Context, revision int64) (Snapshot, error)
}

// Service answers current/check, resolves review contexts and classifies
// report claims. Every call reads the authority; nothing is cached (§3.3).
type Service struct {
	store    Store
	manifest Manifest
}

func NewService(store Store, manifest Manifest) *Service {
	return &Service{store: store, manifest: manifest}
}

// Manifest is the interface manifest this server supports.
func (s *Service) Manifest() Manifest { return s.manifest }

// Unavailable reasons (§5.2, §7).
const (
	ReasonNoActiveRevision     = "noActiveRevision"
	ReasonAuthorityUnavailable = "authorityUnavailable"
)

func unavailableReason(err error) string {
	if errors.Is(err, ErrNoActiveRevision) {
		return ReasonNoActiveRevision
	}
	return ReasonAuthorityUnavailable
}

// ---- current ----

type CurrentEntry struct {
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Interfaces  []string `json:"interfaces"`
	EntryDigest string   `json:"entryDigest"`
}

type CurrentMarket struct {
	Symbol             string   `json:"symbol"`
	PositionManager    string   `json:"positionManager"`
	IndexToken         string   `json:"indexToken"`
	CollateralToken    string   `json:"collateralToken"`
	IndexDecimals      int      `json:"indexDecimals"`
	CollateralDecimals int      `json:"collateralDecimals"`
	PricePrecision     int      `json:"pricePrecision"`
	Components         []string `json:"components"`
	MarketDigest       string   `json:"marketDigest"`
}

type CurrentBinding struct {
	Operation     string            `json:"operation"`
	Market        string            `json:"market"`
	Status        string            `json:"status"`
	InterfaceID   string            `json:"interfaceId"`
	Target        string            `json:"target"`
	Roles         map[string]string `json:"roles"`
	BindingDigest string            `json:"bindingDigest"`
}

type CurrentResponse struct {
	SchemaVersion     int              `json:"schemaVersion"`
	Revision          string           `json:"revision"`
	ActivationVersion string           `json:"activationVersion"`
	ChainID           int              `json:"chainId"`
	ManifestDigest    string           `json:"manifestDigest"`
	ContentDigest     string           `json:"contentDigest"`
	Entries           []CurrentEntry   `json:"entries"`
	Markets           []CurrentMarket  `json:"markets"`
	Bindings          []CurrentBinding `json:"bindings"`
	ObservedAt        string           `json:"observedAt"`
}

// Current is the versioned per-chain view of the active revision.
func (s *Service) Current(ctx context.Context, chainID int) (CurrentResponse, error) {
	active, err := s.store.ActiveRevision(ctx)
	if err != nil {
		return CurrentResponse{}, err
	}
	out := CurrentResponse{SchemaVersion: 1, Revision: strconv.FormatInt(active.Revision, 10), ActivationVersion: strconv.FormatInt(active.ActivationVersion, 10),
		ChainID: chainID, ManifestDigest: active.Snapshot.ManifestDigest, ContentDigest: active.Snapshot.ContentDigest,
		Entries: []CurrentEntry{}, Markets: []CurrentMarket{}, Bindings: []CurrentBinding{}, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, e := range active.Snapshot.Entries {
		if e.ChainID == chainID {
			out.Entries = append(out.Entries, CurrentEntry{Name: e.Name, Address: e.Address, Interfaces: e.Interfaces, EntryDigest: e.EntryDigest})
		}
	}
	for _, m := range active.Snapshot.Markets {
		if m.ChainID == chainID {
			out.Markets = append(out.Markets, CurrentMarket{Symbol: m.Symbol, PositionManager: m.PositionManager, IndexToken: m.IndexToken,
				CollateralToken: m.CollateralToken, IndexDecimals: m.IndexDecimals, CollateralDecimals: m.CollateralDecimals,
				PricePrecision: m.PricePrecision, Components: m.Components, MarketDigest: m.MarketDigest})
		}
	}
	for _, b := range active.Snapshot.Bindings {
		if b.ChainID == chainID {
			out.Bindings = append(out.Bindings, CurrentBinding{Operation: b.Operation, Market: b.Market, Status: b.Status, InterfaceID: b.InterfaceID,
				Target: b.Target, Roles: b.Roles, BindingDigest: b.BindingDigest})
		}
	}
	return out, nil
}

// ---- check (§5.2) ----

type ConsumerClaim struct {
	ID             string   `json:"id"`
	ManifestDigest string   `json:"manifestDigest"`
	Interfaces     []string `json:"interfaces"`
}

type CheckRequest struct {
	ChainID          int           `json:"chainId"`
	RegistryRevision string        `json:"registryRevision"`
	Operation        string        `json:"operation"`
	Market           string        `json:"market"`
	BindingDigest    string        `json:"bindingDigest"`
	To               string        `json:"to"`
	Data             string        `json:"data"`
	Consumer         ConsumerClaim `json:"consumer"`
}

type CheckOutcome struct {
	HTTPStatus        int
	Code              string
	Reason            string
	ActiveRevision    int64
	ActivationVersion int64
}

var (
	revisionPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	consumerPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	dataPattern     = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
)

// ParseRevision accepts a canonical positive BIGINT decimal string.
func ParseRevision(raw string) (int64, bool) {
	if !revisionPattern.MatchString(raw) {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	return v, err == nil
}

func (r CheckRequest) valid() (int64, bool) {
	revision, ok := ParseRevision(r.RegistryRevision)
	if !ok || r.ChainID <= 0 || !IsOperation(r.Operation) || !marketPattern.MatchString(r.Market) || !digestPattern.MatchString(r.BindingDigest) ||
		!addressPattern.MatchString(r.To) || !dataPattern.MatchString(r.Data) || len(r.Data)%2 != 0 || len(r.Data) < 10 || len(r.Data) > 2+2*4096 || !consumerPattern.MatchString(r.Consumer.ID) ||
		!digestPattern.MatchString(r.Consumer.ManifestDigest) || len(r.Consumer.Interfaces) == 0 || len(r.Consumer.Interfaces) > 64 {
		return 0, false
	}
	for _, id := range r.Consumer.Interfaces {
		if !interfaceIDPattern.MatchString(id) {
			return 0, false
		}
	}
	return revision, true
}

func fail(status int, code, reason string, active Active) CheckOutcome {
	return CheckOutcome{HTTPStatus: status, Code: code, Reason: reason, ActiveRevision: active.Revision, ActivationVersion: active.ActivationVersion}
}

// Check compares a frozen intent with the ACTIVE revision immediately before
// the wallet request. Only the binding the intent depends on matters (D6).
func (s *Service) Check(ctx context.Context, req CheckRequest) CheckOutcome {
	claimed, ok := req.valid()
	if !ok {
		return CheckOutcome{HTTPStatus: http.StatusBadRequest, Code: "INVALID_INPUT"}
	}
	active, err := s.store.ActiveRevision(ctx)
	if err != nil {
		return CheckOutcome{HTTPStatus: http.StatusServiceUnavailable, Code: "REGISTRY_UNAVAILABLE", Reason: unavailableReason(err)}
	}
	if claimed != active.Revision {
		if _, err := s.store.Revision(ctx, claimed); errors.Is(err, ErrUnknownRevision) {
			return fail(http.StatusConflict, "EXECUTION_CONFIG_CHANGED", "unknownRevision", active)
		} else if err != nil {
			return CheckOutcome{HTTPStatus: http.StatusServiceUnavailable, Code: "REGISTRY_UNAVAILABLE", Reason: ReasonAuthorityUnavailable}
		}
	}
	binding, ok := active.Snapshot.Binding(req.ChainID, req.Operation, req.Market)
	if !ok {
		return fail(http.StatusUnprocessableEntity, "CAPABILITY_DISABLED", "operationWithdrawn", active)
	}
	if binding.Status != "enabled" {
		return fail(http.StatusUnprocessableEntity, "CAPABILITY_DISABLED", "operationDisabled", active)
	}
	iface, serverSupports := s.manifest.Interfaces[binding.InterfaceID]
	if !serverSupports || !s.consumerCompatible(req.Consumer.ID, req.Consumer.ManifestDigest, binding.InterfaceID) || !contains(req.Consumer.Interfaces, binding.InterfaceID) {
		return fail(http.StatusUnprocessableEntity, "CAPABILITY_DISABLED", "consumerIncompatible", active)
	}
	if binding.BindingDigest != req.BindingDigest {
		return fail(http.StatusConflict, "EXECUTION_CONFIG_CHANGED", "bindingChanged", active)
	}
	resolved, err := resolve(active.Snapshot, active.Revision, req.ChainID, binding.BindingDigest)
	if err != nil || !bytesMatch(iface, resolved, req.To, req.Data) {
		return fail(http.StatusConflict, "EXECUTION_CONFIG_CHANGED", "bytesMismatch", active)
	}
	return fail(http.StatusOK, "", "", active)
}

// bytesMatch verifies target, selector, exact shape and every bound role.
func bytesMatch(iface Interface, resolved ResolvedBinding, to, data string) bool {
	if lower(to) != resolved.TargetAddress {
		return false
	}
	call, ok := iface.Decode(data)
	if !ok {
		return false
	}
	for role, address := range resolved.RoleAddresses {
		if call[role] != address {
			return false
		}
	}
	return true
}

// ---- report classification (§6.2) ----

// Classification results.
const (
	StateRecorded      = "recorded"
	StateClaimMismatch = "claimMismatch"
	StateUnboundExit   = "unboundExit"
)

// ClassifyBound validates a bound claim against SEALED HISTORY (the claimed
// revision, active or not) and the RPC-verified transaction. A non-nil error
// means the registry could not be read: the caller stores claimUnverified.
func (s *Service) ClassifyBound(ctx context.Context, revision int64, chainID int, bindingDigest, operation, market, consumerID, manifestDigest, to, input string, inputKnown bool) (state, reason string, err error) {
	snapshot, err := s.store.Revision(ctx, revision)
	if errors.Is(err, ErrUnknownRevision) {
		return StateClaimMismatch, "unknownRevision", nil
	}
	if err != nil {
		return "", "", err
	}
	resolved, err := resolve(snapshot, revision, chainID, bindingDigest)
	switch {
	case errors.Is(err, errChainMismatch):
		return StateClaimMismatch, "chainMismatch", nil
	case err != nil:
		return StateClaimMismatch, "unknownBinding", nil
	}
	if resolved.Binding.Operation != operation || resolved.Binding.Market != market {
		return StateClaimMismatch, "unknownBinding", nil
	}
	if !s.consumerCompatible(consumerID, manifestDigest, resolved.Binding.InterfaceID) {
		return StateClaimMismatch, "evidenceIncomplete", nil
	}
	if lower(to) != resolved.TargetAddress {
		return StateClaimMismatch, "targetMismatch", nil
	}
	iface, ok := s.manifest.Interfaces[resolved.Binding.InterfaceID]
	if !inputKnown || !ok {
		return StateClaimMismatch, "evidenceIncomplete", nil
	}
	if !bytesMatch(iface, resolved, to, input) {
		return StateClaimMismatch, "bytesMismatch", nil
	}
	return StateRecorded, "", nil
}

// ClassifyUnboundExit needs no registry read: the RPC input must be a close
// interface of this server's manifest.
func (s *Service) ClassifyUnboundExit(input string, inputKnown bool) (state, reason string) {
	if !inputKnown {
		return StateClaimMismatch, "evidenceIncomplete"
	}
	for _, iface := range s.manifest.Interfaces {
		if strings.HasPrefix(iface.Signature, "closePosition(") {
			if _, ok := iface.Decode(input); ok {
				return StateUnboundExit, ""
			}
		}
	}
	return StateClaimMismatch, "notAnExit"
}
