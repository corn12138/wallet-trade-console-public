// Package wallets is the Go port of legacy NestJS wallets.
//
// All five frontend-used routes are implemented here (reads) and in
// mutations.go (writes): GET /, GET /manager, POST /watch-only, POST /groups,
// PUT /{walletId}/profile. Matching the deployed NestJS WalletsController
// (class-level @Public + @UseGuards(Web3AuthGuard); every handler resolves the
// owner via resolveAuthenticatedOwnerAddress), EVERY route is web3-guarded and
// owner-pinned to the authenticated wallet — including the reads, which
// previously mounted public (an over-exposure of owner-private wallet data vs
// the NestJS 401). See Router and guard-parity.md.
//
// The NestJS getWalletManager auto-creates a profile row when the
// owner has a wallet but no matching profile (ensureOwnerWalletProfile).
// That write side-effect is intentionally NOT replicated on the read path —
// it's a UX self-heal, not a read invariant; it lives in the watch-only /
// profile-update transactions in mutations.go where it naturally belongs.
package wallets

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
	"golang.org/x/sync/errgroup"
)

var addressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

// WalletRecord mirrors the AtlasWalletRecord shape on the FE.
type WalletRecord struct {
	ID           string         `json:"id"`
	Address      string         `json:"address"`
	ChainID      int            `json:"chainId"`
	WalletType   string         `json:"walletType"`
	AuthState    string         `json:"authState"`
	LinkedUserID *string        `json:"linkedUserId"`
	LastLoginAt  *string        `json:"lastLoginAt"`
	CreatedAt    string         `json:"createdAt"`
	UpdatedAt    string         `json:"updatedAt"`
	Profile      *WalletProfile `json:"profile,omitempty"`
}

// WalletProfile is the wallet_profiles join on the manager endpoint.
type WalletProfile struct {
	ID           string  `json:"id"`
	OwnerAddress string  `json:"ownerAddress"`
	DisplayName  *string `json:"displayName"`
	GroupID      *string `json:"groupId"`
	GroupName    *string `json:"groupName"`
	GroupColor   *string `json:"groupColor"`
	IsDefault    bool    `json:"isDefault"`
	IsImported   bool    `json:"isImported"`
	AvatarSeed   *string `json:"avatarSeed"`
	Notes        *string `json:"notes"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
}

// WalletGroup mirrors AtlasWalletGroup.
type WalletGroup struct {
	ID           string  `json:"id"`
	OwnerAddress string  `json:"ownerAddress"`
	Name         string  `json:"name"`
	Color        *string `json:"color"`
	SortOrder    int     `json:"sortOrder"`
	WalletCount  int     `json:"walletCount"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
}

// HiddenAsset mirrors AtlasHiddenAsset (also exposed from settings
// package — kept here as a separate type to avoid an import cycle).
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

// ManagerSummary is the per-owner counts strip rendered at the top of
// the WalletManager screen.
type ManagerSummary struct {
	TotalWallets     int     `json:"totalWallets"`
	ConnectedWallets int     `json:"connectedWallets"`
	WatchOnlyWallets int     `json:"watchOnlyWallets"`
	ImportedWallets  int     `json:"importedWallets"`
	GroupedWallets   int     `json:"groupedWallets"`
	HiddenAssetCount int     `json:"hiddenAssetCount"`
	DefaultWalletID  *string `json:"defaultWalletId"`
}

// WalletManager is GET /wallets/manager response shape.
type WalletManager struct {
	OwnerAddress *string        `json:"ownerAddress"`
	Wallets      []WalletRecord `json:"wallets"`
	Groups       []WalletGroup  `json:"groups"`
	HiddenAssets []HiddenAsset  `json:"hiddenAssets"`
	Summary      ManagerSummary `json:"summary"`
}

// ErrPoolUnavailable mirrors the sibling pattern.
var ErrPoolUnavailable = errors.New("wallets repository: database pool not configured")

