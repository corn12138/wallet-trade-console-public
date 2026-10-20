package campaignhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

type feedReader struct {
	calls                     int
	owner, family, cursor, id string
	private                   bool
	limit                     int
}

func (f *feedReader) Read(ctx context.Context, id, family, cursor string, limit int, private bool) (v2.EventFeedResponse, error) {
	f.calls++
	f.owner = auth.AddressFromContext(ctx)
	f.family = family
	f.cursor = cursor
	f.id = id
	f.private = private
	f.limit = limit
	return v2.EventFeedResponse{SchemaVersion: 1, ServerNow: "2026-10-15T07:00:00Z", AsOf: "2026-10-15T07:00:00Z", CampaignID: id, Family: family, HeadVersion: "0", Cursor: "opaque", ResetRequired: true, Events: []v2.EventFeedHint{}}, nil
}
func feedRequest(router http.Handler, path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func TestEventFeedRoutesUseClosedInputsAndPrivateCredentials(t *testing.T) {
	f := authtest.New(t)
	token, sid := f.Login(t, "0x1111111111111111111111111111111111111111", 11155111)
	reader := &feedReader{}
	router := Router(Config{Queries: &variantReader{}, Events: reader, Verifier: f.Verifier, Enabled: true})
	w := feedRequest(router, "/campaigns/one/events?family=publication&cursor=opaque&limit=7", "")
	if w.Code != 200 || reader.private || reader.family != "publication" || reader.owner != "" || reader.limit != 7 || reader.cursor != "opaque" {
		t.Fatalf("public mapping: %d %+v", w.Code, reader)
	}
	for _, path := range []string{"/campaigns/one/events?family=publication&owner=other", "/campaigns/one/events?family=publication&family=interaction", "/campaigns/one/events?family=publication&limit=051", "/campaigns/one/me/events?family=personal", "/campaigns/one/me/events?owner=other"} {
		before := reader.calls
		w = feedRequest(router, path, token)
		if w.Code != 400 || reader.calls != before {
			t.Fatalf("closed query %s: %d", path, w.Code)
		}
	}
	before := reader.calls
	w = feedRequest(router, "/campaigns/one/me/events", "")
	if w.Code != 401 || reader.calls != before {
		t.Fatal("anonymous private feed reached reader")
	}
	w = feedRequest(router, "/campaigns/one/me/events?limit=3", token)
	if w.Code != 200 || !reader.private || reader.family != "personal" || reader.owner == "" || reader.limit != 3 || !strings.Contains(w.Header().Get("Cache-Control"), "private") {
		t.Fatalf("private mapping: %d %+v", w.Code, reader)
	}
	f.Sessions.RevokeID(sid)
	before = reader.calls
	w = feedRequest(router, "/campaigns/one/me/events", token)
	if w.Code != 401 || reader.calls != before {
		t.Fatal("revoked session reached reader")
	}
	f.Sessions.SetDown(true)
	w = feedRequest(router, "/campaigns/one/me/events", token)
	if w.Code != 503 || reader.calls != before {
		t.Fatal("unknown session reached reader")
	}
}
func TestEventFeedUnavailableDoesNotInventEmptySuccess(t *testing.T) {
	router := Router(Config{Queries: &variantReader{}, Enabled: true})
	w := feedRequest(router, "/campaigns/one/events?family=publication", "")
	if w.Code != 503 {
		t.Fatalf("missing reader: %d", w.Code)
	}
	router = Router(Config{Queries: &variantReader{}, Events: &feedReader{}, Enabled: false})
	w = feedRequest(router, "/campaigns/one/events?family=publication", "")
	if w.Code != 503 {
		t.Fatalf("disabled: %d", w.Code)
	}
}
