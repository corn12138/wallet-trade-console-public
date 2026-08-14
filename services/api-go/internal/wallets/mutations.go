// Package wallets's mutation surface mirrors
// legacy NestJS wallets/wallets.service.ts createWatchOnlyWallet /
// createWalletGroup / updateWalletProfile. These three writes share the
// transactional + default-flag-management semantics — there can be only
// one default profile per ownerAddress, and assigning a wallet to a
// group requires the group to be owned by the same wallet.
package wallets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const defaultWeb3ChainID = 11155111

// Mutation sentinels. Handler maps each to the appropriate HTTP status.
var (
	ErrInvalidWalletAddr     = errors.New("wallets: invalid wallet address")
	ErrInvalidOwnerAddr      = errors.New("wallets: invalid owner address")
	ErrGroupNameRequired     = errors.New("wallets: group name is required")
	ErrGroupNotOwned         = errors.New("wallets: wallet group does not belong to the current owner")
	ErrOwnerRequiredForGroup = errors.New("wallets: ownerAddress is required when assigning a wallet group")
	ErrWalletNotFound        = errors.New("wallets: wallet not found")
	ErrMutationRequiresPool  = errors.New("wallets: cannot mutate without a database pool")
)

// CreateWatchOnlyRequest mirrors the @Body of POST /wallets/watch-only.
// OwnerAddress is required by the handler (resolved from the auth
// middleware) but kept as a Request field so the repo signature is
// self-contained.
type CreateWatchOnlyRequest struct {
	Address      string  `json:"address"`
	ChainID      *int    `json:"chainId,omitempty"`
	OwnerAddress string  `json:"ownerAddress"` // populated post-auth
	DisplayName  *string `json:"displayName,omitempty"`
	GroupID      *string `json:"groupId,omitempty"`
	IsImported   *bool   `json:"isImported,omitempty"`
	Notes        *string `json:"notes,omitempty"`
	AvatarSeed   *string `json:"avatarSeed,omitempty"`
}

// CreateGroupRequest mirrors POST /wallets/groups @Body.
type CreateGroupRequest struct {
	OwnerAddress string  `json:"ownerAddress"`
	Name         string  `json:"name"`
	Color        *string `json:"color,omitempty"`
}

// UpdateProfileRequest mirrors PUT /wallets/:walletId/profile @Body.
// Pointer-typed fields distinguish "absent" from "set to null/false".
type UpdateProfileRequest struct {
	OwnerAddress string  `json:"ownerAddress"`
	DisplayName  *string `json:"displayName,omitempty"`
	GroupID      *string `json:"groupId,omitempty"`
	IsDefault    *bool   `json:"isDefault,omitempty"`
	IsImported   *bool   `json:"isImported,omitempty"`
	Notes        *string `json:"notes,omitempty"`
	AvatarSeed   *string `json:"avatarSeed,omitempty"`
}

