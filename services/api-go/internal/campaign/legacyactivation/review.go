package legacyactivation

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	walletPattern   = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
	clientIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	runIDPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	// The versioned API addresses campaigns by this form only.
	campaignIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
	hashPattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// publicStatuses are the legacy states the old API listed (D2).
var publicStatuses = map[string]bool{"upcoming": true, "active": true, "ended": true}

// ReviewInput is a recorded decision for a review-required plan item.
type ReviewInput struct {
	RunID, CampaignID, Decision string
	// AcceptedDifferences are comparison classes the decision accepts:
	// visibility, participantCount, walletState.
	AcceptedDifferences []string
	// AcknowledgedFindings must name every review-level finding kind of the
	// plan item, no more and no fewer.
	AcknowledgedFindings []string
	Reason               string
	Reviewer             string
	ClientCommandID      string
	// Authorize, when set, re-checks the caller inside the transaction before
	// a decision is recorded or replayed.
	Authorize func(context.Context) error
}

type Review struct {
	ID, RunID, CampaignID, Decision           string
	AcceptedDifferences, AcknowledgedFindings []string
	Reason, Reviewer, ClientCommandID         string
	DecidedAt                                 time.Time
	Replayed                                  bool
}

// reviewHook runs after the replay lookup found nothing and before anything
// is written, while the item's review lock is held (tests only).
var reviewHook func()

func distinctSorted(values []string) ([]string, bool) {
	out := append([]string(nil), values...)
	sort.Strings(out)
	for i := 1; i < len(out); i++ {
		if out[i] == out[i-1] {
			return nil, false
		}
	}
	if out == nil {
		out = []string{}
	}
	return out, true
}

func (in ReviewInput) normalized() (ReviewInput, error) {
	var ok bool
	in.Reviewer = strings.ToLower(in.Reviewer)
	switch {
	case !walletPattern.MatchString(in.Reviewer):
		return in, invalid("reviewer must be a wallet")
	case !clientIDPattern.MatchString(in.ClientCommandID):
		return in, invalid("client command ID must be a UUIDv7")
	case !runIDPattern.MatchString(in.RunID) || !campaignIDPattern.MatchString(in.CampaignID):
		return in, invalid("run or campaign identity")
	case in.Decision != "import-public" && in.Decision != "import-unpublished" && in.Decision != "exclude":
		return in, invalid("decision must be import-public, import-unpublished or exclude")
	case strings.TrimSpace(in.Reason) == "" || !utf8.ValidString(in.Reason) || utf8.RuneCountInString(in.Reason) > 1000:
		return in, invalid("reason must be 1-1000 characters")
	}
	if in.AcceptedDifferences, ok = distinctSorted(in.AcceptedDifferences); !ok {
		return in, invalid("accepted differences must be distinct")
	}
	for _, class := range in.AcceptedDifferences {
		if !acceptable[class] {
			return in, invalid("a review accepts only visibility, participantCount and walletState differences")
		}
	}
	if in.AcknowledgedFindings, ok = distinctSorted(in.AcknowledgedFindings); !ok {
		return in, invalid("acknowledged findings must be distinct")
	}
	return in, nil
}

// RecordReview records the decision for a review-required plan item, after
// checking it against the captured item: every review-level finding is
// acknowledged, every accepted difference has a finding (or, for visibility,
// an unpublished import) that explains it, and only a campaign the old API
// listed may become public. The same reviewer and client command ID replays
// the recorded decision; one item admits one decision.
func (e *Engine) RecordReview(ctx context.Context, in ReviewInput) (Review, error) {
	in, err := in.normalized()
	if err != nil {
		return Review{}, err
	}
	var out Review
	err = pgx.BeginTxFunc(ctx, e.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		// Reviews of one plan item wait for each other: a concurrent copy of
		// the same command then replays instead of colliding with its own
		// insert, and the caller is checked again after the wait.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('campaign-legacy-review:' || $1 || ':' || $2, 0))`, in.RunID, in.CampaignID); err != nil {
			return err
		}
		if in.Authorize != nil {
			if err := in.Authorize(ctx); err != nil {
				return err
			}
		}
		if existing, found, err := loadReview(ctx, tx, `decided_by=$1 AND client_command_id=$2`, in.Reviewer, in.ClientCommandID); err != nil || found {
			if err == nil {
				out, err = replayReview(existing, in)
			}
			return err
		}
		if reviewHook != nil {
			reviewHook()
		}
		archived, err := legacyimport.LoadArchived(ctx, tx, in.RunID, in.CampaignID)
		if err != nil {
			return archiveRefusal(err)
		}
		if archived.Item.Decision != "review-required" {
			return refused("CAMPAIGN_LEGACY_NOT_REVIEWABLE", "only a review-required plan item takes a review decision")
		}
		if err = checkReview(in, archived); err != nil {
			return err
		}
		id, err := application.NewID()
		if err != nil {
			return err
		}
		differences, _ := json.Marshal(in.AcceptedDifferences)
		findings, _ := json.Marshal(in.AcknowledgedFindings)
		var decidedAt time.Time
		err = tx.QueryRow(ctx, `INSERT INTO campaign_legacy_review_decisions(id,run_id,campaign_id,decision,accepted_differences,acknowledged_findings,reason,decided_by,client_command_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING decided_at`, id, in.RunID, in.CampaignID, in.Decision, differences, findings, in.Reason, in.Reviewer, in.ClientCommandID).Scan(&decidedAt)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "campaign_legacy_review_once" {
			return refused("CAMPAIGN_LEGACY_ALREADY_REVIEWED", "this plan item already has a review decision; capture a new run to decide again")
		}
		if err != nil {
			return err
		}
		out = Review{ID: id, RunID: in.RunID, CampaignID: in.CampaignID, Decision: in.Decision, AcceptedDifferences: in.AcceptedDifferences,
			AcknowledgedFindings: in.AcknowledgedFindings, Reason: in.Reason, Reviewer: in.Reviewer, ClientCommandID: in.ClientCommandID, DecidedAt: decidedAt.UTC()}
		return nil
	})
	if err != nil {
		return Review{}, unavailable(err)
	}
	return out, nil
}

