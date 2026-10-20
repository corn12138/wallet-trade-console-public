package eventfeed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
)

const walletA = "0x1111111111111111111111111111111111111111"
const walletB = "0x2222222222222222222222222222222222222222"

var observed = time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)

type resolveFunc func(context.Context, string, string, string) (string, error)

func (f resolveFunc) resolve(ctx context.Context, id, family, owner string) (string, error) {
	return f(ctx, id, family, owner)
}

type readFunc func(context.Context, string, *int64, int) (durableoutbox.StreamPage, error)

func (f readFunc) ReadStream(ctx context.Context, stream string, after *int64, limit int) (durableoutbox.StreamPage, error) {
	return f(ctx, stream, after, limit)
}
func fixtureEvent(owner, family string, version int64) durableoutbox.Event {
	e := durableoutbox.Event{ID: "0199c182-9d88-7000-8000-000000000003", Source: "campaign", Stream: "stream", Version: version, Topic: "campaign." + family + ".changed", PayloadVersion: 1, OccurredAt: observed}
	if owner != "" {
		e.OwnerScope = &owner
	}
	e.Payload, _ = json.Marshal(map[string]any{"eventId": e.ID, "source": "campaign", "campaignId": "one", "aggregateId": e.Stream, "aggregateVersion": json.Number("1"), "publicationVersion": "1", "interactionVersion": "9", "eventSchema": 1, "occurredAt": observed.Format(time.RFC3339Nano), "state": map[string]any{"wallet": walletA}, "secretDraft": "never expose"})
	var body map[string]any
	_ = json.Unmarshal(e.Payload, &body)
	body["aggregateVersion"] = "1"
	e.Payload, _ = json.Marshal(body)
	return e
}
func withToken(t *testing.T, v *auth.Verifier, token string, run func(context.Context)) {
	t.Helper()
	called := false
	handler := auth.StrictMiddleware(v)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { called = true; run(r.Context()) }))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if !called {
		t.Fatalf("fixture credential refused: %d", w.Code)
	}
}
func expectError(t *testing.T, err error, code string) {
	t.Helper()
	var domain *v2.DomainError
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
func testService(page durableoutbox.StreamPage) *Service {
	return &Service{resolver: resolveFunc(func(context.Context, string, string, string) (string, error) { return "stream", nil }), reader: readFunc(func(_ context.Context, _ string, after *int64, _ int) (durableoutbox.StreamPage, error) {
		if after == nil {
			page.Events = nil
		}
		return page, nil
	})}
}
func TestAnchorAndContiguousMetadataNeverExposePayload(t *testing.T) {
	page := durableoutbox.StreamPage{Head: 1, ObservedAt: observed, Events: []durableoutbox.Event{fixtureEvent("", "publication", 1)}}
	s := testService(page)
	anchor, err := s.Read(context.Background(), "one", "publication", "", 20, false)
	if err != nil || !anchor.ResetRequired || anchor.HeadVersion != "1" || len(anchor.Events) != 0 || anchor.HasMore {
		t.Fatalf("anchor: %+v %v", anchor, err)
	}
	s = testService(page)
	response, err := s.Read(context.Background(), "one", "publication", encodeCursor(feedScope("one", "publication", "public"), 0), 20, false)
	if err != nil || response.ResetRequired || len(response.Events) != 1 || response.Events[0].InteractionVersion != "9" || response.HasMore {
		t.Fatalf("page: %+v %v", response, err)
	}
	raw, _ := json.Marshal(response)
	for _, private := range []string{walletA, "secretDraft", "aggregateId", "state", "payload"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("payload leaked %s", private)
		}
	}
	if err = v2.DecodeContractResponse("EventFeedResponse", raw, new(v2.EventFeedResponse)); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidEventRequiresRepairWithoutAdvancingBusinessConsumption(t *testing.T) {
	cases := map[string]func(*durableoutbox.Event){
		"gap": func(e *durableoutbox.Event) { e.Version = 2 }, "source": func(e *durableoutbox.Event) { e.Source = "chain" }, "schema": func(e *durableoutbox.Event) { e.PayloadVersion = 2 },
		"owner": func(e *durableoutbox.Event) { owner := walletA; e.OwnerScope = &owner }, "stream": func(e *durableoutbox.Event) { e.Stream = "other" }, "topic": func(e *durableoutbox.Event) { e.Topic = "campaign.personal.changed" },
		"json": func(e *durableoutbox.Event) { e.Payload = []byte("null") }, "metadata": func(e *durableoutbox.Event) {
			e.Payload = []byte(strings.Replace(string(e.Payload), `"campaignId":"one"`, `"campaignId":"two"`, 1))
		},
		"time": func(e *durableoutbox.Event) { e.OccurredAt = e.OccurredAt.Add(time.Second) }, "oversized": func(e *durableoutbox.Event) { e.Payload = make([]byte, 65537) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := fixtureEvent("", "publication", 1)
			mutate(&e)
			s := testService(durableoutbox.StreamPage{Head: 2, ObservedAt: observed, Events: []durableoutbox.Event{e}})
			out, err := s.Read(context.Background(), "one", "publication", encodeCursor(feedScope("one", "publication", "public"), 0), 20, false)
			if err != nil || !out.ResetRequired || len(out.Events) != 0 || out.HasMore || out.HeadVersion != "2" {
				t.Fatalf("reset: %+v %v", out, err)
			}
		})
	}
}
func TestPrivateSessionRevocationAndOutageDuringRead(t *testing.T) {
	for _, outage := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "unknown"}[outage], func(t *testing.T) {
			f := authtest.New(t)
			token, sid := f.Login(t, walletA, 11155111)
			s := testService(durableoutbox.StreamPage{})
			s.verifier = f.Verifier
			s.reader = readFunc(func(context.Context, string, *int64, int) (durableoutbox.StreamPage, error) {
				if outage {
					f.Sessions.SetDown(true)
				} else {
					f.Sessions.RevokeID(sid)
				}
				return durableoutbox.StreamPage{Head: 1, ObservedAt: observed}, nil
			})
			withToken(t, f.Verifier, token, func(ctx context.Context) {
				out, err := s.Read(ctx, "one", "personal", "", 20, true)
				code := "UNAUTHORIZED"
				if outage {
					code = "WEB3_SESSION_UNAVAILABLE"
				}
				expectError(t, err, code)
				if out.Cursor != "" || len(out.Events) != 0 {
					t.Fatal("private response survived revoked/unknown session")
				}
			})
		})
	}
}
func TestPrivateCursorCannotCrossWalletSessionFamilyOrCampaign(t *testing.T) {
	f := authtest.New(t)
	a, _ := f.Login(t, walletA, 11155111)
	sameOwner, _ := f.Login(t, walletA, 11155111)
	b, _ := f.Login(t, walletB, 11155111)
	s := testService(durableoutbox.StreamPage{Head: 0, ObservedAt: observed})
	s.verifier = f.Verifier
	cursor := ""
	withToken(t, f.Verifier, a, func(ctx context.Context) {
		out, err := s.Read(ctx, "one", "personal", "", 20, true)
		if err != nil {
			t.Fatal(err)
		}
		cursor = out.Cursor
	})
	for _, token := range []string{sameOwner, b} {
		withToken(t, f.Verifier, token, func(ctx context.Context) {
			_, err := s.Read(ctx, "one", "personal", cursor, 20, true)
			expectError(t, err, "CAMPAIGN_INVALID_INPUT")
		})
	}
	withToken(t, f.Verifier, a, func(ctx context.Context) {
		_, err := s.Read(ctx, "two", "personal", cursor, 20, true)
		expectError(t, err, "CAMPAIGN_INVALID_INPUT")
	})
	_, err := s.Read(context.Background(), "one", "publication", cursor, 20, false)
	expectError(t, err, "CAMPAIGN_INVALID_INPUT")
	if _, err := s.Read(auth.WithAddress(context.Background(), walletA), "one", "personal", "", 20, true); err == nil {
		t.Fatal("WithAddress accepted as authentication")
	}
}
func TestCursorRequiresCanonicalEncoding(t *testing.T) {
	scope := feedScope("one", "publication", "public")
	good := encodeCursor(scope, 3)
	raw, _ := base64.RawURLEncoding.DecodeString(good)
	for _, value := range []string{string(raw) + " ", strings.Replace(string(raw), `"schema":1`, `"schema":1,"schema":1`, 1), strings.Replace(string(raw), `"version":"3"`, `"version":"03"`, 1)} {
		if _, err := decodeCursor(base64.RawURLEncoding.EncodeToString([]byte(value)), scope); err == nil {
			t.Fatal("noncanonical cursor accepted")
		}
	}
}
