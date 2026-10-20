package adminclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// Controlled legacy import administration
// (docs/campaign-legacy-activation-protocol-2026-10-09.md §5). A review or an
// activation is frozen into a private intent before confirmation and sent with
// a fixed UUIDv7 key. An uncertain outcome is settled from the server's own
// record, the plan item's review or the campaign's activation, never by
// sending under another key.
const (
	LegacyReview     = "review"
	LegacyActivation = "activation"
)

// attemptSuperseded: the item already holds another command's review or
// activation. One item admits one of each, so this intent can never commit.
const attemptSuperseded = "superseded"

var cursorPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// maxPlanPages bounds how far a lookup pages through one run's plan.
const maxPlanPages = 2000

// permanentLegacyRefusals are refusals the server decides after it looked the
// key up and found no record, from facts that never change for this frozen
// request: the captured plan item, its recorded review, the fence's drift and
// the body itself. A delayed copy of the same request meets the same refusal,
// so after an uncertain send they prove that nothing committed. Refusals that
// can lift later (a review still missing, the comparison, the wallet bound,
// the session or role) are not here.
var permanentLegacyRefusals = map[string]map[string]bool{
	LegacyReview: {"CAMPAIGN_LEGACY_NOT_REVIEWABLE": true, "CAMPAIGN_LEGACY_REVIEW_INCOMPLETE": true, "CAMPAIGN_LEGACY_REVIEW_UNEXPLAINED": true,
		"CAMPAIGN_LEGACY_NOT_PUBLIC": true, "CAMPAIGN_LEGACY_NOT_ACTIVATABLE": true},
	LegacyActivation: {"CAMPAIGN_LEGACY_DRIFT": true, "CAMPAIGN_LEGACY_PLAN_CHANGED": true, "CAMPAIGN_LEGACY_EXCLUDED": true, "CAMPAIGN_LEGACY_NOT_PUBLIC": true},
}

// LegacyItem is the plan item as the operator saw it when the intent froze.
type LegacyItem struct {
	AsOf               string                    `json:"asOf"`
	Decision           string                    `json:"decision"`
	LegacyRowSHA256    string                    `json:"legacyRowSha256"`
	InteractionsSHA256 string                    `json:"interactionsSha256"`
	ParticipantRows    int64                     `json:"participantRows"`
	ReminderRows       int64                     `json:"reminderRows"`
	Findings           []v2.LegacyFindingSummary `json:"findings"`
	FencedAt           string                    `json:"fencedAt,omitempty"`
	ReviewID           string                    `json:"reviewId,omitempty"`
	ReviewDecision     string                    `json:"reviewDecision,omitempty"`
}

// LegacyIntent is one review or activation frozen before confirmation. Body
// is the exact request; every resend reuses it and its key.
type LegacyIntent struct {
	SchemaVersion int        `json:"schemaVersion"`
	ID            string     `json:"clientCommandId"`
	CreatedAt     string     `json:"createdAt"`
	API           string     `json:"api"`
	Actor         string     `json:"actor"`
	Operation     string     `json:"operation"`
	RunID         string     `json:"runId"`
	CampaignID    string     `json:"campaignId"`
	Path          string     `json:"path"`
	Body          string     `json:"body"`
	BodySHA256    string     `json:"bodySha256"`
	Item          LegacyItem `json:"item"`
	Status        string     `json:"status"`
	Retryable     bool       `json:"retryable,omitempty"`
	Uncertain     bool       `json:"uncertain,omitempty"`
	Settlement    string     `json:"settlement,omitempty"`
	Attempts      []Attempt  `json:"attempts"`
	// The recorded result, or the last refusal's bounded report.
	Review     *v2.LegacyReviewView     `json:"review,omitempty"`
	Activation *v2.LegacyActivationView `json:"activation,omitempty"`
	Refusal    *v2.LegacyImportRefusal  `json:"refusal,omitempty"`
}

func legacyPath(operation, runID, campaignID string) string {
	return "/v2/campaign-admin/legacy-imports/runs/" + runID + "/campaigns/" + campaignID + "/" + operation
}