// Repository owns the SQL surface. nil pool yields ErrPoolUnavailable
// from every method.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository binds a repo. nil pool tolerated.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// CountWallets returns the total wallet count, optionally filtered by
// chainID. Used by portfolio.GetSummary's `trackedWallets` field —
// matches `this.prisma.web3Wallet.count({where: chainId ? {chainId} : undefined})`.
func (r *Repository) CountWallets(ctx context.Context, chainID *int) (int, error) {
	if r.pool == nil {
		return 0, ErrPoolUnavailable
	}
	args := []any{}
	where := ""
	if chainID != nil {
		args = append(args, *chainID)
		where = `WHERE "chainId" = $1`
	}
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM web3_wallets `+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count web3_wallets: %w", err)
	}
	return n, nil
}

// listWallets returns base wallet rows without any profile/group
// joins. Used by GET /wallets.
func (r *Repository) listWallets(ctx context.Context, address string, chainID *int) ([]WalletRecord, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	args := []any{}
	clauses := []string{}
	if address != "" {
		args = append(args, address)
		clauses = append(clauses, `address = $`+strconv.Itoa(len(args)))
	}
	if chainID != nil {
		args = append(args, *chainID)
		clauses = append(clauses, `"chainId" = $`+strconv.Itoa(len(args)))
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, address, "chainId", "userId", "lastLoginAt", "createdAt", "updatedAt"
		FROM web3_wallets
		`+where+`
		ORDER BY "lastLoginAt" DESC NULLS LAST, "createdAt" DESC
		LIMIT 50
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query web3_wallets: %w", err)
	}
	defer rows.Close()
	out := make([]WalletRecord, 0)
	for rows.Next() {
		w, err := scanBaseWallet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate web3_wallets: %w", err)
	}
	return out, nil
}

// listManagedWallets returns wallet rows + the matching profile row
// (with the joined group's name/color) for the given ownerAddress.
// Matches either the owner's own wallet OR any wallet they have a
// profile row for.
func (r *Repository) listManagedWallets(ctx context.Context, ownerAddress string, chainID *int) ([]WalletRecord, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	args := []any{ownerAddress}
	clauses := []string{
		`(w.address = $1 OR EXISTS (
			SELECT 1 FROM wallet_profiles p2 WHERE p2."walletId" = w.id AND p2."ownerAddress" = $1
		))`,
	}
	if chainID != nil {
		args = append(args, *chainID)
		clauses = append(clauses, `w."chainId" = $`+strconv.Itoa(len(args)))
	}
	rows, err := r.pool.Query(ctx, `
		SELECT w.id, w.address, w."chainId", w."userId", w."lastLoginAt", w."createdAt", w."updatedAt",
		       p.id, p."ownerAddress", p."displayName", p."groupId", p."isDefault", p."isImported",
		       p."avatarSeed", p.notes, p."createdAt", p."updatedAt",
		       g.name, g.color
		FROM web3_wallets w
		LEFT JOIN wallet_profiles p ON p."walletId" = w.id AND p."ownerAddress" = $1
		LEFT JOIN wallet_groups g ON g.id = p."groupId"
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY w."lastLoginAt" DESC NULLS LAST, w."createdAt" DESC
		LIMIT 50
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query managed wallets: %w", err)
	}
	defer rows.Close()
	out := make([]WalletRecord, 0)
	for rows.Next() {
		var (
			w                              WalletRecord
			userID                         *string
			lastLogin                      *time.Time
			created, updated               time.Time
			pID, pOwner, pDisplay, pGroup  *string
			pAvatar, pNotes, gName, gColor *string
			pIsDefault, pIsImported        *bool
			pCreated, pUpdated             *time.Time
		)
		if err := rows.Scan(&w.ID, &w.Address, &w.ChainID, &userID, &lastLogin, &created, &updated,
			&pID, &pOwner, &pDisplay, &pGroup, &pIsDefault, &pIsImported,
			&pAvatar, &pNotes, &pCreated, &pUpdated,
			&gName, &gColor); err != nil {
			return nil, fmt.Errorf("scan managed wallets: %w", err)
		}
		w.LinkedUserID = userID
		w.CreatedAt = created.UTC().Format(time.RFC3339Nano)
		w.UpdatedAt = updated.UTC().Format(time.RFC3339Nano)
		if lastLogin != nil {
			s := lastLogin.UTC().Format(time.RFC3339Nano)
			w.LastLoginAt = &s
		}
		walletType, authState := deriveWalletTypeAndAuth(lastLogin, derefBool(pIsImported))
		w.WalletType = walletType
		w.AuthState = authState
		if pID != nil {
			profile := WalletProfile{
				ID:           *pID,
				OwnerAddress: derefStr(pOwner),
				DisplayName:  pDisplay,
				GroupID:      pGroup,
				GroupName:    gName,
				GroupColor:   gColor,
				IsDefault:    derefBool(pIsDefault),
				IsImported:   derefBool(pIsImported),
				AvatarSeed:   pAvatar,
				Notes:        pNotes,
			}
			if pCreated != nil {
				profile.CreatedAt = pCreated.UTC().Format(time.RFC3339Nano)
			}
			if pUpdated != nil {
				profile.UpdatedAt = pUpdated.UTC().Format(time.RFC3339Nano)
			}
			w.Profile = &profile
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed wallets: %w", err)
	}
	return out, nil
}

