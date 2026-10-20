package adminclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

const (
	testActor     = "0x1111111111111111111111111111111111111111"
	testCampaign  = "campaign-1"
	testActive    = "01900000-0000-4000-8000-0000000000a1"
	testCandidate = "01900000-0000-4000-8000-0000000000c2"
)

// testIntent freezes a real request body for op, so every binding check below
// runs against the same payload hash the server ledger would store.
func testIntent(t *testing.T, op string) *Intent {
	t.Helper()
	id := mustID(t)
	expected := "4"
	var value any
	switch op {
	case "create", "revision":
		raw, err := os.ReadFile("../../../../../../packages/shared/contracts/fixtures/campaign-v2-revision.json")
		if err != nil {
			t.Fatal(err)
		}
		revision, err := v2.DecodeRevision(raw)
		if err != nil {
			t.Fatal(err)
		}
		if op == "create" {
			value = v2.CreateCommandInput{ClientCommandID: id, Reason: "test", Revision: revision}
		} else {
			value = v2.RevisionCommandInput{ClientCommandID: id, Reason: "test", ExpectedPublicationVersion: expected, Revision: revision}
		}
	default:
		body := v2.PublicationCommandInput{ClientCommandID: id, Reason: "test", ExpectedPublicationVersion: expected, Operation: op}
		if op == "publish" || op == "rollback" {
			candidate := testCandidate
			body.CandidateRevisionID = &candidate
		}
		value = body
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	active := testActive
	intent := &Intent{SchemaVersion: 1, ID: id, Actor: testActor, Operation: op, CampaignID: testCampaign, Body: string(raw), BodySHA256: digest(string(raw)), Status: StatusPlanned,
		Review: Review{ExpectedPublicationVersion: &expected, Current: &CurrentState{PublicationVersion: expected, ActiveRevisionID: &active}}}
	intent.Method, intent.Path = http.MethodPost, "/v2/campaign-admin/campaigns/"+testCampaign+"/publications"
	switch op {
	case "create":
		intent.CampaignID, intent.Review, intent.Path = "", Review{}, "/v2/campaign-admin/campaigns"
	case "revision":
		intent.Path = "/v2/campaign-admin/campaigns/" + testCampaign + "/revisions"
	}
	if intent.LedgerPayloadHash, err = intent.ledgerPayloadHash(); err != nil {
		t.Fatal(err)
	}
	return intent
}

// validRecord is the ledger response the server would return for intent: the
// exact identity, the original result its payload implies, current == original.
func validRecord(intent *Intent, replayed bool) v2.AdminCommandResponse {
	target, version, revision := intent.CampaignID, "5", testCandidate
	switch intent.Operation {
	case "create":
		target, version, revision = "campaigns", "0", "01900000-0000-4000-8000-0000000000d1"
	case "revision":
		version, revision = "4", "01900000-0000-4000-8000-0000000000d2"
	case "pause", "resume", "archive":
		revision = testActive
	}
	campaign := intent.CampaignID
	if campaign == "" {
		campaign = "created-campaign"
	}
	result := v2.CommandResult{CampaignID: campaign, RevisionID: &revision, PublicationVersion: version, InteractionVersion: "0", Changed: true}
	return v2.AdminCommandResponse{SchemaVersion: 1, ServerNow: "2026-10-09T00:00:00Z", AsOf: "2026-10-09T00:00:00Z",
		Command:  v2.AdminCommandIdentity{ClientCommandID: intent.ID, Actor: intent.Actor, Operation: intent.Operation, TargetID: target, PayloadHash: intent.LedgerPayloadHash, CommittedAt: "2026-10-09T00:00:00Z"},
		Original: result, Current: result, Replayed: replayed}
}

func encode(t *testing.T, value v2.AdminCommandResponse) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func errorBody(code string) []byte {
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "code": code, "requestId": "req"})
	return raw
}

func strptr(value string) *string { return &value }

