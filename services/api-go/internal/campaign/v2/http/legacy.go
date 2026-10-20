package campaignhttp

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyactivation"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/go-chi/chi/v5"
)

// LegacyImports is the administration port of the controlled legacy import
// (docs/campaign-legacy-activation-protocol-2026-10-09.md §5).
type LegacyImports interface {
	Plan(ctx context.Context, runID, cursor string) (legacyactivation.Plan, error)
	RecordReview(ctx context.Context, in legacyactivation.ReviewInput) (legacyactivation.Review, error)
	Activate(ctx context.Context, in legacyactivation.ActivationInput) (legacyactivation.Activation, error)
	ActivationOf(ctx context.Context, campaignID string) (legacyactivation.Activation, bool, error)
}

const (
	maxWalletRefs   = 20
	maxGroups       = 50
	maxDriftEntries = 50
)

func timestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

// legacyAdmin checks the session and administrator grant before any read or
// write; it returns the actor or writes the failure.
func (h handler) legacyAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.Legacy == nil || h.Access == nil {
		writeFailure(w, r, unavailable("CAMPAIGN_UNAVAILABLE"))
		return "", false
	}
	actor := auth.AddressFromContext(r.Context())
	if err := h.Access.Require(r.Context(), actor, true); err != nil {
		writeFailure(w, r, err)
		return "", false
	}
	return actor, true
}

func (h handler) legacyPlan(w http.ResponseWriter, r *http.Request) {
	q, err := query(r, "cursor")
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	if _, ok := h.legacyAdmin(w, r); !ok {
		return
	}
	plan, err := h.Legacy.Plan(r.Context(), chi.URLParam(r, "runId"), q.Get("cursor"))
	if err != nil {
		writeLegacyFailure(w, r, err)
		return
	}
	now := timestamp(time.Now())
	out := v2.LegacyImportPlanResponse{SchemaVersion: 1, ServerNow: now, AsOf: now, Run: runView(plan.Run), Items: []v2.LegacyPlanItemView{}, UnlistedItems: plan.UnlistedItems}
	for _, item := range plan.Items {
		out.Items = append(out.Items, planItemView(item))
	}
	if plan.NextCursor != "" {
		out.NextCursor = &plan.NextCursor
	}
	writeContract(w, r, "LegacyImportPlanResponse", out, http.StatusOK)
}

func (h handler) legacyReview(w http.ResponseWriter, r *http.Request) {
	var body v2.LegacyReviewCommandInput
	if err := decodeBody(w, r, "LegacyReviewCommandInput", &body); err != nil {
		writeFailure(w, r, err)
		return
	}
	actor, ok := h.legacyAdmin(w, r)
	if !ok {
		return
	}
	review, err := h.Legacy.RecordReview(r.Context(), legacyactivation.ReviewInput{RunID: chi.URLParam(r, "runId"), CampaignID: chi.URLParam(r, "id"),
		Decision: body.Decision, AcceptedDifferences: body.AcceptedDifferences, AcknowledgedFindings: body.AcknowledgedFindings, Reason: body.Reason,
		Reviewer: actor, ClientCommandID: body.ClientCommandID, Authorize: func(ctx context.Context) error { return h.Access.Require(ctx, actor, true) }})
	if err != nil {
		writeLegacyFailure(w, r, err)
		return
	}
	status := http.StatusCreated
	if review.Replayed {
		status = http.StatusOK
	}
	now := timestamp(time.Now())
	writeContract(w, r, "LegacyReviewResponse", v2.LegacyReviewResponse{SchemaVersion: 1, ServerNow: now, AsOf: timestamp(review.DecidedAt), Review: reviewView(review), Replayed: review.Replayed}, status)
}

func (h handler) legacyActivate(w http.ResponseWriter, r *http.Request) {
	var body v2.LegacyActivationCommandInput
	if err := decodeBody(w, r, "LegacyActivationCommandInput", &body); err != nil {
		writeFailure(w, r, err)
		return
	}
	actor, ok := h.legacyAdmin(w, r)
	if !ok {
		return
	}
	activation, err := h.Legacy.Activate(r.Context(), legacyactivation.ActivationInput{RunID: chi.URLParam(r, "runId"), CampaignID: chi.URLParam(r, "id"), Actor: actor,
		ClientCommandID: body.ClientCommandID, Reason: body.Reason, RequestID: requestID(r), LegacyRowSHA256: body.LegacyRowSha256, InteractionsSHA256: body.InteractionsSha256,
		Authorize: func(ctx context.Context) error { return h.Access.Require(ctx, actor, true) }})
	if err != nil {
		writeLegacyFailure(w, r, err)
		return
	}
	status := http.StatusCreated
	if activation.Replayed {
		status = http.StatusOK
	}
	writeActivation(w, r, activation, status)
}

func (h handler) legacyActivation(w http.ResponseWriter, r *http.Request) {
	if _, err := query(r); err != nil {
		writeFailure(w, r, err)
		return
	}
	if _, ok := h.legacyAdmin(w, r); !ok {
		return
	}
	activation, found, err := h.Legacy.ActivationOf(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeLegacyFailure(w, r, err)
		return
	}
	if !found {
		writeFailure(w, r, &v2.DomainError{Code: "CAMPAIGN_LEGACY_NOT_ACTIVATED", Status: http.StatusNotFound})
		return
	}
	writeActivation(w, r, activation, http.StatusOK)
}

