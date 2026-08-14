// Package article is the Go port of legacy NestJS article/.
//
// Phase 4v.1 ports the 2 public read endpoints (list + detail). Mutations
// (create / update / delete / publish / unpublish) all require @Roles
// admin/editor and land alongside the RolesGuard parity middleware in
// a follow-up slice (4v.2).
package article

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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrPoolUnavailable surfaces the degraded contract.
var ErrPoolUnavailable = fmt.Errorf("article repository: database pool not configured")

// ErrNotFound is returned when no article matches.
var ErrNotFound = errors.New("文章不存在")

// Author is the user projection nested under article.author. Kept
// minimal — id + username + avatar match what the article-detail UI
// renders.
type Author struct {
	ID       string  `json:"id"`
	Username string  `json:"username"`
	FullName *string `json:"fullName"`
	Avatar   *string `json:"avatar"`
}

// Category mirrors `category: true` include shape.
type Category struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	Slug *string `json:"slug"`
}

// Tag is the per-article tag projection (id+name+slug only — full tag
// lives at /api/tags/:id).
type Tag struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	Slug *string `json:"slug"`
}

// Article mirrors the Prisma row with its joins.
type Article struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Slug          *string    `json:"slug"`
	Content       string     `json:"content"`
	Summary       *string    `json:"summary"`
	Published     bool       `json:"published"`
	FeaturedImage *string    `json:"featuredImage"`
	ViewCount     int64      `json:"viewCount"`
	AuthorID      string     `json:"authorId"`
	CategoryID    *string    `json:"categoryId"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	PublishedAt   *time.Time `json:"publishedAt"`
	Author        *Author    `json:"author,omitempty"`
	Category      *Category  `json:"category,omitempty"`
	Tags          []Tag      `json:"tags"`
}

// ListResult mirrors `{items, meta}`.
type ListResult struct {
	Items []Article `json:"items"`
	Meta  Meta      `json:"meta"`
}

// Meta mirrors the NestJS pagination payload.
type Meta struct {
	Total      int64 `json:"total"`
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	TotalPages int64 `json:"totalPages"`
}

// ListQuery mirrors GET /articles query params.
type ListQuery struct {
	Page     int
	Limit    int
	Category string
	Tag      string
	Search   string
}

// Repository wraps pgx.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository tolerates a nil pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// List paginates published articles with optional category/tag/search
// filters. Mirrors articleService.findAll(query).
func (r *Repository) List(ctx context.Context, q ListQuery) (ListResult, error) {
	if r.pool == nil {
		return ListResult{}, ErrPoolUnavailable
	}

	clauses := []string{`a.published = true`}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if q.Category != "" {
		add(`a."categoryId" = $%d`, q.Category)
	}
	if q.Tag != "" {
		add(`EXISTS (SELECT 1 FROM "_ArticleToTag" jt WHERE jt."A" = a.id AND jt."B" = $%d)`, q.Tag)
	}
	if q.Search != "" {
		args = append(args, "%"+strings.ToLower(q.Search)+"%")
		ph := strconv.Itoa(len(args))
		clauses = append(clauses, fmt.Sprintf(
			`(LOWER(a.title) LIKE $%s OR LOWER(a.content) LIKE $%s OR LOWER(COALESCE(a.summary,'')) LIKE $%s)`,
			ph, ph, ph,
		))
	}

	where := strings.Join(clauses, " AND ")

	var total int64
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM articles a WHERE "+where, args...).Scan(&total); err != nil {
		return ListResult{}, fmt.Errorf("count articles: %w", err)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 10
	}
	page := q.Page
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * limit
	listArgs := append(args, limit, offset)
	limPH := "$" + strconv.Itoa(len(args)+1)
	offPH := "$" + strconv.Itoa(len(args)+2)

	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.title, a.slug, a.content, a.summary, a.published,
		       a."featuredImage", a."viewCount", a."authorId", a."categoryId",
		       a."createdAt", a."updatedAt", a."publishedAt",
		       u.id, u.username, u."fullName", u.avatar,
		       c.id, c.name, c.slug
		FROM articles a
		LEFT JOIN users u ON u.id = a."authorId"
		LEFT JOIN categories c ON c.id = a."categoryId"
		WHERE `+where+`
		ORDER BY a."createdAt" DESC
		LIMIT `+limPH+` OFFSET `+offPH+`
	`, listArgs...)
	if err != nil {
		return ListResult{}, fmt.Errorf("query articles: %w", err)
	}
	defer rows.Close()

	items := make([]Article, 0, limit)
	ids := []string{}
	idIdx := map[string]int{}
	for rows.Next() {
		var a Article
		var (
			authorID, authorUsername, catID, catName string
			authorFullName, authorAvatar, catSlug    *string
			catIDNullable                            *string
		)
		if err := rows.Scan(
			&a.ID, &a.Title, &a.Slug, &a.Content, &a.Summary, &a.Published,
			&a.FeaturedImage, &a.ViewCount, &a.AuthorID, &catIDNullable,
			&a.CreatedAt, &a.UpdatedAt, &a.PublishedAt,
			&authorID, &authorUsername, &authorFullName, &authorAvatar,
			&catID, &catName, &catSlug,
		); err != nil {
			return ListResult{}, fmt.Errorf("scan article: %w", err)
		}
		if authorID != "" {
			a.Author = &Author{ID: authorID, Username: authorUsername, FullName: authorFullName, Avatar: authorAvatar}
		}
		if catID != "" {
			a.Category = &Category{ID: catID, Name: catName, Slug: catSlug}
		}
		a.CategoryID = catIDNullable
		a.Tags = []Tag{}
		idIdx[a.ID] = len(items)
		ids = append(ids, a.ID)
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, fmt.Errorf("iterate articles: %w", err)
	}

	if len(ids) > 0 {
		if err := r.hydrateTags(ctx, items, idIdx, ids); err != nil {
			return ListResult{}, err
		}
	}

	totalPages := int64(0)
	if total > 0 && limit > 0 {
		totalPages = (total + int64(limit) - 1) / int64(limit)
	}

	return ListResult{
		Items: items,
		Meta: Meta{
			Total:      total,
			Page:       page,
			Limit:      limit,
			TotalPages: totalPages,
		},
	}, nil
}

