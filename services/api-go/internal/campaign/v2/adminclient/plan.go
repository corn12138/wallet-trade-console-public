package adminclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

type PlanInput struct {
	Operation           string
	CampaignID          string
	CandidateRevisionID string
	Slug                string
	Reason              string
	RevisionJSON        []byte
	// ExpectVersion optionally pins the publication version the operator
	// reviewed elsewhere; planning fails if the server has moved on.
	ExpectVersion string
}

var campaignIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var revisionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

func validatePlan(in PlanInput) error {
	reason := strings.TrimSpace(in.Reason)
	if reason == "" || utf8.RuneCountInString(in.Reason) > 1000 || !utf8.ValidString(in.Reason) || strings.ContainsRune(in.Reason, 0) {
		return errors.New("a reason of 1-1000 characters is required for every administrator command")
	}
	needsCampaign, needsRevision, needsCandidate := true, false, false
	switch in.Operation {
	case "create":
		needsCampaign, needsRevision = false, true
		if in.Slug != "" && !slugPattern.MatchString(in.Slug) {
			return errors.New("slug must match ^[a-z0-9][a-z0-9-]{0,99}$")
		}
	case "revision":
		needsRevision = true
	case "publish", "rollback":
		needsCandidate = true
	case "pause", "resume", "archive":
	default:
		return fmt.Errorf("unknown operation %q", in.Operation)
	}
	if needsCampaign != (in.CampaignID != "") || (in.CampaignID != "" && !campaignIDPattern.MatchString(in.CampaignID)) {
		return errors.New("campaign ID missing, unexpected or malformed for this operation")
	}
	if needsRevision != (len(in.RevisionJSON) > 0) {
		return errors.New("revision file required only for create and revision")
	}
	if needsCandidate != (in.CandidateRevisionID != "") || (in.CandidateRevisionID != "" && !revisionIDPattern.MatchString(in.CandidateRevisionID)) {
		return errors.New("candidate revision ID required only for publish and rollback")
	}
	if in.Slug != "" && in.Operation != "create" {
		return errors.New("slug is fixed at creation")
	}
	return nil
}

