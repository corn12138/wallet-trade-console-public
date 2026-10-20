package legacyactivation

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Engine runs reviews and activations against one database.
type Engine struct {
	pool         *pgxpool.Pool
	capabilities func() v2.Capabilities
	// MaxWallets bounds how many wallets one activation compares, each
	// through the real legacy and versioned reads.
	MaxWallets int
	// Timeout bounds one activation transaction, which holds the campaign's
	// fence lock throughout.
	Timeout time.Duration
}

func New(pool *pgxpool.Pool, capabilities func() v2.Capabilities) *Engine {
	return &Engine{pool: pool, capabilities: capabilities, MaxWallets: 10000, Timeout: 5 * time.Minute}
}

// ActivationInput activates one fenced, verified plan item.
type ActivationInput struct {
	RunID, CampaignID string
	Actor             string
	ClientCommandID   string
	Reason, RequestID string
	// The digests of the plan item the operator reviewed; an activation
	// refuses a plan that differs from them.
	LegacyRowSHA256, InteractionsSHA256 string
	// Authorize, when set, re-checks the caller once the campaign's fence lock
	// is held and before anything is written or replayed: a session ended or
	// an administrator grant withdrawn while waiting rolls everything back.
	Authorize func(context.Context) error
}

type Activation struct {
	ID, CampaignID, RunID, Decision, ReviewID string
	RevisionID, CommandID, PublishCommandID   string
	// ClientCommandID and PayloadHash identify the request that activated; a
	// client recognizes its own activation by them after an uncertain send.
	ClientCommandID, PayloadHash           string
	ActivatedBy                            string
	ActivatedAt                            time.Time
	PublicationVersion, InteractionVersion uint64
	ImportedInteractions                   int
	Comparison                             Comparison
	Replayed                               bool
}

// validRequestID accepts what the HTTP layer forwards: 1-96 characters of
// valid UTF-8 without control characters.
func validRequestID(id string) bool {
	if id == "" || len(id) > 96 || !utf8.ValidString(id) {
		return false
	}
	for _, ch := range id {
		if unicode.IsControl(ch) {
			return false
		}
	}
	return true
}

// activationHook runs after the comparison passed and before the activation
// record is written, while the fence lock is held (tests only).
var activationHook func()

// outsider is a wallet with no legacy rows; comparing it checks that absence
// is answered the same way before and after.
const outsider = "0x00000000000000000000000000000000000000ff"

func (in ActivationInput) normalized() (ActivationInput, error) {
	in.Actor = strings.ToLower(in.Actor)
	switch {
	case !walletPattern.MatchString(in.Actor):
		return in, invalid("actor must be a wallet")
	case !clientIDPattern.MatchString(in.ClientCommandID):
		return in, invalid("client command ID must be a UUIDv7")
	case !runIDPattern.MatchString(in.RunID) || in.CampaignID == "" || len(in.CampaignID) > 160:
		return in, invalid("run or campaign identity")
	case !hashPattern.MatchString(in.LegacyRowSHA256) || !hashPattern.MatchString(in.InteractionsSHA256):
		return in, invalid("the reviewed plan digests are required")
	case strings.TrimSpace(in.Reason) == "" || !utf8.ValidString(in.Reason) || utf8.RuneCountInString(in.Reason) > 1000:
		return in, invalid("reason must be 1-1000 characters")
	case !validRequestID(in.RequestID):
		return in, invalid("request ID must be 1-96 characters without control characters")
	}
	return in, nil
}

func (in ActivationInput) payloadHash() (string, error) {
	return v2.LegacyActivationPayloadHash(in.RunID, in.CampaignID, v2.LegacyActivationCommandInput{ClientCommandID: in.ClientCommandID, Reason: in.Reason,
		LegacyRowSha256: in.LegacyRowSHA256, InteractionsSha256: in.InteractionsSHA256})
}

