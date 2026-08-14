// Package security's connected-sites mutation surface mirrors
// legacy NestJS security/connected-sites.service.ts upsertConnectedSite
// and removeConnectedSite — both web3-guarded and owner-pinned via
// auth.ResolveOwner (parity with NestJS resolveAuthenticatedOwnerAddress).
// POST /security/tx-review is implemented in the txreview package and folded
// into the /security mount by httpx/server.go (see Router in security.go);
// it is no longer NestJS-only.
package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
)

// Mutation sentinels.
var (
	ErrInvalidOwnerAddr      = errors.New("security: invalid owner address")
	ErrSiteNameRequired      = errors.New("security: site name is required")
	ErrSiteOriginRequired    = errors.New("security: site origin is required")
	ErrUnsupportedScheme     = errors.New("security: only http/https site origins are supported")
	ErrConnectedSiteNotFound = errors.New("security: connected site not found")
	ErrMutationRequiresPool  = errors.New("security: cannot mutate without a database pool")
)

// UpsertConnectedSiteRequest mirrors POST /security/connected-sites @Body.
type UpsertConnectedSiteRequest struct {
	OwnerAddress string   `json:"ownerAddress"`
	ChainID      *int     `json:"chainId,omitempty"`
	Origin       string   `json:"origin"`
	SiteName     string   `json:"siteName"`
	IconURL      *string  `json:"iconUrl,omitempty"`
	Category     *string  `json:"category,omitempty"`
	RiskLevel    *string  `json:"riskLevel,omitempty"`
	Permissions  []string `json:"permissions,omitempty"`
}

// RemoveConnectedSiteResponse matches the NestJS `{id, removed: true}`
// shape used by settings.RemoveHiddenAsset.
type RemoveConnectedSiteResponse struct {
	ID      string `json:"id"`
	Removed bool   `json:"removed"`
}

// UpsertConnectedSite mirrors connected-sites.service.ts upsertConnectedSite.
// Single-row UPSERT on the (ownerAddress, origin) composite unique
// index. Origin is parsed via net/url to derive the lowercased
// "<scheme>://<host>" canonical form + the bare hostname (domain).
func (r *Repository) UpsertConnectedSite(ctx context.Context, req UpsertConnectedSiteRequest) (ConnectedSite, error) {
	owner := strings.TrimSpace(strings.ToLower(req.OwnerAddress))
	if !addressRE.MatchString(owner) {
		return ConnectedSite{}, ErrInvalidOwnerAddr
	}
	siteName := strings.TrimSpace(req.SiteName)
	if siteName == "" {
		return ConnectedSite{}, ErrSiteNameRequired
	}
	originURL, domain, err := normalizeSiteOrigin(req.Origin)
	if err != nil {
		return ConnectedSite{}, err
	}
	if r.pool == nil {
		return ConnectedSite{}, ErrMutationRequiresPool
	}
	iconURL := normalizeOptionalText(req.IconURL)
	category := normalizeOptionalText(req.Category)
	riskLevel := normalizeRiskLevel(req.RiskLevel)
	perms := dedupePermissions(req.Permissions)
	permsJSON, err := json.Marshal(perms)
	if err != nil {
		return ConnectedSite{}, fmt.Errorf("encode permissions: %w", err)
	}

	var (
		out                               ConnectedSite
		firstConnectedAt, lastConnectedAt time.Time
		createdAt, updatedAt              time.Time
		iconOut, catOut                   *string
		chainOut                          *int
		permsRaw                          []byte
	)
	err = r.pool.QueryRow(ctx, `
		INSERT INTO connected_sites (
			id, "ownerAddress", "chainId", origin, domain, "siteName",
			"iconUrl", category, "riskLevel", permissions,
			"firstConnectedAt", "lastConnectedAt", "createdAt", "updatedAt"
		) VALUES (
			gen_random_uuid()::text, $1, $2, $3, $4, $5,
			$6, $7, $8, $9::jsonb,
			NOW(), NOW(), NOW(), NOW()
		)
		ON CONFLICT ("ownerAddress", origin) DO UPDATE SET
			"chainId"          = $2,
			domain             = EXCLUDED.domain,
			"siteName"         = EXCLUDED."siteName",
			"iconUrl"          = EXCLUDED."iconUrl",
			category           = EXCLUDED.category,
			"riskLevel"        = EXCLUDED."riskLevel",
			permissions        = EXCLUDED.permissions,
			"lastConnectedAt"  = NOW(),
			"updatedAt"        = NOW()
		RETURNING id, "ownerAddress", "chainId", origin, domain, "siteName",
		          "iconUrl", category, "riskLevel", permissions,
		          "firstConnectedAt", "lastConnectedAt", "createdAt", "updatedAt"
	`,
		owner, req.ChainID, originURL, domain, siteName,
		iconURL, category, riskLevel, permsJSON,
	).Scan(&out.ID, &out.OwnerAddress, &chainOut, &out.Origin, &out.Domain, &out.SiteName,
		&iconOut, &catOut, &out.RiskLevel, &permsRaw,
		&firstConnectedAt, &lastConnectedAt, &createdAt, &updatedAt)
	if err != nil {
		return ConnectedSite{}, fmt.Errorf("upsert connected_sites: %w", err)
	}
	out.ChainID = chainOut
	out.IconURL = iconOut
	out.Category = catOut
	out.Permissions = decodePermissions(permsRaw)
	out.FirstConnectedAt = firstConnectedAt.UTC().Format(time.RFC3339Nano)
	out.LastConnectedAt = lastConnectedAt.UTC().Format(time.RFC3339Nano)
	out.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	out.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return out, nil
}

