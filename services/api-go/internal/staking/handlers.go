package staking

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

	"github.com/go-chi/chi/v5"
)

var addressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

// Store is the read+write surface used by the HTTP handlers. Tests
// pass a stub.
type Store interface {
	ListAllPools(ctx context.Context, f PoolFilters) ([]PoolView, error)
	FindPool(ctx context.Context, id string) (PoolView, error)
	CreatePool(ctx context.Context, in CreatePoolInput) (PoolView, error)
	UpdatePool(ctx context.Context, id string, in UpdatePoolInput) (PoolView, error)
	GetPoolStats(ctx context.Context) (PoolStats, error)
	ListActiveUserStakes(ctx context.Context, userAddress string) ([]UserStake, error)
	RecordStake(ctx context.Context, in RecordStakeInput) (UserStake, error)
	RecordUnstake(ctx context.Context, stakeID string) (UserStake, error)
}

// Service wires a Store for the HTTP layer.
type Service struct {
	store Store
}

// NewService accepts a nil store for the degraded contract.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Router mounts /api/staking with the 8 NestJS routes, split by guard.
//
// The pool CATALOG reads (`/pools`, `/pools/stats`, `/pools/{id}`) are
// Go-canonical PUBLIC market/catalog data — the FE advanced-earn page loads
// `/pools` + `/pools/stats` with plain `fetch` and NO auth header (broken
// pre-login on the deployed NestJS, whose StakingController carries no @Public
// so the global JwtAuthGuard 401s them). Same rationale as the token GET reads +
// earn/products + bridge + swap. The admin pool mutations (POST/PUT `/pools`),
// the user-specific stakes read (`/user/{address}`), and the user stake/unstake
// writes stay ACCESS-guarded (parity with the NestJS global JwtAuthGuard) — a nil
// middleware (dev without JWT_SECRET) mounts them open exactly as before.
// See cutover/staking.md + guard-parity.md.
func Router(svc *Service, accessMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	// Public catalog reads (no guard).
	r.Get("/pools", svc.listPools)
	r.Get("/pools/stats", svc.poolStats)
	r.Get("/pools/{id}", svc.findPool)
	// Access-guarded: admin pool mutations + user-specific read + user writes.
	r.Group(func(r chi.Router) {
		if accessMiddleware != nil {
			r.Use(accessMiddleware)
		}
		r.Post("/pools", svc.createPool)
		r.Put("/pools/{id}", svc.updatePool)
		r.Get("/user/{address}", svc.userStakes)
		r.Post("/stake", svc.recordStake)
		r.Post("/unstake/{stakeId}", svc.recordUnstake)
	})
	return r
}

func (s *Service) listPools(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filters := PoolFilters{}
	if v := strings.TrimSpace(q.Get("poolType")); v != "" {
		filters.PoolType = &v
	}
	if v := strings.TrimSpace(q.Get("status")); v != "" {
		filters.Status = &v
	}
	if v := strings.TrimSpace(q.Get("chainId")); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n > 0 {
			filters.ChainID = &n
		}
		// Bad chainId is ignored (matches NestJS Number(chainId)).
	}

	if s.store == nil {
		writeJSON(w, http.StatusOK, []PoolView{})
		return
	}
	out, err := s.store.ListAllPools(r.Context(), filters)
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, []PoolView{})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "staking list pools failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load staking pools")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) poolStats(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusOK, PoolStats{})
		return
	}
	out, err := s.store.GetPoolStats(r.Context())
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, PoolStats{})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "staking pool stats failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load staking stats")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) findPool(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	out, err := s.store.FindPool(r.Context(), id)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if errors.Is(err, ErrPoolNotFound) {
		writeError(w, http.StatusNotFound, "Staking pool not found")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "staking find pool failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load staking pool")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) createPool(w http.ResponseWriter, r *http.Request) {
	var in CreatePoolInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.PoolType = strings.TrimSpace(in.PoolType)
	in.TokenAddress = strings.TrimSpace(in.TokenAddress)
	if in.Name == "" || in.PoolType == "" || in.TokenAddress == "" {
		writeError(w, http.StatusBadRequest, "name, poolType, and tokenAddress are required")
		return
	}
	if in.ChainID <= 0 {
		writeError(w, http.StatusBadRequest, "chainId must be > 0")
		return
	}
	if !addressRE.MatchString(in.TokenAddress) {
		writeError(w, http.StatusBadRequest, "invalid tokenAddress")
		return
	}
	if in.RewardToken != nil && *in.RewardToken != "" && !addressRE.MatchString(*in.RewardToken) {
		writeError(w, http.StatusBadRequest, "invalid rewardToken")
		return
	}

	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	out, err := s.store.CreatePool(r.Context(), in)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "staking create pool failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create staking pool")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Service) updatePool(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var in UpdatePoolInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	out, err := s.store.UpdatePool(r.Context(), id, in)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if errors.Is(err, ErrPoolNotFound) {
		writeError(w, http.StatusNotFound, "Staking pool not found")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "staking update pool failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to update staking pool")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) userStakes(w http.ResponseWriter, r *http.Request) {
	address := strings.TrimSpace(chi.URLParam(r, "address"))
	if !addressRE.MatchString(address) {
		writeError(w, http.StatusBadRequest, "invalid address")
		return
	}
	address = strings.ToLower(address)

	if s.store == nil {
		writeJSON(w, http.StatusOK, []UserStake{})
		return
	}
	out, err := s.store.ListActiveUserStakes(r.Context(), address)
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, []UserStake{})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "staking user stakes failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load user stakes")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) recordStake(w http.ResponseWriter, r *http.Request) {
	var in RecordStakeInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	in.UserAddress = strings.TrimSpace(in.UserAddress)
	if !addressRE.MatchString(in.UserAddress) {
		writeError(w, http.StatusBadRequest, "invalid userAddress")
		return
	}
	if strings.TrimSpace(in.PoolID) == "" {
		writeError(w, http.StatusBadRequest, "poolId is required")
		return
	}
	if in.Amount <= 0 {
		writeError(w, http.StatusBadRequest, "amount must be > 0")
		return
	}
	in.UserAddress = strings.ToLower(in.UserAddress)

	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	out, err := s.store.RecordStake(r.Context(), in)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "staking record stake failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to record stake")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Service) recordUnstake(w http.ResponseWriter, r *http.Request) {
	stakeID := strings.TrimSpace(chi.URLParam(r, "stakeId"))
	if stakeID == "" {
		writeError(w, http.StatusBadRequest, "stakeId is required")
		return
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	out, err := s.store.RecordUnstake(r.Context(), stakeID)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if errors.Is(err, ErrPoolNotFound) {
		writeError(w, http.StatusNotFound, "stake not found")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "staking record unstake failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to record unstake")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("staking response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statusCode": status,
		"message":    message,
	})
}

// _ unused for linter; reserved for richer messaging later.
var _ = fmt.Errorf
