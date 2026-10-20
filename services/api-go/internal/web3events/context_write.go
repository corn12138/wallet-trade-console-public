package web3events

import (
	"context"
	"encoding/json"
	"log/slog"
)

// ContextClassifier validates a claim against sealed registry history
// (implemented by executionregistry.Service, wired in httpx). ClassifyBound
// returns an error only when the registry could not be read.
type ContextClassifier interface {
	ClassifyBound(ctx context.Context, revision int64, chainID int, bindingDigest, operation, market, consumerID, manifestDigest, to, input string, inputKnown bool) (state, reason string, err error)
	ClassifyUnboundExit(input string, inputKnown bool) (state, reason string)
}

// Context states (§6.3). A nil *ContextWrite is "no context": the insert
// leaves the columns null, which is final notRecorded.
const (
	ContextClaimUnverified = "claimUnverified"
	ContextRecorded        = "recorded"
	ContextClaimMismatch   = "claimMismatch"
	ContextUnboundExit     = "unboundExit"
)

// ContextWrite is one classified context, every value already bounded.
type ContextWrite struct {
	State           string
	Mode            *string
	ClaimDigest     *string
	ClaimedRevision *int64
	Operation       *string
	Market          *string
	Binding         *string
	Consumer        *string
	Manifest        *string
	Reason          *string
	Diagnostic      *string
	Revision        *int64
}

// WithContextClassifier enables execution-context classification. Without
// it (unmigrated schema, no registry) claims are ignored and reports behave
// exactly as before.
func (s *Service) WithContextClassifier(classifier ContextClassifier) *Service {
	s.classifier = classifier
	return s
}

func str(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// classifyContext runs before the write transaction (§6.2).
func (s *Service) classifyContext(ctx context.Context, raw json.RawMessage, chainID int, to, input *string) *ContextWrite {
	if s.classifier == nil {
		return nil
	}
	result := NormalizeClaim(raw)
	switch result.Kind {
	case ClaimAbsent:
		return nil
	case ClaimMalformed:
		return &ContextWrite{State: ContextClaimMismatch, Reason: str("malformed"), Diagnostic: str(result.Diagnostic)}
	}
	c := result.Claim
	w := &ContextWrite{Mode: str(c.Mode), ClaimDigest: str(result.Digest), Operation: str(c.Operation), Market: str(c.Market),
		Consumer: str(c.ConsumerID), Manifest: str(c.ManifestDigest)}
	inputValue, inputKnown := "", input != nil
	if inputKnown {
		inputValue = *input
	}
	if c.Mode == "unboundExit" {
		state, reason := s.classifier.ClassifyUnboundExit(inputValue, inputKnown)
		w.State = state
		if state == ContextUnboundExit {
			w.Reason = str(c.Reason)
		} else {
			w.Reason = str(reason)
		}
		return w
	}
	revision := c.RegistryRevision
	w.ClaimedRevision, w.Binding = &revision, str(c.BindingDigest)
	target := ""
	if to != nil {
		target = *to
	}
	state, reason, err := s.classifier.ClassifyBound(ctx, revision, chainID, c.BindingDigest, c.Operation, c.Market, c.ConsumerID, c.ManifestDigest, target, inputValue, inputKnown)
	if err != nil {
		slog.WarnContext(ctx, "execution context left unverified: registry unreadable")
		w.State = ContextClaimUnverified
		return w
	}
	w.State, w.Reason = state, str(reason)
	if state == ContextRecorded {
		w.Revision = &revision
	}
	return w
}
