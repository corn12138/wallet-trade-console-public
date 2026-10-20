package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/eventfeed"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/eventpush"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyactivation"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	campaignhttp "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/http"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/i18n"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type campaignRuntime struct {
	Routes     chi.Router
	Compatible *campaign.Service
}

func buildCampaignRuntime(pool *pgxpool.Pool, verifier *auth.Verifier, adminsGranted bool, tradeMarkets map[int64]map[string]bool) campaignRuntime {
	store := postgres.New(pool)
	// The allowlist is re-read on every authorization; it applies only while
	// it is granted for the current auth epoch (see adminGrantCurrent).
	authorizer := campaignhttp.Authorizer{Verifier: verifier, Admins: func() []string {
		if !adminsGranted {
			return nil
		}
		return strings.Split(os.Getenv("CAMPAIGN_ADMIN_WALLETS"), ",")
	}}
	// Every capability comes from a registry shipped with or owned outside
	// Campaign: the template code, the trusted media registry built into this
	// binary, and the trade domain's deployed markets read at startup
	// (docs/campaign-content-capabilities-protocol-2026-10-13.md). A missing one
	// closes new admission for the content that needs it, nothing else.
	media := map[string]bool{}
	if registry, err := v2.TrustedMedia(); err != nil {
		slog.Error("campaign media registry unavailable; media content stays inadmissible", "error", err)
	} else {
		media = registry.AssetIDs()
	}
	capabilities := func() v2.Capabilities {
		return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}, MediaAssets: media, TradeMarkets: tradeMarkets}
	}
	queries := application.NewQueryService(store, authorizer, capabilities)
	return campaignRuntime{Compatible: campaign.NewCompatibleService(campaign.NewRepository(pool), queries), Routes: campaignhttp.Router(campaignhttp.Config{
		Commands: application.NewService(store, authorizer, capabilities),
		Queries:  queries,
		Events:   eventfeed.New(pool, verifier),
		Verifier: verifier,
		Enabled:  os.Getenv("CAMPAIGN_V2_ENABLED") == "true",
		Legacy:   legacyactivation.New(pool, capabilities),
		Access:   authorizer,
	})}
}

// buildCampaignPush returns the authenticated Campaign invalidation runtime, or
// nil when the Campaign feature, database or verifier is absent. It listens
// for advisory wakeups itself and sweeps durable heads without them, so it
// does not depend on the separate outbox publisher process.
func buildCampaignPush(pool *pgxpool.Pool, verifier *auth.Verifier, getenv func(string) string) *eventpush.Hub {
	if getenv("CAMPAIGN_V2_ENABLED") != "true" || pool == nil || verifier == nil {
		slog.Info("campaign push disabled", "campaignV2", getenv("CAMPAIGN_V2_ENABLED") == "true", "database", pool != nil, "verifier", verifier != nil)
		return nil
	}
	cfg, err := eventpush.ConfigFromEnv(getenv)
	if err != nil {
		slog.Error("campaign push disabled: invalid configuration", "error", err)
		return nil
	}
	cfg = cfg.BoundedBy(pool.Config().MaxConns)
	hub := eventpush.New(eventfeed.New(pool, verifier), durableoutbox.NewRepository(pool), cfg)
	hub.Listen(pool)
	slog.Info("campaign push enabled", "namespace", "/campaign-events", "sweepMs", cfg.SweepInterval.Milliseconds(), "concurrency", cfg.Concurrency)
	return hub
}

// campaignTradeMarkets reads the trade domain's deployed markets once. A
// failure leaves trade CTAs inadmissible; it never affects trading itself.
func campaignTradeMarkets(deployed func(*int) ([]markets.SymbolView, error)) map[int64]map[string]bool {
	out := map[int64]map[string]bool{}
	if deployed == nil {
		return out
	}
	symbols, err := deployed(nil)
	if err != nil {
		slog.Error("deployed markets unavailable; campaign trade entries stay inadmissible", "error", err)
		return out
	}
	for _, symbol := range symbols {
		chain := int64(symbol.ChainID)
		if out[chain] == nil {
			out[chain] = map[string]bool{}
		}
		out[chain][symbol.Symbol] = true
	}
	return out
}

// i18nAdmins is the i18n admin allowlist, empty (deny-all) unless the admin
// allowlists are granted for the current auth epoch.
func i18nAdmins(granted bool) map[string]bool {
	if !granted {
		return map[string]bool{}
	}
	return i18n.ParseAdminWallets(os.Getenv(i18n.AdminWalletsEnv))
}