// UpsertWatchOnly mirrors createWatchOnlyWallet. Multi-step transactional:
//
//  1. Optionally ensure the requested group is owned by ownerAddress.
//  2. UPSERT the web3_wallets row.
//  3. If no owner, return the wallet record without profile bookkeeping.
//  4. Otherwise: read existing profile + check if owner has a default;
//     promote this profile to default if it already was OR the owner has
//     no default; demote sibling profiles; UPSERT the profile row.
func (r *Repository) UpsertWatchOnly(ctx context.Context, req CreateWatchOnlyRequest) (WalletRecord, error) {
	// Validate before the nil-pool gate so bad input always 400s,
	// even when the deploy is in degraded mode. (Otherwise an
	// uninitialized pool would mask malformed bodies as 503.)
	addr := strings.TrimSpace(strings.ToLower(req.Address))
	if !addressRE.MatchString(addr) {
		return WalletRecord{}, ErrInvalidWalletAddr
	}
	owner := strings.TrimSpace(strings.ToLower(req.OwnerAddress))
	if owner != "" && !addressRE.MatchString(owner) {
		return WalletRecord{}, ErrInvalidOwnerAddr
	}
	if req.GroupID != nil && *req.GroupID != "" && owner == "" {
		return WalletRecord{}, ErrOwnerRequiredForGroup
	}
	if r.pool == nil {
		return WalletRecord{}, ErrMutationRequiresPool
	}
	chainID := defaultWeb3ChainID
	if req.ChainID != nil {
		chainID = *req.ChainID
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WalletRecord{}, fmt.Errorf("begin tx: %w", err)
	}
	defer rollbackOnError(ctx, tx)
	if owner != "" {
		if err := ensureGroupOwnership(ctx, tx, owner, req.GroupID); err != nil {
			return WalletRecord{}, err
		}
	}
	wallet, err := upsertWeb3Wallet(ctx, tx, addr, chainID)
	if err != nil {
		return WalletRecord{}, err
	}
	if owner == "" {
		if err := tx.Commit(ctx); err != nil {
			return WalletRecord{}, fmt.Errorf("commit: %w", err)
		}
		return wallet, nil
	}
	if err := upsertProfileWithDefaultManagement(ctx, tx, wallet.ID, owner, profileUpdate{
		displayName: req.DisplayName,
		groupID:     req.GroupID,
		isImported:  req.IsImported,
		notes:       req.Notes,
		avatarSeed:  req.AvatarSeed,
	}); err != nil {
		return WalletRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WalletRecord{}, fmt.Errorf("commit: %w", err)
	}
	return wallet, nil
}