// Activate runs one activation transaction (protocol §2). A refusal is an
// *Error; a failed comparison carries the comparison and writes nothing.
func (e *Engine) Activate(ctx context.Context, in ActivationInput) (Activation, error) {
	in, err := in.normalized()
	if err != nil {
		return Activation{}, err
	}
	payloadHash, err := in.payloadHash()
	if err != nil {
		return Activation{}, err
	}
	if e == nil || e.pool == nil {
		return Activation{}, &Error{Code: "CAMPAIGN_DATA_UNAVAILABLE", Status: 503, Detail: "campaign database unavailable"}
	}
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	var out Activation
	err = pgx.BeginTxFunc(ctx, e.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		var err error
		out, err = e.activate(ctx, tx, in, payloadHash)
		return err
	})
	if err != nil {
		return Activation{}, unavailable(err)
	}
	return out, nil
}

func (e *Engine) activate(ctx context.Context, tx pgx.Tx, in ActivationInput, payloadHash string) (Activation, error) {
	// The exclusive fence lock taken here lasts until the transaction ends:
	// legacy writers, verifications, lifts and other activations of this
	// campaign wait for it.
	verify, err := legacyimport.VerifyInTx(ctx, tx, in.RunID, in.CampaignID)
	if errors.Is(err, legacyimport.ErrNotFenced) {
		return Activation{}, refused("CAMPAIGN_LEGACY_NOT_FENCED", "the campaign has no active fence under this run")
	}
	if err != nil {
		return Activation{}, err
	}
	if in.Authorize != nil {
		if err = in.Authorize(ctx); err != nil {
			return Activation{}, err
		}
	}
	if existing, found, err := loadActivation(ctx, tx, in.CampaignID); err != nil || found {
		if err != nil {
			return Activation{}, err
		}
		return replayActivation(existing, in, payloadHash)
	}
	if !verify.Verified {
		return Activation{}, &Error{Code: "CAMPAIGN_LEGACY_DRIFT", Status: 409, Detail: "the legacy rows changed since the capture; capture a new run", Drift: verify.Drift}
	}
	archived, err := legacyimport.LoadArchived(ctx, tx, in.RunID, in.CampaignID)
	if err != nil {
		return Activation{}, archiveRefusal(err)
	}
	if archived.Item.LegacyRowSHA256 != in.LegacyRowSHA256 || archived.Item.InteractionsSHA256 != in.InteractionsSHA256 {
		return Activation{}, refused("CAMPAIGN_LEGACY_PLAN_CHANGED", "the plan item differs from the reviewed one")
	}
	decision, review, err := planDecision(ctx, tx, archived)
	if err != nil {
		return Activation{}, err
	}
	if !campaignIDPattern.MatchString(in.CampaignID) {
		return Activation{}, refused("CAMPAIGN_LEGACY_NOT_ACTIVATABLE", "the legacy ID cannot be addressed by the versioned API")
	}
	baseline, err := baselineFromArchive(archived)
	if err != nil {
		return Activation{}, refused("CAMPAIGN_LEGACY_NOT_ACTIVATABLE", "the legacy row cannot become a baseline: "+err.Error())
	}
	if decision == "import-public" && !publicStatuses[baseline.LegacyRecord.Status] {
		return Activation{}, refused("CAMPAIGN_LEGACY_NOT_PUBLIC", "only a campaign the old API listed may be imported publicly")
	}
	interactions, err := interactionsFromArchive(archived)
	if err != nil {
		return Activation{}, refused("CAMPAIGN_LEGACY_NOT_ACTIVATABLE", "the legacy interactions cannot be imported: "+err.Error())
	}
	wallets := comparedWallets(interactions)
	if len(wallets) > e.MaxWallets {
		return Activation{}, refused("CAMPAIGN_LEGACY_NOT_ACTIVATABLE", "the campaign has more wallets than one activation compares; raise the bound explicitly")
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return Activation{}, err
	}
	now = now.UTC()

	before, err := answers(wallets, func(wallet string) (campaign.Campaign, error) {
		return campaign.LegacyAnswer(ctx, tx, now, in.CampaignID, wallet)
	})
	if err != nil {
		return Activation{}, err
	}
	activationID, err := application.NewID()
	if err != nil {
		return Activation{}, err
	}
	snapshot, err := json.Marshal(map[string]any{"activationId": activationID, "runId": in.RunID, "capturedAt": archived.CapturedAt.Format(time.RFC3339Nano),
		"algorithm": legacyimport.DigestAlgorithm, "legacyRowSha256": archived.Item.LegacyRowSHA256, "interactionsSha256": archived.Item.InteractionsSHA256,
		"row": json.RawMessage(archived.Campaign.JSON)})
	if err != nil {
		return Activation{}, err
	}
	written, err := postgres.WriteLegacyActivation(ctx, tx, postgres.LegacyActivation{ActivationID: activationID, CampaignID: in.CampaignID, Actor: in.Actor, At: now,
		ClientCommandID: in.ClientCommandID, PayloadHash: payloadHash, Reason: in.Reason, RequestID: in.RequestID, RunID: in.RunID, Decision: decision,
		Snapshot: snapshot, Baseline: baseline, Publish: decision == "import-public", Interactions: interactions})
	if err != nil {
		return Activation{}, err
	}
	capabilities := v2.Capabilities{}
	if e.capabilities != nil {
		capabilities = e.capabilities()
	}
	reader := postgres.NewTxReader(tx, now, capabilities)
	after, err := answers(wallets, func(wallet string) (campaign.Campaign, error) {
		return campaign.CompatibleAnswer(ctx, tx, reader, now, in.CampaignID, wallet)
	})
	if err != nil {
		return Activation{}, err
	}
	compare := &comparator{seen: map[string]bool{}}
	for i, wallet := range append([]string{""}, wallets...) {
		compare.compare(before[i], after[i], wallet)
	}
	var accepted []string
	reviewID := ""
	if review != nil {
		accepted, reviewID = review.AcceptedDifferences, review.ID
	}
	comparison := compare.result(now.Format(time.RFC3339Nano), len(wallets), accepted)
	if !comparison.passes() {
		return Activation{}, &Error{Code: "CAMPAIGN_LEGACY_COMPARISON_FAILED", Status: 409,
			Detail: "the legacy answer would change beyond what the review accepted: " + strings.Join(comparison.Classes, ","), Comparison: &comparison}
	}
	if activationHook != nil {
		activationHook()
	}
	recorded, err := json.Marshal(comparison)
	if err != nil {
		return Activation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO campaign_legacy_activations(id,campaign_id,run_id,decision,review_id,command_id,publish_command_id,revision_id,
		activated_by,activated_at,legacy_row_sha256,interactions_sha256,imported_interactions,comparison)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, activationID, in.CampaignID, in.RunID, decision, nullable(reviewID), written.CommandID,
		nullable(written.PublishCommandID), written.RevisionID, in.Actor, now, archived.Item.LegacyRowSHA256, archived.Item.InteractionsSHA256, len(interactions), recorded); err != nil {
		return Activation{}, err
	}
	return Activation{ID: activationID, CampaignID: in.CampaignID, RunID: in.RunID, Decision: decision, ReviewID: reviewID, RevisionID: written.RevisionID,
		CommandID: written.CommandID, PublishCommandID: written.PublishCommandID, ClientCommandID: in.ClientCommandID, PayloadHash: payloadHash, ActivatedBy: in.Actor, ActivatedAt: now,
		PublicationVersion: written.PublicationVersion, InteractionVersion: written.InteractionVersion, ImportedInteractions: len(interactions), Comparison: comparison}, nil
}

