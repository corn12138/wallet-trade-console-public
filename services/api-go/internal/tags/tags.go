// Package tags is the Go port of legacy NestJS tags/ — three GET
// endpoints (list, by-id, by-slug) over the `tags` table, joined with
// `_ArticleToTag` for the article count and the articles[] relation.
//
// The NestJS service exposes create/update/delete but the controller
// does not wire them. Only the three @Public() reads are reachable.
package tags

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrPoolUnavailable mirrors the pattern used across api-go repos.
var ErrPoolUnavailable = fmt.Errorf("tags repository: database pool not configured")

// ErrNotFound surfaces a 404 to the handler — mirrors NestJS
// NotFoundException raised by findOne/findBySlug.
var ErrNotFound = errors.New("tag not found")

// Tag is the row shape returned by GET /tags (list) — includes the
// joined articleCount but not the full article objects (that's the
// detail endpoint's job).
type Tag struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Slug         *string   `json:"slug"`
	Description  *string   `json:"description"`
	Color        *string   `json:"color"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	ArticleCount int64     `json:"articleCount"`
}

// TagDetail extends Tag with the articles[] relation — used by
// findOne and findBySlug. We keep the field name `articles` to match
// the Prisma `include: { articles: true }` response shape.
type TagDetail struct {
	Tag
	Articles []ArticleSummary `json:"articles"`
}

// ArticleSummary is the projection of `articles` included alongside a
// tag — id + title + slug + published. The full article surface lives
// under /api/articles (Phase 4v).
type ArticleSummary struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Slug      *string `json:"slug"`
	Published bool    `json:"published"`
}

// Repository wraps the pgx pool.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository tolerates a nil pool; reads return ErrPoolUnavailable.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// FindAll returns every tag, alphabetically by name, with article
// count from the implicit `_ArticleToTag` join table.
func (r *Repository) FindAll(ctx context.Context) ([]Tag, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}

	rows, err := r.pool.Query(ctx, `
		SELECT t.id, t.name, t.slug, t.description, t.color,
		       t."createdAt", t."updatedAt",
		       COALESCE(c.cnt, 0)::bigint AS article_count
		FROM tags t
		LEFT JOIN (
			SELECT "B" AS tag_id, COUNT(*)::bigint AS cnt
			FROM "_ArticleToTag"
			GROUP BY "B"
		) c ON c.tag_id = t.id
		ORDER BY t.name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query tags: %w", err)
	}
	defer rows.Close()

	out := make([]Tag, 0)
	for rows.Next() {
		var t Tag
		if err := rows.Scan(
			&t.ID, &t.Name, &t.Slug, &t.Description, &t.Color,
			&t.CreatedAt, &t.UpdatedAt, &t.ArticleCount,
		); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tags: %w", err)
	}
	return out, nil
}

// FindByID returns the tag and its joined articles[] (id, title, slug,
// published only — not the full article body).
func (r *Repository) FindByID(ctx context.Context, id string) (TagDetail, error) {
	return r.findWithArticles(ctx, "id", id)
}

// FindBySlug — same as FindByID but lookup is by `slug`.
func (r *Repository) FindBySlug(ctx context.Context, slug string) (TagDetail, error) {
	return r.findWithArticles(ctx, "slug", slug)
}

func (r *Repository) findWithArticles(ctx context.Context, column, value string) (TagDetail, error) {
	if r.pool == nil {
		return TagDetail{}, ErrPoolUnavailable
	}

	var detail TagDetail
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, slug, description, color, "createdAt", "updatedAt"
		FROM tags WHERE `+column+` = $1
	`, value).Scan(
		&detail.ID, &detail.Name, &detail.Slug, &detail.Description, &detail.Color,
		&detail.CreatedAt, &detail.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return TagDetail{}, ErrNotFound
	}
	if err != nil {
		return TagDetail{}, fmt.Errorf("query tag by %s: %w", column, err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.title, a.slug, a.published
		FROM articles a
		JOIN "_ArticleToTag" jt ON jt."A" = a.id
		WHERE jt."B" = $1
		ORDER BY a."createdAt" DESC
	`, detail.ID)
	if err != nil {
		return TagDetail{}, fmt.Errorf("query articles for tag: %w", err)
	}
	defer rows.Close()

	detail.Articles = make([]ArticleSummary, 0)
	for rows.Next() {
		var a ArticleSummary
		if err := rows.Scan(&a.ID, &a.Title, &a.Slug, &a.Published); err != nil {
			return TagDetail{}, fmt.Errorf("scan article summary: %w", err)
		}
		detail.Articles = append(detail.Articles, a)
	}
	if err := rows.Err(); err != nil {
		return TagDetail{}, fmt.Errorf("iterate articles: %w", err)
	}
	return detail, nil
}

// Reader is the read surface used by handlers. Tests can pass a stub.
type Reader interface {
	FindAll(ctx context.Context) ([]Tag, error)
	FindByID(ctx context.Context, id string) (TagDetail, error)
	FindBySlug(ctx context.Context, slug string) (TagDetail, error)
}

// Service wires the Reader for the HTTP layer.
type Service struct {
	reader Reader
}

// NewService permits a nil reader for the degraded contract.
func NewService(reader Reader) *Service {
	return &Service{reader: reader}
}

// Router mounts the 3 GETs at /api/tags. Order matters: /slug/{slug}
// must be registered before /{id} so chi matches the literal segment.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/", svc.findAll)
	r.Get("/slug/{slug}", svc.findBySlug)
	r.Get("/{id}", svc.findByID)
	return r
}

func (s *Service) findAll(w http.ResponseWriter, r *http.Request) {
	if s.reader == nil {
		writeJSON(w, http.StatusOK, []Tag{})
		return
	}
	out, err := s.reader.FindAll(r.Context())
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, []Tag{})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "tags list failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load tags")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) findByID(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	s.lookup(w, r, func(ctx context.Context) (TagDetail, error) {
		if s.reader == nil {
			return TagDetail{}, ErrPoolUnavailable
		}
		return s.reader.FindByID(ctx, id)
	}, fmt.Sprintf("标签 %s 不存在", id))
}

func (s *Service) findBySlug(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}
	s.lookup(w, r, func(ctx context.Context) (TagDetail, error) {
		if s.reader == nil {
			return TagDetail{}, ErrPoolUnavailable
		}
		return s.reader.FindBySlug(ctx, slug)
	}, fmt.Sprintf("标签 %s 不存在", slug))
}

func (s *Service) lookup(
	w http.ResponseWriter,
	r *http.Request,
	fetch func(context.Context) (TagDetail, error),
	notFoundMessage string,
) {
	detail, err := fetch(r.Context())
	if errors.Is(err, ErrPoolUnavailable) {
		// Degraded: return 503 so the client knows DB is down rather
		// than receive an empty (and misleading) detail object.
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, notFoundMessage)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "tags lookup failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load tag")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("tags response encode failed", "err", err)
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
