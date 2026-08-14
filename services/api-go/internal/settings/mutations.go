// Package settings's mutation surface mirrors
// legacy NestJS settings/settings.service.ts updatePreferences,
// updateNotificationPreferences, hideAsset, removeHiddenAsset.
//
// All four are user-scoped UPSERT/DELETE writes that flow through the
// Phase 4o auth middleware. The hidden-asset key derivation mirrors
// common/utils/web3-hidden-asset.ts so the FE's existing keys stay
// stable across the cutover.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
)

// Mutation sentinels. Handler maps each to HTTP status.
var (
	ErrInvalidOwnerAddr     = errors.New("settings: invalid owner address")
	ErrInvalidWalletAddr    = errors.New("settings: invalid wallet address")
	ErrInvalidAssetAddr     = errors.New("settings: invalid asset address")
	ErrSymbolOrAssetReqd    = errors.New("settings: a symbol or assetAddress is required")
	ErrHiddenAssetNotFound  = errors.New("settings: hidden asset not found")
	ErrMutationRequiresPool = errors.New("settings: cannot mutate without a database pool")
)

// UpdatePreferencesRequest mirrors PUT /settings/preferences @Body.
// All fields are optional — pointer types distinguish "absent" (don't
// touch this column) from explicit zero values.
type UpdatePreferencesRequest struct {
	OwnerAddress         string  `json:"ownerAddress"`
	Theme                *string `json:"theme,omitempty"`
	FiatCurrency         *string `json:"fiatCurrency,omitempty"`
	TestnetEnabled       *bool   `json:"testnetEnabled,omitempty"`
	NotificationsEnabled *bool   `json:"notificationsEnabled,omitempty"`
	SpamFilterLevel      *string `json:"spamFilterLevel,omitempty"`
}

// UpdateNotificationsRequest mirrors PUT /settings/notifications @Body.
type UpdateNotificationsRequest struct {
	OwnerAddress   string `json:"ownerAddress"`
	SecurityAlerts *bool  `json:"securityAlerts,omitempty"`
	TxUpdates      *bool  `json:"txUpdates,omitempty"`
	MarketAlerts   *bool  `json:"marketAlerts,omitempty"`
	ProductUpdates *bool  `json:"productUpdates,omitempty"`
}

// HideAssetRequest mirrors POST /settings/hidden-assets @Body.
// At least one of Symbol or AssetAddress is required (matches NestJS).
type HideAssetRequest struct {
	OwnerAddress  string  `json:"ownerAddress"`
	WalletAddress *string `json:"walletAddress,omitempty"`
	ChainID       int     `json:"chainId"`
	AssetAddress  *string `json:"assetAddress,omitempty"`
	Symbol        string  `json:"symbol"`
	Reason        *string `json:"reason,omitempty"`
}

// UpsertPreferences mirrors updatePreferences. Single-row UPSERT keyed
// on ownerAddress. nil pointer fields preserve existing values via
// COALESCE on the conflict path; defaults apply on insert.
func (r *Repository) UpsertPreferences(ctx context.Context, req UpdatePreferencesRequest) (UserPreferences, error) {
	owner := strings.TrimSpace(strings.ToLower(req.OwnerAddress))
	if !addressRE.MatchString(owner) {
		return UserPreferences{}, ErrInvalidOwnerAddr
	}
	if r.pool == nil {
		return UserPreferences{}, ErrMutationRequiresPool
	}

	theme := normalizePrefValue(req.Theme)
	currency := normalizeCurrency(req.FiatCurrency)
	spamLevel := normalizePrefValue(req.SpamFilterLevel)

	var (
		row                  UserPreferences
		createdAt, updatedAt time.Time
	)
	err := r.pool.QueryRow(ctx, `
		INSERT INTO user_preferences (
			id, "ownerAddress", theme, "fiatCurrency", "testnetEnabled",
			"notificationsEnabled", "spamFilterLevel",
			"createdAt", "updatedAt"
		) VALUES (
			gen_random_uuid()::text,
			$1,
			COALESCE($2, $7),
			COALESCE($3, $8),
			COALESCE($4, TRUE),
			COALESCE($5, TRUE),
			COALESCE($6, $9),
			NOW(), NOW()
		)
		ON CONFLICT ("ownerAddress") DO UPDATE SET
			theme                  = CASE WHEN $2::text IS NULL THEN user_preferences.theme              ELSE $2 END,
			"fiatCurrency"         = CASE WHEN $3::text IS NULL THEN user_preferences."fiatCurrency"     ELSE $3 END,
			"testnetEnabled"       = COALESCE($4, user_preferences."testnetEnabled"),
			"notificationsEnabled" = COALESCE($5, user_preferences."notificationsEnabled"),
			"spamFilterLevel"      = CASE WHEN $6::text IS NULL THEN user_preferences."spamFilterLevel"  ELSE $6 END,
			"updatedAt"            = NOW()
		RETURNING theme, "fiatCurrency", "testnetEnabled", "notificationsEnabled",
		          "spamFilterLevel", "createdAt", "updatedAt"
	`,
		owner,
		theme, currency, req.TestnetEnabled, req.NotificationsEnabled, spamLevel,
		defaultTheme, defaultFiatCurrency, defaultSpamFilterLevel,
	).Scan(&row.Theme, &row.FiatCurrency, &row.TestnetEnabled,
		&row.NotificationsEnabled, &row.SpamFilterLevel, &createdAt, &updatedAt)
	if err != nil {
		return UserPreferences{}, fmt.Errorf("upsert user_preferences: %w", err)
	}
	row.OwnerAddress = &owner
	row.Source = "stored"
	ct := createdAt.UTC().Format(time.RFC3339Nano)
	ut := updatedAt.UTC().Format(time.RFC3339Nano)
	row.CreatedAt = &ct
	row.UpdatedAt = &ut
	return row, nil
}

