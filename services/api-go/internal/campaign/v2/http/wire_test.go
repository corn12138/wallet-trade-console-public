package campaignhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

const testWallet = "0x1111111111111111111111111111111111111111"

// testAuth is this package's credential fixture: a real-policy verifier with
// in-memory sessions. Tests that take the store down build their own.
var testAuth = authtest.MustNew()

type readProbe struct {
	Reader
	calls      int
	broken     bool
	commandErr error
}

func (p *readProbe) List(context.Context, application.ListQuery) (application.ListResponse, error) {
	p.calls++
	if p.broken {
		return application.ListResponse{}, nil
	}
	return application.ListResponse{SchemaVersion: 1, ServerNow: "2026-10-08T00:00:00Z", AsOf: "2026-10-08T00:00:00Z", Items: []application.PublicCampaignView{}}, nil
}
func (p *readProbe) Command(context.Context, string, string, string, string) (application.CommandResponse, error) {
	p.calls++
	return application.CommandResponse{}, p.commandErr
}

type commandProbe struct {
	calls   int
	actor   string
	command application.Command
}

func (p *commandProbe) Execute(_ context.Context, actor string, command application.Command) (application.Outcome, error) {
	p.calls++
	p.actor = actor
	p.command = command
	return application.Outcome{}, nil
}
func bearer(t *testing.T) string {
	t.Helper()
	value, _ := testAuth.Login(t, testWallet, 1)
	return "Bearer " + value
}
func response(t *testing.T, router http.Handler, method, path, body, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func code(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var value struct {
		Code          string `json:"code"`
		RequestID     string `json:"requestId"`
		SchemaVersion int    `json:"schemaVersion"`
	}
	if err := v2.DecodeContractResponse("ErrorResponse", w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.SchemaVersion != 1 || value.RequestID == "" || value.RequestID != w.Header().Get("X-Request-Id") {
		t.Fatalf("inconsistent error envelope: %s", w.Body.String())
	}
	return value.Code
}
func TestClosedInputCannotReachCommander(t *testing.T) {
	reader, writer := &readProbe{}, &commandProbe{}
	r := Router(Config{Commands: writer, Queries: reader, Verifier: testAuth.Verifier, Enabled: true})
	valid := `{"clientCommandId":"019aa123-4567-7000-8000-000000000001","expectedStateVersion":"0","expectedPublicationVersion":"1"}`
	for _, tc := range []struct {
		name, path, body string
		status           int
	}{
		{"owner", "/campaigns/c1/join", strings.TrimSuffix(valid, "}") + `,"wallet":"` + testWallet + `"}`, 400},
		{"duplicate", "/campaigns/c1/join", strings.TrimSuffix(valid, "}") + `,"expectedStateVersion":"1"}`, 400},
		{"number version", "/campaigns/c1/join", strings.Replace(valid, `"expectedStateVersion":"0"`, `"expectedStateVersion":0`, 1), 400},
		{"leading zero", "/campaigns/c1/join", strings.Replace(valid, `"expectedStateVersion":"0"`, `"expectedStateVersion":"00"`, 1), 400},
		{"query owner", "/campaigns/c1/join?owner=x", valid, 400},
		{"trailing JSON", "/campaigns/c1/join", valid + "{}", 400},
		{"too large", "/campaigns/c1/join", strings.Repeat(" ", 65537), 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := response(t, r, "POST", tc.path, tc.body, bearer(t))
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			_ = code(t, w)
		})
	}
	if reader.calls != 0 || writer.calls != 0 {
		t.Fatal("invalid input reached application")
	}
}
func TestStrictAuthAndDisabledRoutes(t *testing.T) {
	reader := &readProbe{}
	r := Router(Config{Queries: reader, Verifier: testAuth.Verifier, Enabled: false})
	request := httptest.NewRequest("GET", "/campaigns/me?ids=c1", nil)
	request.Header.Set("X-Wallet-Address", testWallet)
	request = request.WithContext(auth.WithAddress(request.Context(), testWallet))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if w.Code != 401 || code(t, w) != "UNAUTHORIZED" || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("identity bypass: %s", w.Body.String())
	}
	w = response(t, r, "GET", "/campaigns", "", "")
	if w.Code != 503 || code(t, w) != "CAMPAIGN_UNAVAILABLE" {
		t.Fatalf("disabled public route: %s", w.Body.String())
	}
	if reader.calls != 0 {
		t.Fatal("disabled route read data")
	}
}
func TestPublicQueryBoundsAndOutputValidation(t *testing.T) {
	reader := &readProbe{}
	r := Router(Config{Queries: reader, Enabled: true})
	for _, path := range []string{"/campaigns?owner=x", "/campaigns?limit=1&limit=2", "/campaigns?limit=51", "/campaigns?limit=01", "/campaigns?limit=", "/campaigns?bad=%zz"} {
		w := response(t, r, "GET", path, "", "")
		if w.Code != 400 {
			t.Fatalf("%s status=%d", path, w.Code)
		}
		_ = code(t, w)
	}
	if reader.calls != 0 {
		t.Fatal("invalid query reached reader")
	}
	w := response(t, r, "GET", "/campaigns", "", "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "owner") {
		t.Fatalf("public response: %s", w.Body.String())
	}
	reader.broken = true
	w = response(t, r, "GET", "/campaigns", "", "")
	if w.Code != 503 || code(t, w) != "CAMPAIGN_RESPONSE_INVALID" {
		t.Fatalf("invalid DTO escaped: %s", w.Body.String())
	}
}
func TestCommittedWriteReadFailurePreservesRecoveryIdentity(t *testing.T) {
	reader, writer := &readProbe{commandErr: errors.New("private database diagnostics")}, &commandProbe{}
	r := Router(Config{Commands: writer, Queries: reader, Verifier: testAuth.Verifier, Enabled: true})
	body := `{"clientCommandId":"019aa123-4567-7000-8000-000000000001","expectedStateVersion":"1"}`
	w := response(t, r, "DELETE", "/campaigns/c1/join", body, bearer(t))
	if w.Code != 503 || code(t, w) != "CAMPAIGN_RESULT_UNAVAILABLE" {
		t.Fatalf("recovery response: %s", w.Body.String())
	}
	if writer.calls != 1 || reader.calls != 1 || writer.actor != testWallet || writer.command.ClientCommandID != "019aa123-4567-7000-8000-000000000001" {
		t.Fatal("wrong command identity")
	}
	if writer.command.RequestID != w.Header().Get("X-Request-Id") || strings.Contains(w.Body.String(), "diagnostics") {
		t.Fatal("unstable request ID or error leak")
	}
}

