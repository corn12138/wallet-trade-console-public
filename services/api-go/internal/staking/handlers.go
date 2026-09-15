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

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
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
	RecordUnstake(ctx context.Context, stakeID, ownerAddress string) (UserStake, error)
}

// Service wires a Store for the HTTP layer.
type Service struct {
	store Store
}

// NewService accepts a nil store for the degraded contract.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Router keeps catalog reads public, requires an admin role for pool mutations,
// and requires a SIWE wallet for owner-scoped stake state.
func Router(
	svc *Service,
	adminMiddleware func(http.Handler) http.Handler,
	walletMiddleware func(http.Handler) http.Handler,
) chi.Router {
	r := chi.NewRouter()
	r.Get("/pools", svc.listPools)
	r.Get("/pools/stats", svc.poolStats)
	r.Get("/pools/{id}", svc.findPool)

	r.Group(func(r chi.Router) {
		r.Use(requiredMiddleware(adminMiddleware, "staking admin authentication not configured"))
		r.Post("/pools", svc.createPool)
		r.Put("/pools/{id}", svc.updatePool)
	})
	r.Group(func(r chi.Router) {
		r.Use(requiredMiddleware(walletMiddleware, "staking wallet authentication not configured"))
		r.Get("/user/{address}", svc.userStakes)
		r.Post("/stake", svc.recordStake)
		r.Post("/unstake/{stakeId}", svc.recordUnstake)
	})
	return r
}

func requiredMiddleware(mw func(http.Handler) http.Handler, message string) func(http.Handler) http.Handler {
	if mw != nil {
		return mw
	}
	return func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusServiceUnavailable, message)
		})
	}
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
	address, ok := resolveWalletOwner(w, r, chi.URLParam(r, "address"))
	if !ok {
		return
	}

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
	owner, ok := resolveWalletOwner(w, r, in.UserAddress)
	if !ok {
		return
	}
	in.UserAddress = owner

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
	owner, ok := resolveWalletOwner(w, r, "")
	if !ok {
		return
	}
	out, err := s.store.RecordUnstake(r.Context(), stakeID, owner)
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

func resolveWalletOwner(w http.ResponseWriter, r *http.Request, requested string) (string, bool) {
	owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), strings.TrimSpace(requested))
	if err == nil {
		return owner, true
	}
	switch {
	case errors.Is(err, auth.ErrMissingAuthenticated):
		writeError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, auth.ErrOwnerMismatch):
		writeError(w, http.StatusForbidden, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
	return "", false
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
