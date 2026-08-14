// Package social owns the profile follow graph: durable wallet→wallet
// follow edges in profile_follows (unique per pair), SIWE-guarded
// follow/unfollow mutations, and a public counts/state read. The follower
// identity always comes from the verified token — never the request body —
// and following yourself is rejected.
package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var walletRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

// ErrPoolUnavailable mirrors the sibling repositories' degraded contract.
var ErrPoolUnavailable = errors.New("social repository: database pool not configured")

// ErrSelfFollow rejects following your own address.
var ErrSelfFollow = errors.New("social: cannot follow yourself")

// FollowState is the public read for one profile.
type FollowState struct {
	Address     string `json:"address"`
	Followers   int    `json:"followers"`
	Following   int    `json:"following"`
	IsFollowing *bool  `json:"isFollowing,omitempty"` // only with a verified viewer
}

// Repository wraps the pgx pool with the profile_follows queries.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository accepts a nil pool (methods return ErrPoolUnavailable).
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Follow records the edge (idempotent); reports whether it is new.
// Validation precedes the storage check so a self-follow is a 400 even on a
// degraded deployment.
func (r *Repository) Follow(ctx context.Context, follower, following string) (bool, error) {
	follower, following = strings.ToLower(follower), strings.ToLower(following)
	if follower == following {
		return false, ErrSelfFollow
	}
	if r.pool == nil {
		return false, ErrPoolUnavailable
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO profile_follows (id, follower_address, following_address, created_at)
		VALUES (gen_random_uuid()::text, $1, $2, NOW())
		ON CONFLICT (follower_address, following_address) DO NOTHING
	`, follower, following)
	if err != nil {
		return false, fmt.Errorf("insert profile_follow: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Unfollow removes the edge (idempotent).
func (r *Repository) Unfollow(ctx context.Context, follower, following string) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM profile_follows WHERE follower_address = $1 AND following_address = $2
	`, strings.ToLower(follower), strings.ToLower(following))
	if err != nil {
		return fmt.Errorf("delete profile_follow: %w", err)
	}
	return nil
}

// State returns counts for a profile plus the viewer's edge when known.
func (r *Repository) State(ctx context.Context, profile, viewer string) (FollowState, error) {
	profile = strings.ToLower(profile)
	out := FollowState{Address: profile}
	if r.pool == nil {
		return out, ErrPoolUnavailable
	}
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*)::int FROM profile_follows WHERE following_address = $1),
			(SELECT COUNT(*)::int FROM profile_follows WHERE follower_address  = $1)
	`, profile).Scan(&out.Followers, &out.Following)
	if err != nil {
		return out, fmt.Errorf("count profile_follows: %w", err)
	}
	if viewer != "" {
		var isFollowing bool
		err := r.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM profile_follows
				WHERE follower_address = $1 AND following_address = $2
			)
		`, strings.ToLower(viewer), profile).Scan(&isFollowing)
		if err != nil {
			return out, fmt.Errorf("viewer follow state: %w", err)
		}
		out.IsFollowing = &isFollowing
	}
	return out, nil
}

// WalletResolver extracts an OPTIONAL verified wallet ("" = anonymous) for
// the public state read. Same shape as campaign.WalletResolver.
type WalletResolver func(r *http.Request) string

// Service wires the repository for the HTTP layer.
type Service struct {
	repo *Repository
}

// NewService binds a service over a repo.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// Router mounts the profile-follow routes (mount at /api/profile):
//
//	GET    /{address}/follow-state  — public counts (+ viewer edge with token)
//	POST   /{address}/follow        — SIWE-guarded
//	DELETE /{address}/follow        — SIWE-guarded
func Router(svc *Service, resolveWallet WalletResolver, guard func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Get("/{address}/follow-state", svc.handleState(resolveWallet))

	mutations := chi.NewRouter()
	mutations.Post("/{address}/follow", svc.handleFollow)
	mutations.Delete("/{address}/follow", svc.handleUnfollow)
	if guard != nil {
		r.Group(func(g chi.Router) {
			g.Use(guard)
			g.Mount("/", mutations)
		})
	} else {
		r.Mount("/", mutations)
	}
	return r
}

func profileParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	addr := strings.ToLower(chi.URLParam(r, "address"))
	if !walletRE.MatchString(addr) {
		writeError(w, http.StatusBadRequest, "invalid profile address")
		return "", false
	}
	return addr, true
}

func viewerWallet(w http.ResponseWriter, r *http.Request) (string, bool) {
	wallet := strings.ToLower(auth.AddressFromContext(r.Context()))
	if !walletRE.MatchString(wallet) {
		writeError(w, http.StatusUnauthorized, "wallet authentication required")
		return "", false
	}
	return wallet, true
}

func (s *Service) handleState(resolveWallet WalletResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		profile, ok := profileParam(w, r)
		if !ok {
			return
		}
		viewer := ""
		if resolveWallet != nil {
			viewer = resolveWallet(r)
		}
		state, err := s.repo.State(r.Context(), profile, viewer)
		if errors.Is(err, ErrPoolUnavailable) {
			// Read degrades to zero counts (matches sibling reads).
			writeJSON(w, http.StatusOK, state)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "follow state failed", "err", err)
			writeError(w, http.StatusInternalServerError, "failed to load follow state")
			return
		}
		writeJSON(w, http.StatusOK, state)
	}
}

func (s *Service) handleFollow(w http.ResponseWriter, r *http.Request) {
	profile, ok := profileParam(w, r)
	if !ok {
		return
	}
	viewer, ok := viewerWallet(w, r)
	if !ok {
		return
	}
	created, err := s.repo.Follow(r.Context(), viewer, profile)
	switch {
	case errors.Is(err, ErrSelfFollow):
		writeError(w, http.StatusBadRequest, "cannot follow yourself")
		return
	case errors.Is(err, ErrPoolUnavailable):
		writeError(w, http.StatusServiceUnavailable, "follow store unavailable")
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "follow failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to follow")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"address": profile, "isFollowing": true, "created": created,
	})
}

func (s *Service) handleUnfollow(w http.ResponseWriter, r *http.Request) {
	profile, ok := profileParam(w, r)
	if !ok {
		return
	}
	viewer, ok := viewerWallet(w, r)
	if !ok {
		return
	}
	err := s.repo.Unfollow(r.Context(), viewer, profile)
	switch {
	case errors.Is(err, ErrPoolUnavailable):
		writeError(w, http.StatusServiceUnavailable, "follow store unavailable")
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "unfollow failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to unfollow")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"address": profile, "isFollowing": false,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("social response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": status, "message": message})
}
