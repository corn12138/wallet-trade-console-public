package application

import (
	"context"
	"strings"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

type fakeReader struct {
	calls   []string
	list    ListQuery
	history HistoryQuery
	key     CommandKey
	actor   string
	ids     []string
	err     error
}

func (r *fakeReader) List(_ context.Context, q ListQuery, _ v2.Capabilities) (ListResponse, error) {
	r.calls = append(r.calls, "list")
	r.list = q
	return ListResponse{SchemaVersion: 1}, r.err
}
func (r *fakeReader) Detail(context.Context, string, v2.Variants, v2.Capabilities) (DetailResponse, error) {
	r.calls = append(r.calls, "detail")
	return DetailResponse{}, r.err
}
func (r *fakeReader) Me(_ context.Context, a string, ids []string) (MeResponse, error) {
	r.calls = append(r.calls, "me")
	r.actor = a
	r.ids = ids
	return MeResponse{SchemaVersion: 1, Items: []MyCampaignView{{ID: "private"}}}, r.err
}
func (r *fakeReader) History(_ context.Context, a string, q HistoryQuery) (HistoryResponse, error) {
	r.calls = append(r.calls, "history")
	r.actor = a
	r.history = q
	return HistoryResponse{SchemaVersion: 1}, r.err
}
func (r *fakeReader) CommandResult(_ context.Context, key CommandKey) (CommandResponse, error) {
	r.calls = append(r.calls, "command")
	r.key = key
	return CommandResponse{SchemaVersion: 1}, r.err
}
func (r *fakeReader) Preview(context.Context, string, string, v2.Capabilities) (PreviewResponse, error) {
	r.calls = append(r.calls, "preview")
	return PreviewResponse{SchemaVersion: 1}, r.err
}
func (r *fakeReader) AdminCampaign(context.Context, string, v2.Capabilities) (AdminCampaignResponse, error) {
	r.calls = append(r.calls, "admin")
	return AdminCampaignResponse{SchemaVersion: 1}, r.err
}
func (r *fakeReader) Baselines(_ context.Context, refs []RevisionRef) (map[RevisionRef]v2.LegacyBaseline, error) {
	r.calls = append(r.calls, "baselines")
	return map[RevisionRef]v2.LegacyBaseline{}, r.err
}
func queryFixture() (*fakeReader, *testAuthorizer, *QueryService) {
	r := &fakeReader{}
	calls := []string{}
	a := &testAuthorizer{calls: &calls}
	return r, a, NewQueryService(r, a, func() v2.Capabilities { return testCapabilities })
}
func TestQueryAuthorizationRevokedDuringReadDiscardsResponse(t *testing.T) {
	for _, name := range []string{"me", "history", "command", "preview", "admin"} {
		t.Run(name, func(t *testing.T) {
			r, a, s := queryFixture()
			a.denyAt = 2
			var err error
			switch name {
			case "me":
				var value MeResponse
				value, err = s.Me(context.Background(), actorA, []string{"campaign-1"})
				if len(value.Items) != 0 {
					t.Fatal("private result escaped revocation")
				}
			case "history":
				var value HistoryResponse
				value, err = s.History(context.Background(), actorA, HistoryQuery{})
				if value.SchemaVersion != 0 {
					t.Fatal("history escaped revocation")
				}
			case "command":
				var value CommandResponse
				value, err = s.Command(context.Background(), actorA, "campaign-1", "join", commandID(testNow))
				if value.SchemaVersion != 0 {
					t.Fatal("ledger escaped revocation")
				}
			case "preview":
				var value PreviewResponse
				value, err = s.Preview(context.Background(), actorA, "campaign-1", candidateID)
				if value.SchemaVersion != 0 {
					t.Fatal("draft escaped revocation")
				}
			case "admin":
				var value AdminCampaignResponse
				value, err = s.AdminCampaign(context.Background(), actorA, "campaign-1")
				if value.SchemaVersion != 0 {
					t.Fatal("administrator view escaped revocation")
				}
			}
			assertCode(t, err, "FORBIDDEN")
			if len(r.calls) != 1 || a.count != 2 {
				t.Fatal("authorization must surround one read")
			}
		})
	}
}

// The administrator view lists drafts and lock facts, so it must request the
// campaign-admin role (not just a signed-in wallet) both before and after reading.
func TestAdminCampaignViewRequiresAdministrator(t *testing.T) {
	r, a, s := queryFixture()
	if _, err := s.AdminCampaign(context.Background(), actorA, "campaign-1"); err != nil {
		t.Fatal(err)
	}
	if len(a.adminFlags) != 2 || !a.adminFlags[0] || !a.adminFlags[1] || len(r.calls) != 1 {
		t.Fatal("administrator view must be admin-authorized around one read")
	}
	r, _, s = queryFixture()
	_, err := s.AdminCampaign(context.Background(), actorA, "../campaign")
	assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
	if len(r.calls) != 0 {
		t.Fatal("invalid identity reached the store")
	}
}
func TestQueryAdminAndCollectionScope(t *testing.T) {
	for _, op := range []string{"create", "revision", "publish", "rollback", "pause", "resume", "archive", "join", "leave", "set-reminder", "remove-reminder"} {
		t.Run(op, func(t *testing.T) {
			r, a, s := queryFixture()
			target := "campaign-1"
			if op == "create" {
				target = "campaigns"
			}
			value, err := s.Command(context.Background(), actorA, target, op, commandID(testNow.AddDate(-4, 0, 0)))
			if err != nil {
				t.Fatal(err)
			}
			expectedAdmin := interactionKind(op) == ""
			if len(a.adminFlags) != 2 || a.adminFlags[0] != expectedAdmin || a.adminFlags[1] != expectedAdmin || !value.Replayed || r.key.TargetID != target {
				t.Fatal("command query authorization/scope invalid")
			}
		})
	}
	r, _, s := queryFixture()
	_, err := s.Command(context.Background(), actorA, "campaign-1", "create", commandID(testNow))
	assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
	if len(r.calls) != 0 {
		t.Fatal("noncollection create reached ledger")
	}
}
func TestQueryValidationBeforeReader(t *testing.T) {
	r, _, s := queryFixture()
	ctx := context.Background()
	for _, q := range []ListQuery{{Limit: 51}, {Limit: -1}, {Phase: "paused"}, {Cursor: "bad="}} {
		_, err := s.List(ctx, q)
		assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
	}
	_, err := s.Me(ctx, actorA, nil)
	assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
	_, err = s.Me(ctx, actorA, []string{"bad/id"})
	assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
	_, err = s.Preview(ctx, actorA, "campaign-1", "not-a-uuid")
	assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
	_, err = s.Command(ctx, actorA, "campaign-1", "join", "not-a-uuid")
	assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
	if len(r.calls) != 0 {
		t.Fatal("invalid query reached reader")
	}
	if _, err = s.List(ctx, ListQuery{}); err != nil || r.list.Limit != 20 {
		t.Fatal("list default limit")
	}
	if _, err = s.History(ctx, actorA, HistoryQuery{}); err != nil || r.history.Limit != 20 {
		t.Fatal("history default limit")
	}
	if _, err = s.Me(ctx, strings.ToUpper(actorA), []string{"campaign-1", "campaign-1", "campaign-2"}); err != nil || r.actor != actorA || len(r.ids) != 2 {
		t.Fatal("normalized principal/deduplicated bounded IDs")
	}
}
func TestQueryFailsClosedWithoutAuthOrDependency(t *testing.T) {
	r := &fakeReader{}
	s := NewQueryService(r, nil, nil)
	_, err := s.Me(context.Background(), actorA, []string{"campaign-1"})
	assertCode(t, err, "CAMPAIGN_DATA_UNAVAILABLE")
	_, err = s.List(context.Background(), ListQuery{})
	assertCode(t, err, "CAMPAIGN_DATA_UNAVAILABLE")
	_, err = s.Me(context.Background(), "body-wallet", []string{"campaign-1"})
	assertCode(t, err, "UNAUTHORIZED")
	if len(r.calls) != 0 {
		t.Fatal("unauthorized query reached storage")
	}
}
func TestQueryStoreFailureCannotBecomeAbsent(t *testing.T) {
	r, _, s := queryFixture()
	r.err = unavailable("database down")
	value, err := s.Me(context.Background(), actorA, []string{"campaign-1"})
	assertCode(t, err, "CAMPAIGN_DATA_UNAVAILABLE")
	if len(value.Items) != 0 {
		t.Fatal("store failure returned private state")
	}
}
func TestQueryCursorBindsOwnerFilterAndOrdering(t *testing.T) {
	list := QueryCursor{Kind: "list", ID: "campaign-1", Phase: "active", SortOrder: 40}
	encoded := EncodeQueryCursor(list)
	if got, err := DecodeQueryCursor(encoded, "list", "", "active"); err != nil || got != list {
		t.Fatal("list round trip")
	}
	if _, err := DecodeQueryCursor(encoded, "list", "", "ended"); err == nil {
		t.Fatal("cursor filter changed")
	}
	history := QueryCursor{Kind: "history", Scope: QueryScope(actorA), ID: "campaign-1", At: testNow.Format(time.RFC3339Nano), StateKind: "join", RowVersion: 4}
	encoded = EncodeQueryCursor(history)
	if strings.Contains(encoded, actorA) {
		t.Fatal("cursor exposed wallet")
	}
	if _, err := DecodeQueryCursor(encoded, "history", QueryScope(actorA), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeQueryCursor(encoded, "history", QueryScope("other"), ""); err == nil {
		t.Fatal("cross-owner cursor accepted")
	}
	history.RowVersion = 0
	if _, err := DecodeQueryCursor(EncodeQueryCursor(history), "history", QueryScope(actorA), ""); err == nil {
		t.Fatal("history version zero accepted")
	}
}
