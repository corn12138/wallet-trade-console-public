// Package settings is the Go port of legacy NestJS settings.
//
// All seven frontend-used routes are implemented here (reads) and in
// mutations.go (writes): GET/PUT preferences, GET/PUT notifications,
// GET/POST/DELETE hidden-assets. Matching the deployed NestJS
// SettingsController (class-level @Public + @UseGuards(Web3AuthGuard); every
// handler resolves the owner via resolveAuthenticatedOwnerAddress), EVERY route
// is web3-guarded and owner-pinned to the authenticated wallet — including the
// reads, which previously mounted public (an over-exposure of owner-private
// settings vs the NestJS 401). See Router and guard-parity.md.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// addressRE mirrors common/utils/web3-address.ts isValidAddress:
// 0x + 40 hex chars, case-insensitive.
var addressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

// UserPreferences is the GET /api/settings/preferences shape.
// `source` is "stored" when a row exists, "default" when defaults are
// substituted. `createdAt` / `updatedAt` are null in the default case.
type UserPreferences struct {
	OwnerAddress         *string `json:"ownerAddress"`
	Theme                string  `json:"theme"`
	FiatCurrency         string  `json:"fiatCurrency"`
	TestnetEnabled       bool    `json:"testnetEnabled"`
	NotificationsEnabled bool    `json:"notificationsEnabled"`
	SpamFilterLevel      string  `json:"spamFilterLevel"`
	Source               string  `json:"source"`
	CreatedAt            *string `json:"createdAt"`
	UpdatedAt            *string `json:"updatedAt"`
}

// NotificationPreferences is the GET /api/settings/notifications shape.
type NotificationPreferences struct {
	OwnerAddress   *string `json:"ownerAddress"`
	SecurityAlerts bool    `json:"securityAlerts"`
	TxUpdates      bool    `json:"txUpdates"`
	MarketAlerts   bool    `json:"marketAlerts"`
	ProductUpdates bool    `json:"productUpdates"`
	Source         string  `json:"source"`
	CreatedAt      *string `json:"createdAt"`
	UpdatedAt      *string `json:"updatedAt"`
}

