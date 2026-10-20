package adminclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const logoutToken = "logout-test-token"

// logoutServer answers POST /api/web3-auth/logout with a scripted reply and
// records what reached it.
type logoutServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	status   int
	body     string
	hang     bool
}

func newLogoutServer(t *testing.T, status int, body string) *logoutServer {
	t.Helper()
	s := &logoutServer{status: status, body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Clone(context.Background()))
		hang := s.hang
		s.mu.Unlock()
		if hang {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.Close)
	return s
}

func storeLogoutCredential(t *testing.T, home Home, api string) {
	t.Helper()
	credential := Credential{API: api, Address: "0x1111111111111111111111111111111111111111", ChainID: 11155111, Token: logoutToken,
		SessionExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if err := home.SaveCredential(credential); err != nil {
		t.Fatal(err)
	}
}

func TestLogoutReportsOnlyWhatTheServerConfirmed(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   LogoutResult
	}{
		{"revoked", 200, `{"revoked":true,"alreadyRevoked":false}`, LogoutRevoked},
		{"already revoked", 200, `{"revoked":true,"alreadyRevoked":true}`, LogoutAlreadyRevoked},
		{"token no longer accepted", 401, `{"statusCode":401}`, LogoutRejected},
		{"session store down", 503, `{"statusCode":503}`, LogoutUnconfirmed},
		{"gateway page", 502, `<html>bad gateway</html>`, LogoutUnconfirmed},
		{"unexpected body", 200, `{"ok":true}`, LogoutUnconfirmed},
		{"not revoked", 200, `{"revoked":false,"alreadyRevoked":false}`, LogoutUnconfirmed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newLogoutServer(t, tc.status, tc.body)
			home := testHome(t)
			storeLogoutCredential(t, home, server.URL+"/api")
			outcome, err := Logout(context.Background(), home, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Result != tc.want {
				t.Fatalf("result %q, want %q (detail %q)", outcome.Result, tc.want, outcome.Detail)
			}
			if strings.Contains(outcome.Detail, "html") || strings.Contains(outcome.Detail, logoutToken) {
				t.Fatalf("detail echoes the response or token: %q", outcome.Detail)
			}
			if _, err := home.LoadCredential(time.Now()); !errors.Is(err, ErrCredentialMissing) {
				t.Fatalf("local credential must be gone whatever the server said: %v", err)
			}
			if len(server.requests) != 1 {
				t.Fatalf("requests %d", len(server.requests))
			}
			request := server.requests[0]
			if request.Method != http.MethodPost || request.URL.Path != "/api/web3-auth/logout" || request.Header.Get("Authorization") != "Bearer "+logoutToken {
				t.Fatalf("request %s %s bearer present=%t", request.Method, request.URL.Path, request.Header.Get("Authorization") != "")
			}
		})
	}
}

func TestLogoutWithoutAnAnswerIsUnconfirmedButLocal(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		server := newLogoutServer(t, 200, "")
		server.hang = true
		home := testHome(t)
		storeLogoutCredential(t, home, server.URL+"/api")
		outcome, err := Logout(context.Background(), home, 200*time.Millisecond)
		if err != nil || outcome.Result != LogoutUnconfirmed || outcome.Detail != "timeout" {
			t.Fatalf("outcome %+v err %v", outcome, err)
		}
		if _, err := home.LoadCredential(time.Now()); !errors.Is(err, ErrCredentialMissing) {
			t.Fatal("local credential must be gone")
		}
	})
	t.Run("server gone", func(t *testing.T) {
		server := newLogoutServer(t, 200, "")
		api := server.URL + "/api"
		server.Close()
		home := testHome(t)
		storeLogoutCredential(t, home, api)
		outcome, err := Logout(context.Background(), home, time.Second)
		if err != nil || outcome.Result != LogoutUnconfirmed || !strings.HasPrefix(outcome.Detail, "transport failure") {
			t.Fatalf("outcome %+v err %v", outcome, err)
		}
	})
	t.Run("no session", func(t *testing.T) {
		outcome, err := Logout(context.Background(), testHome(t), time.Second)
		if err != nil || outcome.Result != LogoutNoSession {
			t.Fatalf("outcome %+v err %v", outcome, err)
		}
	})
	t.Run("unreadable credential is removed without contacting anyone", func(t *testing.T) {
		server := newLogoutServer(t, 200, `{"revoked":true,"alreadyRevoked":false}`)
		home := testHome(t)
		storeLogoutCredential(t, home, server.URL+"/api")
		if err := os.Chmod(home.credentialPath(), 0o644); err != nil {
			t.Fatal(err)
		}
		outcome, err := Logout(context.Background(), home, time.Second)
		if err != nil || outcome.Result != LogoutUnconfirmed || len(server.requests) != 0 {
			t.Fatalf("outcome %+v err %v requests %d", outcome, err, len(server.requests))
		}
		if _, err := os.Lstat(home.credentialPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unreadable credential must still be removed")
		}
	})
	t.Run("credential for a non-loopback cleartext API is never sent", func(t *testing.T) {
		home := testHome(t)
		storeLogoutCredential(t, home, "http://api.example.com/api")
		outcome, err := Logout(context.Background(), home, time.Second)
		if err != nil || outcome.Result != LogoutUnconfirmed || !strings.Contains(outcome.Detail, "unusable API") {
			t.Fatalf("outcome %+v err %v", outcome, err)
		}
	})
}