func checkReview(in ReviewInput, archived legacyimport.ArchivedCampaign) error {
	review, kinds := map[string]bool{}, map[string]bool{}
	for _, finding := range archived.Item.Findings {
		kinds[finding.Kind] = true
		if finding.Severity == legacyimport.SeverityReview {
			review[finding.Kind] = true
		}
	}
	want := make([]string, 0, len(review))
	for kind := range review {
		want = append(want, kind)
	}
	sort.Strings(want)
	if strings.Join(want, ",") != strings.Join(in.AcknowledgedFindings, ",") {
		return refused("CAMPAIGN_LEGACY_REVIEW_INCOMPLETE", "a review must acknowledge exactly the plan item's review-level findings: "+strings.Join(want, ","))
	}
	if in.Decision == "exclude" && len(in.AcceptedDifferences) > 0 {
		return invalid("an excluded item accepts no differences")
	}
	for _, class := range in.AcceptedDifferences {
		explained := false
		switch class {
		case ClassVisibility:
			explained = in.Decision == "import-unpublished"
		case ClassParticipantCount:
			explained = kinds["PARTICIPANT_CASE_DUPLICATE"]
		case ClassWalletState:
			explained = kinds["PARTICIPANT_WALLET_CASE_LOOKUP"] || kinds["REMINDER_WALLET_CASE_LOOKUP"] || kinds["PARTICIPANT_CASE_DUPLICATE"] || kinds["REMINDER_CASE_DUPLICATE"]
		}
		if !explained {
			return refused("CAMPAIGN_LEGACY_REVIEW_UNEXPLAINED", "the "+class+" difference is not explained by the plan item")
		}
	}
	if in.Decision == "import-public" {
		baseline, err := baselineFromArchive(archived)
		if err != nil {
			return refused("CAMPAIGN_LEGACY_NOT_ACTIVATABLE", "the legacy row cannot become a baseline: "+err.Error())
		}
		if !publicStatuses[baseline.LegacyRecord.Status] {
			return refused("CAMPAIGN_LEGACY_NOT_PUBLIC", "only a campaign the old API listed may be imported publicly")
		}
	}
	return nil
}

func loadReview(ctx context.Context, tx pgx.Tx, where string, args ...any) (Review, bool, error) {
	var out Review
	var differences, findings []byte
	err := tx.QueryRow(ctx, `SELECT id::text,run_id::text,campaign_id,decision,accepted_differences,acknowledged_findings,reason,decided_by,client_command_id::text,decided_at
		FROM campaign_legacy_review_decisions WHERE `+where, args...).Scan(&out.ID, &out.RunID, &out.CampaignID, &out.Decision, &differences, &findings,
		&out.Reason, &out.Reviewer, &out.ClientCommandID, &out.DecidedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if err = json.Unmarshal(differences, &out.AcceptedDifferences); err != nil {
		return out, false, err
	}
	if err = json.Unmarshal(findings, &out.AcknowledgedFindings); err != nil {
		return out, false, err
	}
	out.DecidedAt = out.DecidedAt.UTC()
	return out, true, nil
}

func replayReview(existing Review, in ReviewInput) (Review, error) {
	if existing.RunID != in.RunID || existing.CampaignID != in.CampaignID || existing.Decision != in.Decision || existing.Reason != in.Reason ||
		strings.Join(existing.AcceptedDifferences, ",") != strings.Join(in.AcceptedDifferences, ",") ||
		strings.Join(existing.AcknowledgedFindings, ",") != strings.Join(in.AcknowledgedFindings, ",") {
		return Review{}, refused("CAMPAIGN_LEGACY_IDEMPOTENCY_CONFLICT", "this client command ID recorded a different review")
	}
	existing.Replayed = true
	return existing, nil
}