// planDecision is the item's own decision, or its recorded review's.
func planDecision(ctx context.Context, tx pgx.Tx, archived legacyimport.ArchivedCampaign) (string, *Review, error) {
	switch archived.Item.Decision {
	case "import-public":
		return "import-public", nil, nil
	case "review-required":
		review, found, err := loadReview(ctx, tx, `run_id=$1 AND campaign_id=$2`, archived.RunID, archived.CampaignID)
		if err != nil {
			return "", nil, err
		}
		if !found {
			return "", nil, refused("CAMPAIGN_LEGACY_REVIEW_REQUIRED", "this plan item needs a recorded review decision first")
		}
		if review.Decision == "exclude" {
			return "", nil, refused("CAMPAIGN_LEGACY_EXCLUDED", "the review excluded this campaign from the import")
		}
		return review.Decision, &review, nil
	}
	return "", nil, refused("CAMPAIGN_LEGACY_NOT_ACTIVATABLE", "a "+archived.Item.Decision+" plan item cannot be activated")
}

// comparedWallets are the imported wallets, sorted, plus one outsider.
func comparedWallets(interactions []postgres.LegacyInteraction) []string {
	seen := map[string]bool{outsider: true}
	out := []string{outsider}
	for _, item := range interactions {
		if !seen[item.Wallet] {
			seen[item.Wallet] = true
			out = append(out, item.Wallet)
		}
	}
	sort.Strings(out)
	return out
}