// UpsertNotificationPreferences mirrors updateNotificationPreferences.
// Same single-row UPSERT shape as UpsertPreferences.
func (r *Repository) UpsertNotificationPreferences(ctx context.Context, req UpdateNotificationsRequest) (NotificationPreferences, error) {
	owner := strings.TrimSpace(strings.ToLower(req.OwnerAddress))
	if !addressRE.MatchString(owner) {
		return NotificationPreferences{}, ErrInvalidOwnerAddr
	}
	if r.pool == nil {
		return NotificationPreferences{}, ErrMutationRequiresPool
	}
	var (
		row                  NotificationPreferences
		createdAt, updatedAt time.Time
	)
	err := r.pool.QueryRow(ctx, `
		INSERT INTO notification_prefs (
			id, "ownerAddress", "securityAlerts", "txUpdates", "marketAlerts", "productUpdates",
			"createdAt", "updatedAt"
		) VALUES (
			gen_random_uuid()::text,
			$1,
			COALESCE($2, TRUE),
			COALESCE($3, TRUE),
			COALESCE($4, TRUE),
			COALESCE($5, FALSE),
			NOW(), NOW()
		)
		ON CONFLICT ("ownerAddress") DO UPDATE SET
			"securityAlerts" = COALESCE($2, notification_prefs."securityAlerts"),
			"txUpdates"      = COALESCE($3, notification_prefs."txUpdates"),
			"marketAlerts"   = COALESCE($4, notification_prefs."marketAlerts"),
			"productUpdates" = COALESCE($5, notification_prefs."productUpdates"),
			"updatedAt"      = NOW()
		RETURNING "securityAlerts", "txUpdates", "marketAlerts", "productUpdates",
		          "createdAt", "updatedAt"
	`,
		owner, req.SecurityAlerts, req.TxUpdates, req.MarketAlerts, req.ProductUpdates,
	).Scan(&row.SecurityAlerts, &row.TxUpdates, &row.MarketAlerts, &row.ProductUpdates,
		&createdAt, &updatedAt)
	if err != nil {
		return NotificationPreferences{}, fmt.Errorf("upsert notification_prefs: %w", err)
	}
	row.OwnerAddress = &owner
	row.Source = "stored"
	ct := createdAt.UTC().Format(time.RFC3339Nano)
	ut := updatedAt.UTC().Format(time.RFC3339Nano)
	row.CreatedAt = &ct
	row.UpdatedAt = &ut
	return row, nil
}

