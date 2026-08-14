package mobilebff

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound surfaces a 404 to a handler.
var ErrNotFound = errors.New("not found")

// ErrPoolUnavailable is returned by writes when the pool is nil.
var ErrPoolUnavailable = errors.New("mobilebff repository: database pool not configured")

// ArticleRow is the article projection used to build cards.
type ArticleRow struct {
	ID             string
	Title          string
	Summary        *string
	Content        *string
	FeaturedImage  *string
	ViewCount      int64
	Published      bool
	PublishedAt    *time.Time
	CreatedAt      time.Time
	AuthorID       *string
	AuthorUsername *string
	AuthorFullName *string
	AuthorAvatar   *string
	AuthorBio      *string
	CategoryName   *string
	CommentCount   int64
	Tags           []string
}

// UserRow is the user projection (+ counts).
type UserRow struct {
	ID                string
	Username          string
	FullName          *string
	Avatar            *string
	Bio               *string
	ArticleCount      int64
	CommentCount      int64
	ConversationCount int64
}

// TagRow is the tag projection (+ article count).
type TagRow struct {
	ID           string
	Name         string
	Slug         *string
	Description  *string
	ArticleCount int64
}

// CommentRow is the comment projection (+ author).
type CommentRow struct {
	ID             string
	Content        string
	ParentID       *string
	CreatedAt      time.Time
	AuthorID       *string
	AuthorUsername *string
	AuthorFullName *string
	AuthorAvatar   *string
	AuthorBio      *string
}

// ConversationRow is the conversation projection (+ latest message).
type ConversationRow struct {
	ID            string
	Title         string
	UpdatedAt     time.Time
	CreatedAt     time.Time
	LatestContent *string
	LatestRole    *string
	LatestReadAt  *time.Time
	LatestUserID  *string
}

// Store is the data surface the service needs; *Repository implements it and
// tests pass a stub.
type Store interface {
	FeedArticles(ctx context.Context, limit int) ([]ArticleRow, error)
	RelatedArticles(ctx context.Context, excludeID string, limit int) ([]ArticleRow, error)
	SearchArticles(ctx context.Context, query string, limit int) ([]ArticleRow, error)
	ArticleByID(ctx context.Context, id string) (ArticleRow, error)
	AuthoredArticles(ctx context.Context, userID string, limit int) ([]ArticleRow, error)
	TopTags(ctx context.Context, limit int) ([]TagRow, error)
	TopUsers(ctx context.Context, limit int) ([]UserRow, error)
	UserProfile(ctx context.Context, userID string) (UserRow, error)
	CommentsByArticle(ctx context.Context, articleID string) ([]CommentRow, error)
	ArticleTitleAndCommentCount(ctx context.Context, articleID string) (string, int64, bool)
	ArticlePublished(ctx context.Context, id string) (bool, bool)
	CommentArticleID(ctx context.Context, commentID string) (string, bool)
	CommentCount(ctx context.Context, articleID string) (int64, error)
	CreateComment(ctx context.Context, articleID, authorID string, parentID *string, content string) (CommentRow, error)
	ConversationsWithLatest(ctx context.Context, userID string, limit int) ([]ConversationRow, error)
	UnreadIncomingCount(ctx context.Context, userID string) (int64, error)
	FindUnreadMessageIDs(ctx context.Context, userID string, messageIDs, conversationIDs []string) ([]string, error)
	MarkMessagesRead(ctx context.Context, ids []string) error
	TagWithArticles(ctx context.Context, slug string, limit int) (TagRow, []ArticleRow, bool)
	TagForShare(ctx context.Context, resourceID string) (TagRow, bool)
}

// Repository wraps the pgx pool; reads degrade to empty on a nil pool.
type Repository struct{ pool *pgxpool.Pool }

