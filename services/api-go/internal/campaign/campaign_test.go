package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
)

const testWallet = "0x1111111111111111111111111111111111111111"

// campaignAuth is this package's credential fixture: one real-policy verifier
// and its in-memory sessions. Tests that take the store down build their own.
var campaignAuth = authtest.MustNew()

type stubCampaignReader struct {
	rows                        []Campaign
	readErr, countErr, stateErr error
	counts                      map[string]int
	states                      map[string]WalletState
	limit, calls                int
}

func (s *stubCampaignReader) ListAll(_ context.Context, _ string, limit ...int) ([]Campaign, error) {
	s.calls++
	s.limit = limit[0]
	return s.rows, s.readErr
}
func (s *stubCampaignReader) GetActive(ctx context.Context, _ time.Time, limit ...int) ([]Campaign, error) {
	return s.ListAll(ctx, "active", limit...)
}
func (s *stubCampaignReader) FindByID(context.Context, string) (Campaign, error) {
	s.calls++
	if s.readErr != nil {
		return Campaign{}, s.readErr
	}
	if len(s.rows) == 0 {
		return Campaign{}, ErrNotFound
	}
	return s.rows[0], nil
}
func (s *stubCampaignReader) ParticipantCounts(context.Context, []string) (map[string]int, error) {
	return s.counts, s.countErr
}
func (s *stubCampaignReader) WalletStates(context.Context, []string, string) (map[string]WalletState, error) {
	return s.states, s.stateErr
}

// campaignToken signs testWallet in against campaignAuth: a genuine token
// bound to a live session.
func campaignToken(t *testing.T) string {
	t.Helper()
	token, _ := campaignAuth.Login(t, testWallet, 11155111)
	return token
}

// strictResolver mirrors the production resolver: the request's own token,
// authenticated against its live session.
func strictResolver(v *auth.Verifier) WalletResolver {
	return func(r *http.Request) string {
		token, ok := auth.Bearer(r)
		if !ok {
			return ""
		}
		c, err := v.Authenticate(r.Context(), token)
		if err != nil {
			return ""
		}
		return c.Sub
	}
}

func TestRepositoryUnavailableNeverInventsData(t *testing.T) {
	for _, repo := range []*Repository{nil, NewRepository(nil)} {
		ctx := t.Context()
		checks := []func() error{
			func() error { _, err := repo.ListAll(ctx, ""); return err },
			func() error { _, err := repo.GetActive(ctx, time.Now()); return err },
			func() error { _, err := repo.FindByID(ctx, "c"); return err },
			func() error { _, err := repo.ParticipantCounts(ctx, nil); return err },
			func() error { _, err := repo.WalletStates(ctx, nil, testWallet); return err },
			func() error { _, err := repo.Create(ctx, CreateInput{}); return err },
			func() error { _, err := repo.Join(ctx, "c", testWallet); return err },
			func() error { return repo.Leave(ctx, "c", testWallet) },
			func() error { _, err := repo.SetReminder(ctx, "c", testWallet); return err },
			func() error { return repo.RemoveReminder(ctx, "c", testWallet) },
		}
		for _, check := range checks {
			if err := check(); !errors.Is(err, ErrPoolUnavailable) {
				t.Fatalf("unavailable repository returned %v", err)
			}
		}
	}
}

func TestLegacyMutationsRequireStrictIdentityAndUpgrade(t *testing.T) {
	v := campaignAuth.Verifier
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{"/", "/c", "/c/join", "/c/reminder", "/unknown/nested"} {
			t.Run(method+path, func(t *testing.T) {
				for _, authorized := range []bool{false, true} {
					req := httptest.NewRequest(method, path, strings.NewReader("invalid old body"))
					req.Header.Set("X-Wallet-Address", testWallet)
					if authorized {
						req.Header.Set("Authorization", "Bearer "+campaignToken(t))
					}
					out := httptest.NewRecorder()
					// A nil service demonstrates that no legacy write touches persistence.
					Router(nil, nil, auth.StrictMiddleware(v)).ServeHTTP(out, req)
					want := http.StatusUnauthorized
					if authorized {
						want = http.StatusConflict
					}
					if out.Code != want {
						t.Fatalf("authorized=%v status=%d body=%s", authorized, out.Code, out.Body.String())
					}
					if authorized && !strings.Contains(out.Body.String(), "CAMPAIGN_CLIENT_UPGRADE_REQUIRED") {
						t.Fatal(out.Body.String())
					}
					if out.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatal("mutation response cacheable")
					}
				}
			})
		}
	}
}