// Plan reads authoritative state, derives the review shown before confirmation,
// and persists the frozen request under a fresh UUIDv7 before anything is sent.
// Predictions in the review are advisory; the server's locked CAS decides.
func Plan(ctx context.Context, home Home, client *Client, credential Credential, in PlanInput, now time.Time) (*Intent, error) {
	if err := validatePlan(in); err != nil {
		return nil, err
	}
	id, err := NewCommandID(now)
	if err != nil {
		return nil, err
	}
	intent := &Intent{SchemaVersion: 1, ID: id, CreatedAt: now.UTC().Format(time.RFC3339Nano), API: client.Base(), Actor: credential.Address, Operation: in.Operation, CampaignID: in.CampaignID, Method: http.MethodPost, Status: StatusPlanned, Attempts: []Attempt{}}
	intent.Review = Review{Reason: in.Reason, Impact: []string{}, Warnings: []string{}}
	var revision v2.RevisionInput
	if len(in.RevisionJSON) > 0 {
		// The strict decoder rejects unknown fields, duplicate keys and unknown
		// versions exactly as the server does, before any key is consumed.
		if revision, err = v2.DecodeRevision(in.RevisionJSON); err != nil {
			return nil, fmt.Errorf("revision file rejected by the closed contract: %w", err)
		}
		if intent.Review.Candidate, err = localCandidate(revision); err != nil {
			return nil, err
		}
	}
	var view v2.AdminCampaignResponse
	if in.Operation != "create" {
		if err = client.GetContract(ctx, "/v2/campaign-admin/campaigns/"+in.CampaignID, "AdminCampaignResponse", &view); err != nil {
			return nil, err
		}
		if in.ExpectVersion != "" && in.ExpectVersion != view.Item.PublicationVersion {
			return nil, fmt.Errorf("publication version is %s, not the expected %s; review the current state", view.Item.PublicationVersion, in.ExpectVersion)
		}
		expected := view.Item.PublicationVersion
		intent.Review.ExpectedPublicationVersion = &expected
		if intent.Review.Current, err = currentState(ctx, client, view); err != nil {
			return nil, err
		}
	}
	if in.CandidateRevisionID != "" {
		if intent.Review.Candidate, err = previewCandidate(ctx, client, in.CampaignID, in.CandidateRevisionID); err != nil {
			return nil, err
		}
	}
	describeImpact(intent, in)
	var body any
	switch in.Operation {
	case "create":
		value := v2.CreateCommandInput{ClientCommandID: id, Reason: in.Reason, Revision: revision}
		if in.Slug != "" {
			value.Slug = &in.Slug
		}
		intent.Path, body = "/v2/campaign-admin/campaigns", value
	case "revision":
		intent.Path = "/v2/campaign-admin/campaigns/" + in.CampaignID + "/revisions"
		body = v2.RevisionCommandInput{ClientCommandID: id, Reason: in.Reason, ExpectedPublicationVersion: *intent.Review.ExpectedPublicationVersion, Revision: revision}
	default:
		value := v2.PublicationCommandInput{ClientCommandID: id, Reason: in.Reason, ExpectedPublicationVersion: *intent.Review.ExpectedPublicationVersion, Operation: in.Operation}
		if in.CandidateRevisionID != "" {
			value.CandidateRevisionID = &in.CandidateRevisionID
		}
		intent.Path, body = "/v2/campaign-admin/campaigns/"+in.CampaignID+"/publications", value
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	contract := map[string]string{"create": "CreateCommandInput", "revision": "RevisionCommandInput"}[in.Operation]
	if contract == "" {
		contract = "PublicationCommandInput"
	}
	var checked any
	if err = v2.DecodeContract(contract, raw, &checked); err != nil {
		return nil, fmt.Errorf("frozen request violates %s: %w", contract, err)
	}
	intent.Body, intent.BodySHA256 = string(raw), digest(string(raw))
	// The ledger hash is shown in the review and is what every success must
	// report back; computing it here also proves the body maps to a command.
	if intent.LedgerPayloadHash, err = intent.ledgerPayloadHash(); err != nil {
		return nil, fmt.Errorf("frozen request cannot be mapped to a ledger command: %w", err)
	}
	return intent, home.SaveIntent(intent)
}

func localCandidate(input v2.RevisionInput) (*Candidate, error) {
	terms, err := v2.CanonicalHash(input.MaterialTerms)
	if err != nil {
		return nil, err
	}
	content, err := v2.CanonicalHash(input)
	if err != nil {
		return nil, err
	}
	return &Candidate{MaterialTermsHash: terms, ContentHash: content, HashSource: "local-precompute (server recomputes after validation)", StartAt: input.MaterialTerms.StartAt, EndAt: input.MaterialTerms.EndAt, TitleEn: input.MaterialTerms.Copy.En.Title}, nil
}

func currentState(ctx context.Context, client *Client, view v2.AdminCampaignResponse) (*CurrentState, error) {
	item := view.Item
	state := &CurrentState{AsOf: view.AsOf, PublicationVersion: item.PublicationVersion, Mode: item.Mode, EffectivePhase: item.EffectivePhase, ActiveRevisionID: item.ActiveRevisionID, MaterialTermsLock: item.MaterialTermsLock}
	if item.ActiveRevisionID == nil {
		return state, nil
	}
	for _, revision := range item.Revisions {
		if revision.ID == *item.ActiveRevisionID {
			state.ActiveTermsHash = &revision.MaterialTermsHash
			return state, nil
		}
	}
	// The active revision can be older than the newest 50 summaries.
	active, err := previewCandidate(ctx, client, item.CampaignID, *item.ActiveRevisionID)
	if err != nil {
		return nil, err
	}
	state.ActiveTermsHash = &active.MaterialTermsHash
	return state, nil
}

func previewCandidate(ctx context.Context, client *Client, campaignID, revisionID string) (*Candidate, error) {
	var preview v2.QueryPreviewResponse
	if err := client.GetContract(ctx, "/v2/campaign-admin/campaigns/"+campaignID+"/revisions/"+revisionID+"/preview", "QueryPreviewResponse", &preview); err != nil {
		return nil, err
	}
	item := preview.Item
	published := item.IsPublished
	candidate := &Candidate{RevisionID: item.RevisionID, MaterialTermsHash: item.MaterialTermsHash, ContentHash: item.ContentHash, HashSource: "server preview", ContentState: item.ContentState, PreviouslyActive: &published}
	if item.Content != nil {
		candidate.StartAt, candidate.EndAt, candidate.TitleEn = item.Content.MaterialTerms.StartAt, item.Content.MaterialTerms.EndAt, item.Content.MaterialTerms.Copy.En.Title
	}
	return candidate, nil
}
