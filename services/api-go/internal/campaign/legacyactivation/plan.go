package legacyactivation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	"github.com/jackc/pgx/v5"
)

// PlanPageSize bounds one page of plan items.
const PlanPageSize = 50

// MaxListedIDBytes bounds the campaign IDs a plan lists. A longer legacy ID can
// be neither fenced nor activated; the plan only counts such items and the
// capture report keeps them.
const MaxListedIDBytes = 160

// RunSummary is the shareable state of a capture run.
type RunSummary struct {
	RunID, Status, Operator string
	CapturedAt              *time.Time
	SourceSHA256            string
	Rows                    int64
	RetainUntil             time.Time
}

// FindingSummary counts one finding kind of a plan item. Row-level detail
// stays in the capture report and the database.
type FindingSummary struct {
	Kind, Severity string
	Count          int
}

type FenceState struct {
	FencedBy string
	FencedAt time.Time
	LiftedBy *string
	LiftedAt *time.Time
}

type ActivationSummary struct {
	ActivationID, Decision, ActivatedBy string
	ActivatedAt                         time.Time
	Classes                             []string
}

type PlanItem struct {
	CampaignID, Decision                string
	LegacyRowSHA256, InteractionsSHA256 string
	ParticipantRows, ReminderRows       int
	Findings                            []FindingSummary
	Fence                               *FenceState
	Review                              *Review
	Activation                          *ActivationSummary
}

type Plan struct {
	Run           RunSummary
	Items         []PlanItem
	NextCursor    string
	UnlistedItems int64
}

var severityRank = map[string]int{legacyimport.SeverityBlocker: 0, legacyimport.SeverityReview: 1, legacyimport.SeverityInfo: 2}