// A missing guard denies (401) and a guard without a verifier is an outage
// (503); neither trusts an address injected into the context.
func TestNilGuardCannotTrustInjectedAddress(t *testing.T) {
	for _, tc := range []struct {
		guard func(http.Handler) http.Handler
		want  int
	}{{nil, http.StatusUnauthorized}, {auth.StrictMiddleware(nil), http.StatusServiceUnavailable}} {
		req := httptest.NewRequest("POST", "/c/join", nil)
		req = req.WithContext(auth.WithAddress(req.Context(), testWallet))
		req.Header.Set("Authorization", "Bearer "+campaignToken(t))
		out := httptest.NewRecorder()
		Router(nil, nil, tc.guard).ServeHTTP(out, req)
		if out.Code != tc.want || strings.Contains(out.Body.String(), "CAMPAIGN_CLIENT_UPGRADE_REQUIRED") {
			t.Fatalf("nil guard/verifier status=%d body=%s", out.Code, out.Body.String())
		}
	}
}

func TestReadUnavailableIs503AndPublicResponsesNeverCache(t *testing.T) {
	for _, svc := range []*Service{nil, NewService(nil), NewService(NewRepository(nil))} {
		for _, path := range []string{"/", "/active", "/c"} {
			out := httptest.NewRecorder()
			Router(svc, nil, nil).ServeHTTP(out, httptest.NewRequest("GET", path, nil))
			if out.Code != http.StatusServiceUnavailable || out.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s status=%d cache=%q", path, out.Code, out.Header().Get("Cache-Control"))
			}
		}
	}
}

func TestReadDependencyFailureDoesNotBecomeZeroOrFalse(t *testing.T) {
	v := campaignAuth.Verifier
	for _, failure := range []string{"base", "count", "wallet"} {
		for _, path := range []string{"/", "/active", "/c"} {
			reader := &stubCampaignReader{rows: []Campaign{{ID: "c"}}}
			switch failure {
			case "base":
				reader.readErr = errors.New("db down")
			case "count":
				reader.countErr = errors.New("counts down")
			case "wallet":
				reader.stateErr = errors.New("states down")
			}
			req := httptest.NewRequest("GET", path, nil)
			req.Header.Set("Authorization", "Bearer "+campaignToken(t))
			out := httptest.NewRecorder()
			Router(&Service{repo: reader}, strictResolver(v), auth.StrictMiddleware(v)).ServeHTTP(out, req)
			if out.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s/%s status=%d", failure, path, out.Code)
			}
			if out.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("identity-specific response cacheable")
			}
		}
	}
}

func TestListBoundsAndKnownStatus(t *testing.T) {
	for _, path := range []string{"/?limit=0", "/?limit=-1", "/?limit=51", "/?limit=nope", "/?limit=", "/?limit=1&limit=2", "/?status=draft", "/?status=ACTIVE", "/?status=active&status=ended", "/active?limit=51"} {
		reader := &stubCampaignReader{}
		out := httptest.NewRecorder()
		Router(&Service{repo: reader}, nil, nil).ServeHTTP(out, httptest.NewRequest("GET", path, nil))
		if out.Code != http.StatusBadRequest || reader.calls != 0 {
			t.Fatalf("%s status=%d repository calls=%d", path, out.Code, reader.calls)
		}
	}
	for _, tc := range []struct {
		path  string
		limit int
	}{{"/", 20}, {"/active", 20}, {"/?limit=50&status=upcoming", 50}, {"/?limit=1&status=ended", 1}} {
		reader := &stubCampaignReader{}
		out := httptest.NewRecorder()
		Router(&Service{repo: reader}, nil, nil).ServeHTTP(out, httptest.NewRequest("GET", tc.path, nil))
		if out.Code != http.StatusOK || reader.limit != tc.limit || strings.TrimSpace(out.Body.String()) != "[]" {
			t.Fatalf("%s status=%d limit=%d body=%s", tc.path, out.Code, reader.limit, out.Body.String())
		}
	}
}