// CreateGroup mirrors createWalletGroup. SortOrder = last group's
// sortOrder + 1, owner-scoped. Group name must be non-empty after trim.
func (r *Repository) CreateGroup(ctx context.Context, req CreateGroupRequest) (WalletGroup, error) {
	owner := strings.TrimSpace(strings.ToLower(req.OwnerAddress))
	if !addressRE.MatchString(owner) {
		return WalletGroup{}, ErrInvalidOwnerAddr
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return WalletGroup{}, ErrGroupNameRequired
	}
	if r.pool == nil {
		return WalletGroup{}, ErrMutationRequiresPool
	}
	color := normalizeOptionalText(req.Color)
	// Compute the next sortOrder. -1 base means the first row lands at 0.
	var lastOrder int
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(MAX("sortOrder"), -1)
		FROM wallet_groups
		WHERE "ownerAddress" = $1
	`, owner).Scan(&lastOrder)
	if err != nil {
		return WalletGroup{}, fmt.Errorf("query max sortOrder: %w", err)
	}
	g := WalletGroup{OwnerAddress: owner, Name: name, Color: color, SortOrder: lastOrder + 1}
	var (
		createdAt, updatedAt time.Time
		colorOut             *string
	)
	err = r.pool.QueryRow(ctx, `
		INSERT INTO wallet_groups (id, "ownerAddress", name, color, "sortOrder", "createdAt", "updatedAt")
		VALUES (gen_random_uuid()::text, $1, $2, $3, $4, NOW(), NOW())
		RETURNING id, color, "createdAt", "updatedAt"
	`, owner, name, color, g.SortOrder).Scan(&g.ID, &colorOut, &createdAt, &updatedAt)
	if err != nil {
		return WalletGroup{}, fmt.Errorf("insert wallet_group: %w", err)
	}
	g.Color = colorOut
	g.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	g.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	g.WalletCount = 0
	return g, nil
}

// UpdateProfile mirrors updateWalletProfile. Looks up the wallet row,
// validates group ownership (if changing groupId), then runs the same
// default-flag promotion logic as UpsertWatchOnly. Returns the updated
// profile row in the lightweight WalletProfile shape.
func (r *Repository) UpdateProfile(ctx context.Context, walletID string, req UpdateProfileRequest) (WalletProfile, error) {
	owner := strings.TrimSpace(strings.ToLower(req.OwnerAddress))
	if !addressRE.MatchString(owner) {
		return WalletProfile{}, ErrInvalidOwnerAddr
	}
	if r.pool == nil {
		return WalletProfile{}, ErrMutationRequiresPool
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return WalletProfile{}, fmt.Errorf("begin tx: %w", err)
	}
	defer rollbackOnError(ctx, tx)
	// Confirm wallet exists. NestJS throws NotFoundException here.
	var exists string
	err = tx.QueryRow(ctx, `SELECT id FROM web3_wallets WHERE id = $1`, walletID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return WalletProfile{}, ErrWalletNotFound
	}
	if err != nil {
		return WalletProfile{}, fmt.Errorf("lookup wallet: %w", err)
	}
	if err := ensureGroupOwnership(ctx, tx, owner, req.GroupID); err != nil {
		return WalletProfile{}, err
	}
	if err := upsertProfileWithDefaultManagement(ctx, tx, walletID, owner, profileUpdate{
		displayName: req.DisplayName,
		groupID:     req.GroupID,
		isDefault:   req.IsDefault,
		isImported:  req.IsImported,
		notes:       req.Notes,
		avatarSeed:  req.AvatarSeed,
	}); err != nil {
		return WalletProfile{}, err
	}
	// Read back the updated profile for the response.
	out, err := readProfileForOwnerWallet(ctx, tx, owner, walletID)
	if err != nil {
		return WalletProfile{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WalletProfile{}, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// profileUpdate carries the optional patch fields for the profile
// upsert path. Pointers preserve "absent" → don't touch this column.
type profileUpdate struct {
	displayName *string
	groupID     *string
	isDefault   *bool
	isImported  *bool
	notes       *string
	avatarSeed  *string
}

// upsertProfileWithDefaultManagement runs the multi-step default-flag
// logic NestJS bundles inside the upsert calls:
//
//  1. SELECT existing profile (if any) for this (owner, wallet)
//  2. SELECT whether owner has any other default profile
//  3. shouldBeDefault =
//     isDefault (if explicitly supplied)
//     OR existingProfile.isDefault
//     OR (no other default exists)
//  4. If shouldBeDefault, demote all sibling profiles to isDefault=false
//  5. UPSERT this profile with the computed isDefault
//
// The table is wallet_profiles — the Prisma @@map of Web3WalletProfile (NOT
// "web3_wallet_profiles"; only the wallet table is web3_wallets). Reads in
// wallets.go use the same name; the authenticated wallets-write smoke is the
// regression guard (nil-pool unit tests never exercise this SQL).
func upsertProfileWithDefaultManagement(ctx context.Context, tx pgx.Tx, walletID, owner string, p profileUpdate) error {
	var existingIsDefault bool
	var existingFound bool
	err := tx.QueryRow(ctx, `
		SELECT "isDefault" FROM wallet_profiles
		WHERE "ownerAddress" = $1 AND "walletId" = $2
	`, owner, walletID).Scan(&existingIsDefault)
	if err == nil {
		existingFound = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("query existing profile: %w", err)
	}
	var otherDefaultID string
	hasOtherDefault := true
	err = tx.QueryRow(ctx, `
		SELECT id FROM wallet_profiles
		WHERE "ownerAddress" = $1 AND "isDefault" = TRUE AND "walletId" <> $2
		LIMIT 1
	`, owner, walletID).Scan(&otherDefaultID)
	if errors.Is(err, pgx.ErrNoRows) {
		hasOtherDefault = false
	} else if err != nil {
		return fmt.Errorf("query other-default profile: %w", err)
	}
	shouldBeDefault := !hasOtherDefault
	if existingFound {
		shouldBeDefault = existingIsDefault || !hasOtherDefault
	}
	if p.isDefault != nil {
		shouldBeDefault = *p.isDefault
	}
	if shouldBeDefault {
		if _, err := tx.Exec(ctx, `
			UPDATE wallet_profiles
			SET "isDefault" = FALSE, "updatedAt" = NOW()
			WHERE "ownerAddress" = $1 AND "walletId" <> $2
		`, owner, walletID); err != nil {
			return fmt.Errorf("demote siblings: %w", err)
		}
	}
	// UPSERT. On conflict, apply the supplied fields and the computed
	// isDefault. nil-pointer fields fall back to COALESCE(EXCLUDED, current).
	if _, err := tx.Exec(ctx, `
		INSERT INTO wallet_profiles (
			id, "walletId", "ownerAddress", "displayName", "groupId",
			"isDefault", "isImported", "avatarSeed", "notes",
			"createdAt", "updatedAt"
		) VALUES (
			gen_random_uuid()::text, $1, $2, $3, $4, $5, COALESCE($6, FALSE), $7, $8, NOW(), NOW()
		)
		ON CONFLICT ("ownerAddress", "walletId") DO UPDATE SET
			"displayName" = COALESCE(EXCLUDED."displayName", wallet_profiles."displayName"),
			"groupId"     = CASE WHEN $9::boolean THEN EXCLUDED."groupId" ELSE wallet_profiles."groupId" END,
			"isDefault"   = $5,
			"isImported"  = COALESCE(EXCLUDED."isImported", wallet_profiles."isImported"),
			"avatarSeed"  = COALESCE(EXCLUDED."avatarSeed", wallet_profiles."avatarSeed"),
			"notes"       = COALESCE(EXCLUDED."notes", wallet_profiles."notes"),
			"updatedAt"   = NOW()
	`,
		walletID, owner,
		normalizePtrText(p.displayName),
		normalizePtrText(p.groupID),
		shouldBeDefault,
		p.isImported,
		normalizePtrText(p.avatarSeed),
		normalizePtrText(p.notes),
		p.groupID != nil,
	); err != nil {
		return fmt.Errorf("upsert profile: %w", err)
	}
	return nil
}

func ensureGroupOwnership(ctx context.Context, tx pgx.Tx, owner string, groupID *string) error {
	if groupID == nil || *groupID == "" {
		return nil
	}
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id FROM wallet_groups
		WHERE id = $1 AND "ownerAddress" = $2
	`, *groupID, owner).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrGroupNotOwned
	}
	if err != nil {
		return fmt.Errorf("ensure group ownership: %w", err)
	}
	return nil
}