// Plan reads one page of a run's plan, ordered by campaign ID, with each
// item's fence, review and activation. The cursor is opaque.
func (e *Engine) Plan(ctx context.Context, runID, cursor string) (Plan, error) {
	if !runIDPattern.MatchString(runID) {
		return Plan{}, invalid("run identity")
	}
	after := ""
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(raw) == 0 || len(raw) > MaxListedIDBytes {
			return Plan{}, invalid("plan cursor")
		}
		after = string(raw)
	}
	var out Plan
	err := pgx.BeginTxFunc(ctx, e.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		run := &out.Run
		var source *string
		var rows *int64
		err := tx.QueryRow(ctx, `SELECT id::text,status,operator,captured_at,source_sha256,row_count,retain_until FROM campaign_legacy_import_runs WHERE id=$1`, runID).
			Scan(&run.RunID, &run.Status, &run.Operator, &run.CapturedAt, &source, &rows, &run.RetainUntil)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("run not found")
		}
		if err != nil {
			return err
		}
		if source != nil {
			run.SourceSHA256 = *source
		}
		if rows != nil {
			run.Rows = *rows
		}
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM campaign_legacy_import_items WHERE run_id=$1 AND octet_length(campaign_id) > $2`, runID, MaxListedIDBytes).
			Scan(&out.UnlistedItems); err != nil {
			return err
		}
		page, err := tx.Query(ctx, `SELECT i.campaign_id,i.decision,i.legacy_row_sha256,i.interactions_sha256,i.participant_rows,i.reminder_rows,i.findings,
				f.fenced_by,f.fenced_at,f.lifted_by,f.lifted_at,a.id::text,a.decision,a.activated_by,a.activated_at,a.comparison
			FROM campaign_legacy_import_items i
			LEFT JOIN campaign_legacy_write_fences f ON f.run_id=i.run_id AND f.campaign_id=i.campaign_id
			LEFT JOIN campaign_legacy_activations a ON a.run_id=i.run_id AND a.campaign_id=i.campaign_id
			WHERE i.run_id=$1 AND octet_length(i.campaign_id) <= $4 AND ($2='' OR i.campaign_id > $2) ORDER BY i.campaign_id LIMIT $3`, runID, after, PlanPageSize+1, MaxListedIDBytes)
		if err != nil {
			return err
		}
		for page.Next() {
			var item PlanItem
			var findings, comparison []byte
			var fencedBy, activationID, activationDecision, activatedBy *string
			var fencedAt, activatedAt *time.Time
			fence := FenceState{}
			if err = page.Scan(&item.CampaignID, &item.Decision, &item.LegacyRowSHA256, &item.InteractionsSHA256, &item.ParticipantRows, &item.ReminderRows, &findings,
				&fencedBy, &fencedAt, &fence.LiftedBy, &fence.LiftedAt, &activationID, &activationDecision, &activatedBy, &activatedAt, &comparison); err != nil {
				page.Close()
				return err
			}
			if item.Findings, err = summarizeFindings(findings); err != nil {
				page.Close()
				return err
			}
			if fencedBy != nil && fencedAt != nil {
				fence.FencedBy, fence.FencedAt = *fencedBy, fencedAt.UTC()
				if fence.LiftedAt != nil {
					lifted := fence.LiftedAt.UTC()
					fence.LiftedAt = &lifted
				}
				item.Fence = &fence
			}
			if activationID != nil {
				var recorded Comparison
				if err = json.Unmarshal(comparison, &recorded); err != nil {
					page.Close()
					return err
				}
				item.Activation = &ActivationSummary{ActivationID: *activationID, Decision: *activationDecision, ActivatedBy: *activatedBy, ActivatedAt: activatedAt.UTC(), Classes: recorded.Classes}
			}
			out.Items = append(out.Items, item)
		}
		page.Close()
		if err = page.Err(); err != nil {
			return err
		}
		if len(out.Items) > PlanPageSize {
			out.Items = out.Items[:PlanPageSize]
			out.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(out.Items[PlanPageSize-1].CampaignID))
		}
		for i := range out.Items {
			review, found, err := loadReview(ctx, tx, `run_id=$1 AND campaign_id=$2`, runID, out.Items[i].CampaignID)
			if err != nil {
				return err
			}
			if found {
				out.Items[i].Review = &review
			}
		}
		return nil
	})
	if err != nil {
		return Plan{}, unavailable(err)
	}
	if out.Run.CapturedAt != nil {
		captured := out.Run.CapturedAt.UTC()
		out.Run.CapturedAt = &captured
	}
	out.Run.RetainUntil = out.Run.RetainUntil.UTC()
	return out, nil
}

func summarizeFindings(raw []byte) ([]FindingSummary, error) {
	var findings []legacyimport.Finding
	if err := json.Unmarshal(raw, &findings); err != nil {
		return nil, err
	}
	counts := map[[2]string]int{}
	for _, finding := range findings {
		counts[[2]string{finding.Kind, finding.Severity}]++
	}
	out := make([]FindingSummary, 0, len(counts))
	for key, count := range counts {
		out = append(out, FindingSummary{Kind: key[0], Severity: key[1], Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if severityRank[out[i].Severity] != severityRank[out[j].Severity] {
			return severityRank[out[i].Severity] < severityRank[out[j].Severity]
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

// ActivationOf returns the campaign's recorded activation, if any.
func (e *Engine) ActivationOf(ctx context.Context, campaignID string) (Activation, bool, error) {
	if campaignID == "" || len(campaignID) > 160 {
		return Activation{}, false, invalid("campaign identity")
	}
	var out Activation
	var found bool
	err := pgx.BeginTxFunc(ctx, e.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		record, ok, err := loadActivation(ctx, tx, campaignID)
		out, found = record.Activation, ok
		return err
	})
	if err != nil {
		return Activation{}, false, unavailable(err)
	}
	return out, found, nil
}

// DifferenceGroup aggregates the differences that share a class, field and
// before/after values, with up to a bounded number of wallet references.
type DifferenceGroup struct {
	Class, Field, Before, After string
	Count                       int
	WalletRefs                  []string
}

// Groups aggregates the comparison for display; the stored comparison keeps
// every difference.
func (c Comparison) Groups(maxRefs int) []DifferenceGroup {
	index := map[[4]string]int{}
	out := []DifferenceGroup{}
	for _, d := range c.Differences {
		key := [4]string{d.Class, d.Field, d.Before, d.After}
		position, ok := index[key]
		if !ok {
			position = len(out)
			index[key] = position
			out = append(out, DifferenceGroup{Class: d.Class, Field: d.Field, Before: d.Before, After: d.After, WalletRefs: []string{}})
		}
		group := &out[position]
		group.Count++
		if d.WalletRef != "" && len(group.WalletRefs) < maxRefs {
			group.WalletRefs = append(group.WalletRefs, d.WalletRef)
		}
	}
	return out
}
