// Package mobiledoc is the Go port of legacy NestJS mobile/ — the MobileDoc
// CMS surface served by the content app (app.module.ts) under three prefixes:
//
//	/api/mobile/docs/*       MobileController        (legacy, raw responses)
//	/api/mobile/v1/docs/*    MobileV1Controller      (SuccessResponse envelope)
//	/api/web/v1/docs/*       WebV1Controller         (SuccessResponse, web-enhanced)
//
// All three are backed by the same `mobile_docs` table and the MobileService
// logic (legacy NestJS mobile/mobile.service.ts). Parity notes:
//
//   - Auth: the deployed content app wires a global JwtAuthGuard, so routes
//     WITHOUT @Public() are JWT-guarded. We mirror the @Public() map exactly:
//     the legacy reads + create/batch/clear are public; the legacy
//     PATCH/DELETE/unpublish and EVERY v1 / web-v1 route are guarded (mounted
//     under the access middleware; nil guard → open, as elsewhere in api-go).
//   - Response shape: Go serves RAW (ADR 0005). The legacy controller returns
//     raw rows/arrays; the v1 + web controllers declare a SuccessResponse
//     envelope ({success,data,message?,traceId,timestamp}) which we reproduce.
//   - The MobileDoc id is `String @id @default(uuid())` (app-generated in
//     Prisma, no DB default) — raw inserts use gen_random_uuid()::text, the
//     same trick the other api-go writers use.
package mobiledoc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrPoolUnavailable mirrors the degraded-contract sentinel used across api-go.
var ErrPoolUnavailable = fmt.Errorf("mobiledoc repository: database pool not configured")

// ErrNotFound surfaces a 404 — mirrors the NestJS NotFoundException raised by
// findOne / update / remove.
var ErrNotFound = errors.New("文档不存在")

// ErrInvalidInput surfaces a 400 for validation failures (title/content/category).
var ErrInvalidInput = errors.New("invalid mobile doc input")

// validCategories mirrors enum DocCategory in the Prisma schema.
var validCategories = map[string]bool{
	"LATEST": true, "FRONTEND": true, "BACKEND": true,
	"AI": true, "MOBILE": true, "DESIGN": true,
}

// categoryLabels mirrors MobileService.getCategoryLabel.
var categoryLabels = map[string]string{
	"LATEST": "最新", "FRONTEND": "前端", "BACKEND": "后端",
	"AI": "AI", "MOBILE": "移动端", "DESIGN": "设计",
}

const docColumns = `id, title, content, summary, "filePath", category, tags, "isHot", "sortOrder", published, "createdAt", "updatedAt"`