// Every field that ties a response to this command is checked independently:
// a well-formed record that differs in any of them must stay unknown.
func TestBindingAcceptsOnlyThisIntentsLedgerRecord(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*v2.AdminCommandResponse)
		want   string
	}{
		{"exact record", func(*v2.AdminCommandResponse) {}, StatusCommitted},
		{"other key", func(r *v2.AdminCommandResponse) { r.Command.ClientCommandID = "01900000-0000-7000-8000-000000000999" }, StatusUnknown},
		{"other actor", func(r *v2.AdminCommandResponse) { r.Command.Actor = "0x2222222222222222222222222222222222222222" }, StatusUnknown},
		{"other operation", func(r *v2.AdminCommandResponse) { r.Command.Operation = "rollback" }, StatusUnknown},
		{"other target", func(r *v2.AdminCommandResponse) { r.Command.TargetID = "campaign-2" }, StatusUnknown},
		{"other payload", func(r *v2.AdminCommandResponse) { r.Command.PayloadHash = strings.Repeat("e", 64) }, StatusUnknown},
		{"other campaign", func(r *v2.AdminCommandResponse) {
			r.Original.CampaignID, r.Current.CampaignID = "campaign-2", "campaign-2"
		}, StatusUnknown},
		{"wrong original version", func(r *v2.AdminCommandResponse) {
			r.Original.PublicationVersion, r.Current.PublicationVersion = "999", "999"
		}, StatusUnknown},
		{"wrong candidate", func(r *v2.AdminCommandResponse) {
			r.Original.RevisionID = strptr("01900000-0000-4000-8000-0000000000c3")
		}, StatusUnknown},
		{"current older than original", func(r *v2.AdminCommandResponse) { r.Current.PublicationVersion = "4" }, StatusUnknown},
		{"current of another campaign", func(r *v2.AdminCommandResponse) { r.Current.CampaignID = "campaign-2" }, StatusUnknown},
		{"replay claimed on first send", func(r *v2.AdminCommandResponse) { r.Replayed = true }, StatusUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			intent := testIntent(t, "publish")
			record := validRecord(intent, false)
			c.mutate(&record)
			_, status, _, result := classifySubmit(intent, Reply{Status: 200, Body: encode(t, record)})
			if status != c.want || (result != nil) != (c.want == StatusCommitted) {
				t.Fatalf("status %s, result %t", status, result != nil)
			}
		})
	}
	intent := testIntent(t, "publish")
	plain, _ := json.Marshal(map[string]any{"schemaVersion": 1, "serverNow": "2026-10-09T00:00:00Z", "asOf": "2026-10-09T00:00:00Z", "original": validRecord(intent, false).Original, "current": validRecord(intent, false).Current, "replayed": false})
	if _, status, _, _ := classifySubmit(intent, Reply{Status: 200, Body: plain}); status != StatusUnknown {
		t.Fatal("a participant-shaped response without ledger identity must stay unknown")
	}
}

func TestBindingPerOperation(t *testing.T) {
	check := func(t *testing.T, intent *Intent, httpStatus int, record v2.AdminCommandResponse, want string) {
		t.Helper()
		if _, status, _, _ := classifySubmit(intent, Reply{Status: httpStatus, Body: encode(t, record)}); status != want {
			t.Fatalf("%s %d replayed=%t: got %s want %s", intent.Operation, httpStatus, record.Replayed, status, want)
		}
	}
	create := testIntent(t, "create")
	check(t, create, 201, validRecord(create, false), StatusCommitted)
	check(t, create, 200, validRecord(create, false), StatusUnknown)
	check(t, create, 201, validRecord(create, true), StatusUnknown)
	draft := validRecord(create, false)
	draft.Original.PublicationVersion, draft.Current.PublicationVersion = "1", "1"
	check(t, create, 201, draft, StatusUnknown)
	create.Attempts = []Attempt{{Kind: "send", Outcome: attemptUncertain}}
	check(t, create, 200, validRecord(create, true), StatusCommitted)

	revision := testIntent(t, "revision")
	check(t, revision, 200, validRecord(revision, false), StatusCommitted)
	advanced := validRecord(revision, false)
	advanced.Original.PublicationVersion = "5"
	check(t, revision, 200, advanced, StatusUnknown)

	pause := testIntent(t, "pause")
	check(t, pause, 200, validRecord(pause, false), StatusCommitted)
	other := validRecord(pause, false)
	other.Original.RevisionID = strptr(testCandidate)
	check(t, pause, 200, other, StatusUnknown)

	rollback := testIntent(t, "rollback")
	check(t, rollback, 200, validRecord(rollback, false), StatusCommitted)
}

// A legitimate replay can arrive after later commands moved the campaign on;
// it is this command's record, so it is committed and the advance is reported.
func TestReplayWithAdvancedCurrentIsThisCommand(t *testing.T) {
	intent := testIntent(t, "publish")
	intent.Attempts = []Attempt{{Kind: "send", Outcome: attemptUncertain}}
	record := validRecord(intent, true)
	record.Current.PublicationVersion = "9"
	record.Current.RevisionID = strptr(testActive)
	attempt, status, _, result := classifySubmit(intent, Reply{Status: 200, Body: encode(t, record)})
	if status != StatusCommitted || result == nil || !strings.Contains(attempt.Note, "advanced to version 9") {
		t.Fatalf("status %s note %q", status, attempt.Note)
	}
	if got, _ := validateCommandResponse(intent, 200, encode(t, validRecord(intent, false)), "reconcile"); got != nil {
		t.Fatal("a ledger read must be a replay")
	}
	if got, _ := validateCommandResponse(intent, 200, encode(t, record), "reconcile"); got == nil {
		t.Fatal("a ledger read of this command with an advanced current must verify")
	}
}

