package web3events

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

type corpusAction struct {
	Name string `json:"name"`
	Row  struct {
		ID             string  `json:"id"`
		ChainID        int     `json:"chainId"`
		ClientActionID string  `json:"clientActionId"`
		ActionType     *string `json:"actionType"`
		Status         string  `json:"status"`
		AttemptCount   int     `json:"attemptCount"`
		CreatedAt      string  `json:"createdAt"`
		UpdatedAt      string  `json:"updatedAt"`
	} `json:"row"`
	Wire json.RawMessage `json:"wire"`
}

type corpusAttempt struct {
	Name string `json:"name"`
	Row  struct {
		ID              int64   `json:"id"`
		AttemptNumber   int     `json:"attemptNumber"`
		ChainID         int     `json:"chainId"`
		TxHash          string  `json:"txHash"`
		FromAddress     string  `json:"fromAddress"`
		ToAddress       *string `json:"toAddress"`
		ContractAddress *string `json:"contractAddress"`
		Value           *string `json:"value"`
		SenderNonce     *int64  `json:"senderNonce"`
		BlockNumber     int64   `json:"blockNumber"`
		BlockHash       *string `json:"blockHash"`
		Confirmations   int     `json:"confirmations"`
		GasUsed         *int64  `json:"gasUsed"`
		GasPrice        *int64  `json:"gasPrice"`
		Status          string  `json:"status"`
		ReplacedByHash  *string `json:"replacedByHash"`
		FailureCode     *string `json:"failureCode"`
		SubmittedAt     *string `json:"submittedAt"`
		ConfirmedAt     *string `json:"confirmedAt"`
		FinalizedAt     *string `json:"finalizedAt"`
		LastCheckedAt   *string `json:"lastCheckedAt"`
		CreatedAt       string  `json:"createdAt"`
	} `json:"row"`
	Wire json.RawMessage `json:"wire"`
}

func mustTime(t *testing.T, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("corpus time %q: %v", raw, err)
	}
	return parsed
}

func mustTimePtr(t *testing.T, raw *string) *time.Time {
	if raw == nil {
		return nil
	}
	parsed := mustTime(t, *raw)
	return &parsed
}

func sameJSON(t *testing.T, name string, got any, want json.RawMessage) {
	t.Helper()
	raw, _ := json.Marshal(got)
	var a, b any
	_ = json.Unmarshal(raw, &a)
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatalf("%s: corpus wire: %v", name, err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("%s:\n got  %s\n want %s", name, ja, jb)
	}
}