// listWalletGroups returns the wallet_groups for the owner with a
// COUNT(*) of attached profiles per group (matches Prisma _count).
func (r *Repository) listWalletGroups(ctx context.Context, ownerAddress string) ([]WalletGroup, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	rows, err := r.pool.Query(ctx, `
		SELECT g.id, g."ownerAddress", g.name, g.color, g."sortOrder",
		       (SELECT COUNT(*) FROM wallet_profiles p WHERE p."groupId" = g.id) AS wallet_count,
		       g."createdAt", g."updatedAt"
		FROM wallet_groups g
		WHERE g."ownerAddress" = $1
		ORDER BY g."sortOrder" ASC, g."createdAt" ASC
	`, ownerAddress)
	if err != nil {
		return nil, fmt.Errorf("query wallet_groups: %w", err)
	}
	defer rows.Close()
	out := make([]WalletGroup, 0)
	for rows.Next() {
		var (
			g                WalletGroup
			created, updated time.Time
		)
		if err := rows.Scan(&g.ID, &g.OwnerAddress, &g.Name, &g.Color, &g.SortOrder,
			&g.WalletCount, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan wallet_groups: %w", err)
		}
		g.CreatedAt = created.UTC().Format(time.RFC3339Nano)
		g.UpdatedAt = updated.UTC().Format(time.RFC3339Nano)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wallet_groups: %w", err)
	}
	return out, nil
}

// listHiddenAssets is a private duplicate of settings.ListHiddenAssets
// (defined here to avoid an import cycle and a cross-package dep).
func (r *Repository) listHiddenAssets(ctx context.Context, ownerAddress string, chainID *int) ([]HiddenAsset, error) {
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
			h                HiddenAsset
			created, updated time.Time
		)
		if err := rows.Scan(&h.ID, &h.OwnerAddress, &h.WalletAddress, &h.ChainID,
			&h.AssetKey, &h.AssetAddress, &h.Symbol, &h.Reason, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan hidden_assets: %w", err)
		}
		h.CreatedAt = created.UTC().Format(time.RFC3339Nano)
		h.UpdatedAt = updated.UTC().Format(time.RFC3339Nano)
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hidden_assets: %w", err)
	}
	return out, nil
}

func scanBaseWallet(rows pgx.Rows) (WalletRecord, error) {
	var (
		w                WalletRecord
		userID           *string
		lastLogin        *time.Time
		created, updated time.Time
	)
	if err := rows.Scan(&w.ID, &w.Address, &w.ChainID, &userID, &lastLogin, &created, &updated); err != nil {
		return WalletRecord{}, fmt.Errorf("scan web3_wallets: %w", err)
	}
	w.LinkedUserID = userID
	w.CreatedAt = created.UTC().Format(time.RFC3339Nano)
	w.UpdatedAt = updated.UTC().Format(time.RFC3339Nano)
	if lastLogin != nil {
		s := lastLogin.UTC().Format(time.RFC3339Nano)
		w.LastLoginAt = &s
	}
	walletType, authState := deriveWalletTypeAndAuth(lastLogin, false)
	w.WalletType = walletType
	w.AuthState = authState
	return w, nil
}