// HiddenAsset is one row of GET /api/settings/hidden-assets.
type HiddenAsset struct {
	ID            string  `json:"id"`
	OwnerAddress  string  `json:"ownerAddress"`
	WalletAddress *string `json:"walletAddress"`
	ChainID       int     `json:"chainId"`
	AssetKey      string  `json:"assetKey"`
	AssetAddress  *string `json:"assetAddress"`
	Symbol        string  `json:"symbol"`
	Reason        string  `json:"reason"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
}

// Default values match DEFAULT_USER_PREFERENCES /
// DEFAULT_NOTIFICATION_PREFERENCES in settings.service.ts.
const (
	defaultTheme                = "system"
	defaultFiatCurrency         = "USD"
	defaultTestnetEnabled       = true
	defaultNotificationsEnabled = true
	defaultSpamFilterLevel      = "standard"

	defaultSecurityAlerts = true
	defaultTxUpdates      = true
	defaultMarketAlerts   = true
	defaultProductUpdates = false
)

// ErrPoolUnavailable mirrors the sibling pattern.
var ErrPoolUnavailable = errors.New("settings repository: database pool not configured")

// Repository owns the pgx queries. A nil pool yields ErrPoolUnavailable
// from every method, except the lookup short-circuits in
// service-default paths that don't need the DB at all.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository binds a repo. nil pool is tolerated.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// FindUserPreferences returns the row matching ownerAddress or nil
// when none exists. The caller (Service) substitutes defaults for nil.
func (r *Repository) FindUserPreferences(ctx context.Context, ownerAddress string) (*UserPreferences, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	var (
		theme, fiatCurrency, spamLevel string
		testnetEnabled                 bool
		notificationsEnabled           bool
		createdAt, updatedAt           time.Time
	)
	// Columns use Prisma's default camelCase (no @map on individual
	// fields); only the table is renamed via @@map.
	err := r.pool.QueryRow(ctx, `
		SELECT theme, "fiatCurrency", "testnetEnabled", "notificationsEnabled",
		       "spamFilterLevel", "createdAt", "updatedAt"
		FROM user_preferences
		WHERE "ownerAddress" = $1
	`, ownerAddress).Scan(&theme, &fiatCurrency, &testnetEnabled,
		&notificationsEnabled, &spamLevel, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query user_preferences: %w", err)
	}
	owner := ownerAddress
	cIso := createdAt.UTC().Format(time.RFC3339Nano)
	uIso := updatedAt.UTC().Format(time.RFC3339Nano)
	return &UserPreferences{
		OwnerAddress:         &owner,
		Theme:                theme,
		FiatCurrency:         fiatCurrency,
		TestnetEnabled:       testnetEnabled,
		NotificationsEnabled: notificationsEnabled,
		SpamFilterLevel:      spamLevel,
		Source:               "stored",
		CreatedAt:            &cIso,
		UpdatedAt:            &uIso,
	}, nil
}

// FindNotificationPreferences mirrors FindUserPreferences for the
// notification_prefs table.
func (r *Repository) FindNotificationPreferences(ctx context.Context, ownerAddress string) (*NotificationPreferences, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	var (
		security, txUpdates, market, product bool
		createdAt, updatedAt                 time.Time
	)
	err := r.pool.QueryRow(ctx, `
		SELECT "securityAlerts", "txUpdates", "marketAlerts", "productUpdates",
		       "createdAt", "updatedAt"
		FROM notification_prefs
		WHERE "ownerAddress" = $1
	`, ownerAddress).Scan(&security, &txUpdates, &market, &product, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query notification_prefs: %w", err)
	}
	owner := ownerAddress
	cIso := createdAt.UTC().Format(time.RFC3339Nano)
	uIso := updatedAt.UTC().Format(time.RFC3339Nano)
	return &NotificationPreferences{
		OwnerAddress:   &owner,
		SecurityAlerts: security,
		TxUpdates:      txUpdates,
		MarketAlerts:   market,
		ProductUpdates: product,
		Source:         "stored",
		CreatedAt:      &cIso,
		UpdatedAt:      &uIso,
	}, nil
}

// ListHiddenAssets returns hidden assets for owner+chainId. chainId nil
// means all chains. Rows are ordered by createdAt DESC.
func (r *Repository) ListHiddenAssets(ctx context.Context, ownerAddress string, chainID *int) ([]HiddenAsset, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	args := []any{ownerAddress}
	where := `"ownerAddress" = $1`
	if chainID != nil {
		args = append(args, *chainID)
		where += ` AND "chainId" = $2`
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, "ownerAddress", "walletAddress", "chainId", "assetKey",
		       "assetAddress", symbol, reason, "createdAt", "updatedAt"
		FROM hidden_assets
		WHERE `+where+`
		ORDER BY "createdAt" DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query hidden_assets: %w", err)
	}
	defer rows.Close()

	out := make([]HiddenAsset, 0)
	for rows.Next() {
		var (
			h                    HiddenAsset
			createdAt, updatedAt time.Time
		)
		if err := rows.Scan(&h.ID, &h.OwnerAddress, &h.WalletAddress, &h.ChainID,
			&h.AssetKey, &h.AssetAddress, &h.Symbol, &h.Reason, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan hidden_assets: %w", err)
		}
		h.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		h.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hidden_assets: %w", err)
	}
	return out, nil
}

// --- service ---

// Service wraps the repo with default substitution and address
// validation. nil-pool errors degrade to a default response so the FE
// preferences screen renders without 500s even before SIWE sign-in.
type Service struct {
	repo *Repository
}

// NewService binds a service over a repo.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// GetPreferences returns the stored row or defaults. Invalid or empty
// address returns the default-source response (no DB hit).
func (s *Service) GetPreferences(ctx context.Context, ownerAddress string) (UserPreferences, error) {
	addr, ok := normalizeIfValid(ownerAddress)
	if !ok {
		return defaultUserPreferences(nil), nil
	}
	prefs, err := s.repo.FindUserPreferences(ctx, addr)
	if errors.Is(err, ErrPoolUnavailable) {
		return defaultUserPreferences(&addr), nil
	}
	if err != nil {
		return UserPreferences{}, err
	}
	if prefs == nil {
		return defaultUserPreferences(&addr), nil
	}
	return *prefs, nil
}

// GetNotificationPreferences mirrors GetPreferences.
func (s *Service) GetNotificationPreferences(ctx context.Context, ownerAddress string) (NotificationPreferences, error) {
	addr, ok := normalizeIfValid(ownerAddress)
	if !ok {
		return defaultNotificationPreferences(nil), nil
	}
	prefs, err := s.repo.FindNotificationPreferences(ctx, addr)
	if errors.Is(err, ErrPoolUnavailable) {
		return defaultNotificationPreferences(&addr), nil
	}
	if err != nil {
		return NotificationPreferences{}, err
	}
	if prefs == nil {
		return defaultNotificationPreferences(&addr), nil
	}
	return *prefs, nil
}

// ListHiddenAssets returns the asset list. Empty/invalid owner → [].
// nil pool also surfaces as [] to keep the page rendering.
func (s *Service) ListHiddenAssets(ctx context.Context, ownerAddress string, chainID *int) ([]HiddenAsset, error) {
	addr, ok := normalizeIfValid(ownerAddress)
	if !ok {
		return []HiddenAsset{}, nil
	}
	rows, err := s.repo.ListHiddenAssets(ctx, addr, chainID)
	if errors.Is(err, ErrPoolUnavailable) {
		return []HiddenAsset{}, nil
	}
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// --- helpers ---

func normalizeIfValid(addr string) (string, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" || !addressRE.MatchString(addr) {
		return "", false
	}
	return strings.ToLower(addr), true
}

func defaultUserPreferences(addr *string) UserPreferences {
	return UserPreferences{
		OwnerAddress:         addr,
		Theme:                defaultTheme,
		FiatCurrency:         defaultFiatCurrency,
		TestnetEnabled:       defaultTestnetEnabled,
		NotificationsEnabled: defaultNotificationsEnabled,
		SpamFilterLevel:      defaultSpamFilterLevel,
		Source:               "default",
		CreatedAt:            nil,
		UpdatedAt:            nil,
	}
}

func defaultNotificationPreferences(addr *string) NotificationPreferences {
	return NotificationPreferences{
		OwnerAddress:   addr,
		SecurityAlerts: defaultSecurityAlerts,
		TxUpdates:      defaultTxUpdates,
		MarketAlerts:   defaultMarketAlerts,
		ProductUpdates: defaultProductUpdates,
		Source:         "default",
		CreatedAt:      nil,
		UpdatedAt:      nil,
	}
}

// --- router ---

// Router mounts /api/settings. Mirrors the deployed NestJS SettingsController,
// where EVERY route is web3-guarded (class-level @UseGuards(Web3AuthGuard)) and
// owner-pinned to the authenticated wallet (resolveAuthenticatedOwnerAddress).
//
// Reads (preferences, notifications, hidden-assets) are owner-pinned via
// auth.ResolveOwner inside each handler and mount inside a guarded group — the
// guard is applied when authMiddleware is configured; a nil guard (dev without
// JWT_SECRET) still mounts them, but the owner-pin returns 401 without an
// authenticated address (same as portfolio). This replaces the earlier
// public-read mounting, which over-exposed owner-private settings (the NestJS
// reads 401 unauthenticated). Mutation routes (PUT/POST/DELETE) mount only when
// authMiddleware is non-nil — same pattern as token and wallets.
func Router(svc *Service, authMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		if authMiddleware != nil {
			r.Use(authMiddleware)
		}
		r.Get("/preferences", makePreferencesHandler(svc))
		r.Get("/notifications", makeNotificationsHandler(svc))
		r.Get("/hidden-assets", makeHiddenAssetsHandler(svc))
	})
	if authMiddleware != nil {
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware)
			r.Put("/preferences", makeUpdatePreferencesHandler(svc))
			r.Put("/notifications", makeUpdateNotificationsHandler(svc))
			r.Post("/hidden-assets", makeHideAssetHandler(svc))
			r.Delete("/hidden-assets/{hiddenAssetId}", makeRemoveHiddenAssetHandler(svc))
		})
	}
	return r
}

// makePreferencesHandler owner-pins the read to the authenticated wallet,
// mirroring NestJS resolveAuthenticatedOwnerAddress: no ?ownerAddress= → the JWT
// wallet; a mismatched ?ownerAddress= → 403; no authenticated address → 401.
func makePreferencesHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), r.URL.Query().Get("ownerAddress"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		out, err := svc.GetPreferences(r.Context(), owner)
		if err != nil {
			slog.ErrorContext(r.Context(), "settings preferences failed", "err", err)
			http.Error(w, "failed to load preferences", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeNotificationsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), r.URL.Query().Get("ownerAddress"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		out, err := svc.GetNotificationPreferences(r.Context(), owner)
		if err != nil {
			slog.ErrorContext(r.Context(), "settings notifications failed", "err", err)
			http.Error(w, "failed to load notification preferences", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeHiddenAssetsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), q.Get("ownerAddress"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		var chainID *int
		if raw := q.Get("chainId"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil {
				chainID = &v
			}
		}
		out, err := svc.ListHiddenAssets(r.Context(), owner, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "settings hidden-assets failed", "err", err)
			http.Error(w, "failed to load hidden assets", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("settings response encode failed", "err", err)
	}
}