// The same corpus runs in the Web decoder (features/activity/model/workbench.spec.ts).
func TestWorkbenchCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/shared/contracts/fixtures/web3-action-workbench-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Actions  []corpusAction  `json:"actions"`
		Attempts []corpusAttempt `json:"attempts"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Actions) == 0 || len(corpus.Attempts) == 0 {
		t.Fatal("empty corpus")
	}
	for _, c := range corpus.Actions {
		r := c.Row
		sameJSON(t, c.Name, NewActionView(ActionRow{ID: r.ID, ChainID: r.ChainID, ClientActionID: r.ClientActionID, ActionType: r.ActionType,
			Status: r.Status, AttemptCount: r.AttemptCount, CreatedAt: mustTime(t, r.CreatedAt), UpdatedAt: mustTime(t, r.UpdatedAt)}), c.Wire)
	}
	for _, c := range corpus.Attempts {
		r := c.Row
		sameJSON(t, c.Name, NewAttemptView(AttemptRow{ID: r.ID, AttemptNumber: r.AttemptNumber, ChainID: r.ChainID, TxHash: r.TxHash,
			FromAddress: r.FromAddress, ToAddress: r.ToAddress, ContractAddress: r.ContractAddress, Value: r.Value, SenderNonce: r.SenderNonce,
			BlockNumber: r.BlockNumber, BlockHash: r.BlockHash, Confirmations: r.Confirmations, GasUsed: r.GasUsed, GasPrice: r.GasPrice,
			Status: r.Status, ReplacedByHash: r.ReplacedByHash, FailureCode: r.FailureCode, SubmittedAt: mustTimePtr(t, r.SubmittedAt),
			ConfirmedAt: mustTimePtr(t, r.ConfirmedAt), FinalizedAt: mustTimePtr(t, r.FinalizedAt), LastCheckedAt: mustTimePtr(t, r.LastCheckedAt),
			CreatedAt: mustTime(t, r.CreatedAt)}), c.Wire)
	}
}

func TestLegacyAxesNeverOverclaim(t *testing.T) {
	for _, status := range []string{StatusPending, StatusConfirmed, StatusFailed, StatusReplaced, StatusDropped, StatusReorged} {
		if _, ok := legacyAxes[status]; !ok {
			t.Errorf("legacy status %q has no explicit conversion", status)
		}
	}
	for status, axes := range legacyAxes {
		if axes.finality == "finalized" || axes.finality == "safe" || axes.canonicality == "canonical" {
			t.Errorf("%q claims %+v, which nothing in this slice observes", status, axes)
		}
	}
	if got := axesOf("confirmedish"); got != (statusAxes{"unknown", "unknown", "unknown"}) {
		t.Errorf("unrecognized status = %+v, want all unknown", got)
	}
	if workflowOf(StatusReplaced) != "needs_attention" || workflowOf("mystery") != "needs_attention" {
		t.Error("a dead or unknown action must need attention, not complete")
	}
}

const (
	wbOwnerA = "0x000000000000000000000000000000000000dead"
	wbOwnerB = "0x000000000000000000000000000000000000beef"
)

func sampleActionState() ActionCursorState {
	at := time.Date(2026, 10, 13, 8, 0, 0, 123e6, time.UTC)
	return ActionCursorState{After: ActionKey{at.Add(-time.Minute), "a1"}, HighWater: ActionKey{at, "a9"}, AsOf: at.Add(time.Second)}
}

func reencode(t *testing.T, raw string, mutate func(map[string]any)) string {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	mutate(m)
	out, _ := json.Marshal(m)
	return base64.RawURLEncoding.EncodeToString(out)
}

func TestActionCursorIsBoundToItsIssuer(t *testing.T) {
	state := sampleActionState()
	raw := EncodeActionCursor(wbOwnerA, 11155111, "pending", state)
	got, err := DecodeActionCursor(raw, wbOwnerA, 11155111, "pending")
	if err != nil || !got.After.At.Equal(state.After.At) || got.After.ID != "a1" || got.HighWater.ID != "a9" || !got.AsOf.Equal(state.AsOf) {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if strings.Contains(string(mustB64(t, raw)), strings.TrimPrefix(wbOwnerA, "0x")) {
		t.Error("the cursor must not carry the wallet")
	}
	refused := map[string]string{
		"other owner":         mustDecodeErr(DecodeActionCursor(raw, wbOwnerB, 11155111, "pending")),
		"other chain":         mustDecodeErr(DecodeActionCursor(raw, wbOwnerA, 31337, "pending")),
		"other filter":        mustDecodeErr(DecodeActionCursor(raw, wbOwnerA, 11155111, "")),
		"other version":       mustDecodeErr(DecodeActionCursor(reencode(t, raw, func(m map[string]any) { m["v"] = 2 }), wbOwnerA, 11155111, "pending")),
		"attempt kind":        mustDecodeErr(DecodeActionCursor(reencode(t, raw, func(m map[string]any) { m["k"] = "attempts" }), wbOwnerA, 11155111, "pending")),
		"unknown field":       mustDecodeErr(DecodeActionCursor(reencode(t, raw, func(m map[string]any) { m["owner"] = wbOwnerB }), wbOwnerA, 11155111, "pending")),
		"above high water":    mustDecodeErr(DecodeActionCursor(reencode(t, raw, func(m map[string]any) { m["at"] = "2026-10-13T09:00:00.000Z" }), wbOwnerA, 11155111, "pending")),
		"non-UTC time":        mustDecodeErr(DecodeActionCursor(reencode(t, raw, func(m map[string]any) { m["at"] = "2026-10-13T07:00:00+08:00" }), wbOwnerA, 11155111, "pending")),
		"malformed id":        mustDecodeErr(DecodeActionCursor(reencode(t, raw, func(m map[string]any) { m["id"] = "a1' OR 1=1" }), wbOwnerA, 11155111, "pending")),
		"padding":             mustDecodeErr(DecodeActionCursor(raw+"==", wbOwnerA, 11155111, "pending")),
		"oversize":            mustDecodeErr(DecodeActionCursor(strings.Repeat("A", maxWorkbenchCursor+1), wbOwnerA, 11155111, "pending")),
		"attempt cursor":      mustDecodeErr(DecodeActionCursor(EncodeAttemptCursor(wbOwnerA, 11155111, "a1", AttemptKey{1, 1}), wbOwnerA, 11155111, "pending")),
		"trailing JSON value": mustDecodeErr(DecodeActionCursor(base64.RawURLEncoding.EncodeToString(append(mustB64(t, raw), []byte(" {}")...)), wbOwnerA, 11155111, "pending")),
	}
	for name, outcome := range refused {
		if outcome != "refused" {
			t.Errorf("%s: cursor accepted", name)
		}
	}
}

func TestAttemptCursorIsBoundToItsAction(t *testing.T) {
	raw := EncodeAttemptCursor(wbOwnerA, 11155111, "a1", AttemptKey{Number: 2, ID: 40})
	if got, err := DecodeAttemptCursor(raw, wbOwnerA, 11155111, "a1"); err != nil || got != (AttemptKey{2, 40}) {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	for name, outcome := range map[string]string{
		"other action": mustDecodeErr(DecodeAttemptCursor(raw, wbOwnerA, 11155111, "a2")),
		"other owner":  mustDecodeErr(DecodeAttemptCursor(raw, wbOwnerB, 11155111, "a1")),
		"other chain":  mustDecodeErr(DecodeAttemptCursor(raw, wbOwnerA, 1, "a1")),
		"zero number":  mustDecodeErr(DecodeAttemptCursor(reencode(t, raw, func(m map[string]any) { m["n"] = 0 }), wbOwnerA, 11155111, "a1")),
	} {
		if outcome != "refused" {
			t.Errorf("%s: cursor accepted", name)
		}
	}
}

func mustB64(t *testing.T, raw string) []byte {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustDecodeErr[T any](_ T, err error) string {
	if errors.Is(err, ErrInvalidCursor) {
		return "refused"
	}
	return "accepted"
}

// stubWorkbench enforces owner and chain the way the SQL does, so a handler
// that passed the wrong identity would be caught by the 404 assertions.
type stubWorkbench struct {
	actions  map[string]ActionRow // by id
	owners   map[string]string    // id -> owner
	attempts map[string][]AttemptRow
	err      error
	lastList *ActionListQuery
	lastAtt  *AttemptListQuery
	page     ActionPage
}

var stubAsOf = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func (s *stubWorkbench) ListActions(_ context.Context, q ActionListQuery) (ActionPage, error) {
	s.lastList = &q
	if s.err != nil {
		return ActionPage{}, s.err
	}
	page := s.page
	page.AsOf = stubAsOf
	return page, nil
}

func (s *stubWorkbench) owned(owner string, chainID int, id string) (ActionRow, bool) {
	a, ok := s.actions[id]
	return a, ok && s.owners[id] == owner && a.ChainID == chainID
}

func (s *stubWorkbench) GetAction(_ context.Context, owner string, chainID int, id string) (ActionRow, bool, time.Time, error) {
	if s.err != nil {
		return ActionRow{}, false, time.Time{}, s.err
	}
	a, ok := s.owned(owner, chainID, id)
	return a, ok, stubAsOf, nil
}

func (s *stubWorkbench) ListAttempts(_ context.Context, q AttemptListQuery) (AttemptPage, bool, error) {
	s.lastAtt = &q
	if s.err != nil {
		return AttemptPage{}, false, s.err
	}
	if _, ok := s.owned(q.Owner, q.ChainID, q.ActionID); !ok {
		return AttemptPage{}, false, nil
	}
	rows := s.attempts[q.ActionID]
	return AttemptPage{Rows: rows, HasMore: true, AsOf: stubAsOf}, true, nil
}

type wbResponse struct {
	code   int
	header http.Header
	body   map[string]any
}

func workbenchHarness(t *testing.T, reader WorkbenchReader) func(path, token string) wbResponse {
	t.Helper()
	svc := NewService(stubReader{}).WithWrites(&stubWriter{}, auth.StrictMiddleware(writeAuth.Verifier), &stubTransactionVerifier{})
	if reader != nil {
		svc = svc.WithWorkbench(reader)
	}
	router := Router(svc)
	return func(path, token string) wbResponse {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return wbResponse{rec.Code, rec.Header(), body}
	}
}

func seededWorkbench() *stubWorkbench {
	created := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	actionA := ActionRow{ID: "act-a", ChainID: 11155111, ClientActionID: "srv:nonce:11155111:5", Status: StatusPending, AttemptCount: 2, CreatedAt: created, UpdatedAt: created}
	return &stubWorkbench{
		actions: map[string]ActionRow{"act-a": actionA},
		owners:  map[string]string{"act-a": wbOwnerA},
		attempts: map[string][]AttemptRow{"act-a": {
			{ID: 10, AttemptNumber: 1, ChainID: 11155111, TxHash: "0x" + strings.Repeat("1", 64), FromAddress: wbOwnerA, Status: StatusReplaced, CreatedAt: created},
		}},
		page: ActionPage{Rows: []ActionRow{actionA, {ID: "act-0", ChainID: 11155111, ClientActionID: "srv:nonce:11155111:4", Status: StatusConfirmed, AttemptCount: 1, CreatedAt: created.Add(-time.Hour), UpdatedAt: created}}, HasMore: true},
	}
}

func TestWorkbenchRoutesRequireTheSessionGuard(t *testing.T) {
	do := workbenchHarness(t, seededWorkbench())
	for _, path := range []string{"/actions?chainId=11155111", "/actions/act-a?chainId=11155111", "/actions/act-a/attempts?chainId=11155111"} {
		if got := do(path, ""); got.code != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", path, got.code)
		}
		if got := do(path, "not-a-jwt"); got.code != http.StatusUnauthorized {
			t.Errorf("%s with a malformed token = %d, want 401", path, got.code)
		}
	}
}

func TestWorkbenchRejectsAmbiguousQueries(t *testing.T) {
	do := workbenchHarness(t, seededWorkbench())
	tok := signWeb3(t, wbOwnerA)
	for path, code := range map[string]string{
		"/actions":                                    "INVALID_CHAIN_ID",
		"/actions?chainId=":                           "INVALID_CHAIN_ID",
		"/actions?chainId=0":                          "INVALID_CHAIN_ID",
		"/actions?chainId=-1":                         "INVALID_CHAIN_ID",
		"/actions?chainId=011155111":                  "INVALID_CHAIN_ID",
		"/actions?chainId=1e3":                        "INVALID_CHAIN_ID",
		"/actions?chainId=4294967296":                 "INVALID_CHAIN_ID",
		"/actions?chainId=1&chainId=11155111":         "INVALID_CHAIN_ID",
		"/actions?chainId=11155111&limit=0":           "INVALID_QUERY",
		"/actions?chainId=11155111&limit=101":         "INVALID_QUERY",
		"/actions?chainId=11155111&limit=ten":         "INVALID_QUERY",
		"/actions?chainId=11155111&status=done":       "INVALID_QUERY",
		"/actions?chainId=11155111&cursor=%%%":        "INVALID_QUERY",
		"/actions?chainId=11155111&cursor=a.b":        "INVALID_CURSOR",
		"/actions?chainId=11155111&cursor=a&cursor=b": "INVALID_CURSOR",
		"/actions/act-a?chainId=abc":                  "INVALID_CHAIN_ID",
		"/actions/act-a/attempts":                     "INVALID_CHAIN_ID",
	} {
		got := do(path, tok)
		if got.code != http.StatusBadRequest || got.body["code"] != code {
			t.Errorf("%s = %d %v, want 400 %s", path, got.code, got.body["code"], code)
		}
	}
}

func TestWorkbenchUsesOnlyTheVerifiedOwner(t *testing.T) {
	reader := seededWorkbench()
	do := workbenchHarness(t, reader)
	tokA, tokB := signWeb3(t, wbOwnerA), signWeb3(t, wbOwnerB)

	// Query parameters naming another wallet are not inputs at all.
	if got := do("/actions?chainId=11155111&ownerAddress="+wbOwnerB+"&address="+wbOwnerB, tokA); got.code != http.StatusOK || reader.lastList.Owner != wbOwnerA {
		t.Fatalf("list = %d owner %q, want 200 for the verified owner", got.code, reader.lastList.Owner)
	}
	notFound := func(path, token string) map[string]any {
		got := do(path, token)
		if got.code != http.StatusNotFound || got.body["code"] != "ACTION_NOT_FOUND" {
			t.Fatalf("%s = %d %v, want 404 ACTION_NOT_FOUND", path, got.code, got.body)
		}
		delete(got.body, "requestId")
		return got.body
	}
	absent := notFound("/actions/act-missing?chainId=11155111", tokA)
	for name, body := range map[string]map[string]any{
		"another owner's action":        notFound("/actions/act-a?chainId=11155111", tokB),
		"another owner's attempts":      notFound("/actions/act-a/attempts?chainId=11155111", tokB),
		"own action on another chain":   notFound("/actions/act-a?chainId=31337", tokA),
		"own attempts on another chain": notFound("/actions/act-a/attempts?chainId=31337", tokA),
		"malformed id":                  notFound("/actions/act%20a?chainId=11155111", tokA),
		"oversize id":                   notFound("/actions/"+strings.Repeat("a", 65)+"?chainId=11155111", tokA),
	} {
		a, _ := json.Marshal(absent)
		b, _ := json.Marshal(body)
		if string(a) != string(b) {
			t.Errorf("%s is distinguishable from an absent action: %s vs %s", name, b, a)
		}
	}
	if got := do("/actions/act-a?chainId=11155111", tokA); got.code != http.StatusOK {
		t.Errorf("own action = %d, want 200", got.code)
	}
}

func TestWorkbenchPagesCarryTheirBinding(t *testing.T) {
	reader := seededWorkbench()
	do := workbenchHarness(t, reader)
	tokA, tokB := signWeb3(t, wbOwnerA), signWeb3(t, wbOwnerB)

	first := do("/actions?chainId=11155111&status=pending&limit=2", tokA)
	if first.code != http.StatusOK || first.body["hasMore"] != true || first.body["hasUpdates"] != false {
		t.Fatalf("first page = %d %v", first.code, first.body)
	}
	next, _ := first.body["nextCursor"].(string)
	state, err := DecodeActionCursor(next, wbOwnerA, 11155111, "pending")
	if err != nil || state.HighWater.ID != "act-a" || state.After.ID != "act-0" || !state.AsOf.Equal(stubAsOf) {
		t.Fatalf("next cursor = %+v, %v", state, err)
	}
	// The continuation keeps the first page's high-water mark and asOf.
	if got := do("/actions?chainId=11155111&status=pending&limit=2&cursor="+next, tokA); got.code != http.StatusOK ||
		reader.lastList.Cursor == nil || reader.lastList.Cursor.HighWater.ID != "act-a" {
		t.Fatalf("continuation = %d %+v", got.code, reader.lastList.Cursor)
	}
	for name, path := range map[string]string{
		"another owner":  "/actions?chainId=11155111&status=pending&cursor=" + next,
		"another chain":  "/actions?chainId=31337&status=pending&cursor=" + next,
		"another filter": "/actions?chainId=11155111&cursor=" + next,
		"attempt route":  "/actions/act-a/attempts?chainId=11155111&cursor=" + next,
	} {
		token := tokA
		if name == "another owner" {
			token = tokB
		}
		if got := do(path, token); got.code != http.StatusBadRequest || got.body["code"] != "INVALID_CURSOR" {
			t.Errorf("%s: %d %v, want 400 INVALID_CURSOR", name, got.code, got.body["code"])
		}
	}

	attempts := do("/actions/act-a/attempts?chainId=11155111&limit=1", tokA)
	items, _ := attempts.body["items"].([]any)
	if attempts.code != http.StatusOK || len(items) != 1 {
		t.Fatalf("attempts = %d %v", attempts.code, attempts.body)
	}
	item := items[0].(map[string]any)
	if item["legacyStatus"] != "replaced" || item["executionStatus"] != "replaced" || item["finality"] != "unknown" || item["indexingStatus"] != nil {
		t.Errorf("attempt axes = %v", item)
	}
	if ctx := item["executionContext"].(map[string]any); ctx["state"] != "notRecorded" || ctx["registryRevision"] != nil {
		t.Errorf("execution context = %v, want notRecorded", ctx)
	}
	attemptCursor, _ := attempts.body["nextCursor"].(string)
	reader.actions["act-b"] = ActionRow{ID: "act-b", ChainID: 11155111, Status: StatusPending}
	reader.owners["act-b"] = wbOwnerA
	if got := do("/actions/act-b/attempts?chainId=11155111&cursor="+attemptCursor, tokA); got.code != http.StatusBadRequest || got.body["code"] != "INVALID_CURSOR" {
		t.Errorf("an attempt cursor reused for another action = %d %v", got.code, got.body["code"])
	}
}

func TestWorkbenchUnavailableIsNeverAnEmptyHistory(t *testing.T) {
	tok := signWeb3(t, wbOwnerA)
	for name, reader := range map[string]WorkbenchReader{"unwired": nil, "failing": &stubWorkbench{err: errors.New("pool exhausted: secret detail")}} {
		do := workbenchHarness(t, reader)
		for _, path := range []string{"/actions?chainId=11155111", "/actions/act-a?chainId=11155111", "/actions/act-a/attempts?chainId=11155111"} {
			got := do(path, tok)
			if got.code != http.StatusServiceUnavailable || got.body["code"] != "DATA_SOURCE_UNAVAILABLE" || got.body["retryable"] != true {
				t.Errorf("%s %s = %d %v, want 503 DATA_SOURCE_UNAVAILABLE", name, path, got.code, got.body)
			}
			if _, leaked := got.body["items"]; leaked || strings.Contains(got.body["message"].(string), "secret") {
				t.Errorf("%s %s leaked a body or detail: %v", name, path, got.body)
			}
		}
	}
}

func TestWorkbenchResponsesArePrivate(t *testing.T) {
	do := workbenchHarness(t, seededWorkbench())
	tok := signWeb3(t, wbOwnerA)
	for _, path := range []string{"/actions?chainId=11155111", "/actions/act-a?chainId=11155111", "/actions/act-missing?chainId=11155111", "/actions?chainId=0"} {
		got := do(path, tok)
		if got.header.Get("Cache-Control") != "private, no-store" || !strings.Contains(strings.Join(got.header.Values("Vary"), ","), "Authorization") {
			t.Errorf("%s: Cache-Control %q Vary %q", path, got.header.Get("Cache-Control"), got.header.Values("Vary"))
		}
	}
}