func validLegacyTarget(runID, campaignID string) error {
	if !revisionIDPattern.MatchString(runID) {
		return errors.New("malformed run ID")
	}
	if !campaignIDPattern.MatchString(campaignID) {
		return errors.New("malformed campaign ID")
	}
	return nil
}

func (h Home) legacyDir() (string, error) {
	dir := filepath.Join(h.Dir, "legacy-intents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, requirePrivate(dir, true)
}

func (h Home) SaveLegacyIntent(intent *LegacyIntent) error {
	if !intentIDPattern.MatchString(intent.ID) {
		return errors.New("intent ID must be a canonical UUIDv7")
	}
	dir, err := h.legacyDir()
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, intent.ID+".json"), intent)
}

// LoadLegacyIntent refuses a record whose request no longer matches what was
// frozen, so an edited file is never sent under the reviewed key.
func (h Home) LoadLegacyIntent(id string) (*LegacyIntent, error) {
	if !intentIDPattern.MatchString(id) {
		return nil, errors.New("intent ID must be a canonical UUIDv7")
	}
	dir, err := h.legacyDir()
	if err != nil {
		return nil, err
	}
	var intent LegacyIntent
	if err = readPrivate(filepath.Join(dir, id+".json"), &intent); err != nil {
		return nil, err
	}
	if intent.ID != id || intent.SchemaVersion != 1 || digest(intent.Body) != intent.BodySHA256 || validLegacyTarget(intent.RunID, intent.CampaignID) != nil ||
		intent.Path != legacyPath(intent.Operation, intent.RunID, intent.CampaignID) || intent.frozenKey() != id {
		return nil, fmt.Errorf("legacy intent %s failed integrity check; do not send it", id)
	}
	return &intent, nil
}

// frozenKey decodes the frozen body under its contract and returns its key.
func (i *LegacyIntent) frozenKey() string {
	switch i.Operation {
	case LegacyReview:
		var body v2.LegacyReviewCommandInput
		if v2.DecodeContract("LegacyReviewCommandInput", []byte(i.Body), &body) == nil {
			return body.ClientCommandID
		}
	case LegacyActivation:
		var body v2.LegacyActivationCommandInput
		if v2.DecodeContract("LegacyActivationCommandInput", []byte(i.Body), &body) == nil {
			return body.ClientCommandID
		}
	}
	return ""
}

func (h Home) ListLegacyIntents() ([]*LegacyIntent, error) {
	dir, err := h.legacyDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var result []*LegacyIntent
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !intentIDPattern.MatchString(id) {
			continue
		}
		intent, err := h.LoadLegacyIntent(id)
		if err != nil {
			return nil, err
		}
		result = append(result, intent)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// LockLegacyIntent keeps two terminals from sending or settling one intent at
// once; the kernel releases the lock when the process exits.
func (h Home) LockLegacyIntent(id string) (func(), error) {
	if !intentIDPattern.MatchString(id) {
		return nil, errors.New("intent ID must be a canonical UUIDv7")
	}
	dir, err := h.legacyDir()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("legacy intent %s is in use by another campaign-admin process", id)
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }, nil
}

// LegacyPlanPage reads one page of a run's plan.
func LegacyPlanPage(ctx context.Context, client *Client, runID, cursor string) (v2.LegacyImportPlanResponse, error) {
	var page v2.LegacyImportPlanResponse
	if !revisionIDPattern.MatchString(runID) {
		return page, errors.New("malformed run ID")
	}
	path := "/v2/campaign-admin/legacy-imports/runs/" + runID
	if cursor != "" {
		if len(cursor) > 2048 || !cursorPattern.MatchString(cursor) {
			return page, errors.New("malformed plan cursor")
		}
		path += "?cursor=" + cursor
	}
	return page, client.GetContract(ctx, path, "LegacyImportPlanResponse", &page)
}

