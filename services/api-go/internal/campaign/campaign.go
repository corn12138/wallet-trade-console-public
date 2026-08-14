// Package campaign is the Go port of legacy NestJS campaign/
// campaign.service.ts. The GET endpoints (list, active, by-id) landed in
// Phase 3b; the POST/PUT mutations are ported here to close the route-parity
// gap the harness flagged (campaign was read-only in Go).
//
// Every /api/campaign endpoint is public in NestJS (no @UseGuards), so the
// writes mount unguarded for parity. Reads degrade to empty/404 on a nil
// pool; writes surface 503 (a write with no database must not silently
// succeed).
package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Campaign mirrors the launchpad_campaigns row shape that NestJS
// emits via Prisma. Field tags use the literal column names Prisma
// produces (snake-cased columns where @map() applies, camelCase
// otherwise).
//
// We re-emit all fields the NestJS service returns even if the
// frontend only uses a subset — the contract is shape-equality so
// switching upstream providers stays drop-in.
type Campaign struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Description  *string   `json:"description"`
	Banner       *string   `json:"banner"`
	StartDate    time.Time `json:"startDate"`
	EndDate      time.Time `json:"endDate"`
	Status       string    `json:"status"`
	Reward       *string   `json:"reward"`
	Participants int       `json:"participants"`
	Metadata     any       `json:"metadata"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`

	// ParticipantCount is the REAL number of campaign_participants rows
	// (unlike the legacy scalar `participants`, which is editorial).
	// IsParticipating/HasReminder appear only when the request carried a
	// verifiable wallet identity.
	ParticipantCount int   `json:"participantCount"`
	IsParticipating  *bool `json:"isParticipating,omitempty"`
	HasReminder      *bool `json:"hasReminder,omitempty"`
}

// ErrNotFound is returned by FindByID when no row matches.
var ErrNotFound = errors.New("campaign not found")

// ErrPoolUnavailable mirrors the sibling pattern.
var ErrPoolUnavailable = errors.New("campaign repository: database pool not configured")

// Repository wraps the pgxpool with the launchpad_campaigns queries.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository binds the repo. nil pool → ErrPoolUnavailable from
// all methods, matches the markets/discover degraded pattern.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ListAll returns campaigns ORDER BY start_date DESC, optionally
// filtered by status. status="" means no filter.
func (r *Repository) ListAll(ctx context.Context, status string) ([]Campaign, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	var (
		rows pgx.Rows
		err  error
	)
	if status != "" {
		rows, err = r.pool.Query(ctx, `
			SELECT id, title, description, banner, start_date, end_date, status, reward,
			       participants, metadata, created_at, updated_at
			FROM launchpad_campaigns
			WHERE status = $1
			ORDER BY start_date DESC
		`, status)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT id, title, description, banner, start_date, end_date, status, reward,
			       participants, metadata, created_at, updated_at
			FROM launchpad_campaigns
			ORDER BY start_date DESC
		`)
	}
	if err != nil {
		return nil, fmt.Errorf("query launchpad_campaigns: %w", err)
	}
	defer rows.Close()
	return scanCampaigns(rows)
}

// GetActive returns campaigns where now ∈ [startDate, endDate] AND
// status='active', ordered by endDate ASC (soonest-ending first).
func (r *Repository) GetActive(ctx context.Context, now time.Time) ([]Campaign, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, title, description, banner, start_date, end_date, status, reward,
		       participants, metadata, created_at, updated_at
		FROM launchpad_campaigns
		WHERE status = $1 AND start_date <= $2 AND end_date >= $2
		ORDER BY end_date ASC
	`, "active", now)
	if err != nil {
		return nil, fmt.Errorf("query launchpad_campaigns (active): %w", err)
	}
	defer rows.Close()
	return scanCampaigns(rows)
}