// answers reads the anonymous answer, then one per wallet.
func answers(wallets []string, read func(string) (campaign.Campaign, error)) ([]answer, error) {
	out := make([]answer, 0, len(wallets)+1)
	for _, wallet := range append([]string{""}, wallets...) {
		value, err := answerOf(read(wallet))
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func loadActivation(ctx context.Context, tx pgx.Tx, campaignID string) (activationRecord, bool, error) {
	var out activationRecord
	var reviewID, publishID *string
	var comparison, result []byte
	err := tx.QueryRow(ctx, `SELECT a.id::text,a.run_id::text,a.decision,a.review_id::text,a.command_id::text,a.publish_command_id::text,a.revision_id::text,
		a.activated_by,a.activated_at,a.imported_interactions,a.comparison,c.actor,c.client_command_id::text,c.payload_hash,c.result
		FROM campaign_legacy_activations a JOIN campaign_commands c ON c.id=a.command_id WHERE a.campaign_id=$1`, campaignID).
		Scan(&out.ID, &out.RunID, &out.Decision, &reviewID, &out.CommandID, &publishID, &out.RevisionID, &out.ActivatedBy, &out.ActivatedAt,
			&out.ImportedInteractions, &comparison, &out.actor, &out.ClientCommandID, &out.PayloadHash, &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	out.CampaignID = campaignID
	if reviewID != nil {
		out.ReviewID = *reviewID
	}
	if publishID != nil {
		out.PublishCommandID = *publishID
	}
	out.ActivatedAt = out.ActivatedAt.UTC()
	if err = json.Unmarshal(comparison, &out.Comparison); err != nil {
		return out, false, err
	}
	var versions struct {
		Publication string `json:"publicationVersion"`
		Interaction string `json:"interactionVersion"`
	}
	if err = json.Unmarshal(result, &versions); err != nil {
		return out, false, err
	}
	if out.PublicationVersion, err = strconv.ParseUint(versions.Publication, 10, 64); err != nil {
		return out, false, err
	}
	if out.InteractionVersion, err = strconv.ParseUint(versions.Interaction, 10, 64); err != nil {
		return out, false, err
	}
	return out, true, nil
}

type activationRecord struct {
	Activation
	actor string
}

// replayActivation returns the recorded activation to the command that made
// it; any other command meets an activated campaign.
func replayActivation(existing activationRecord, in ActivationInput, payloadHash string) (Activation, error) {
	if existing.actor != in.Actor || existing.ClientCommandID != in.ClientCommandID {
		return Activation{}, refused("CAMPAIGN_LEGACY_ALREADY_ACTIVATED", "this campaign is already activated")
	}
	if existing.PayloadHash != payloadHash || existing.RunID != in.RunID {
		return Activation{}, refused("CAMPAIGN_LEGACY_IDEMPOTENCY_CONFLICT", "this client command ID activated with a different request")
	}
	out := existing.Activation
	out.Replayed = true
	return out, nil
}