// FindLegacyItem pages through a run's plan to one campaign's item.
func FindLegacyItem(ctx context.Context, client *Client, runID, campaignID string) (v2.LegacyPlanItemView, string, error) {
	cursor := ""
	for pages := 0; pages < maxPlanPages; pages++ {
		page, err := LegacyPlanPage(ctx, client, runID, cursor)
		if err != nil {
			return v2.LegacyPlanItemView{}, "", err
		}
		for _, item := range page.Items {
			if item.CampaignID == campaignID {
				return item, page.AsOf, nil
			}
		}
		if page.NextCursor == nil {
			return v2.LegacyPlanItemView{}, "", fmt.Errorf("campaign %s is not in the plan of run %s", campaignID, runID)
		}
		cursor = *page.NextCursor
	}
	return v2.LegacyPlanItemView{}, "", errors.New("the plan is longer than this client pages through")
}

// ReviewKinds are the review-level finding kinds a review must acknowledge.
func ReviewKinds(item v2.LegacyPlanItemView) []string {
	kinds := []string{}
	for _, finding := range item.Findings {
		if finding.Severity == "review" {
			kinds = append(kinds, finding.Kind)
		}
	}
	sort.Strings(kinds)
	return kinds
}

func sortedUnique(values []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		if seen[value] {
			return nil, fmt.Errorf("%s is listed twice", value)
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func snapshotItem(item v2.LegacyPlanItemView, asOf string) LegacyItem {
	out := LegacyItem{AsOf: asOf, Decision: item.Decision, LegacyRowSHA256: item.LegacyRowSha256, InteractionsSHA256: item.InteractionsSha256,
		ParticipantRows: item.ParticipantRows, ReminderRows: item.ReminderRows, Findings: append([]v2.LegacyFindingSummary{}, item.Findings...)}
	if item.Fence != nil {
		out.FencedAt = item.Fence.FencedAt
	}
	if item.Review != nil {
		out.ReviewID, out.ReviewDecision = item.Review.ReviewID, item.Review.Decision
	}
	return out
}

type LegacyReviewInput struct {
	RunID, CampaignID, Decision, Reason string
	Accept, Acknowledge                 []string
}

// PlanLegacyReview freezes a review of one review-required plan item. It
// checks locally what it can; the server decides.
func PlanLegacyReview(ctx context.Context, home Home, client *Client, credential Credential, in LegacyReviewInput, now time.Time) (*LegacyIntent, error) {
	if err := validLegacyTarget(in.RunID, in.CampaignID); err != nil {
		return nil, err
	}
	accept, err := sortedUnique(in.Accept)
	if err != nil {
		return nil, err
	}
	acknowledge, err := sortedUnique(in.Acknowledge)
	if err != nil {
		return nil, err
	}
	item, asOf, err := FindLegacyItem(ctx, client, in.RunID, in.CampaignID)
	if err != nil {
		return nil, err
	}
	switch {
	case item.Decision != "review-required":
		return nil, fmt.Errorf("the plan item is %s; only a review-required item takes a review", item.Decision)
	case item.Review != nil:
		return nil, fmt.Errorf("the plan item already has review %s; capture a new run to decide again", item.Review.ReviewID)
	case item.Activation != nil:
		return nil, fmt.Errorf("the campaign is already activated (%s)", item.Activation.ActivationID)
	}
	if want := ReviewKinds(item); strings.Join(acknowledge, ",") != strings.Join(want, ",") {
		return nil, fmt.Errorf("a review must acknowledge exactly the item's review-level findings: %s", strings.Join(want, ","))
	}
	id, err := NewCommandID(now)
	if err != nil {
		return nil, err
	}
	body := v2.LegacyReviewCommandInput{ClientCommandID: id, Decision: in.Decision, AcceptedDifferences: accept, AcknowledgedFindings: acknowledge, Reason: in.Reason}
	return freezeLegacy(home, client, credential, LegacyReview, id, in.RunID, in.CampaignID, body, "LegacyReviewCommandInput", snapshotItem(item, asOf), now)
}

// PlanLegacyActivation freezes an activation of one fenced plan item with the
// digests read now; the server refuses it if the item differs when it runs.
func PlanLegacyActivation(ctx context.Context, home Home, client *Client, credential Credential, runID, campaignID, reason string, now time.Time) (*LegacyIntent, error) {
	if err := validLegacyTarget(runID, campaignID); err != nil {
		return nil, err
	}
	item, asOf, err := FindLegacyItem(ctx, client, runID, campaignID)
	if err != nil {
		return nil, err
	}
	if err = activatable(item); err != nil {
		return nil, err
	}
	id, err := NewCommandID(now)
	if err != nil {
		return nil, err
	}
	body := v2.LegacyActivationCommandInput{ClientCommandID: id, Reason: reason, LegacyRowSha256: item.LegacyRowSha256, InteractionsSha256: item.InteractionsSha256}
	return freezeLegacy(home, client, credential, LegacyActivation, id, runID, campaignID, body, "LegacyActivationCommandInput", snapshotItem(item, asOf), now)
}

func activatable(item v2.LegacyPlanItemView) error {
	switch {
	case item.Fence == nil || item.Fence.LiftedAt != nil:
		return errors.New("the campaign is not fenced under this run; fence it with campaign-legacy-import first")
	case item.Activation != nil:
		return fmt.Errorf("the campaign is already activated (%s)", item.Activation.ActivationID)
	case item.Decision == "review-required" && item.Review == nil:
		return errors.New("the plan item needs a review decision first (campaign-admin legacy review)")
	case item.Decision == "review-required" && item.Review.Decision == "exclude":
		return errors.New("the review excluded this campaign from the import")
	case item.Decision != "import-public" && item.Decision != "review-required":
		return fmt.Errorf("a %s plan item cannot be activated", item.Decision)
	}
	return nil
}

func freezeLegacy(home Home, client *Client, credential Credential, operation, id, runID, campaignID string, body any, contract string, item LegacyItem, now time.Time) (*LegacyIntent, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var check any
	if err = v2.DecodeContract(contract, raw, &check); err != nil {
		return nil, fmt.Errorf("the request violates %s: %v", contract, err)
	}
	intent := &LegacyIntent{SchemaVersion: 1, ID: id, CreatedAt: now.UTC().Format(time.RFC3339Nano), API: client.Base(), Actor: credential.Address, Operation: operation,
		RunID: runID, CampaignID: campaignID, Path: legacyPath(operation, runID, campaignID), Body: string(raw), BodySHA256: digest(string(raw)), Item: item,
		Status: StatusPlanned, Attempts: []Attempt{}}
	return intent, home.SaveLegacyIntent(intent)
}

// open checks, before a send, that the item can still take this intent: the
// digests it froze, and no other command's review or activation.
func (i *LegacyIntent) open(item v2.LegacyPlanItemView) error {
	if item.LegacyRowSha256 != i.Item.LegacyRowSHA256 || item.InteractionsSha256 != i.Item.InteractionsSHA256 || item.Decision != i.Item.Decision {
		return errors.New("the plan item differs from the one this intent froze; abandon it and plan again")
	}
	if i.Operation == LegacyReview {
		if item.Review != nil {
			return fmt.Errorf("the plan item already has review %s", item.Review.ReviewID)
		}
		if item.Activation != nil {
			return errors.New("the campaign is already activated")
		}
		return nil
	}
	if err := activatable(item); err != nil {
		return err
	}
	if item.Review != nil && item.Review.ReviewID != i.Item.ReviewID {
		return errors.New("the plan item's review differs from the one this intent froze; abandon it and plan again")
	}
	return nil
}

func (i *LegacyIntent) normalize() {
	if i.Status == StatusSending || i.Status == StatusUnknown {
		i.Status, i.Uncertain = StatusUnknown, true
	}
}

func (i *LegacyIntent) sentBefore() bool {
	for _, attempt := range i.Attempts {
		if attempt.Kind == "send" {
			return true
		}
	}
	return false
}

func (i *LegacyIntent) settle(status, how string) {
	i.Status, i.Settlement, i.Uncertain, i.Retryable = status, how, false, false
}

func legacyOwner(intent *LegacyIntent, client *Client, credential Credential) error {
	if intent.API != client.Base() {
		return fmt.Errorf("intent targets %s, not %s", intent.API, client.Base())
	}
	if intent.Actor != credential.Address {
		return fmt.Errorf("intent belongs to administrator %s; current session is %s", intent.Actor, credential.Address)
	}
	return nil
}

// ownReview reports whether a recorded review is exactly this intent's
// frozen request; ok is false when the record is another command's.
func (i *LegacyIntent) ownReview(review v2.LegacyReviewView) (own bool, note string) {
	if review.ClientCommandID != i.ID {
		return false, ""
	}
	var body v2.LegacyReviewCommandInput
	if err := v2.DecodeContract("LegacyReviewCommandInput", []byte(i.Body), &body); err != nil {
		return false, "frozen body cannot be decoded"
	}
	if review.Reviewer != i.Actor || review.RunID != i.RunID || review.CampaignID != i.CampaignID || review.Decision != body.Decision || review.Reason != body.Reason ||
		strings.Join(review.AcceptedDifferences, ",") != strings.Join(body.AcceptedDifferences, ",") ||
		strings.Join(review.AcknowledgedFindings, ",") != strings.Join(body.AcknowledgedFindings, ",") {
		return false, "a review under this key differs from the frozen request"
	}
	return true, ""
}

// ownActivation reports whether an activation record is exactly this
// intent's frozen request, by its key, actor and ledger payload hash.
func (i *LegacyIntent) ownActivation(activation v2.LegacyActivationView) (own bool, note string) {
	if activation.ClientCommandID != i.ID {
		return false, ""
	}
	var body v2.LegacyActivationCommandInput
	if err := v2.DecodeContract("LegacyActivationCommandInput", []byte(i.Body), &body); err != nil {
		return false, "frozen body cannot be decoded"
	}
	want, err := v2.LegacyActivationPayloadHash(i.RunID, i.CampaignID, body)
	if err != nil || activation.PayloadHash != want || activation.ActivatedBy != i.Actor || activation.RunID != i.RunID || activation.CampaignID != i.CampaignID {
		return false, "an activation under this key differs from the frozen request"
	}
	return true, ""
}

// readLegacyRecord reads the server's record for the intent's target and
// classifies it: this intent's own (committed), another command's
// (superseded), none yet (absent), refused or unreadable.
func readLegacyRecord(ctx context.Context, client *Client, intent *LegacyIntent, now func() time.Time) Attempt {
	attempt := Attempt{At: now().UTC().Format(time.RFC3339Nano), Kind: "reconcile"}
	refused := func(err error) Attempt {
		var read *ReadError
		if errors.As(err, &read) {
			attempt.HTTPStatus, attempt.RequestID = read.Reply.Status, read.Reply.RequestID
			attempt.Code, _ = read.Reply.ErrorCode()
		}
		if attempt.Code == "UNAUTHORIZED" || attempt.Code == "CAMPAIGN_ADMIN_REQUIRED" {
			attempt.Outcome, attempt.Note = attemptRefused, "read refused; restore the session or administrator role before settling this intent"
		} else {
			attempt.Outcome, attempt.Note = attemptUnreadable, "record unreadable: "+err.Error()
		}
		return attempt
	}
	if intent.Operation == LegacyReview {
		item, _, err := FindLegacyItem(ctx, client, intent.RunID, intent.CampaignID)
		if err != nil {
			return refused(err)
		}
		attempt.HTTPStatus = http.StatusOK
		switch own, note := intent.ownReview(derefReview(item.Review)); {
		case item.Review == nil:
			attempt.Outcome, attempt.Note = attemptAbsent, "the plan item has no review yet; not proof that an earlier or in-flight request will not record one"
		case own:
			intent.Review = item.Review
			attempt.Outcome, attempt.Note = attemptCommitted, "the plan item holds this intent's review "+item.Review.ReviewID
		case note != "":
			attempt.Outcome, attempt.Note = attemptUnreadable, note
		default:
			attempt.Outcome, attempt.Note = attemptSuperseded, "the plan item holds another command's review "+item.Review.ReviewID+"; one item admits one review"
		}
		return attempt
	}
	reply := client.Do(ctx, http.MethodGet, "/v2/campaign-admin/legacy-imports/campaigns/"+intent.CampaignID+"/activation", nil,
		"campaign-admin-"+intent.ID+"-r"+strconv.Itoa(len(intent.Attempts)+1))
	attempt.HTTPStatus, attempt.RequestID = reply.Status, reply.RequestID
	attempt.Code, _ = reply.ErrorCode()
	if reply.Err != nil || reply.Status != http.StatusOK {
		if attempt.Code == "CAMPAIGN_LEGACY_NOT_ACTIVATED" && reply.Status == http.StatusNotFound {
			attempt.Outcome, attempt.Note = attemptAbsent, "the campaign has no activation yet; not proof that an earlier or in-flight request will not activate it"
			return attempt
		}
		return refused(&ReadError{Reply: reply})
	}
	var value v2.LegacyActivationResponse
	if err := v2.DecodeContractResponse("LegacyActivationResponse", reply.Body, &value); err != nil {
		attempt.Outcome, attempt.Note = attemptUnreadable, "activation record violates LegacyActivationResponse"
		return attempt
	}
	switch own, note := intent.ownActivation(value.Activation); {
	case own:
		intent.Activation = &value.Activation
		attempt.Outcome, attempt.Note = attemptCommitted, "the campaign holds this intent's activation "+value.Activation.ActivationID
	case note != "":
		attempt.Outcome, attempt.Note = attemptUnreadable, note
	default:
		attempt.Outcome, attempt.Note = attemptSuperseded, "the campaign holds another command's activation "+value.Activation.ActivationID+"; a campaign is activated once"
	}
	return attempt
}

func derefReview(review *v2.LegacyReviewView) v2.LegacyReviewView {
	if review == nil {
		return v2.LegacyReviewView{}
	}
	return *review
}

// classifyLegacySend maps one send observation. A success counts only when it
// is provably this intent's record; only a contract-valid campaign 4xx proves
// that this attempt did not execute.
func classifyLegacySend(intent *LegacyIntent, reply Reply) (Attempt, string, bool) {
	attempt := Attempt{RequestID: reply.RequestID, HTTPStatus: reply.Status}
	if reply.Err != nil {
		attempt.Outcome, attempt.Note = attemptUncertain, reply.Err.Error()
		return attempt, StatusUnknown, false
	}
	if reply.Status == http.StatusOK || reply.Status == http.StatusCreated {
		replayed, own, note := false, false, "success status with a body that violates the response contract"
		if intent.Operation == LegacyReview {
			var value v2.LegacyReviewResponse
			if v2.DecodeContractResponse("LegacyReviewResponse", reply.Body, &value) == nil {
				replayed = value.Replayed
				if own, note = intent.ownReview(value.Review); own {
					intent.Review = &value.Review
				}
			}
		} else {
			var value v2.LegacyActivationResponse
			if v2.DecodeContractResponse("LegacyActivationResponse", reply.Body, &value) == nil {
				replayed = value.Replayed
				if own, note = intent.ownActivation(value.Activation); own {
					intent.Activation = &value.Activation
				}
			}
		}
		switch {
		case !own && note == "":
			note = "the response is another command's record"
		case own && (reply.Status == http.StatusCreated) == replayed:
			own, note = false, "status code disagrees with the replay flag"
		case own && replayed && !intent.sentBefore():
			own, note = false, "replay reported for a key this client never sent before"
		}
		if !own {
			intent.Review, intent.Activation = nil, nil
			attempt.Outcome, attempt.Note = attemptUncertain, note
			return attempt, StatusUnknown, false
		}
		attempt.Outcome, attempt.Note = attemptCommitted, map[bool]string{true: "replayed: the original record is returned", false: "executed"}[replayed]
		return attempt, StatusCommitted, false
	}
	if intent.Operation == LegacyActivation && reply.Status == http.StatusConflict {
		var refusal v2.LegacyImportRefusal
		if v2.DecodeContractResponse("LegacyImportRefusal", reply.Body, &refusal) == nil {
			intent.Refusal = &refusal
			attempt.Code, attempt.Outcome = refusal.Code, attemptRefused
			attempt.Note = fmt.Sprintf("refused with %d drift entries and %s", len(refusal.Drift), comparisonSummary(refusal.Comparison))
			return attempt, StatusRejected, false
		}
	}
	code, ok := reply.ErrorCode()
	attempt.Code = code
	if !ok || reply.Status < 400 || reply.Status >= 500 {
		attempt.Outcome, attempt.Note = attemptUncertain, reply.Describe()
		return attempt, StatusUnknown, false
	}
	attempt.Outcome = attemptRefused
	switch reply.Status {
	case http.StatusUnauthorized:
		attempt.Note = "session rejected before execution; log in again and resubmit this same intent"
		return attempt, StatusRejected, true
	case http.StatusForbidden:
		attempt.Note = "not a campaign administrator; resubmit this intent only after authorization is restored"
		return attempt, StatusRejected, true
	}
	attempt.Note = "refused before execution"
	return attempt, StatusRejected, false
}

func comparisonSummary(c *v2.LegacyComparisonView) string {
	if c == nil {
		return "no comparison"
	}
	return fmt.Sprintf("a comparison of %d wallets with differences %s (accepted %s)", c.Wallets, orNone(c.Classes), orNone(c.Accepted))
}

func orNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ",")
}

