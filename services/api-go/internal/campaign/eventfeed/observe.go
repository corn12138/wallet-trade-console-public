package eventfeed

import (
	"context"
	"errors"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// Subject is a verified push credential. The raw token stays private so every
// observation re-authenticates the persistent session instead of trusting the
// result captured when the connection opened.
type Subject struct {
	token    string
	owner    string
	identity string
}

func (Subject) String() string   { return "eventfeed.Subject{redacted}" }
func (Subject) GoString() string { return "eventfeed.Subject{redacted}" }

// Authenticate verifies a push credential with the same verifier and session
// store as the strict HTTP middleware.
func (s *Service) Authenticate(ctx context.Context, token string) (*Subject, error) {
	if s == nil {
		return nil, failure("WEB3_SESSION_UNAVAILABLE", 503)
	}
	claims, err := s.verifier.Authenticate(ctx, token)
	if err != nil {
		return nil, authorization(err)
	}
	return &Subject{token: token, owner: claims.Sub, identity: sessionScope(claims)}, nil
}

func (s *Service) revalidate(ctx context.Context, subject *Subject) error {
	claims, err := s.verifier.Authenticate(ctx, subject.token)
	if err != nil {
		return authorization(err)
	}
	// A credential that now names another wallet or session generation is not
	// the identity this subscription was authorized for.
	if claims.Sub != subject.owner || sessionScope(claims) != subject.identity {
		return failure("UNAUTHORIZED", 401)
	}
	return nil
}

// Observe returns the durable head of one authorized stream for push
// invalidation. It applies Read's visibility and private-session checks before
// and after the bounded durable read, discloses only the head that the REST
// feed would report, and never writes receipts or delivery metadata.
func (s *Service) Observe(ctx context.Context, subject *Subject, id, family string) (int64, error) {
	private := family == "personal"
	if !campaignPattern.MatchString(id) || (!private && family != "publication" && family != "interaction") {
		return 0, v2.InvalidInput("invalid event feed query")
	}
	if private && subject == nil {
		return 0, failure("UNAUTHORIZED", 401)
	}
	if s == nil || s.reader == nil || s.resolver == nil {
		return 0, failure("CAMPAIGN_UNAVAILABLE", 503)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	owner := ""
	if private {
		if err := s.revalidate(ctx, subject); err != nil {
			return 0, err
		}
		owner = subject.owner
	}
	stream, err := s.resolver.resolve(ctx, id, family, owner)
	if err != nil {
		return 0, err
	}
	page, err := s.reader.ReadStream(ctx, stream, nil, 1)
	if err != nil || page.Head < 0 || page.ObservedAt.IsZero() {
		return 0, failure("CAMPAIGN_UNAVAILABLE", 503)
	}
	current, err := s.resolver.resolve(ctx, id, family, owner)
	if err != nil {
		return 0, err
	}
	if current != stream {
		return 0, failure("CAMPAIGN_UNAVAILABLE", 503)
	}
	if private {
		if err := s.revalidate(ctx, subject); err != nil {
			return 0, err
		}
	}
	return page.Head, nil
}

// Code reports the closed domain code of an Observe or Authenticate failure.
func Code(err error) string {
	var domain *v2.DomainError
	if errors.As(err, &domain) {
		return domain.Code
	}
	return "CAMPAIGN_UNAVAILABLE"
}