// FindByID returns the full article with joins + increments viewCount
// in the same transaction so the returned record matches what's on
// disk after the call.
func (r *Repository) FindByID(ctx context.Context, id string) (Article, error) {
	if r.pool == nil {
		return Article{}, ErrPoolUnavailable
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Article{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var (
		a                                        Article
		authorID, authorUsername, catID, catName string
		authorFullName, authorAvatar, catSlug    *string
		catIDNullable                            *string
	)
	err = tx.QueryRow(ctx, `
		SELECT a.id, a.title, a.slug, a.content, a.summary, a.published,
		       a."featuredImage", a."viewCount", a."authorId", a."categoryId",
		       a."createdAt", a."updatedAt", a."publishedAt",
		       COALESCE(u.id, ''), COALESCE(u.username, ''), u."fullName", u.avatar,
		       COALESCE(c.id, ''), COALESCE(c.name, ''), c.slug
		FROM articles a
		LEFT JOIN users u ON u.id = a."authorId"
		LEFT JOIN categories c ON c.id = a."categoryId"
		WHERE a.id = $1
	`, id).Scan(
		&a.ID, &a.Title, &a.Slug, &a.Content, &a.Summary, &a.Published,
		&a.FeaturedImage, &a.ViewCount, &a.AuthorID, &catIDNullable,
		&a.CreatedAt, &a.UpdatedAt, &a.PublishedAt,
		&authorID, &authorUsername, &authorFullName, &authorAvatar,
		&catID, &catName, &catSlug,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Article{}, ErrNotFound
	}
	if err != nil {
		return Article{}, fmt.Errorf("query article: %w", err)
	}

	// Increment view count and reflect in the returned row.
	if _, err := tx.Exec(ctx, `UPDATE articles SET "viewCount" = "viewCount" + 1 WHERE id = $1`, id); err != nil {
		return Article{}, fmt.Errorf("increment view count: %w", err)
	}
	a.ViewCount++

	tagRows, err := tx.Query(ctx, `
		SELECT t.id, t.name, t.slug
		FROM tags t
		JOIN "_ArticleToTag" jt ON jt."B" = t.id
		WHERE jt."A" = $1
	`, id)
	if err != nil {
		return Article{}, fmt.Errorf("query article tags: %w", err)
	}
	defer tagRows.Close()
	a.Tags = []Tag{}
	for tagRows.Next() {
		var t Tag
		if err := tagRows.Scan(&t.ID, &t.Name, &t.Slug); err != nil {
			return Article{}, fmt.Errorf("scan article tag: %w", err)
		}
		a.Tags = append(a.Tags, t)
	}
	if err := tagRows.Err(); err != nil {
		return Article{}, fmt.Errorf("iterate article tags: %w", err)
	}

	if authorID != "" {
		a.Author = &Author{ID: authorID, Username: authorUsername, FullName: authorFullName, Avatar: authorAvatar}
	}
	if catID != "" {
		a.Category = &Category{ID: catID, Name: catName, Slug: catSlug}
	}
	a.CategoryID = catIDNullable

	if err := tx.Commit(ctx); err != nil {
		return Article{}, fmt.Errorf("commit view-count increment: %w", err)
	}
	return a, nil
}

// hydrateTags fills in the Tags slice for every article in items by
// doing one batched query against `_ArticleToTag` JOIN tags.
func (r *Repository) hydrateTags(ctx context.Context, items []Article, idx map[string]int, ids []string) error {
	rows, err := r.pool.Query(ctx, `
		SELECT jt."A", t.id, t.name, t.slug
		FROM "_ArticleToTag" jt
		JOIN tags t ON t.id = jt."B"
		WHERE jt."A" = ANY($1::text[])
	`, ids)
	if err != nil {
		return fmt.Errorf("query article tags batch: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			articleID string
			t         Tag
		)
		if err := rows.Scan(&articleID, &t.ID, &t.Name, &t.Slug); err != nil {
			return fmt.Errorf("scan article tag batch: %w", err)
		}
		if i, ok := idx[articleID]; ok {
			items[i].Tags = append(items[i].Tags, t)
		}
	}
	return rows.Err()
}

// CreateInput mirrors CreateArticleDto.
type CreateInput struct {
	Title         string   `json:"title"`
	Content       string   `json:"content"`
	Summary       *string  `json:"summary,omitempty"`
	Published     *bool    `json:"published,omitempty"`
	FeaturedImage *string  `json:"featuredImage,omitempty"`
	Slug          *string  `json:"slug,omitempty"`
	CategoryID    *string  `json:"categoryId,omitempty"`
	Tags          []string `json:"tags,omitempty"`
}

// UpdateInput mirrors UpdateArticleDto — fields nil → leave unchanged.
type UpdateInput struct {
	Title         *string   `json:"title,omitempty"`
	Content       *string   `json:"content,omitempty"`
	Summary       *string   `json:"summary,omitempty"`
	Published     *bool     `json:"published,omitempty"`
	FeaturedImage *string   `json:"featuredImage,omitempty"`
	Slug          *string   `json:"slug,omitempty"`
	CategoryID    *string   `json:"categoryId,omitempty"`
	Tags          *[]string `json:"tags,omitempty"`
}

// ErrAuthorMismatch surfaces 404 (matches NestJS: "article not found
// or you don't have permission") when the article exists but isn't
// owned by the authenticated user.
var ErrAuthorMismatch = errors.New("文章不存在或您没有权限修改")

// Create inserts a new article + tag connections. Tags are upserted
// by name (creates missing entries).
func (r *Repository) Create(ctx context.Context, authorID string, in CreateInput) (Article, error) {
	if r.pool == nil {
		return Article{}, ErrPoolUnavailable
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Article{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	published := false
	if in.Published != nil {
		published = *in.Published
	}

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO articles (id, title, slug, content, summary, published,
		                     "featuredImage", "authorId", "categoryId",
		                     "createdAt", "updatedAt")
		VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
		RETURNING id
	`, in.Title, in.Slug, in.Content, in.Summary, published, in.FeaturedImage, authorID, in.CategoryID).Scan(&id)
	if err != nil {
		return Article{}, fmt.Errorf("insert article: %w", err)
	}

	if len(in.Tags) > 0 {
		if err := connectTagsByName(ctx, tx, id, in.Tags); err != nil {
			return Article{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Article{}, fmt.Errorf("commit article: %w", err)
	}
	return r.FindByID(ctx, id)
}

// Update applies a partial change. Author-scoped — returns
// ErrAuthorMismatch if the article exists but authorID doesn't own it.
func (r *Repository) Update(ctx context.Context, authorID, articleID string, in UpdateInput) (Article, error) {
	if r.pool == nil {
		return Article{}, ErrPoolUnavailable
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Article{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var rowAuthor string
	if err := tx.QueryRow(ctx, `SELECT "authorId" FROM articles WHERE id = $1`, articleID).Scan(&rowAuthor); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Article{}, ErrAuthorMismatch
		}
		return Article{}, fmt.Errorf("query article author: %w", err)
	}
	if rowAuthor != authorID {
		return Article{}, ErrAuthorMismatch
	}

	sets := []string{}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf(clause, len(args)))
	}
	if in.Title != nil {
		add(`title = $%d`, *in.Title)
	}
	if in.Content != nil {
		add(`content = $%d`, *in.Content)
	}
	if in.Summary != nil {
		add(`summary = $%d`, *in.Summary)
	}
	if in.Published != nil {
		add(`published = $%d`, *in.Published)
	}
	if in.FeaturedImage != nil {
		add(`"featuredImage" = $%d`, *in.FeaturedImage)
	}
	if in.Slug != nil {
		add(`slug = $%d`, *in.Slug)
	}
	if in.CategoryID != nil {
		add(`"categoryId" = $%d`, *in.CategoryID)
	}
	if len(sets) > 0 {
		sets = append(sets, `"updatedAt" = NOW()`)
		args = append(args, articleID)
		_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE articles SET %s WHERE id = $%d`, strings.Join(sets, ", "), len(args)), args...)
		if err != nil {
			return Article{}, fmt.Errorf("update article: %w", err)
		}
	}

	if in.Tags != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM "_ArticleToTag" WHERE "A" = $1`, articleID); err != nil {
			return Article{}, fmt.Errorf("clear article tags: %w", err)
		}
		if len(*in.Tags) > 0 {
			if err := connectTagsByName(ctx, tx, articleID, *in.Tags); err != nil {
				return Article{}, err
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Article{}, fmt.Errorf("commit update: %w", err)
	}
	return r.FindByID(ctx, articleID)
}

// Delete removes the article (author-scoped). ErrAuthorMismatch on miss
// or wrong author.
func (r *Repository) Delete(ctx context.Context, authorID, articleID string) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM articles WHERE id = $1 AND "authorId" = $2`, articleID, authorID)
	if err != nil {
		return fmt.Errorf("delete article: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAuthorMismatch
	}
	return nil
}

// SetPublished updates the published flag (author-scoped).
func (r *Repository) SetPublished(ctx context.Context, authorID, articleID string, published bool) (Article, error) {
	if r.pool == nil {
		return Article{}, ErrPoolUnavailable
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE articles SET published = $1, "updatedAt" = NOW(),
		       "publishedAt" = CASE WHEN $1 = true THEN NOW() ELSE "publishedAt" END
		WHERE id = $2 AND "authorId" = $3
	`, published, articleID, authorID)
	if err != nil {
		return Article{}, fmt.Errorf("update published: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Article{}, ErrAuthorMismatch
	}
	return r.FindByID(ctx, articleID)
}

// connectTagsByName mirrors getOrCreateTags + connect: looks up each
// tag by name, creates missing ones, then inserts the join rows.
func connectTagsByName(ctx context.Context, tx pgx.Tx, articleID string, names []string) error {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var tagID string
		err := tx.QueryRow(ctx, `SELECT id FROM tags WHERE name = $1`, name).Scan(&tagID)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.QueryRow(ctx, `
				INSERT INTO tags (id, name, "createdAt", "updatedAt")
				VALUES (gen_random_uuid()::text, $1, NOW(), NOW())
				RETURNING id
			`, name).Scan(&tagID); err != nil {
				return fmt.Errorf("create tag %q: %w", name, err)
			}
		} else if err != nil {
			return fmt.Errorf("find tag %q: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO "_ArticleToTag" ("A", "B") VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, articleID, tagID); err != nil {
			return fmt.Errorf("connect tag %q: %w", name, err)
		}
	}
	return nil
}

// --- HTTP layer ---

// Reader is the surface the handlers use. Tests pass a stub.
type Reader interface {
	List(ctx context.Context, q ListQuery) (ListResult, error)
	FindByID(ctx context.Context, id string) (Article, error)
}

// Writer is the mutation surface (admin/editor-gated).
type Writer interface {
	Create(ctx context.Context, authorID string, in CreateInput) (Article, error)
	Update(ctx context.Context, authorID, articleID string, in UpdateInput) (Article, error)
	Delete(ctx context.Context, authorID, articleID string) error
	SetPublished(ctx context.Context, authorID, articleID string, published bool) (Article, error)
}

// Service wires a Reader + Writer.
type Service struct {
	reader Reader
	writer Writer
}

// NewService accepts nil for either interface to honor the degraded
// contract — reads degrade to empty envelopes; mutations refuse to mount.
func NewService(reader Reader, writer Writer) *Service {
	return &Service{reader: reader, writer: writer}
}

// Router mounts the 2 reads always. Mutations (5 routes) only mount
// when the rolesGuard middleware is supplied AND a Writer is wired.
func Router(svc *Service, rolesGuard func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Get("/", svc.list)
	r.Get("/{id}", svc.findOne)
	if svc.writer != nil && rolesGuard != nil {
		r.With(rolesGuard).Post("/", svc.create)
		r.With(rolesGuard).Patch("/{id}", svc.update)
		r.With(rolesGuard).Delete("/{id}", svc.remove)
		r.With(rolesGuard).Post("/{id}/publish", svc.publish)
		r.With(rolesGuard).Post("/{id}/unpublish", svc.unpublish)
	}
	return r
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	authorID := authUserID(r)
	if authorID == "" {
		writeError(w, http.StatusUnauthorized, "未认证")
		return
	}
	var in CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Content) == "" {
		writeError(w, http.StatusBadRequest, "title and content are required")
		return
	}
	out, err := s.writer.Create(r.Context(), authorID, in)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "article create failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create article")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	authorID := authUserID(r)
	if authorID == "" {
		writeError(w, http.StatusUnauthorized, "未认证")
		return
	}
	id := chi.URLParam(r, "id")
	var in UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	out, err := s.writer.Update(r.Context(), authorID, id, in)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if errors.Is(err, ErrAuthorMismatch) {
		writeError(w, http.StatusNotFound, ErrAuthorMismatch.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "article update failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to update article")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) remove(w http.ResponseWriter, r *http.Request) {
	authorID := authUserID(r)
	if authorID == "" {
		writeError(w, http.StatusUnauthorized, "未认证")
		return
	}
	id := chi.URLParam(r, "id")
	err := s.writer.Delete(r.Context(), authorID, id)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if errors.Is(err, ErrAuthorMismatch) {
		writeError(w, http.StatusNotFound, ErrAuthorMismatch.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "article delete failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to delete article")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "文章已成功删除"})
}

func (s *Service) publish(w http.ResponseWriter, r *http.Request) {
	s.setPublished(w, r, true)
}

func (s *Service) unpublish(w http.ResponseWriter, r *http.Request) {
	s.setPublished(w, r, false)
}

func (s *Service) setPublished(w http.ResponseWriter, r *http.Request, published bool) {
	authorID := authUserID(r)
	if authorID == "" {
		writeError(w, http.StatusUnauthorized, "未认证")
		return
	}
	id := chi.URLParam(r, "id")
	out, err := s.writer.SetPublished(r.Context(), authorID, id, published)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if errors.Is(err, ErrAuthorMismatch) {
		writeError(w, http.StatusNotFound, ErrAuthorMismatch.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "article publish toggle failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to update article")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// authUserID pulls the authenticated userID from the request context
// (placed there by userauth.AccessMiddleware → auth.WithUser).
func authUserID(r *http.Request) string {
	return auth.UserIDFromContext(r.Context())
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, ok := parsePositiveInt(q.Get("page"), 1, 1, 0)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid page")
		return
	}
	limit, ok := parsePositiveInt(q.Get("limit"), 10, 1, 100)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return
	}

	query := ListQuery{
		Page:     page,
		Limit:    limit,
		Category: strings.TrimSpace(q.Get("category")),
		Tag:      strings.TrimSpace(q.Get("tag")),
		Search:   strings.TrimSpace(q.Get("search")),
	}

	if s.reader == nil {
		writeJSON(w, http.StatusOK, ListResult{Items: []Article{}, Meta: Meta{Page: page, Limit: limit}})
		return
	}
	out, err := s.reader.List(r.Context(), query)
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, ListResult{Items: []Article{}, Meta: Meta{Page: page, Limit: limit}})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "article list failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load articles")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) findOne(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if s.reader == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	out, err := s.reader.FindByID(r.Context(), id)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, ErrNotFound.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "article findOne failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load article")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func parsePositiveInt(raw string, fallback, minVal, maxVal int) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	if v < minVal {
		return 0, false
	}
	if maxVal > 0 && v > maxVal {
		return 0, false
	}
	return v, true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("article response encode failed", "err", err)
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
