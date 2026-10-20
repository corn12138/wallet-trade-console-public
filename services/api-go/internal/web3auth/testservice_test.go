package web3auth

import (
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
)

// testService is NewService with a real credential policy and in-memory
// sessions attached, so /verify issues tokens exactly as in production.
func testService(t testing.TB, sessionTTL, nonceTTL time.Duration, allowedChainIDs []int) *Service {
	t.Helper()
	return attachSessions(t, NewService(sessionTTL, nonceTTL, allowedChainIDs))
}

func attachSessions(t testing.TB, svc *Service) *Service {
	t.Helper()
	svc, _ = withFixture(t, svc)
	return svc
}

// withFixture attaches a credential fixture and returns it, for tests that
// inspect or revoke the sessions the service persisted.
func withFixture(t testing.TB, svc *Service) (*Service, *authtest.Fixture) {
	t.Helper()
	f := authtest.New(t)
	svc.credentials, svc.sessions = f.Verifier, f.Sessions
	return svc, f
}