// FindByID returns one campaign by primary key or ErrNotFound.
func (r *Repository) FindByID(ctx context.Context, id string) (Campaign, error) {
	if r.pool == nil {
		return Campaign{}, ErrPoolUnavailable
	}
	var c Campaign
	var metadata []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, title, description, banner, start_date, end_date, status, reward,
		       participants, metadata, created_at, updated_at
		FROM launchpad_campaigns
		WHERE id = $1
	`, id).Scan(&c.ID, &c.Title, &c.Description, &c.Banner, &c.StartDate, &c.EndDate, &c.Status, &c.Reward,
		&c.Participants, &metadata, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("query launchpad_campaigns (by id): %w", err)
	}
	c.Metadata = decodeMetadata(metadata)
	return c, nil
}

// allColumns is the RETURNING/SELECT list shared by every query — kept in one
// place so reads and writes stay shape-identical.
const allColumns = `id, title, description, banner, start_date, end_date, status, reward,
	participants, metadata, created_at, updated_at`

// CreateInput is the POST /campaign body. Mirrors the NestJS controller's
// inline DTO: title + dates required, the rest optional. status, participants,
// metadata are NOT settable here — Prisma applies their defaults ("upcoming",
// 0, null) and the controller never overrides them, so the INSERT hardcodes
// the same defaults.
type CreateInput struct {
	Title       string
	Description *string
	Banner      *string
	StartDate   time.Time
	EndDate     time.Time
	Reward      *string
}

// Create inserts a campaign and returns the stored row. Like the sibling
// write modules (staking, papertrade, users, article) it generates the id
// with gen_random_uuid()::text and sets created_at/updated_at to NOW() —
// Prisma's @default(cuid()) / @default(now()) / @updatedAt are client-side,
// not DB defaults.
func (r *Repository) Create(ctx context.Context, in CreateInput) (Campaign, error) {
	if r.pool == nil {
		return Campaign{}, ErrPoolUnavailable
	}
	var c Campaign
	var metadata []byte
	err := r.pool.QueryRow(ctx, `
		INSERT INTO launchpad_campaigns
			(id, title, description, banner, start_date, end_date, status, reward, participants, metadata, created_at, updated_at)
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, 'upcoming', $6, 0, NULL, NOW(), NOW())
		RETURNING `+allColumns,
		in.Title, in.Description, in.Banner, in.StartDate, in.EndDate, in.Reward).
		Scan(&c.ID, &c.Title, &c.Description, &c.Banner, &c.StartDate, &c.EndDate, &c.Status, &c.Reward,
			&c.Participants, &metadata, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return Campaign{}, fmt.Errorf("insert launchpad_campaigns: %w", err)
	}
	c.Metadata = decodeMetadata(metadata)
	return c, nil
}

// updatableColumns maps a PUT-body JSON key to its column. Only these may be
// changed — mirrors the Partial<> type on CampaignService.update().
var updatableColumns = map[string]string{
	"title":        "title",
	"description":  "description",
	"banner":       "banner",
	"status":       "status",
	"reward":       "reward",
	"participants": "participants",
	"metadata":     "metadata",
}

// Update applies only the fields present in the body (present-null sets the
// column to NULL) and always bumps updated_at, then returns the stored row.
// ErrNotFound if no row has the id. An empty field set still touches
// updated_at — parity with Prisma's update({ data: {} }).
func (r *Repository) Update(ctx context.Context, id string, fields map[string]any) (Campaign, error) {
	if r.pool == nil {
		return Campaign{}, ErrPoolUnavailable
	}
	set := make([]string, 0, len(fields)+1)
	args := make([]any, 0, len(fields)+1)
	i := 1
	for _, key := range sortedKeys(fields) { // deterministic SQL
		col := updatableColumns[key]
		if col == "metadata" {
			set = append(set, fmt.Sprintf("%s = $%d::jsonb", col, i))
		} else {
			set = append(set, fmt.Sprintf("%s = $%d", col, i))
		}
		args = append(args, fields[key])
		i++
	}
	set = append(set, "updated_at = NOW()")
	args = append(args, id)

	query := fmt.Sprintf(`
		UPDATE launchpad_campaigns
		SET %s
		WHERE id = $%d
		RETURNING %s
	`, strings.Join(set, ", "), i, allColumns)

	var c Campaign
	var metadata []byte
	err := r.pool.QueryRow(ctx, query, args...).Scan(&c.ID, &c.Title, &c.Description, &c.Banner, &c.StartDate, &c.EndDate, &c.Status, &c.Reward,
		&c.Participants, &metadata, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("update launchpad_campaigns: %w", err)
	}
	c.Metadata = decodeMetadata(metadata)
	return c, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// parseUpdateFields extracts the updatable fields present in the PUT body,
// preserving present-null as a nil value and ignoring unknown keys. Returns
// an error only on malformed JSON or a type mismatch on a known key.
func parseUpdateFields(body []byte) (map[string]any, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(raw))
	for key := range updatableColumns {
		rawVal, ok := raw[key]
		if !ok {
			continue
		}
		if string(rawVal) == "null" {
			out[key] = nil
			continue
		}
		switch key {
		case "participants":
			var n int
			if err := json.Unmarshal(rawVal, &n); err != nil {
				return nil, fmt.Errorf("field %q: %w", key, err)
			}
			out[key] = n
		case "metadata":
			out[key] = string(rawVal) // raw JSON, cast ::jsonb in the SQL
		default:
			var s string
			if err := json.Unmarshal(rawVal, &s); err != nil {
				return nil, fmt.Errorf("field %q: %w", key, err)
			}
			out[key] = s
		}
	}
	return out, nil
}

func scanCampaigns(rows pgx.Rows) ([]Campaign, error) {
	out := make([]Campaign, 0)
	for rows.Next() {
		var c Campaign
		var metadata []byte
		if err := rows.Scan(&c.ID, &c.Title, &c.Description, &c.Banner, &c.StartDate, &c.EndDate, &c.Status, &c.Reward,
			&c.Participants, &metadata, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan launchpad_campaigns: %w", err)
		}
		c.Metadata = decodeMetadata(metadata)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate launchpad_campaigns: %w", err)
	}
	return out, nil
}

// decodeMetadata turns the JSONB column into a generic any. Bad JSON or
// SQL NULL surface as nil — matches Prisma's `Json?` behavior.
func decodeMetadata(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

// Service is a thin wrapper that lets handlers and future internal
// callers share the same code path.
type Service struct {
	repo *Repository
}

// NewService binds a service over a repo.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// List proxies to repo.ListAll with degrade-on-pool-unavailable: a
// nil pool returns an empty slice rather than 500, matching the
// markets/discover degraded pattern.
func (s *Service) List(ctx context.Context, status string) ([]Campaign, error) {
	out, err := s.repo.ListAll(ctx, status)
	if errors.Is(err, ErrPoolUnavailable) {
		return []Campaign{}, nil
	}
	return out, err
}

// Active proxies to repo.GetActive with degrade-on-pool-unavailable.
func (s *Service) Active(ctx context.Context, now time.Time) ([]Campaign, error) {
	out, err := s.repo.GetActive(ctx, now)
	if errors.Is(err, ErrPoolUnavailable) {
		return []Campaign{}, nil
	}
	return out, err
}

// FindByID proxies to repo with ErrNotFound preserved. Pool-unavailable
// is treated as not-found from the caller's perspective so the handler
// returns 404 rather than 500 when DB envs are missing.
func (s *Service) FindByID(ctx context.Context, id string) (Campaign, error) {
	out, err := s.repo.FindByID(ctx, id)
	if errors.Is(err, ErrPoolUnavailable) {
		return Campaign{}, ErrNotFound
	}
	return out, err
}

// Create proxies to repo.Create. Unlike the reads it does NOT degrade on a
// nil pool — ErrPoolUnavailable propagates so the handler returns 503 rather
// than pretending a write succeeded.
func (s *Service) Create(ctx context.Context, in CreateInput) (Campaign, error) {
	return s.repo.Create(ctx, in)
}

// Update proxies to repo.Update; ErrPoolUnavailable and ErrNotFound propagate.
func (s *Service) Update(ctx context.Context, id string, fields map[string]any) (Campaign, error) {
	return s.repo.Update(ctx, id, fields)
}

// Router exposes the /campaign endpoints on a chi sub-router. Reads stay
// public (NestJS parity) but are enriched with real participant counts and,
// when the request carries a verifiable web3 token (resolveWallet), the
// caller's join/reminder state. The join/reminder mutations mount behind the
// SIWE guard via RegisterInteractionRoutes. Either extra may be nil.
func Router(svc *Service, resolveWallet WalletResolver, guard func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Get("/", makeListHandler(svc, resolveWallet))
	r.Get("/active", makeActiveHandler(svc, resolveWallet))
	r.Get("/{id}", makeByIDHandler(svc, resolveWallet))
	r.Post("/", makeCreateHandler(svc))
	r.Put("/{id}", makeUpdateHandler(svc))
	RegisterInteractionRoutes(r, svc, guard)
	return r
}

func optionalWallet(resolveWallet WalletResolver, r *http.Request) string {
	if resolveWallet == nil {
		return ""
	}
	return resolveWallet(r)
}

func makeListHandler(svc *Service, resolveWallet WalletResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		out, err := svc.List(r.Context(), status)
		if err != nil {
			slog.ErrorContext(r.Context(), "campaign list failed", "err", err)
			http.Error(w, "failed to load campaigns", http.StatusInternalServerError)
			return
		}
		out = svc.enrichForWallet(r.Context(), out, optionalWallet(resolveWallet, r))
		writeJSON(w, http.StatusOK, out)
	}
}

func makeActiveHandler(svc *Service, resolveWallet WalletResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := svc.Active(r.Context(), time.Now())
		if err != nil {
			slog.ErrorContext(r.Context(), "campaign active failed", "err", err)
			http.Error(w, "failed to load active campaigns", http.StatusInternalServerError)
			return
		}
		out = svc.enrichForWallet(r.Context(), out, optionalWallet(resolveWallet, r))
		writeJSON(w, http.StatusOK, out)
	}
}

func makeByIDHandler(svc *Service, resolveWallet WalletResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		out, err := svc.FindByID(r.Context(), id)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "campaign not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "campaign by id failed", "err", err, "id", id)
			http.Error(w, "failed to load campaign", http.StatusInternalServerError)
			return
		}
		enriched := svc.enrichForWallet(r.Context(), []Campaign{out}, optionalWallet(resolveWallet, r))
		writeJSON(w, http.StatusOK, enriched[0])
	}
}

func makeCreateHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Title       string  `json:"title"`
			Description *string `json:"description"`
			Banner      *string `json:"banner"`
			StartDate   string  `json:"startDate"`
			EndDate     string  `json:"endDate"`
			Reward      *string `json:"reward"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Title) == "" {
			http.Error(w, "title is required", http.StatusBadRequest)
			return
		}
		start, err := parseDate(body.StartDate)
		if err != nil {
			http.Error(w, "invalid startDate", http.StatusBadRequest)
			return
		}
		end, err := parseDate(body.EndDate)
		if err != nil {
			http.Error(w, "invalid endDate", http.StatusBadRequest)
			return
		}
		out, err := svc.Create(r.Context(), CreateInput{
			Title:       body.Title,
			Description: body.Description,
			Banner:      body.Banner,
			StartDate:   start,
			EndDate:     end,
			Reward:      body.Reward,
		})
		if errors.Is(err, ErrPoolUnavailable) {
			http.Error(w, "campaign store unavailable", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "campaign create failed", "err", err)
			http.Error(w, "failed to create campaign", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, out) // NestJS @Post → 201
	}
}

func makeUpdateHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		fields, err := parseUpdateFields(raw)
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		out, err := svc.Update(r.Context(), id, fields)
		if errors.Is(err, ErrPoolUnavailable) {
			http.Error(w, "campaign store unavailable", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "campaign not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "campaign update failed", "err", err, "id", id)
			http.Error(w, "failed to update campaign", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// parseDate accepts the ISO forms the frontend sends (RFC3339, with or without
// fractional seconds, and date-only), mirroring JS `new Date(str)` leniency.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty date")
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable date %q", s)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("campaign response encode failed", "err", err)
	}
}