func upsertWeb3Wallet(ctx context.Context, tx pgx.Tx, address string, chainID int) (WalletRecord, error) {
	var w WalletRecord
	var lastLoginAt *time.Time
	var createdAt, updatedAt time.Time
	var linkedUserID *string
	err := tx.QueryRow(ctx, `
		INSERT INTO web3_wallets (id, address, "chainId", "createdAt", "updatedAt")
		VALUES (gen_random_uuid()::text, $1, $2, NOW(), NOW())
		ON CONFLICT (address) DO UPDATE SET
			"chainId"   = EXCLUDED."chainId",
			"updatedAt" = NOW()
		RETURNING id, address, "chainId", "userId", "lastLoginAt", "createdAt", "updatedAt"
	`, address, chainID).Scan(&w.ID, &w.Address, &w.ChainID, &linkedUserID, &lastLoginAt, &createdAt, &updatedAt)
	if err != nil {
		return WalletRecord{}, fmt.Errorf("upsert web3_wallet: %w", err)
	}
	w.LinkedUserID = linkedUserID
	if lastLoginAt != nil {
		s := lastLoginAt.UTC().Format(time.RFC3339Nano)
		w.LastLoginAt = &s
		w.WalletType = "connected"
		w.AuthState = "siwe-authenticated"
	} else {
		w.WalletType = "watch-only"
		w.AuthState = "tracked"
	}
	w.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	w.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return w, nil
}