// LegacyConfirm shows the frozen intent and the item read just now. Returning
// an error aborts before anything is sent.
type LegacyConfirm func(intent *LegacyIntent, fresh v2.LegacyPlanItemView) error

// SubmitLegacy sends a planned intent, or settles an uncertain one with the
// same key and bytes. An uncertain intent first reads the server's record:
// its own record settles it, another command's record proves it can never
// commit, an unreadable record stops, and only an absent one leads to a resend.
// Before any send the plan item is read again and must match the frozen one.
func SubmitLegacy(ctx context.Context, home Home, client *Client, credential Credential, id string, now func() time.Time, confirm LegacyConfirm) (*LegacyIntent, error) {
	unlock, err := home.LockLegacyIntent(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	intent, err := home.LoadLegacyIntent(id)
	if err != nil {
		return nil, err
	}
	if err = legacyOwner(intent, client, credential); err != nil {
		return intent, err
	}
	intent.normalize()
	switch {
	case intent.Status == StatusCommitted:
		return intent, errors.New("intent already committed; use legacy status to read its record")
	case intent.Status == StatusAbandoned:
		return intent, errors.New("intent was abandoned before sending")
	case intent.Status == StatusRejected && !intent.Retryable:
		return intent, errors.New("server refused this intent; review the plan and freeze a new one")
	case intent.Uncertain:
		attempt := readLegacyRecord(ctx, client, intent, now)
		intent.Attempts = append(intent.Attempts, attempt)
		switch attempt.Outcome {
		case attemptCommitted:
			intent.settle(StatusCommitted, "ledger-record")
			return intent, home.SaveLegacyIntent(intent)
		case attemptSuperseded:
			intent.settle(StatusRejected, "proven-not-executed:superseded")
			return intent, home.SaveLegacyIntent(intent)
		case attemptAbsent:
		default:
			if err = home.SaveLegacyIntent(intent); err != nil {
				return intent, err
			}
			return intent, fmt.Errorf("cannot settle: %s; an earlier attempt may have committed, so do not freeze a replacement", attempt.Note)
		}
	}
	item, _, err := FindLegacyItem(ctx, client, intent.RunID, intent.CampaignID)
	if err != nil {
		return intent, err
	}
	if err = intent.open(item); err != nil {
		return intent, err
	}
	if err = confirm(intent, item); err != nil {
		return intent, err
	}
	uncertainBefore := intent.Uncertain
	intent.Status, intent.Refusal = StatusSending, nil
	if err = home.SaveLegacyIntent(intent); err != nil {
		return intent, err
	}
	requestID := "campaign-admin-" + intent.ID + "-" + strconv.Itoa(len(intent.Attempts)+1)
	reply := client.Do(ctx, http.MethodPost, intent.Path, []byte(intent.Body), requestID)
	attempt, status, retryable := classifyLegacySend(intent, reply)
	attempt.At, attempt.Kind = now().UTC().Format(time.RFC3339Nano), "send"
	intent.Attempts = append(intent.Attempts, attempt)
	switch {
	case status == StatusCommitted:
		intent.settle(StatusCommitted, map[bool]string{true: "ledger-record", false: "executed"}[strings.HasPrefix(attempt.Note, "replayed")])
	case status == StatusRejected && uncertainBefore:
		// The refusal proves only that this resend did not execute; the
		// record decides what the earlier attempt did.
		check := readLegacyRecord(ctx, client, intent, now)
		intent.Attempts = append(intent.Attempts, check)
		switch {
		case check.Outcome == attemptCommitted:
			intent.settle(StatusCommitted, "ledger-record")
		case check.Outcome == attemptSuperseded:
			intent.settle(StatusRejected, "proven-not-executed:superseded")
		case check.Outcome == attemptAbsent && permanentLegacyRefusals[intent.Operation][attempt.Code]:
			intent.settle(StatusRejected, "proven-not-executed:"+attempt.Code)
		default:
			intent.Status, intent.Uncertain, intent.Retryable = StatusUnknown, true, false
		}
	case status == StatusRejected:
		intent.settle(StatusRejected, "refused-before-execution:"+attempt.Code)
		intent.Retryable = retryable
	default:
		intent.Status, intent.Uncertain, intent.Retryable = StatusUnknown, true, false
	}
	return intent, home.SaveLegacyIntent(intent)
}

// ReconcileLegacy reads the server's record for an intent. Only its own
// record (committed) or another command's record for an uncertain intent
// (proven not executed) changes the intent.
func ReconcileLegacy(ctx context.Context, home Home, client *Client, credential Credential, id string, now func() time.Time) (*LegacyIntent, error) {
	unlock, err := home.LockLegacyIntent(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	intent, err := home.LoadLegacyIntent(id)
	if err != nil {
		return nil, err
	}
	if err = legacyOwner(intent, client, credential); err != nil {
		return intent, err
	}
	intent.normalize()
	if intent.Status == StatusPlanned || intent.Status == StatusAbandoned {
		return intent, errors.New("intent was never sent; nothing to reconcile")
	}
	attempt := readLegacyRecord(ctx, client, intent, now)
	if attempt.Outcome == attemptCommitted && intent.Status == StatusRejected {
		attempt.Note += "; the server holds this key's record although a send was classified as refused"
	}
	intent.Attempts = append(intent.Attempts, attempt)
	switch {
	case attempt.Outcome == attemptCommitted:
		intent.settle(StatusCommitted, "ledger-record")
	case attempt.Outcome == attemptSuperseded && intent.Uncertain:
		intent.settle(StatusRejected, "proven-not-executed:superseded")
	}
	return intent, home.SaveLegacyIntent(intent)
}

// AbandonLegacy retires an intent that was never sent.
func AbandonLegacy(home Home, id string, now time.Time) (*LegacyIntent, error) {
	unlock, err := home.LockLegacyIntent(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	intent, err := home.LoadLegacyIntent(id)
	if err != nil {
		return nil, err
	}
	if intent.Status != StatusPlanned {
		return intent, fmt.Errorf("only a never-sent intent can be abandoned (status %s)", intent.Status)
	}
	intent.Attempts = append(intent.Attempts, Attempt{At: now.UTC().Format(time.RFC3339Nano), Kind: "abandon", Outcome: StatusAbandoned})
	intent.Status = StatusAbandoned
	return intent, home.SaveLegacyIntent(intent)
}