func TestInvalidCredentialsCannotBecomeAnonymousReads(t *testing.T) {
	v := campaignAuth.Verifier
	for _, headers := range [][]string{{"Bearer broken"}, {""}, {"Basic token"}, {"Bearer a", "Bearer b"}} {
		reader := &stubCampaignReader{}
		req := httptest.NewRequest("GET", "/", nil)
		for _, header := range headers {
			req.Header.Add("Authorization", header)
		}
		out := httptest.NewRecorder()
		Router(&Service{repo: reader}, strictResolver(v), nil).ServeHTTP(out, req)
		if out.Code != http.StatusUnauthorized || reader.calls != 0 {
			t.Fatalf("status=%d calls=%d", out.Code, reader.calls)
		}
	}
}

func TestAnonymousReadIgnoresInjectedOrHeaderWallet(t *testing.T) {
	reader := &stubCampaignReader{rows: []Campaign{{ID: "c"}}, counts: map[string]int{"c": 7}}
	req := httptest.NewRequest("GET", "/c", nil)
	req.Header.Set("X-Wallet-Address", testWallet)
	req = req.WithContext(auth.WithAddress(req.Context(), testWallet))
	out := httptest.NewRecorder()
	Router(&Service{repo: reader}, func(*http.Request) string { t.Fatal("resolver used without credential"); return testWallet }, nil).ServeHTTP(out, req)
	var got Campaign
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if out.Code != http.StatusOK || got.ParticipantCount != 7 || got.IsParticipating != nil || got.HasReminder != nil {
		t.Fatalf("anonymous result %+v", got)
	}
}

func TestServiceForBFFPropagatesCountFailure(t *testing.T) {
	failure := errors.New("count unavailable")
	svc := &Service{repo: &stubCampaignReader{rows: []Campaign{{ID: "c"}}, countErr: failure}}
	if _, err := svc.List(t.Context(), ""); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := svc.Active(t.Context(), time.Now()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := svc.FindByID(t.Context(), "c"); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestParseAdminWallets(t *testing.T) {
	got := ParseAdminWallets(" 0x00000000000000000000000000000000000000AD ,,not-an-address,0xGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGG,0x0000000000000000000000000000000000000000")
	if len(got) != 1 || !got["0x00000000000000000000000000000000000000ad"] {
		t.Fatal(got)
	}
}

// An authenticated legacy read is personalised only for a live session: a
// revoked session is 401 and an unconfirmable one 503, never an anonymous or
// stale private answer.
func TestLegacyReadsFollowThePersistentSession(t *testing.T) {
	f := authtest.New(t)
	token, sid := f.Login(t, testWallet, 11155111)
	read := func() (*httptest.ResponseRecorder, *stubCampaignReader) {
		reader := &stubCampaignReader{rows: []Campaign{{ID: "c"}}, counts: map[string]int{"c": 2}, states: map[string]WalletState{"c": {IsParticipating: true}}}
		req := httptest.NewRequest("GET", "/c", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		Router(&Service{repo: reader}, strictResolver(f.Verifier), auth.StrictMiddleware(f.Verifier)).ServeHTTP(out, req)
		return out, reader
	}
	out, _ := read()
	var got Campaign
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != http.StatusOK || got.IsParticipating == nil || !*got.IsParticipating {
		t.Fatalf("live session: %d %s", out.Code, out.Body.String())
	}
	f.Sessions.SetDown(true)
	if out, reader := read(); out.Code != http.StatusServiceUnavailable || reader.calls != 0 {
		t.Fatalf("store down: %d calls=%d", out.Code, reader.calls)
	}
	f.Sessions.SetDown(false)
	f.Sessions.RevokeID(sid)
	if out, reader := read(); out.Code != http.StatusUnauthorized || reader.calls != 0 {
		t.Fatalf("revoked: %d calls=%d", out.Code, reader.calls)
	}
}