// NewRepository tolerates a nil pool.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const articleSelect = `
	SELECT a.id, a.title, a.summary, a.content, a."featuredImage", a."viewCount",
	       a.published, a."publishedAt", a."createdAt",
	       u.id, u.username, u."fullName", u.avatar, u.bio,
	       c.name,
	       (SELECT COUNT(*) FROM comments cm WHERE cm."articleId" = a.id)::bigint,
	       (SELECT COALESCE(array_agg(t.name ORDER BY t.name), ARRAY[]::text[])
	          FROM "_ArticleToTag" jt JOIN tags t ON t.id = jt."B" WHERE jt."A" = a.id)
	FROM articles a
	LEFT JOIN users u ON u.id = a."authorId"
	LEFT JOIN categories c ON c.id = a."categoryId"`

func scanArticle(row pgx.Row) (ArticleRow, error) {
	var a ArticleRow
	err := row.Scan(&a.ID, &a.Title, &a.Summary, &a.Content, &a.FeaturedImage, &a.ViewCount,
		&a.Published, &a.PublishedAt, &a.CreatedAt,
		&a.AuthorID, &a.AuthorUsername, &a.AuthorFullName, &a.AuthorAvatar, &a.AuthorBio,
		&a.CategoryName, &a.CommentCount, &a.Tags)
	if a.Tags == nil {
		a.Tags = []string{}
	}
	return a, err
}

func (r *Repository) queryArticles(ctx context.Context, sql string, args ...any) ([]ArticleRow, error) {
	out := []ArticleRow{}
	if r.pool == nil {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return out, fmt.Errorf("query articles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanArticle(rows)
		if err != nil {
			return out, fmt.Errorf("scan article: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FeedArticles returns the most recent published articles.
func (r *Repository) FeedArticles(ctx context.Context, limit int) ([]ArticleRow, error) {
	return r.queryArticles(ctx, articleSelect+`
		WHERE a.published = true
		ORDER BY a."publishedAt" DESC NULLS LAST, a."createdAt" DESC
		LIMIT $1`, limit)
}

// RelatedArticles returns published articles excluding one id.
func (r *Repository) RelatedArticles(ctx context.Context, excludeID string, limit int) ([]ArticleRow, error) {
	return r.queryArticles(ctx, articleSelect+`
		WHERE a.id <> $1 AND a.published = true
		ORDER BY a."publishedAt" DESC NULLS LAST, a."createdAt" DESC
		LIMIT $2`, excludeID, limit)
}

// SearchArticles returns published articles optionally filtered by a query.
func (r *Repository) SearchArticles(ctx context.Context, query string, limit int) ([]ArticleRow, error) {
	limit = normalizeSearchResultLimit(limit)
	query = strings.TrimSpace(query)
	if query == "" {
		return r.queryArticles(ctx, articleSelect+`
			WHERE a.published = true
			ORDER BY a."publishedAt" DESC NULLS LAST, a."createdAt" DESC
			LIMIT $1`, limit)
	}
	return r.queryArticles(ctx, articleSelect+`
		WHERE a.published = true AND (
		  a.title ILIKE '%' || $1 || '%'
		  OR COALESCE(a.summary,'') ILIKE '%' || $1 || '%'
		  OR COALESCE(a.content,'') ILIKE '%' || $1 || '%')
		ORDER BY a."publishedAt" DESC NULLS LAST, a."createdAt" DESC
		LIMIT $2`, query, limit)
}

// ArticleByID returns one article (any published state) — nil pool / miss → ErrNotFound.
func (r *Repository) ArticleByID(ctx context.Context, id string) (ArticleRow, error) {
	if r.pool == nil {
		return ArticleRow{}, ErrNotFound
	}
	a, err := scanArticle(r.pool.QueryRow(ctx, articleSelect+` WHERE a.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return ArticleRow{}, ErrNotFound
	}
	if err != nil {
		return ArticleRow{}, fmt.Errorf("query article: %w", err)
	}
	return a, nil
}