// DeleteConnectedSite mirrors removeConnectedSite: owner-scoped delete
// by site ID. Zero rows → ErrConnectedSiteNotFound (NestJS 404).
func (r *Repository) DeleteConnectedSite(ctx context.Context, ownerAddress, siteID string) error {
	owner := strings.TrimSpace(strings.ToLower(ownerAddress))
	if !addressRE.MatchString(owner) {
		return ErrInvalidOwnerAddr
	}
	if r.pool == nil {
		return ErrMutationRequiresPool
	}
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM connected_sites
		WHERE id = $1 AND "ownerAddress" = $2
	`, siteID, owner)
	if err != nil {
		return fmt.Errorf("delete connected_sites: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConnectedSiteNotFound
	}
	return nil
}

// normalizeSiteOrigin mirrors the NestJS URL-based normalization:
// origin → "<scheme>://<host>" lowercased + the lowercased host alone
// as domain. http and https only; missing scheme defaults to https.
func normalizeSiteOrigin(rawOrigin string) (originURL, domain string, err error) {
	trimmed := strings.TrimSpace(rawOrigin)
	if trimmed == "" {
		return "", "", ErrSiteOriginRequired
	}
	if !strings.HasPrefix(strings.ToLower(trimmed), "http") {
		trimmed = "https://" + trimmed
	}
	u, perr := url.Parse(trimmed)
	if perr != nil || u.Host == "" {
		return "", "", ErrSiteOriginRequired
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", ErrUnsupportedScheme
	}
	return scheme + "://" + strings.ToLower(u.Host), strings.ToLower(u.Hostname()), nil
}

// normalizeOptionalText mirrors the NestJS normalizeOptionalText:
// nil → nil; whitespace-only → nil; else trimmed pointer.
func normalizeOptionalText(p *string) *string {
	if p == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*p)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// normalizeRiskLevel: trim + lowercase, defaulting to "unknown" when
// the input is missing or blank. Matches the NestJS short-circuit.
func normalizeRiskLevel(p *string) string {
	v := normalizeOptionalText(p)
	if v == nil {
		return "unknown"
	}
	return strings.ToLower(*v)
}

// dedupePermissions trims each entry, drops empties, removes duplicates.
// Order-preserving so the stored JSON matches the input intent.
func dedupePermissions(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// decodePermissions parses the JSONB permissions column back into a
// []string, dropping non-string entries (matches the NestJS .filter).
// Empty / invalid raw → empty slice (never nil — the FE expects an array).
func decodePermissions(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var anys []any
	if err := json.Unmarshal(raw, &anys); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(anys))
	for _, v := range anys {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// -------- service layer --------

func (s *Service) UpsertConnectedSite(ctx context.Context, req UpsertConnectedSiteRequest) (ConnectedSite, error) {
	return s.repo.UpsertConnectedSite(ctx, req)
}

func (s *Service) RemoveConnectedSite(ctx context.Context, ownerAddress, siteID string) (RemoveConnectedSiteResponse, error) {
	if err := s.repo.DeleteConnectedSite(ctx, ownerAddress, siteID); err != nil {
		return RemoveConnectedSiteResponse{}, err
	}
	return RemoveConnectedSiteResponse{ID: siteID, Removed: true}, nil
}

// -------- handlers --------

func makeUpsertConnectedSiteHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authAddr := auth.AddressFromContext(r.Context())
		if authAddr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req UpsertConnectedSiteRequest
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
		out, err := svc.UpsertConnectedSite(r.Context(), req)
		mapMutationErr(w, r, err, "security upsert connected-site failed", req.Origin, http.StatusCreated, out)
	}
}

func makeRemoveConnectedSiteHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authAddr := auth.AddressFromContext(r.Context())
		if authAddr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		// Owner sourced from the query string (NestJS @Query('ownerAddress')).
		owner, err := auth.ResolveOwner(authAddr, r.URL.Query().Get("ownerAddress"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		out, err := svc.RemoveConnectedSite(r.Context(), owner, id)
		mapMutationErr(w, r, err, "security remove connected-site failed", id, http.StatusOK, out)
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
		errors.Is(err, ErrSiteNameRequired),
		errors.Is(err, ErrSiteOriginRequired),
		errors.Is(err, ErrUnsupportedScheme):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrConnectedSiteNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrMutationRequiresPool):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		slog.ErrorContext(r.Context(), logMsg, "err", err, "key", logKey)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
