package txreview

import (
	"context"
	"fmt"
	"time"
)

// ExecutionContextResolver reads the active ExecutionRegistrySnapshot once
// per review (LG-03b §5.3). The review stays advisory: the context never
// upgrades its status, and when the registry is wired the PositionManager
// decoder resolves addresses from that same snapshot read instead of the
// live deployment files.
type ExecutionContextResolver interface {
	ResolveReview(ctx context.Context, chainID int, to, data string) ReviewExecutionContext
}

// ReviewBinding is one enabled binding the exact transaction bytes satisfy.
type ReviewBinding struct {
	Operation     string `json:"operation"`
	Market        string `json:"market"`
	BindingDigest string `json:"bindingDigest"`
}

// ReviewExecutionContext is the additive tx-review field. State is bound,
// unbound or unavailable; identical approve bytes can satisfy several
// markets, so every match is listed instead of one being guessed.
type ReviewExecutionContext struct {
	State            string                `json:"state"`
	Reason           string                `json:"reason,omitempty"`
	RegistryRevision string                `json:"registryRevision,omitempty"`
	Bindings         []ReviewBinding       `json:"bindings,omitempty"`
	ValidUntil       *string               `json:"validUntil"`
	Lookup           ContractAddressLookup `json:"-"`
}

// The caller supplies its frozen review deadline. This metadata is advisory,
// not a new authority lease or a replacement for the calldata deadline.
func reviewValidUntil(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if len(*value) > 64 {
		return nil, fmt.Errorf("%w: invalid validUntil", ErrInvalidInput)
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil, fmt.Errorf("%w: validUntil must be an RFC3339 timestamp", ErrInvalidInput)
	}
	normalized := parsed.UTC().Format(time.RFC3339Nano)
	return &normalized, nil
}

// WithExecutionContext wires the resolver.
func (s *Service) WithExecutionContext(resolver ExecutionContextResolver) *Service {
	s.execution = resolver
	return s
}