func writeActivation(w http.ResponseWriter, r *http.Request, a legacyactivation.Activation, status int) {
	now := timestamp(time.Now())
	view := v2.LegacyActivationView{ActivationID: a.ID, CampaignID: a.CampaignID, RunID: a.RunID, Decision: a.Decision, RevisionID: a.RevisionID, CommandID: a.CommandID,
		ClientCommandID: a.ClientCommandID, PayloadHash: a.PayloadHash, ActivatedBy: a.ActivatedBy, ActivatedAt: timestamp(a.ActivatedAt), PublicationVersion: strconv.FormatUint(a.PublicationVersion, 10),
		InteractionVersion: strconv.FormatUint(a.InteractionVersion, 10), ImportedInteractions: int64(a.ImportedInteractions), Comparison: comparisonView(a.Comparison)}
	if a.ReviewID != "" {
		view.ReviewID = &a.ReviewID
	}
	if a.PublishCommandID != "" {
		view.PublishCommandID = &a.PublishCommandID
	}
	writeContract(w, r, "LegacyActivationResponse", v2.LegacyActivationResponse{SchemaVersion: 1, ServerNow: now, AsOf: timestamp(a.ActivatedAt), Activation: view, Replayed: a.Replayed}, status)
}

// writeLegacyFailure keeps a refusal's code and status. A drift or a failed
// comparison is returned with its (bounded, digest-only) report.
func writeLegacyFailure(w http.ResponseWriter, r *http.Request, err error) {
	var refusal *legacyactivation.Error
	if !errors.As(err, &refusal) {
		writeFailure(w, r, err)
		return
	}
	if len(refusal.Drift) == 0 && refusal.Comparison == nil {
		writeFailure(w, r, &v2.DomainError{Code: refusal.Code, Status: refusal.Status, Detail: refusal.Detail})
		return
	}
	out := v2.LegacyImportRefusal{SchemaVersion: 1, Code: refusal.Code, RequestID: requestID(r), Drift: []v2.LegacyDriftView{}}
	for i, drift := range refusal.Drift {
		if i == maxDriftEntries {
			out.DriftTruncated = true
			break
		}
		out.Drift = append(out.Drift, driftView(drift))
	}
	if refusal.Comparison != nil {
		comparison := comparisonView(*refusal.Comparison)
		out.Comparison = &comparison
	}
	writeContract(w, r, "LegacyImportRefusal", out, refusal.Status)
}

func runView(run legacyactivation.RunSummary) v2.LegacyRunView {
	out := v2.LegacyRunView{RunID: run.RunID, Status: run.Status, Operator: run.Operator, Rows: run.Rows, RetainUntil: timestamp(run.RetainUntil)}
	if run.CapturedAt != nil {
		captured := timestamp(*run.CapturedAt)
		out.CapturedAt = &captured
	}
	if run.SourceSHA256 != "" {
		out.SourceSha256 = &run.SourceSHA256
	}
	return out
}

func planItemView(item legacyactivation.PlanItem) v2.LegacyPlanItemView {
	out := v2.LegacyPlanItemView{CampaignID: item.CampaignID, Decision: item.Decision, LegacyRowSha256: item.LegacyRowSHA256, InteractionsSha256: item.InteractionsSHA256,
		ParticipantRows: int64(item.ParticipantRows), ReminderRows: int64(item.ReminderRows), Findings: []v2.LegacyFindingSummary{}}
	for _, finding := range item.Findings {
		out.Findings = append(out.Findings, v2.LegacyFindingSummary{Kind: finding.Kind, Severity: finding.Severity, Count: int64(finding.Count)})
	}
	if fence := item.Fence; fence != nil {
		view := v2.LegacyFenceView{FencedBy: fence.FencedBy, FencedAt: timestamp(fence.FencedAt), LiftedBy: fence.LiftedBy}
		if fence.LiftedAt != nil {
			lifted := timestamp(*fence.LiftedAt)
			view.LiftedAt = &lifted
		}
		out.Fence = &view
	}
	if item.Review != nil {
		review := reviewView(*item.Review)
		out.Review = &review
	}
	if a := item.Activation; a != nil {
		classes := append([]string{}, a.Classes...)
		out.Activation = &v2.LegacyActivationSummary{ActivationID: a.ActivationID, Decision: a.Decision, ActivatedBy: a.ActivatedBy, ActivatedAt: timestamp(a.ActivatedAt), Classes: classes}
	}
	return out
}

func reviewView(review legacyactivation.Review) v2.LegacyReviewView {
	return v2.LegacyReviewView{ReviewID: review.ID, RunID: review.RunID, CampaignID: review.CampaignID, Decision: review.Decision,
		AcceptedDifferences: append([]string{}, review.AcceptedDifferences...), AcknowledgedFindings: append([]string{}, review.AcknowledgedFindings...),
		Reason: review.Reason, Reviewer: review.Reviewer, ClientCommandID: review.ClientCommandID, DecidedAt: timestamp(review.DecidedAt)}
}

func comparisonView(c legacyactivation.Comparison) v2.LegacyComparisonView {
	out := v2.LegacyComparisonView{Instant: c.Instant, Wallets: int64(c.Wallets), Groups: []v2.LegacyDifferenceGroupView{},
		Classes: append([]string{}, c.Classes...), Accepted: append([]string{}, c.Accepted...)}
	for i, group := range c.Groups(maxWalletRefs) {
		if i == maxGroups {
			out.GroupsTruncated = true
			break
		}
		out.Groups = append(out.Groups, v2.LegacyDifferenceGroupView{Class: group.Class, Field: group.Field, Before: group.Before, After: group.After,
			Count: int64(group.Count), WalletRefs: append([]string{}, group.WalletRefs...)})
	}
	return out
}

// driftView splits a changed row's "before>after" reference.
func driftView(drift legacyimport.Drift) v2.LegacyDriftView {
	out := v2.LegacyDriftView{Table: drift.Table, Change: drift.Change, RowRef: drift.RowRef}
	if before, after, changed := strings.Cut(drift.RowRef, ">"); changed {
		out.RowRef, out.AfterRef = before, &after
	}
	return out
}