// AuthoredArticles returns a user's published articles.
func (r *Repository) AuthoredArticles(ctx context.Context, userID string, limit int) ([]ArticleRow, error) {
	return r.queryArticles(ctx, articleSelect+`
		WHERE a."authorId" = $1 AND a.published = true
		ORDER BY a."publishedAt" DESC NULLS LAST, a."createdAt" DESC
		LIMIT $2`, userID, limit)
}

// ArticlesForTag returns published articles joined to a tag.
func (r *Repository) ArticlesForTag(ctx context.Context, tagID string, limit int) ([]ArticleRow, error) {
	return r.queryArticles(ctx, articleSelect+`
		JOIN "_ArticleToTag" jt2 ON jt2."A" = a.id
		WHERE jt2."B" = $1 AND a.published = true
		ORDER BY a."publishedAt" DESC NULLS LAST, a."createdAt" DESC
		LIMIT $2`, tagID, limit)
}

// TopTags returns tags with article counts (service sorts by count).
func (r *Repository) TopTags(ctx context.Context, limit int) ([]TagRow, error) {
	limit = normalizeSearchResultLimit(limit)
	out := []TagRow{}
	if r.pool == nil {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT t.id, t.name, t.slug, t.description,
		       (SELECT COUNT(*) FROM "_ArticleToTag" jt WHERE jt."B" = t.id)::bigint
		FROM tags t ORDER BY t.name ASC LIMIT $1`, limit)
	if err != nil {
		return out, fmt.Errorf("query tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t TagRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.Description, &t.ArticleCount); err != nil {
			return out, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TopUsers returns recent users with article counts (service filters count>0).
func (r *Repository) TopUsers(ctx context.Context, limit int) ([]UserRow, error) {
	limit = normalizeSearchResultLimit(limit)
	out := []UserRow{}
	if r.pool == nil {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT u.id, u.username, u."fullName", u.avatar, u.bio,
		       (SELECT COUNT(*) FROM articles a WHERE a."authorId" = u.id)::bigint
		FROM users u ORDER BY u."createdAt" DESC LIMIT $1`, limit)
	if err != nil {
		return out, fmt.Errorf("query users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.Username, &u.FullName, &u.Avatar, &u.Bio, &u.ArticleCount); err != nil {
			return out, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserProfile returns a user with counts (nil pool / miss → ErrNotFound).
func (r *Repository) UserProfile(ctx context.Context, userID string) (UserRow, error) {
	if r.pool == nil {
		return UserRow{}, ErrNotFound
	}
	var u UserRow
	err := r.pool.QueryRow(ctx, `
		SELECT u.id, u.username, u."fullName", u.avatar, u.bio,
		       (SELECT COUNT(*) FROM articles a WHERE a."authorId"=u.id)::bigint,
		       (SELECT COUNT(*) FROM comments cm WHERE cm."authorId"=u.id)::bigint,
		       (SELECT COUNT(*) FROM conversations co WHERE co."userId"=u.id)::bigint
		FROM users u WHERE u.id = $1`, userID).
		Scan(&u.ID, &u.Username, &u.FullName, &u.Avatar, &u.Bio, &u.ArticleCount, &u.CommentCount, &u.ConversationCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRow{}, ErrNotFound
	}
	if err != nil {
		return UserRow{}, fmt.Errorf("query user profile: %w", err)
	}
	return u, nil
}

// CommentsByArticle returns comments (with author) ordered oldest-first.
func (r *Repository) CommentsByArticle(ctx context.Context, articleID string) ([]CommentRow, error) {
	out := []CommentRow{}
	if r.pool == nil {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT cm.id, cm.content, cm."parentId", cm."createdAt",
		       u.id, u.username, u."fullName", u.avatar, u.bio
		FROM comments cm LEFT JOIN users u ON u.id = cm."authorId"
		WHERE cm."articleId" = $1 ORDER BY cm."createdAt" ASC`, articleID)
	if err != nil {
		return out, fmt.Errorf("query comments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c CommentRow
		if err := rows.Scan(&c.ID, &c.Content, &c.ParentID, &c.CreatedAt,
			&c.AuthorID, &c.AuthorUsername, &c.AuthorFullName, &c.AuthorAvatar, &c.AuthorBio); err != nil {
			return out, fmt.Errorf("scan comment: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ArticleTitleAndCommentCount returns (title, count, found).
func (r *Repository) ArticleTitleAndCommentCount(ctx context.Context, articleID string) (string, int64, bool) {
	if r.pool == nil {
		return "", 0, false
	}
	var title string
	var count int64
	err := r.pool.QueryRow(ctx, `
		SELECT a.title, (SELECT COUNT(*) FROM comments cm WHERE cm."articleId"=a.id)::bigint
		FROM articles a WHERE a.id = $1`, articleID).Scan(&title, &count)
	if err != nil {
		return "", 0, false
	}
	return title, count, true
}

// ArticlePublished returns (published, found).
func (r *Repository) ArticlePublished(ctx context.Context, id string) (bool, bool) {
	if r.pool == nil {
		return false, false
	}
	var published bool
	if err := r.pool.QueryRow(ctx, `SELECT published FROM articles WHERE id = $1`, id).Scan(&published); err != nil {
		return false, false
	}
	return published, true
}

// CommentArticleID returns (articleId, found) for a comment.
func (r *Repository) CommentArticleID(ctx context.Context, commentID string) (string, bool) {
	if r.pool == nil {
		return "", false
	}
	var articleID string
	if err := r.pool.QueryRow(ctx, `SELECT "articleId" FROM comments WHERE id = $1`, commentID).Scan(&articleID); err != nil {
		return "", false
	}
	return articleID, true
}

// CommentCount returns the comment count for an article.
func (r *Repository) CommentCount(ctx context.Context, articleID string) (int64, error) {
	if r.pool == nil {
		return 0, ErrPoolUnavailable
	}
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*)::bigint FROM comments WHERE "articleId" = $1`, articleID).Scan(&count)
	return count, err
}

// CreateComment inserts a comment and returns it with its author.
func (r *Repository) CreateComment(ctx context.Context, articleID, authorID string, parentID *string, content string) (CommentRow, error) {
	if r.pool == nil {
		return CommentRow{}, ErrPoolUnavailable
	}
	var c CommentRow
	err := r.pool.QueryRow(ctx, `
		WITH ins AS (
		  INSERT INTO comments (id, content, "authorId", "articleId", "parentId", "createdAt", "updatedAt")
		  VALUES (gen_random_uuid()::text, $1, $2, $3, $4, NOW(), NOW())
		  RETURNING id, content, "parentId", "createdAt", "authorId"
		)
		SELECT ins.id, ins.content, ins."parentId", ins."createdAt",
		       u.id, u.username, u."fullName", u.avatar, u.bio
		FROM ins LEFT JOIN users u ON u.id = ins."authorId"`,
		content, authorID, articleID, parentID).
		Scan(&c.ID, &c.Content, &c.ParentID, &c.CreatedAt,
			&c.AuthorID, &c.AuthorUsername, &c.AuthorFullName, &c.AuthorAvatar, &c.AuthorBio)
	if err != nil {
		return CommentRow{}, fmt.Errorf("create comment: %w", err)
	}
	return c, nil
}

// ConversationsWithLatest returns a user's conversations with the latest message.
func (r *Repository) ConversationsWithLatest(ctx context.Context, userID string, limit int) ([]ConversationRow, error) {
	out := []ConversationRow{}
	if r.pool == nil {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT co.id, co.title, co."updatedAt", co."createdAt",
		       lm.content, COALESCE(lm."roleEnum"::text, lm.role), lm."readAt", lm."userId"
		FROM conversations co
		LEFT JOIN LATERAL (
		  SELECT content, "roleEnum", role, "readAt", "userId"
		  FROM messages m WHERE m."conversationId" = co.id
		  ORDER BY m."createdAt" DESC LIMIT 1
		) lm ON true
		WHERE co."userId" = $1
		ORDER BY co."updatedAt" DESC, co."createdAt" DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return out, fmt.Errorf("query conversations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c ConversationRow
		if err := rows.Scan(&c.ID, &c.Title, &c.UpdatedAt, &c.CreatedAt,
			&c.LatestContent, &c.LatestRole, &c.LatestReadAt, &c.LatestUserID); err != nil {
			return out, fmt.Errorf("scan conversation: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UnreadIncomingCount counts unread incoming messages for a user.
func (r *Repository) UnreadIncomingCount(ctx context.Context, userID string) (int64, error) {
	if r.pool == nil {
		return 0, nil
	}
	var count int64
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)::bigint FROM messages m
		JOIN conversations co ON co.id = m."conversationId"
		WHERE co."userId" = $1 AND m."readAt" IS NULL AND (m."userId" IS NULL OR m."userId" <> $1)`, userID).Scan(&count)
	return count, err
}

// FindUnreadMessageIDs returns unread incoming message ids matching the targets.
func (r *Repository) FindUnreadMessageIDs(ctx context.Context, userID string, messageIDs, conversationIDs []string) ([]string, error) {
	out := []string{}
	if r.pool == nil || (len(messageIDs) == 0 && len(conversationIDs) == 0) {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT m.id FROM messages m
		JOIN conversations co ON co.id = m."conversationId"
		WHERE co."userId" = $1 AND m."readAt" IS NULL AND (m."userId" IS NULL OR m."userId" <> $1)
		  AND (m.id = ANY($2) OR m."conversationId" = ANY($3))`,
		userID, messageIDs, conversationIDs)
	if err != nil {
		return out, fmt.Errorf("query unread messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return out, fmt.Errorf("scan message id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// MarkMessagesRead sets readAt on the given message ids.
func (r *Repository) MarkMessagesRead(ctx context.Context, ids []string) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx, `UPDATE messages SET "readAt" = NOW() WHERE id = ANY($1)`, ids)
	return err
}

// TagWithArticles finds a tag by slug or humanized name + its published articles.
func (r *Repository) TagWithArticles(ctx context.Context, slug string, limit int) (TagRow, []ArticleRow, bool) {
	if r.pool == nil {
		return TagRow{}, nil, false
	}
	nameGuess := strings.ReplaceAll(slug, "-", " ")
	var t TagRow
	err := r.pool.QueryRow(ctx, `
		SELECT t.id, t.name, t.slug, t.description
		FROM tags t WHERE t.slug = $1 OR LOWER(t.name) = LOWER($2) LIMIT 1`, slug, nameGuess).
		Scan(&t.ID, &t.Name, &t.Slug, &t.Description)
	if err != nil {
		return TagRow{}, nil, false
	}
	arts, _ := r.ArticlesForTag(ctx, t.ID, limit)
	return t, arts, true
}

// TagForShare finds a tag by id, slug, or humanized name.
func (r *Repository) TagForShare(ctx context.Context, resourceID string) (TagRow, bool) {
	if r.pool == nil {
		return TagRow{}, false
	}
	nameGuess := strings.ReplaceAll(resourceID, "-", " ")
	var t TagRow
	err := r.pool.QueryRow(ctx, `
		SELECT t.id, t.name, t.slug, t.description
		FROM tags t WHERE t.id = $1 OR t.slug = $1 OR LOWER(t.name) = LOWER($2) LIMIT 1`, resourceID, nameGuess).
		Scan(&t.ID, &t.Name, &t.Slug, &t.Description)
	if err != nil {
		return TagRow{}, false
	}
	return t, true
}