// Each row is a line of the transition table in outcome.go.
func TestTransitionTable(t *testing.T) {
	cases := []struct {
		name      string
		uncertain bool
		reply     Reply
		status    string
		retryable bool
	}{
		{"certain timeout", false, Reply{Err: errors.New("timeout")}, StatusUnknown, false},
		{"certain result unavailable", false, Reply{Status: 503, Body: errorBody("CAMPAIGN_RESULT_UNAVAILABLE")}, StatusUnknown, false},
		{"certain gateway page", false, Reply{Status: 502, Body: []byte("<html>bad gateway</html>")}, StatusUnknown, false},
		{"certain proxy 404 page", false, Reply{Status: 404, Body: []byte("not found")}, StatusUnknown, false},
		{"certain redirect", false, Reply{Status: 307}, StatusUnknown, false},
		{"certain expired session", false, Reply{Status: 401, Body: errorBody("UNAUTHORIZED")}, StatusRejected, true},
		{"certain revoked administrator", false, Reply{Status: 403, Body: errorBody("CAMPAIGN_ADMIN_REQUIRED")}, StatusRejected, true},
		{"certain version conflict", false, Reply{Status: 409, Body: errorBody("CAMPAIGN_VERSION_CONFLICT")}, StatusRejected, false},
		{"certain unsupported", false, Reply{Status: 422, Body: errorBody("CAMPAIGN_UNSUPPORTED")}, StatusRejected, false},
		{"certain expired key", false, Reply{Status: 410, Body: errorBody("CAMPAIGN_COMMAND_EXPIRED")}, StatusRejected, false},
		{"certain absent campaign", false, Reply{Status: 404, Body: errorBody("CAMPAIGN_NOT_FOUND")}, StatusRejected, false},
		{"uncertain then revoked", true, Reply{Status: 403, Body: errorBody("CAMPAIGN_ADMIN_REQUIRED")}, StatusUnknown, false},
		{"uncertain then expired session", true, Reply{Status: 401, Body: errorBody("UNAUTHORIZED")}, StatusUnknown, false},
		{"uncertain then not found", true, Reply{Status: 404, Body: errorBody("CAMPAIGN_NOT_FOUND")}, StatusUnknown, false},
		{"uncertain then outside window", true, Reply{Status: 410, Body: errorBody("CAMPAIGN_COMMAND_EXPIRED")}, StatusUnknown, false},
		{"uncertain then unsupported", true, Reply{Status: 422, Body: errorBody("CAMPAIGN_UNSUPPORTED")}, StatusUnknown, false},
		{"uncertain then key bound to another payload", true, Reply{Status: 409, Body: errorBody("IDEMPOTENCY_CONFLICT")}, StatusUnknown, false},
		{"uncertain then timeout", true, Reply{Err: errors.New("timeout")}, StatusUnknown, false},
		{"uncertain then version conflict", true, Reply{Status: 409, Body: errorBody("CAMPAIGN_VERSION_CONFLICT")}, StatusRejected, false},
		{"uncertain then archived", true, Reply{Status: 409, Body: errorBody("CAMPAIGN_ARCHIVED")}, StatusRejected, false},
		{"uncertain then terms locked", true, Reply{Status: 409, Body: errorBody("CAMPAIGN_TERMS_LOCKED")}, StatusRejected, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			intent := testIntent(t, "publish")
			intent.Uncertain = c.uncertain
			attempt, status, retryable, result := classifySubmit(intent, c.reply)
			if status != c.status || retryable != c.retryable || result != nil {
				t.Fatalf("got %s retryable=%t (attempt %s)", status, retryable, attempt.Outcome)
			}
			if c.uncertain && status == StatusUnknown && attempt.Outcome == attemptRefused && !strings.Contains(attempt.Note, "says nothing") {
				t.Fatal("a refusal after uncertainty must say it does not settle the earlier attempt")
			}
		})
	}
}

func TestCommandIDsAndVersionsInBodyAreAuthoritative(t *testing.T) {
	intent := testIntent(t, "publish")
	var body map[string]any
	_ = json.Unmarshal([]byte(intent.Body), &body)
	body["expectedPublicationVersion"] = "7"
	raw, _ := json.Marshal(body)
	intent.Body, intent.BodySHA256 = string(raw), digest(string(raw))
	record := validRecord(intent, false)
	record.Command.PayloadHash, _ = intent.ledgerPayloadHash()
	if _, status, _, _ := classifySubmit(intent, Reply{Status: 200, Body: encode(t, record)}); status != StatusUnknown {
		t.Fatal("the expected version comes from the frozen body, not the review snapshot")
	}
	record.Original.PublicationVersion, record.Current.PublicationVersion = strconv.Itoa(8), strconv.Itoa(8)
	if _, status, _, _ := classifySubmit(intent, Reply{Status: 200, Body: encode(t, record)}); status != StatusCommitted {
		t.Fatal("version 8 is what a body expecting 7 produces")
	}
}
