// Package eventfeed exposes authorized invalidation hints. HTTP reads never
// acknowledge durable consumption; clients repair their state through REST.
package eventfeed

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

type streamReader interface {
	ReadStream(context.Context, string, *int64, int) (durableoutbox.StreamPage, error)
}
type scopeResolver interface {
	resolve(context.Context, string, string, string) (string, error)
}
type Service struct {
	resolver scopeResolver
	reader   streamReader
	verifier *auth.Verifier
}

func New(pool *pgxpool.Pool, verifier *auth.Verifier) *Service {
	return &Service{resolver: resolver{pool}, reader: durableoutbox.NewRepository(pool), verifier: verifier}
}
func failure(code string, status int) error { return &v2.DomainError{Code: code, Status: status} }
func authorization(err error) error {
	if errors.Is(err, auth.ErrSessionUnavailable) {
		return failure("WEB3_SESSION_UNAVAILABLE", 503)
	}
	return failure("UNAUTHORIZED", 401)
}

// Read binds a private stream to the live request session, never an owner
// supplied by a query parameter. An absent cursor anchors before REST repair;
// taking the snapshot first could lose a mutation between snapshot and anchor.
func (s *Service) Read(ctx context.Context, id, family, cursor string, limit int, private bool) (v2.EventFeedResponse, error) {
	var empty v2.EventFeedResponse
	if !campaignPattern.MatchString(id) || limit < 1 || limit > 50 ||
		(private && family != "personal") || (!private && family != "publication" && family != "interaction") {
		return empty, v2.InvalidInput("invalid event feed query")
	}
	if s == nil || s.reader == nil || s.resolver == nil {
		return empty, failure("CAMPAIGN_UNAVAILABLE", 503)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	owner, identity := "", "public"
	if private {
		claims, err := auth.RevalidateStrict(ctx, s.verifier)
		if err != nil {
			return empty, authorization(err)
		}
		owner, identity = claims.Sub, sessionScope(claims)
	}
	scope := feedScope(id, family, identity)
	after, err := decodeCursor(cursor, scope)
	if err != nil {
		return empty, err
	}
	stream, err := s.resolver.resolve(ctx, id, family, owner)
	if err != nil {
		return empty, err
	}
	page, err := s.reader.ReadStream(ctx, stream, after, limit)
	if err != nil {
		return empty, failure("CAMPAIGN_UNAVAILABLE", 503)
	}
	// Recheck both visibility and identity after a blocked database read. A
	// formerly valid token or campaign lookup is not a durable disclosure grant.
	current, err := s.resolver.resolve(ctx, id, family, owner)
	if err != nil {
		return empty, err
	}
	if current != stream {
		return empty, failure("CAMPAIGN_UNAVAILABLE", 503)
	}
	if private {
		claims, authErr := auth.RevalidateStrict(ctx, s.verifier)
		if authErr != nil {
			return empty, authorization(authErr)
		}
		if claims.Sub != owner || sessionScope(claims) != identity {
			return empty, failure("UNAUTHORIZED", 401)
		}
	}
	result := v2.EventFeedResponse{SchemaVersion: 1, CampaignID: id, Family: family, HeadVersion: strconv.FormatInt(page.Head, 10),
		ServerNow: page.ObservedAt.UTC().Format(time.RFC3339Nano), AsOf: page.ObservedAt.UTC().Format(time.RFC3339Nano), Events: []v2.EventFeedHint{}}
	reset := func() (v2.EventFeedResponse, error) {
		result.Events = []v2.EventFeedHint{}
		result.Cursor = encodeCursor(scope, page.Head)
		result.HasMore = false
		result.ResetRequired = true
		return result, nil
	}
	if page.Head < 0 || page.ObservedAt.IsZero() {
		return empty, failure("CAMPAIGN_UNAVAILABLE", 503)
	}
	if after == nil || *after > page.Head {
		return reset()
	}
	version := *after
	for _, event := range page.Events {
		if version >= page.Head || event.Version != version+1 {
			return reset()
		}
		hint, valid := invalidation(event, id, family, stream, owner)
		if !valid {
			return reset()
		}
		result.Events = append(result.Events, hint)
		version = event.Version
	}
	if version < page.Head && len(result.Events) == 0 {
		return reset()
	}
	result.Cursor = encodeCursor(scope, version)
	result.HasMore = version < page.Head
	return result, nil
}