// UpsertHiddenAsset mirrors hideAsset. The (ownerAddress, assetKey)
// composite-unique index drives the UPSERT; assetKey is derived from
// buildHiddenAssetKey so the FE's existing keys stay stable.
func (r *Repository) UpsertHiddenAsset(ctx context.Context, req HideAssetRequest) (HiddenAsset, error) {
	owner := strings.TrimSpace(strings.ToLower(req.OwnerAddress))
	if !addressRE.MatchString(owner) {
		return HiddenAsset{}, ErrInvalidOwnerAddr
	}
	wallet, err := normalizeOptionalAddress(req.WalletAddress)
	if err != nil {
		return HiddenAsset{}, ErrInvalidWalletAddr
	}
	asset, err := normalizeOptionalAddress(req.AssetAddress)
	if err != nil {
		return HiddenAsset{}, ErrInvalidAssetAddr
	}
	symbol := strings.ToUpper(strings.TrimSpace(req.Symbol))
	if symbol == "" && asset == nil {
		return HiddenAsset{}, ErrSymbolOrAssetReqd
	}
	if r.pool == nil {
		return HiddenAsset{}, ErrMutationRequiresPool
	}
	reason := normalizePrefValue(req.Reason)
	if reason == nil {
		v := "manual"
		reason = &v
	}
	storedSymbol := symbol
	if storedSymbol == "" {
		storedSymbol = "UNKNOWN"
	}
	key := buildHiddenAssetKey(req.ChainID, asset, &symbol, wallet)
	var (
		out                  HiddenAsset
		createdAt, updatedAt time.Time
	)
	err = r.pool.QueryRow(ctx, `
		INSERT INTO hidden_assets (
			id, "ownerAddress", "walletAddress", "chainId", "assetKey", "assetAddress",
			symbol, reason, "createdAt", "updatedAt"
		) VALUES (
			gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, NOW(), NOW()
		)
		ON CONFLICT ("ownerAddress", "assetKey") DO UPDATE SET
			"walletAddress" = EXCLUDED."walletAddress",
			"chainId"       = EXCLUDED."chainId",
			"assetAddress"  = EXCLUDED."assetAddress",
			symbol          = EXCLUDED.symbol,
			reason          = EXCLUDED.reason,
			"updatedAt"     = NOW()
		RETURNING id, "ownerAddress", "walletAddress", "chainId", "assetKey",
		          "assetAddress", symbol, reason, "createdAt", "updatedAt"
	`,
		owner, wallet, req.ChainID, key, asset, storedSymbol, *reason,
	).Scan(&out.ID, &out.OwnerAddress, &out.WalletAddress, &out.ChainID, &out.AssetKey,
		&out.AssetAddress, &out.Symbol, &out.Reason, &createdAt, &updatedAt)
	if err != nil {
		return HiddenAsset{}, fmt.Errorf("upsert hidden_assets: %w", err)
	}
	out.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	out.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return out, nil
}