func readProfileForOwnerWallet(ctx context.Context, tx pgx.Tx, owner, walletID string) (WalletProfile, error) {
	var p WalletProfile
	var createdAt, updatedAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT p.id, p."ownerAddress", p."displayName", p."groupId",
		       g.name, g.color,
		       p."isDefault", p."isImported", p."avatarSeed", p.notes,
		       p."createdAt", p."updatedAt"
		FROM wallet_profiles p
		LEFT JOIN wallet_groups g ON g.id = p."groupId"
		WHERE p."ownerAddress" = $1 AND p."walletId" = $2
	`, owner, walletID).Scan(
		&p.ID, &p.OwnerAddress, &p.DisplayName, &p.GroupID,
		&p.GroupName, &p.GroupColor,
		&p.IsDefault, &p.IsImported, &p.AvatarSeed, &p.Notes,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return WalletProfile{}, fmt.Errorf("read profile: %w", err)
	}
	p.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	p.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return p, nil
}

// rollbackOnError is the deferred cleanup. If the transaction has
// already been committed, Rollback is a no-op error we swallow.
func rollbackOnError(ctx context.Context, tx pgx.Tx) {
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		slog.WarnContext(ctx, "wallet mutation rollback failed", "err", err)
	}
}

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

func normalizePtrText(p *string) *string {
	return normalizeOptionalText(p)
}

// Service-layer wrappers. Degraded contract (nil pool → 503-shaped
// sentinel) flows through unchanged.
func (s *Service) CreateWatchOnly(ctx context.Context, req CreateWatchOnlyRequest) (WalletRecord, error) {
	return s.repo.UpsertWatchOnly(ctx, req)
}

func (s *Service) CreateGroup(ctx context.Context, req CreateGroupRequest) (WalletGroup, error) {
	return s.repo.CreateGroup(ctx, req)
}

func (s *Service) UpdateProfile(ctx context.Context, walletID string, req UpdateProfileRequest) (WalletProfile, error) {
	return s.repo.UpdateProfile(ctx, walletID, req)
}

// Mutation handlers. Each reads the auth context for the authenticated
// address, resolves the optional body.ownerAddress against it (so the
// caller can't act on behalf of another wallet), then dispatches.
func makeCreateWatchOnlyHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authAddr := auth.AddressFromContext(r.Context())
		if authAddr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req CreateWatchOnlyRequest
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
		out, err := svc.CreateWatchOnly(r.Context(), req)
		mapMutationErr(w, r, err, "watch-only create failed", req.Address, http.StatusCreated, out)
	}
}

func makeCreateGroupHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authAddr := auth.AddressFromContext(r.Context())
		if authAddr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req CreateGroupRequest
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
		out, err := svc.CreateGroup(r.Context(), req)
		mapMutationErr(w, r, err, "wallet group create failed", req.Name, http.StatusCreated, out)
	}
}

func makeUpdateProfileHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		walletID := chi.URLParam(r, "walletId")
		if walletID == "" {
			http.Error(w, "walletId required", http.StatusBadRequest)
			return
		}
		authAddr := auth.AddressFromContext(r.Context())
		if authAddr == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req UpdateProfileRequest
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
		out, err := svc.UpdateProfile(r.Context(), walletID, req)
		mapMutationErr(w, r, err, "wallet profile update failed", walletID, http.StatusOK, out)
	}
}

// mapResolveOwnerErr writes the right status for each ResolveOwner
// sentinel. Kept in one place so the three mutation handlers above
// match the NestJS resolveAuthenticatedOwnerAddress error mapping.
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

// mapMutationErr is the shared sentinel-to-status mapping for the
// three mutation handlers. successStatus is 201 for create paths and
// 200 for update. payload is written when err == nil.
func mapMutationErr(w http.ResponseWriter, r *http.Request, err error, logMsg, logKey string, successStatus int, payload any) {
	switch {
	case err == nil:
		writeJSON(w, successStatus, payload)
	case errors.Is(err, ErrInvalidWalletAddr),
		errors.Is(err, ErrInvalidOwnerAddr),
		errors.Is(err, ErrGroupNameRequired),
		errors.Is(err, ErrOwnerRequiredForGroup),
		errors.Is(err, ErrGroupNotOwned):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrWalletNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, ErrMutationRequiresPool):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	default:
		slog.ErrorContext(r.Context(), logMsg, "err", err, "key", logKey)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