// MobileDoc mirrors the `mobile_docs` row. JSON field names match Prisma's
// (camelCase, no @map on fields), so the frontend contract is unchanged.
type MobileDoc struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Summary   *string   `json:"summary"`
	FilePath  *string   `json:"filePath"`
	Category  string    `json:"category"`
	Tags      []string  `json:"tags"`
	IsHot     bool      `json:"isHot"`
	SortOrder int       `json:"sortOrder"`
	Published bool      `json:"published"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// PaginatedResult mirrors mobile.service.ts PaginatedResult<T>.
type PaginatedResult struct {
	Items    []MobileDoc `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"pageSize"`
	HasMore  bool        `json:"hasMore"`
}

// CategoryCount mirrors getCategories() entries.
type CategoryCount struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// StatsResult mirrors getStats().
type StatsResult struct {
	Total       int64           `json:"total"`
	Published   int64           `json:"published"`
	Draft       int64           `json:"draft"`
	Hot         int64           `json:"hot"`
	Categories  []CategoryCount `json:"categories"`
	LastUpdated time.Time       `json:"lastUpdated"`
}

// QueryParams mirrors QueryMobileDocDto after transform/defaults.
type QueryParams struct {
	Page      int
	PageSize  int
	Category  string
	Search    string
	Tag       string
	IsHot     *bool
	Published bool
}

// CreateInput mirrors the persisted subset of CreateMobileDocDto (the DTO also
// declares author/readTime/imageUrl/docType, which are not `mobile_docs`
// columns and so cannot persist — accepted and ignored, as in Prisma).
type CreateInput struct {
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	Summary   *string  `json:"summary"`
	FilePath  *string  `json:"filePath"`
	Category  string   `json:"category"`
	Tags      []string `json:"tags"`
	IsHot     *bool    `json:"isHot"`
	SortOrder *int     `json:"sortOrder"`
	Published *bool    `json:"published"`
}

func (c CreateInput) validate() error {
	if strings.TrimSpace(c.Title) == "" || len(c.Title) > 200 {
		return fmt.Errorf("%w: title required, max 200 chars", ErrInvalidInput)
	}
	if strings.TrimSpace(c.Content) == "" {
		return fmt.Errorf("%w: content required", ErrInvalidInput)
	}
	if c.Summary != nil && len(*c.Summary) > 500 {
		return fmt.Errorf("%w: summary max 500 chars", ErrInvalidInput)
	}
	if !validCategories[c.Category] {
		return fmt.Errorf("%w: category must be one of LATEST/FRONTEND/BACKEND/AI/MOBILE/DESIGN", ErrInvalidInput)
	}
	return nil
}

// UpdateInput mirrors UpdateMobileDocDto (all optional). Only set fields update.
type UpdateInput struct {
	Title     *string   `json:"title"`
	Content   *string   `json:"content"`
	Summary   *string   `json:"summary"`
	FilePath  *string   `json:"filePath"`
	Category  *string   `json:"category"`
	Tags      *[]string `json:"tags"`
	IsHot     *bool     `json:"isHot"`
	SortOrder *int      `json:"sortOrder"`
	Published *bool     `json:"published"`
}

// Store is the data surface used by handlers; tests pass a stub.
type Store interface {
	Create(ctx context.Context, in CreateInput) (MobileDoc, error)
	CreateMany(ctx context.Context, ins []CreateInput) (int64, error)
	FindAll(ctx context.Context, q QueryParams) (PaginatedResult, error)
	FindOne(ctx context.Context, id string) (MobileDoc, error)
	GetStatsByCategory(ctx context.Context) (map[string]int64, error)
	GetHotDocs(ctx context.Context, limit int) ([]MobileDoc, error)
	GetRelatedDocs(ctx context.Context, id string, limit int) ([]MobileDoc, error)
	Update(ctx context.Context, id string, patch UpdateInput) (MobileDoc, error)
	Remove(ctx context.Context, id string) error
	SoftRemove(ctx context.Context, id string) (MobileDoc, error)
	ClearAll(ctx context.Context) error
	GetCategories(ctx context.Context) ([]CategoryCount, error)
	GetStats(ctx context.Context) (StatsResult, error)
}

// Repository wraps the pgx pool; a nil pool returns ErrPoolUnavailable.
type Repository struct{ pool *pgxpool.Pool }

// NewRepository tolerates a nil pool.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func scanDoc(row pgx.Row) (MobileDoc, error) {
	var d MobileDoc
	err := row.Scan(&d.ID, &d.Title, &d.Content, &d.Summary, &d.FilePath,
		&d.Category, &d.Tags, &d.IsHot, &d.SortOrder, &d.Published,
		&d.CreatedAt, &d.UpdatedAt)
	if d.Tags == nil {
		d.Tags = []string{}
	}
	return d, err
}

// Create inserts a doc and returns the persisted row. Defaults mirror the
// Prisma model (@default): category FRONTEND, isHot false, sortOrder 0,
// published true, tags [].
func (r *Repository) Create(ctx context.Context, in CreateInput) (MobileDoc, error) {
	if r.pool == nil {
		return MobileDoc{}, ErrPoolUnavailable
	}
	category := in.Category
	if category == "" {
		category = "FRONTEND"
	}
	tags := in.Tags
	if tags == nil {
		tags = []string{}
	}
	isHot := false
	if in.IsHot != nil {
		isHot = *in.IsHot
	}
	sortOrder := 0
	if in.SortOrder != nil {
		sortOrder = *in.SortOrder
	}
	published := true
	if in.Published != nil {
		published = *in.Published
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO mobile_docs
		  (id, title, content, summary, "filePath", category, tags, "isHot", "sortOrder", published, "createdAt", "updatedAt")
		VALUES
		  (gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
		RETURNING `+docColumns,
		in.Title, in.Content, in.Summary, in.FilePath, category, tags, isHot, sortOrder, published)
	return scanDoc(row)
}