// deriveWalletTypeAndAuth mirrors serializeManagedWallet / serializeWalletRecord:
//   - any wallet with a lastLoginAt → "connected" / "siwe-authenticated"
//   - otherwise, profile.isImported → "imported" / "imported-local"
//   - otherwise → "watch-only" / "tracked"
//
// Base /wallets has no profile join, so isImported is always false there,
// degenerating to "watch-only" / "tracked" for never-signed-in rows.
func deriveWalletTypeAndAuth(lastLogin *time.Time, isImported bool) (string, string) {
	if lastLogin != nil {
		return "connected", "siwe-authenticated"
	}
	if isImported {
		return "imported", "imported-local"
	}
	return "watch-only", "tracked"
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// --- service ---

// Service composes the manager response with degraded-mode handling.
type Service struct {
	repo *Repository
}

// NewService binds a service over a repo.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// GetWallets returns the wallet list. Empty / invalid address returns
// nothing, nil pool degrades to []. address is optional (filter only).
func (s *Service) GetWallets(ctx context.Context, address string, chainID *int) ([]WalletRecord, error) {
	addr := ""
	if address != "" {
		normalized, ok := normalizeIfValid(address)
		if !ok {
			return []WalletRecord{}, nil
		}
		addr = normalized
	}
	rows, err := s.repo.listWallets(ctx, addr, chainID)
	if errors.Is(err, ErrPoolUnavailable) {
		return []WalletRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// GetWalletManager runs the three parallel queries and assembles the
// envelope. Empty / invalid owner → fully empty envelope with
// ownerAddress=null (matches NestJS no-owner path).
func (s *Service) GetWalletManager(ctx context.Context, ownerAddress string, chainID *int) (WalletManager, error) {
	addr, ok := normalizeIfValid(ownerAddress)
	if !ok {
		return WalletManager{
			OwnerAddress: nil,
			Wallets:      []WalletRecord{},
			Groups:       []WalletGroup{},
			HiddenAssets: []HiddenAsset{},
			Summary:      ManagerSummary{},
		}, nil
	}

	g, gctx := errgroup.WithContext(ctx)
	var (
		wallets []WalletRecord
		groups  []WalletGroup
		hidden  []HiddenAsset
	)
	g.Go(func() error { var e error; wallets, e = s.repo.listManagedWallets(gctx, addr, chainID); return e })
	g.Go(func() error { var e error; groups, e = s.repo.listWalletGroups(gctx, addr); return e })
	g.Go(func() error { var e error; hidden, e = s.repo.listHiddenAssets(gctx, addr, chainID); return e })

	if err := g.Wait(); err != nil {
		if errors.Is(err, ErrPoolUnavailable) {
			return WalletManager{
				OwnerAddress: &addr,
				Wallets:      []WalletRecord{},
				Groups:       []WalletGroup{},
				HiddenAssets: []HiddenAsset{},
				Summary:      ManagerSummary{},
			}, nil
		}
		return WalletManager{}, err
	}

	// Compute summary.
	var (
		connected, watchOnly, imported, grouped int
		defaultID                               *string
	)
	for _, w := range wallets {
		switch w.WalletType {
		case "connected":
			connected++
		case "watch-only":
			watchOnly++
		case "imported":
			imported++
		}
		if w.Profile != nil {
			if w.Profile.GroupID != nil {
				grouped++
			}
			if w.Profile.IsDefault && defaultID == nil {
				id := w.ID
				defaultID = &id
			}
		}
	}

	return WalletManager{
		OwnerAddress: &addr,
		Wallets:      wallets,
		Groups:       groups,
		HiddenAssets: hidden,
		Summary: ManagerSummary{
			TotalWallets:     len(wallets),
			ConnectedWallets: connected,
			WatchOnlyWallets: watchOnly,
			ImportedWallets:  imported,
			GroupedWallets:   grouped,
			HiddenAssetCount: len(hidden),
			DefaultWalletID:  defaultID,
		},
	}, nil
}

func normalizeIfValid(addr string) (string, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" || !addressRE.MatchString(addr) {
		return "", false
	}
	return strings.ToLower(addr), true
}

// --- router ---

// Router mounts /api/wallets. Mirrors the deployed NestJS WalletsController,
// where EVERY route is web3-guarded (class-level @UseGuards(Web3AuthGuard)) and
// owner-pinned to the authenticated wallet (resolveAuthenticatedOwnerAddress).
//
// Reads (/ and /manager) are owner-pinned via auth.ResolveOwner inside each
// handler and mount inside a guarded group — the guard is applied when
// authMiddleware is configured; a nil guard (dev without JWT_SECRET) still
// mounts them, but the owner-pin returns 401 without an authenticated address
// (same as settings/portfolio). This replaces the earlier public-read mounting,
// which over-exposed owner-private wallet data (the NestJS reads 401
// unauthenticated). Mutation routes (POST /watch-only, POST /groups, PUT
// /{walletId}/profile) mount only when authMiddleware is non-nil — same pattern
// as settings and token.
func Router(svc *Service, authMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		if authMiddleware != nil {
			r.Use(authMiddleware)
		}
		r.Get("/", makeListHandler(svc))
		r.Get("/manager", makeManagerHandler(svc))
	})
	if authMiddleware != nil {
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware)
			r.Post("/watch-only", makeCreateWatchOnlyHandler(svc))
			r.Post("/groups", makeCreateGroupHandler(svc))
			r.Put("/{walletId}/profile", makeUpdateProfileHandler(svc))
		})
	}
	return r
}

// makeListHandler owner-pins the read to the authenticated wallet, mirroring
// NestJS resolveAuthenticatedOwnerAddress(authenticated, ?address=, 'address'):
// no ?address= → the JWT wallet; a mismatched ?address= → 403; no authenticated
// address → 401. The resolved owner is the address filter listWallets applies,
// so the list is always scoped to the caller's own wallet (same as NestJS).
func makeListHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), q.Get("address"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		chainID := parseChainID(q.Get("chainId"))
		out, err := svc.GetWallets(r.Context(), owner, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "wallets list failed", "err", err)
			http.Error(w, "failed to load wallets", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeManagerHandler owner-pins the manager read the same way, against
// ?ownerAddress= (NestJS resolveAuthenticatedOwnerAddress default label).
func makeManagerHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), q.Get("ownerAddress"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		chainID := parseChainID(q.Get("chainId"))
		out, err := svc.GetWalletManager(r.Context(), owner, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "wallet manager failed", "err", err)
			http.Error(w, "failed to load wallet manager", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func parseChainID(raw string) *int {
	if raw == "" {
		return nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &v
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("wallets response encode failed", "err", err)
	}
}
