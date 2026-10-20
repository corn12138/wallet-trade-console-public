package application

import (
	"context"
	"strings"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

type QueryService struct {
	reader       ReadStore
	authorizer   Authorizer
	capabilities func() v2.Capabilities
}

func NewQueryService(reader ReadStore, authorizer Authorizer, capabilities func() v2.Capabilities) *QueryService {
	return &QueryService{reader, authorizer, capabilities}
}
func (s *QueryService) ready(content bool) error {
	if s == nil || s.reader == nil || (content && s.capabilities == nil) {
		return unavailable("campaign read dependencies unavailable")
	}
	return nil
}
func queryLimit(limit int) (int, error) {
	if limit == 0 {
		return 20, nil
	}
	if limit < 1 || limit > 50 {
		return 0, v2.InvalidInput("limit must be between 1 and 50")
	}
	return limit, nil
}
func (s *QueryService) authorize(ctx context.Context, actor string, admin bool) error {
	if !walletPattern.MatchString(actor) {
		return &v2.DomainError{Code: "UNAUTHORIZED", Status: 401, Detail: "verified wallet required"}
	}
	if s == nil || s.authorizer == nil {
		return unavailable("campaign authorization unavailable")
	}
	return s.authorizer.Require(ctx, actor, admin)
}
func (s *QueryService) List(ctx context.Context, query ListQuery) (ListResponse, error) {
	var empty ListResponse
	if err := s.ready(true); err != nil {
		return empty, err
	}
	switch query.Phase {
	case "", "upcoming", "active", "ended", "unsupported":
	default:
		return empty, v2.InvalidInput("invalid phase")
	}
	var err error
	query.Limit, err = queryLimit(query.Limit)
	if err != nil {
		return empty, err
	}
	if _, err = DecodeQueryCursor(query.Cursor, "list", ListScope(query), query.Phase); err != nil {
		return empty, err
	}
	return s.reader.List(ctx, query, s.capabilities())
}
func (s *QueryService) Detail(ctx context.Context, id string, variants v2.Variants) (DetailResponse, error) {
	if err := s.ready(true); err != nil {
		return DetailResponse{}, err
	}
	if !identityPattern.MatchString(id) {
		return DetailResponse{}, v2.InvalidInput("campaign identity")
	}
	return s.reader.Detail(ctx, id, variants, s.capabilities())
}

// Baselines returns the verified legacy baselines among the referenced
// revisions; other revisions are left out. It serves the in-process legacy
// compatibility adapter only, which needs a baseline's original values to
// answer the legacy DTO; HTTP never exposes it.
func (s *QueryService) Baselines(ctx context.Context, refs []RevisionRef) (map[RevisionRef]v2.LegacyBaseline, error) {
	if err := s.ready(false); err != nil {
		return nil, err
	}
	if err := validRevisionRefs(refs); err != nil {
		return nil, err
	}
	return s.reader.Baselines(ctx, refs)
}

func validRevisionRefs(refs []RevisionRef) error {
	if len(refs) < 1 || len(refs) > 50 {
		return v2.InvalidInput("revision references must name between 1 and 50 revisions")
	}
	for _, ref := range refs {
		if !identityPattern.MatchString(ref.CampaignID) || !revisionIDPattern.MatchString(ref.RevisionID) {
			return v2.InvalidInput("revision reference")
		}
	}
	return nil
}

func (s *QueryService) Me(ctx context.Context, actor string, ids []string) (MeResponse, error) {
	var empty MeResponse
	actor = strings.ToLower(actor)
	if err := s.ready(false); err != nil {
		return empty, err
	}
	if err := s.authorize(ctx, actor, false); err != nil {
		return empty, err
	}
	if len(ids) < 1 || len(ids) > 50 {
		return empty, v2.InvalidInput("ids must contain between 1 and 50 campaigns")
	}
	frozen := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if !identityPattern.MatchString(id) {
			return empty, v2.InvalidInput("campaign identity")
		}
		if !seen[id] {
			frozen = append(frozen, id)
			seen[id] = true
		}
	}
	value, err := s.reader.Me(ctx, actor, frozen)
	if err != nil {
		return empty, err
	}
	if err = s.authorize(ctx, actor, false); err != nil {
		return empty, err
	}
	return value, nil
}
func (s *QueryService) History(ctx context.Context, actor string, query HistoryQuery) (HistoryResponse, error) {
	var empty HistoryResponse
	actor = strings.ToLower(actor)
	if err := s.ready(false); err != nil {
		return empty, err
	}
	if err := s.authorize(ctx, actor, false); err != nil {
		return empty, err
	}
	var err error
	query.Limit, err = queryLimit(query.Limit)
	if err != nil {
		return empty, err
	}
	if _, err = DecodeQueryCursor(query.Cursor, "history", HistoryScope(actor, query.Variants), ""); err != nil {
		return empty, err
	}
	value, err := s.reader.History(ctx, actor, query)
	if err != nil {
		return empty, err
	}
	if err = s.authorize(ctx, actor, false); err != nil {
		return empty, err
	}
	return value, nil
}
func (s *QueryService) Command(ctx context.Context, actor, targetID, operation, clientID string) (CommandResponse, error) {
	var empty CommandResponse
	actor = strings.ToLower(actor)
	if err := s.ready(false); err != nil {
		return empty, err
	}
	admin := true
	switch operation {
	case "create", "revision", "publish", "rollback", "pause", "resume", "archive":
	case "join", "leave", "set-reminder", "remove-reminder":
		admin = false
	default:
		return empty, v2.InvalidInput("unknown operation")
	}
	if err := s.authorize(ctx, actor, admin); err != nil {
		return empty, err
	}
	if !identityPattern.MatchString(targetID) || !clientIDPattern.MatchString(clientID) || operation == "create" && targetID != "campaigns" {
		return empty, v2.InvalidInput("command query identity")
	}
	value, err := s.reader.CommandResult(ctx, CommandKey{Actor: actor, Operation: operation, TargetID: targetID, ClientID: clientID})
	if err != nil {
		return empty, err
	}
	if err = s.authorize(ctx, actor, admin); err != nil {
		return empty, err
	}
	value.Replayed = true
	return value, nil
}
func (s *QueryService) Preview(ctx context.Context, actor, campaignID, revisionID string) (PreviewResponse, error) {
	var empty PreviewResponse
	actor = strings.ToLower(actor)
	if err := s.ready(true); err != nil {
		return empty, err
	}
	if err := s.authorize(ctx, actor, true); err != nil {
		return empty, err
	}
	if !identityPattern.MatchString(campaignID) || !revisionIDPattern.MatchString(revisionID) {
		return empty, v2.InvalidInput("preview identity")
	}
	value, err := s.reader.Preview(ctx, campaignID, revisionID, s.capabilities())
	if err != nil {
		return empty, err
	}
	if err = s.authorize(ctx, actor, true); err != nil {
		return empty, err
	}
	return value, nil
}

// AdminCampaign exposes drafts, hashes and lock facts, so it is authorized as a
// campaign administrator before and after the snapshot like Preview. Its values
// guide confirmation only; publication commands still CAS under the row lock.
func (s *QueryService) AdminCampaign(ctx context.Context, actor, campaignID string) (AdminCampaignResponse, error) {
	var empty AdminCampaignResponse
	actor = strings.ToLower(actor)
	if err := s.ready(true); err != nil {
		return empty, err
	}
	if err := s.authorize(ctx, actor, true); err != nil {
		return empty, err
	}
	if !identityPattern.MatchString(campaignID) {
		return empty, v2.InvalidInput("campaign identity")
	}
	value, err := s.reader.AdminCampaign(ctx, campaignID, s.capabilities())
	if err != nil {
		return empty, err
	}
	if err = s.authorize(ctx, actor, true); err != nil {
		return empty, err
	}
	return value, nil
}