// DeleteHiddenAsset mirrors removeHiddenAsset: deletes a single row
// scoped to (owner, id) and returns the constant removed payload.
// 404 when no row matches (the NestJS NotFoundException).
func (r *Repository) DeleteHiddenAsset(ctx context.Context, ownerAddress, hiddenAssetID string) error {
	owner := strings.TrimSpace(strings.ToLower(ownerAddress))
	if !addressRE.MatchString(owner) {
		return ErrInvalidOwnerAddr
	}
	if r.pool == nil {
		return ErrMutationRequiresPool
	}
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM hidden_assets
		WHERE id = $1 AND "ownerAddress" = $2
	`, hiddenAssetID, owner)
	if err != nil {
		return fmt.Errorf("delete hidden_assets: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrHiddenAssetNotFound
	}
	return nil
}

// buildHiddenAssetKey mirrors common/utils/web3-hidden-asset.ts. Format:
// "<chainId>:<scope>:<identity>" where scope is the normalized wallet
// address or "all-wallets", and identity is the normalized asset
// address or "symbol:<UPPER>".
func buildHiddenAssetKey(chainID int, assetAddress *string, symbol *string, walletAddress *string) string {
	scope := "all-wallets"
	if walletAddress != nil {
		scope = *walletAddress
	}
	identity := "symbol:UNKNOWN"
	if assetAddress != nil {
		identity = *assetAddress
	} else if symbol != nil {
		s := strings.ToUpper(strings.TrimSpace(*symbol))
		if s == "" {
			s = "UNKNOWN"
		}
		identity = "symbol:" + s
	}
	return strconv.Itoa(chainID) + ":" + scope + ":" + identity
}

// normalizeOptionalAddress returns nil for nil/empty inputs; otherwise
// validates and lowercases. Returns an error for the invalid-shape case
// so the caller can pick the right sentinel (wallet vs asset).
func normalizeOptionalAddress(p *string) (*string, error) {
	if p == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*p)
	if trimmed == "" {
		return nil, nil
	}
	if !addressRE.MatchString(trimmed) {
		return nil, errors.New("invalid address")
	}
	lowered := strings.ToLower(trimmed)
	return &lowered, nil
}

// normalizePrefValue trims, returns nil for empty. NestJS's
// normalizePreferenceValue equivalent.
func normalizePrefValue(p *string) *string {
	if p == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*p)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// normalizeCurrency trims, uppercases, returns nil for empty.
func normalizeCurrency(p *string) *string {
	v := normalizePrefValue(p)
	if v == nil {
		return nil
	}
	upper := strings.ToUpper(*v)
	return &upper
}

// -------- service layer --------

func (s *Service) UpdatePreferences(ctx context.Context, req UpdatePreferencesRequest) (UserPreferences, error) {
	return s.repo.UpsertPreferences(ctx, req)
}

func (s *Service) UpdateNotificationPreferences(ctx context.Context, req UpdateNotificationsRequest) (NotificationPreferences, error) {
	return s.repo.UpsertNotificationPreferences(ctx, req)
}

func (s *Service) HideAsset(ctx context.Context, req HideAssetRequest) (HiddenAsset, error) {
	return s.repo.UpsertHiddenAsset(ctx, req)
}

// RemoveHiddenAsset matches NestJS removeHiddenAsset return shape:
// `{id, removed: true}`. Encapsulated as a typed struct so the
// handler can write JSON without an inline map literal.
type RemoveHiddenAssetResponse struct {
	ID      string `json:"id"`
	Removed bool   `json:"removed"`
}

func (s *Service) RemoveHiddenAsset(ctx context.Context, ownerAddress, hiddenAssetID string) (RemoveHiddenAssetResponse, error) {
	if err := s.repo.DeleteHiddenAsset(ctx, ownerAddress, hiddenAssetID); err != nil {
		return RemoveHiddenAssetResponse{}, err
	}
	return RemoveHiddenAssetResponse{ID: hiddenAssetID, Removed: true}, nil
}

// -------- handlers --------

func makeUpdatePreferencesHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authAddr := auth.AddressFromContext(r.Context())
		if authAddr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req UpdatePreferencesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		owner, err := auth.ResolveOwner(authAddr, req.OwnerAddress)
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		req.OwnerAddress = owner
		out, err := svc.UpdatePreferences(r.Context(), req)
		mapMutationErr(w, r, err, "settings preferences update failed", req.OwnerAddress, http.StatusOK, out)
	}
}

func makeUpdateNotificationsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authAddr := auth.AddressFromContext(r.Context())
		if authAddr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req UpdateNotificationsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		owner, err := auth.ResolveOwner(authAddr, req.OwnerAddress)
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		req.OwnerAddress = owner
		out, err := svc.UpdateNotificationPreferences(r.Context(), req)
		mapMutationErr(w, r, err, "settings notifications update failed", req.OwnerAddress, http.StatusOK, out)
	}
}

func makeHideAssetHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authAddr := auth.AddressFromContext(r.Context())
		if authAddr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req HideAssetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		owner, err := auth.ResolveOwner(authAddr, req.OwnerAddress)
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		req.OwnerAddress = owner
		out, err := svc.HideAsset(r.Context(), req)
		mapMutationErr(w, r, err, "settings hide-asset failed", req.OwnerAddress, http.StatusCreated, out)
	}
}

func makeRemoveHiddenAssetHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authAddr := auth.AddressFromContext(r.Context())
		if authAddr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := chi.URLParam(r, "hiddenAssetId")
		if id == "" {
			http.Error(w, "hiddenAssetId required", http.StatusBadRequest)
			return
		}
		// Owner is read from the query string (NestJS parameter shape).
		requested := r.URL.Query().Get("ownerAddress")
		owner, err := auth.ResolveOwner(authAddr, requested)
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		out, err := svc.RemoveHiddenAsset(r.Context(), owner, id)
		mapMutationErr(w, r, err, "settings remove-hidden-asset failed", id, http.StatusOK, out)
	}
}

func mapResolveOwnerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrMissingAuthenticated):
		http.Error(w, err.Error(), http.StatusUnauthorized)
	case errors.Is(err, auth.ErrOwnerMismatch):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func mapMutationErr(w http.ResponseWriter, r *http.Request, err error, logMsg, logKey string, successStatus int, payload any) {
	switch {
	case err == nil:
		writeJSON(w, successStatus, payload)
	case errors.Is(err, ErrInvalidOwnerAddr),
		errors.Is(err, ErrInvalidWalletAddr),
		errors.Is(err, ErrInvalidAssetAddr),
		errors.Is(err, ErrSymbolOrAssetReqd):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrHiddenAssetNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrMutationRequiresPool):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		slog.ErrorContext(r.Context(), logMsg, "err", err, "key", logKey)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