// The private group answers a session that cannot be confirmed with 503 in
// the campaign envelope (the client keeps its credential and any command stays
// reconcilable) and a revoked session with 401; neither reaches the
// application.
func TestPrivateRoutesFollowThePersistentSession(t *testing.T) {
	f := authtest.New(t)
	reader, writer := &readProbe{}, &commandProbe{}
	r := Router(Config{Commands: writer, Queries: reader, Verifier: f.Verifier, Enabled: true})
	token, sid := f.Login(t, testWallet, 1)
	body := `{"clientCommandId":"019aa123-4567-7000-8000-000000000001","expectedStateVersion":"0","expectedPublicationVersion":"1"}`
	f.Sessions.SetDown(true)
	for _, path := range []string{"/campaigns/me?ids=c1", "/campaigns/c1/join"} {
		method := "GET"
		if strings.HasSuffix(path, "/join") {
			method = "POST"
		}
		w := response(t, r, method, path, body, "Bearer "+token)
		if w.Code != 503 || code(t, w) != "WEB3_SESSION_UNAVAILABLE" || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s store down: %d %s", path, w.Code, w.Body.String())
		}
	}
	f.Sessions.SetDown(false)
	f.Sessions.RevokeID(sid)
	w := response(t, r, "POST", "/campaigns/c1/join", body, "Bearer "+token)
	if w.Code != 401 || code(t, w) != "UNAUTHORIZED" {
		t.Fatalf("revoked: %d %s", w.Code, w.Body.String())
	}
	if reader.calls != 0 || writer.calls != 0 {
		t.Fatal("an unconfirmed session reached the application")
	}
	if err := (Authorizer{Verifier: f.Verifier}).Require(context.Background(), testWallet, false); err == nil {
		t.Fatal("authorization without the middleware credential must fail")
	}
}
