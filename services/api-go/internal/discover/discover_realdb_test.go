package discover

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3events"
)

// TestGetHome_RealDB exercises discover/home against a REAL database, using the
// same concrete repositories cmd/api wires in production
// (token/staking/web3events + the markets service). The unit tests in this
// package use FAKE repos, so the real concurrent (errgroup) queries
// ListTrendingFull / ListActivePools / GetStats are otherwise never run against
// a real schema — which is the production path a gateway 502 made us suspect.
// (Root cause turned out to be a stale gateway upstream IP, not Go; this test
// is the regression guard that the real-DB discover path stays healthy.)
//
// Gated: skips unless WALLET_TRADE_TEST_DATABASE_URL (or DATABASE_URL) is set,
// so normal CI without a database is unaffected. Run it with, e.g.:
//
//	WALLET_TRADE_TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:6543/wallet_trade_test' \
//	  go test ./internal/discover -run TestGetHome_RealDB -v
func TestGetHome_RealDB(t *testing.T) {
	dsn := os.Getenv("WALLET_TRADE_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("no WALLET_TRADE_TEST_DATABASE_URL / DATABASE_URL set; skipping real-DB discover test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()

	tokenRepo := token.NewRepository(pool)
	stakingRepo := staking.NewRepository(pool)
	eventsRepo := web3events.NewRepository(pool)
	tradingRepo := trading.NewRepository(pool)
	marketsSvc := markets.NewService(markets.NewDirLoader(), tradingRepo, tokenRepo)

	chainID := 11155111

	// Exercise each real downstream query on its own first, so a failure points
	// at the exact repo rather than the aggregated handler.
	if _, err := tokenRepo.ListTrendingFull(ctx, &chainID, 6); err != nil {
		t.Fatalf("token.ListTrendingFull (real DB): %v", err)
	}
	if _, err := stakingRepo.ListActivePools(ctx, &chainID); err != nil {
		t.Fatalf("staking.ListActivePools (real DB): %v", err)
	}
	if _, err := eventsRepo.GetStats(ctx, chainID); err != nil {
		t.Fatalf("web3events.GetStats (real DB): %v", err)
	}

	// Now the full aggregated handler, exactly as production wires it.
	svc := NewService(marketsSvc, tokenRepo, stakingRepo, eventsRepo)
	home, err := svc.GetHome(ctx, &chainID)
	if err != nil {
		t.Fatalf("GetHome (real DB) returned error: %v", err)
	}
	if len(home.CuratedDapps) == 0 {
		t.Errorf("expected the static curatedDapps to be present, got none")
	}
	if home.GeneratedAt == "" {
		t.Errorf("expected generatedAt to be set")
	}
	t.Logf("real-DB discover/home ok: trending=%d earn=%d marketPulse=%d riskMarkers=%d",
		len(home.TrendingTokens), len(home.EarnProducts), len(home.MarketPulse), len(home.RiskMarkers))
}