// CreateMany mirrors prisma.createMany — returns the inserted count.
func (r *Repository) CreateMany(ctx context.Context, ins []CreateInput) (int64, error) {
	if r.pool == nil {
		return 0, ErrPoolUnavailable
	}
	var count int64
	for _, in := range ins {
		if _, err := r.Create(ctx, in); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// FindAll mirrors the paginated/filtered list. Order: isHot DESC, sortOrder
// DESC, createdAt DESC. Default published=true.
func (r *Repository) FindAll(ctx context.Context, q QueryParams) (PaginatedResult, error) {
	page := q.Page
	if page < 1 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize < 1 {
		pageSize = 10
	}
	result := PaginatedResult{Items: []MobileDoc{}, Page: page, PageSize: pageSize}
	if r.pool == nil {
		return result, ErrPoolUnavailable
	}

	conds := []string{`published = $1`}
	args := []any{q.Published}
	n := 1
	if q.Category != "" && q.Category != "LATEST" {
		n++
		conds = append(conds, fmt.Sprintf(`category = $%d`, n))
		args = append(args, q.Category)
	}
	if q.Search != "" {
		n++
		conds = append(conds, fmt.Sprintf(`(title ILIKE '%%' || $%d || '%%' OR summary ILIKE '%%' || $%d || '%%' OR content ILIKE '%%' || $%d || '%%')`, n, n, n))
		args = append(args, q.Search)
	}
	if q.Tag != "" {
		n++
		conds = append(conds, fmt.Sprintf(`$%d = ANY(tags)`, n))
		args = append(args, q.Tag)
	}
	if q.IsHot != nil {
		n++
		conds = append(conds, fmt.Sprintf(`"isHot" = $%d`, n))
		args = append(args, *q.IsHot)
	}
	where := "WHERE " + strings.Join(conds, " AND ")

	var total int64
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM mobile_docs `+where, args...).Scan(&total); err != nil {
		return result, fmt.Errorf("count mobile_docs: %w", err)
	}
	result.Total = total

	listArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := r.pool.Query(ctx, `SELECT `+docColumns+` FROM mobile_docs `+where+
		fmt.Sprintf(` ORDER BY "isHot" DESC, "sortOrder" DESC, "createdAt" DESC LIMIT $%d OFFSET $%d`, n+1, n+2),
		listArgs...)
	if err != nil {
		return result, fmt.Errorf("query mobile_docs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return result, fmt.Errorf("scan mobile_doc: %w", err)
		}
		result.Items = append(result.Items, d)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("iterate mobile_docs: %w", err)
	}
	result.HasMore = int64((page-1)*pageSize+pageSize) < total
	return result, nil
}

// FindOne returns a PUBLISHED doc by id (mirrors findFirst{ id, published:true }).
func (r *Repository) FindOne(ctx context.Context, id string) (MobileDoc, error) {
	if r.pool == nil {
		return MobileDoc{}, ErrPoolUnavailable
	}
	row := r.pool.QueryRow(ctx, `SELECT `+docColumns+` FROM mobile_docs WHERE id = $1 AND published = true`, id)
	d, err := scanDoc(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return MobileDoc{}, ErrNotFound
	}
	if err != nil {
		return MobileDoc{}, fmt.Errorf("query mobile_doc: %w", err)
	}
	return d, nil
}

// GetStatsByCategory mirrors groupBy category where published.
func (r *Repository) GetStatsByCategory(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	if r.pool == nil {
		return out, ErrPoolUnavailable
	}
	rows, err := r.pool.Query(ctx, `SELECT category, COUNT(*)::bigint FROM mobile_docs WHERE published = true GROUP BY category`)
	if err != nil {
		return out, fmt.Errorf("group mobile_docs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cat string
		var cnt int64
		if err := rows.Scan(&cat, &cnt); err != nil {
			return out, fmt.Errorf("scan group: %w", err)
		}
		out[cat] = cnt
	}
	return out, rows.Err()
}

// GetHotDocs mirrors findMany{ isHot:true, published:true } limit.
func (r *Repository) GetHotDocs(ctx context.Context, limit int) ([]MobileDoc, error) {
	if limit <= 0 {
		limit = 5
	}
	if r.pool == nil {
		return []MobileDoc{}, ErrPoolUnavailable
	}
	rows, err := r.pool.Query(ctx, `SELECT `+docColumns+` FROM mobile_docs WHERE "isHot" = true AND published = true ORDER BY "sortOrder" DESC, "createdAt" DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query hot docs: %w", err)
	}
	defer rows.Close()
	return collectDocs(rows)
}

// GetRelatedDocs mirrors the related query (same category OR overlapping tags).
func (r *Repository) GetRelatedDocs(ctx context.Context, id string, limit int) ([]MobileDoc, error) {
	if limit <= 0 {
		limit = 5
	}
	current, err := r.FindOne(ctx, id)
	if err != nil {
		return nil, err
	}
	tags := current.Tags
	if tags == nil {
		tags = []string{}
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+docColumns+` FROM mobile_docs
		WHERE id <> $1 AND published = true AND (category = $2 OR tags && $3)
		ORDER BY "isHot" DESC, "createdAt" DESC LIMIT $4`,
		id, current.Category, tags, limit)
	if err != nil {
		return nil, fmt.Errorf("query related docs: %w", err)
	}
	defer rows.Close()
	return collectDocs(rows)
}

// Update mirrors update(): verify the doc exists+published, then patch.
func (r *Repository) Update(ctx context.Context, id string, patch UpdateInput) (MobileDoc, error) {
	if r.pool == nil {
		return MobileDoc{}, ErrPoolUnavailable
	}
	if _, err := r.FindOne(ctx, id); err != nil {
		return MobileDoc{}, err
	}
	sets := []string{}
	args := []any{}
	n := 0
	add := func(col string, val any) {
		n++
		sets = append(sets, fmt.Sprintf(`%s = $%d`, col, n))
		args = append(args, val)
	}
	if patch.Title != nil {
		add(`title`, *patch.Title)
	}
	if patch.Content != nil {
		add(`content`, *patch.Content)
	}
	if patch.Summary != nil {
		add(`summary`, *patch.Summary)
	}
	if patch.FilePath != nil {
		add(`"filePath"`, *patch.FilePath)
	}
	if patch.Category != nil {
		add(`category`, *patch.Category)
	}
	if patch.Tags != nil {
		add(`tags`, *patch.Tags)
	}
	if patch.IsHot != nil {
		add(`"isHot"`, *patch.IsHot)
	}
	if patch.SortOrder != nil {
		add(`"sortOrder"`, *patch.SortOrder)
	}
	if patch.Published != nil {
		add(`published`, *patch.Published)
	}
	sets = append(sets, `"updatedAt" = NOW()`)
	n++
	args = append(args, id)
	row := r.pool.QueryRow(ctx, `UPDATE mobile_docs SET `+strings.Join(sets, ", ")+
		fmt.Sprintf(` WHERE id = $%d RETURNING `, n)+docColumns, args...)
	return scanDoc(row)
}

// Remove mirrors remove(): verify exists (published) then delete.
func (r *Repository) Remove(ctx context.Context, id string) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	if _, err := r.FindOne(ctx, id); err != nil {
		return err
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM mobile_docs WHERE id = $1`, id)
	return err
}

// SoftRemove mirrors softRemove(): set published=false (via Update).
func (r *Repository) SoftRemove(ctx context.Context, id string) (MobileDoc, error) {
	falseVal := false
	return r.Update(ctx, id, UpdateInput{Published: &falseVal})
}

// ClearAll mirrors clearAll(): deleteMany({}).
func (r *Repository) ClearAll(ctx context.Context) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	_, err := r.pool.Exec(ctx, `DELETE FROM mobile_docs`)
	return err
}

// GetCategories mirrors getCategories(): grouped, labelled, count DESC.
func (r *Repository) GetCategories(ctx context.Context) ([]CategoryCount, error) {
	out := []CategoryCount{}
	if r.pool == nil {
		return out, ErrPoolUnavailable
	}
	rows, err := r.pool.Query(ctx, `SELECT category, COUNT(*)::bigint AS cnt FROM mobile_docs WHERE published = true GROUP BY category ORDER BY cnt DESC`)
	if err != nil {
		return out, fmt.Errorf("query categories: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c CategoryCount
		if err := rows.Scan(&c.Value, &c.Count); err != nil {
			return out, fmt.Errorf("scan category: %w", err)
		}
		c.Label = categoryLabel(c.Value)
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetStats mirrors getStats(): total/published/draft/hot/categories/lastUpdated.
func (r *Repository) GetStats(ctx context.Context) (StatsResult, error) {
	out := StatsResult{Categories: []CategoryCount{}, LastUpdated: time.Now().UTC()}
	if r.pool == nil {
		return out, ErrPoolUnavailable
	}
	if err := r.pool.QueryRow(ctx, `
		SELECT
		  COUNT(*)::bigint,
		  COUNT(*) FILTER (WHERE published = true)::bigint,
		  COUNT(*) FILTER (WHERE "isHot" = true)::bigint
		FROM mobile_docs`).Scan(&out.Total, &out.Published, &out.Hot); err != nil {
		return out, fmt.Errorf("stats counts: %w", err)
	}
	out.Draft = out.Total - out.Published
	cats, err := r.GetCategories(ctx)
	if err != nil {
		return out, err
	}
	out.Categories = cats
	return out, nil
}

func collectDocs(rows pgx.Rows) ([]MobileDoc, error) {
	out := []MobileDoc{}
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return out, fmt.Errorf("scan mobile_doc: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func categoryLabel(cat string) string {
	if l, ok := categoryLabels[cat]; ok {
		return l
	}
	return cat
}

// Service wires a Store for the HTTP layer.
type Service struct{ store Store }

// NewService permits a nil store for the degraded contract.
func NewService(store Store) *Service { return &Service{store: store} }
