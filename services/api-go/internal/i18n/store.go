package i18n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Domain records.

type Locale struct {
	Code        string     `json:"code"`
	EnglishName string     `json:"englishName"`
	NativeName  string     `json:"nativeName"`
	Enabled     bool       `json:"enabled"`
	IsDefault   bool       `json:"isDefault"`
	UpdatedBy   *string    `json:"updatedBy,omitempty"`
	UpdatedAt   *time.Time `json:"updatedAt,omitempty"`
}

type DraftRow struct {
	Namespace    string    `json:"namespace"`
	Key          string    `json:"key"`
	Value        string    `json:"value"`
	Version      int       `json:"version"`
	UpdatedBy    *string   `json:"updatedBy,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
	DefaultValue *string   `json:"defaultValue,omitempty"`
}

type NamespaceStat struct {
	Namespace string `json:"namespace"`
	Keys      int    `json:"keys"`
	Missing   int    `json:"missing"` // default-locale keys absent from this locale
}

type Revision struct {
	ID               string          `json:"id"`
	LocaleCode       string          `json:"locale"`
	Version          int             `json:"version"`
	Catalog          json.RawMessage `json:"-"`
	Checksum         string          `json:"checksum"`
	PublishedBy      string          `json:"publishedBy"`
	PublishedAt      time.Time       `json:"publishedAt"`
	SourceRevisionID *string         `json:"sourceRevisionId,omitempty"`
	KeyCount         int             `json:"keyCount,omitempty"`
}

type AuditRow struct {
	ID         string          `json:"id"`
	Actor      string          `json:"actor"`
	Action     string          `json:"action"`
	LocaleCode *string         `json:"locale,omitempty"`
	Namespace  *string         `json:"namespace,omitempty"`
	MessageKey *string         `json:"key,omitempty"`
	Metadata   json.RawMessage `json:"metadata"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type AuditEntry struct {
	Actor      string
	Action     string
	LocaleCode string
	Namespace  string
	MessageKey string
	Metadata   map[string]any
}

// Sentinel errors mapped to HTTP statuses by the handlers.
var (
	ErrNotFound        = errors.New("i18n: not found")
	ErrVersionConflict = errors.New("i18n: draft version conflict")
	ErrLocaleExists    = errors.New("i18n: locale already exists")
)

// Store is the persistence boundary. The pg implementation below is the
// production store; tests substitute a fake.
type Store interface {
	ListLocales(ctx context.Context) ([]Locale, error)
	GetLocale(ctx context.Context, code string) (Locale, bool, error)
	CreateLocale(ctx context.Context, loc Locale, actor string) error
	UpdateLocale(ctx context.Context, loc Locale, clearOtherDefault bool, audit AuditEntry) error

	ListDrafts(ctx context.Context, locale string) ([]FlatMessage, error)
	PageDrafts(ctx context.Context, locale, defaultLocale, namespace, search string, page, pageSize int) ([]DraftRow, int, error)
	Namespaces(ctx context.Context, locale, defaultLocale string) ([]NamespaceStat, error)
	UpsertDraft(ctx context.Context, locale, namespace, key, value string, expectedVersion int, actor string) (DraftRow, error)

	LatestRevision(ctx context.Context, locale string) (*Revision, error)
	GetRevision(ctx context.Context, id string) (*Revision, error)
	PageRevisions(ctx context.Context, locale string, page, pageSize int) ([]Revision, int, error)
	CreateRevision(ctx context.Context, locale string, catalog json.RawMessage, checksum, publishedBy string, sourceRevisionID *string, resetDrafts []FlatMessage, audit AuditEntry) (Revision, error)

	InsertAudit(ctx context.Context, e AuditEntry) error
	PageAudit(ctx context.Context, locale, actor, action string, page, pageSize int) ([]AuditRow, int, error)
}

// PgStore is the PostgreSQL store.
type PgStore struct{ pool *pgxpool.Pool }

func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{pool: pool} }

// ErrPoolUnavailable marks a store without a database pool. The public
// catalog degrades to the embedded baseline; admin operations surface 500.
var ErrPoolUnavailable = errors.New("i18n: database pool not configured")

func (s *PgStore) ready() error {
	if s == nil || s.pool == nil {
		return ErrPoolUnavailable
	}
	return nil
}

func (s *PgStore) ListLocales(ctx context.Context) ([]Locale, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT code, english_name, native_name, enabled, is_default, updated_by, updated_at
		FROM i18n_locales ORDER BY is_default DESC, code ASC`)
	if err != nil {
		return nil, fmt.Errorf("i18n list locales: %w", err)
	}
	defer rows.Close()
	var out []Locale
	for rows.Next() {
		var l Locale
		if err := rows.Scan(&l.Code, &l.EnglishName, &l.NativeName, &l.Enabled, &l.IsDefault, &l.UpdatedBy, &l.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *PgStore) GetLocale(ctx context.Context, code string) (Locale, bool, error) {
	if err := s.ready(); err != nil {
		return Locale{}, false, err
	}
	var l Locale
	err := s.pool.QueryRow(ctx, `
		SELECT code, english_name, native_name, enabled, is_default, updated_by, updated_at
		FROM i18n_locales WHERE code = $1`, code).
		Scan(&l.Code, &l.EnglishName, &l.NativeName, &l.Enabled, &l.IsDefault, &l.UpdatedBy, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Locale{}, false, nil
	}
	if err != nil {
		return Locale{}, false, fmt.Errorf("i18n get locale: %w", err)
	}
	return l, true, nil
}

func (s *PgStore) CreateLocale(ctx context.Context, loc Locale, actor string) error {
	if err := s.ready(); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO i18n_locales (code, english_name, native_name, enabled, is_default, created_by, updated_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,false,$5,$5,NOW(),NOW())
		ON CONFLICT (code) DO NOTHING`,
		loc.Code, loc.EnglishName, loc.NativeName, loc.Enabled, actor)
	if err != nil {
		return fmt.Errorf("i18n create locale: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLocaleExists
	}
	return s.InsertAudit(ctx, AuditEntry{Actor: actor, Action: "locale.create", LocaleCode: loc.Code,
		Metadata: map[string]any{"englishName": loc.EnglishName, "enabled": loc.Enabled}})
}

func (s *PgStore) UpdateLocale(ctx context.Context, loc Locale, clearOtherDefault bool, audit AuditEntry) error {
	if err := s.ready(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if clearOtherDefault {
		if _, err := tx.Exec(ctx, `UPDATE i18n_locales SET is_default = false, updated_at = NOW() WHERE is_default AND code <> $1`, loc.Code); err != nil {
			return fmt.Errorf("i18n clear default: %w", err)
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE i18n_locales SET english_name=$2, native_name=$3, enabled=$4, is_default=$5, updated_by=$6, updated_at=NOW()
		WHERE code=$1`,
		loc.Code, loc.EnglishName, loc.NativeName, loc.Enabled, loc.IsDefault, audit.Actor)
	if err != nil {
		return fmt.Errorf("i18n update locale: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := insertAuditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) ListDrafts(ctx context.Context, locale string) ([]FlatMessage, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT namespace, message_key, value FROM i18n_message_drafts
		WHERE locale_code = $1 ORDER BY namespace, message_key`, locale)
	if err != nil {
		return nil, fmt.Errorf("i18n list drafts: %w", err)
	}
	defer rows.Close()
	var out []FlatMessage
	for rows.Next() {
		var m FlatMessage
		if err := rows.Scan(&m.Namespace, &m.Key, &m.Value); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *PgStore) PageDrafts(ctx context.Context, locale, defaultLocale, namespace, search string, page, pageSize int) ([]DraftRow, int, error) {
	if err := s.ready(); err != nil {
		return nil, 0, err
	}
	where := `d.locale_code = $1`
	args := []any{locale, defaultLocale}
	if namespace != "" {
		args = append(args, namespace)
		where += fmt.Sprintf(" AND d.namespace = $%d", len(args))
	}
	if search != "" {
		args = append(args, "%"+search+"%")
		where += fmt.Sprintf(" AND (d.message_key ILIKE $%d OR d.value ILIKE $%d)", len(args), len(args))
	}
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM i18n_message_drafts d WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("i18n count drafts: %w", err)
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, `
		SELECT d.namespace, d.message_key, d.value, d.version, d.updated_by, d.updated_at, def.value
		FROM i18n_message_drafts d
		LEFT JOIN i18n_message_drafts def
		  ON def.locale_code = $2 AND def.namespace = d.namespace AND def.message_key = d.message_key
		WHERE `+where+`
		ORDER BY d.namespace, d.message_key
		LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("i18n page drafts: %w", err)
	}
	defer rows.Close()
	var out []DraftRow
	for rows.Next() {
		var d DraftRow
		if err := rows.Scan(&d.Namespace, &d.Key, &d.Value, &d.Version, &d.UpdatedBy, &d.UpdatedAt, &d.DefaultValue); err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}

func (s *PgStore) Namespaces(ctx context.Context, locale, defaultLocale string) ([]NamespaceStat, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT def.namespace,
		       COUNT(*) AS def_keys,
		       COUNT(*) FILTER (WHERE tgt.message_key IS NULL) AS missing
		FROM i18n_message_drafts def
		LEFT JOIN i18n_message_drafts tgt
		  ON tgt.locale_code = $1 AND tgt.namespace = def.namespace AND tgt.message_key = def.message_key
		WHERE def.locale_code = $2
		GROUP BY def.namespace ORDER BY def.namespace`, locale, defaultLocale)
	if err != nil {
		return nil, fmt.Errorf("i18n namespaces: %w", err)
	}
	defer rows.Close()
	var out []NamespaceStat
	for rows.Next() {
		var n NamespaceStat
		if err := rows.Scan(&n.Namespace, &n.Keys, &n.Missing); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *PgStore) UpsertDraft(ctx context.Context, locale, namespace, key, value string, expectedVersion int, actor string) (DraftRow, error) {
	if err := s.ready(); err != nil {
		return DraftRow{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DraftRow{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var d DraftRow
	var before *string
	if err := tx.QueryRow(ctx, `SELECT value FROM i18n_message_drafts WHERE locale_code=$1 AND namespace=$2 AND message_key=$3`,
		locale, namespace, key).Scan(&before); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DraftRow{}, fmt.Errorf("i18n read draft before-value: %w", err)
	}
	if expectedVersion <= 0 {
		err = tx.QueryRow(ctx, `
			INSERT INTO i18n_message_drafts (id, locale_code, namespace, message_key, value, version, updated_by, created_at, updated_at)
			VALUES (gen_random_uuid()::text, $1, $2, $3, $4, 1, $5, NOW(), NOW())
			ON CONFLICT (locale_code, namespace, message_key) DO NOTHING
			RETURNING namespace, message_key, value, version, updated_by, updated_at`,
			locale, namespace, key, value, actor).
			Scan(&d.Namespace, &d.Key, &d.Value, &d.Version, &d.UpdatedBy, &d.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return DraftRow{}, ErrVersionConflict // exists already — caller must send its version
		}
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE i18n_message_drafts
			SET value = $5, version = version + 1, updated_by = $6, updated_at = NOW()
			WHERE locale_code=$1 AND namespace=$2 AND message_key=$3 AND version=$4
			RETURNING namespace, message_key, value, version, updated_by, updated_at`,
			locale, namespace, key, expectedVersion, value, actor).
			Scan(&d.Namespace, &d.Key, &d.Value, &d.Version, &d.UpdatedBy, &d.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			// Either the row is missing (404) or the version is stale (409).
			var exists bool
			if e2 := tx.QueryRow(ctx, `SELECT true FROM i18n_message_drafts WHERE locale_code=$1 AND namespace=$2 AND message_key=$3`,
				locale, namespace, key).Scan(&exists); e2 == nil && exists {
				return DraftRow{}, ErrVersionConflict
			}
			return DraftRow{}, ErrNotFound
		}
	}
	if err != nil {
		return DraftRow{}, fmt.Errorf("i18n upsert draft: %w", err)
	}
	if err := insertAuditTx(ctx, tx, AuditEntry{
		Actor: actor, Action: "draft.update", LocaleCode: locale, Namespace: namespace, MessageKey: key,
		Metadata: map[string]any{"before": before, "after": value, "version": d.Version},
	}); err != nil {
		return DraftRow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DraftRow{}, err
	}
	return d, nil
}

func (s *PgStore) LatestRevision(ctx context.Context, locale string) (*Revision, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.scanRevision(s.pool.QueryRow(ctx, `
		SELECT id, locale_code, version, catalog, checksum, published_by, published_at, source_revision_id
		FROM i18n_catalog_revisions WHERE locale_code = $1
		ORDER BY version DESC LIMIT 1`, locale))
}

func (s *PgStore) GetRevision(ctx context.Context, id string) (*Revision, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.scanRevision(s.pool.QueryRow(ctx, `
		SELECT id, locale_code, version, catalog, checksum, published_by, published_at, source_revision_id
		FROM i18n_catalog_revisions WHERE id = $1`, id))
}

func (s *PgStore) scanRevision(row pgx.Row) (*Revision, error) {
	var r Revision
	err := row.Scan(&r.ID, &r.LocaleCode, &r.Version, &r.Catalog, &r.Checksum, &r.PublishedBy, &r.PublishedAt, &r.SourceRevisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("i18n revision scan: %w", err)
	}
	return &r, nil
}

func (s *PgStore) PageRevisions(ctx context.Context, locale string, page, pageSize int) ([]Revision, int, error) {
	if err := s.ready(); err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM i18n_catalog_revisions WHERE locale_code=$1`, locale).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, locale_code, version, checksum, published_by, published_at, source_revision_id
		FROM i18n_catalog_revisions WHERE locale_code = $1
		ORDER BY version DESC LIMIT $2 OFFSET $3`, locale, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.ID, &r.LocaleCode, &r.Version, &r.Checksum, &r.PublishedBy, &r.PublishedAt, &r.SourceRevisionID); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// CreateRevision atomically publishes a new immutable revision (+ audit row),
// optionally resetting drafts to the snapshot (rollback). Version numbering is
// computed inside the transaction; the unique (locale, version) index makes a
// concurrent publish lose cleanly.
func (s *PgStore) CreateRevision(ctx context.Context, locale string, catalog json.RawMessage, checksum, publishedBy string, sourceRevisionID *string, resetDrafts []FlatMessage, audit AuditEntry) (Revision, error) {
	if err := s.ready(); err != nil {
		return Revision{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Revision{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var r Revision
	err = tx.QueryRow(ctx, `
		INSERT INTO i18n_catalog_revisions (id, locale_code, version, catalog, checksum, published_by, published_at, source_revision_id)
		SELECT gen_random_uuid()::text, $1::varchar(35),
		       COALESCE((SELECT MAX(version) FROM i18n_catalog_revisions WHERE locale_code = $1::varchar(35)), 0) + 1,
		       $2::jsonb, $3::varchar(64), $4::varchar(42), NOW(), $5::text
		RETURNING id, locale_code, version, checksum, published_by, published_at, source_revision_id`,
		locale, catalog, checksum, publishedBy, sourceRevisionID).
		Scan(&r.ID, &r.LocaleCode, &r.Version, &r.Checksum, &r.PublishedBy, &r.PublishedAt, &r.SourceRevisionID)
	if err != nil {
		return Revision{}, fmt.Errorf("i18n create revision: %w", err)
	}
	r.Catalog = catalog

	if resetDrafts != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM i18n_message_drafts WHERE locale_code = $1`, locale); err != nil {
			return Revision{}, fmt.Errorf("i18n rollback reset drafts: %w", err)
		}
		for _, m := range resetDrafts {
			if _, err := tx.Exec(ctx, `
				INSERT INTO i18n_message_drafts (id, locale_code, namespace, message_key, value, version, updated_by, created_at, updated_at)
				VALUES (gen_random_uuid()::text, $1, $2, $3, $4, 1, $5, NOW(), NOW())`,
				locale, m.Namespace, m.Key, m.Value, publishedBy); err != nil {
				return Revision{}, fmt.Errorf("i18n rollback insert draft: %w", err)
			}
		}
	}

	if err := insertAuditTx(ctx, tx, audit); err != nil {
		return Revision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Revision{}, err
	}
	return r, nil
}

func (s *PgStore) InsertAudit(ctx context.Context, e AuditEntry) error {
	if err := s.ready(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := insertAuditTx(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func insertAuditTx(ctx context.Context, tx pgx.Tx, e AuditEntry) error {
	meta, err := json.Marshal(sanitizeMetadata(e.Metadata))
	if err != nil {
		meta = []byte(`{}`)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO i18n_audit_logs (id, actor, action, locale_code, namespace, message_key, metadata, created_at)
		VALUES (gen_random_uuid()::text, $1, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), $6, NOW())`,
		strings.ToLower(e.Actor), e.Action, e.LocaleCode, e.Namespace, e.MessageKey, meta)
	if err != nil {
		return fmt.Errorf("i18n insert audit: %w", err)
	}
	return nil
}

// sanitizeMetadata drops obviously secret-shaped keys so an audit row can
// never leak credentials even if a caller passes them by mistake.
func sanitizeMetadata(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "secret") || strings.Contains(lk, "token") ||
			strings.Contains(lk, "password") || strings.Contains(lk, "private") ||
			strings.Contains(lk, "authorization") || strings.Contains(lk, "cookie") {
			continue
		}
		out[k] = v
	}
	return out
}

func (s *PgStore) PageAudit(ctx context.Context, locale, actor, action string, page, pageSize int) ([]AuditRow, int, error) {
	if err := s.ready(); err != nil {
		return nil, 0, err
	}
	where := "TRUE"
	args := []any{}
	if locale != "" {
		args = append(args, locale)
		where += fmt.Sprintf(" AND locale_code = $%d", len(args))
	}
	if actor != "" {
		args = append(args, strings.ToLower(actor))
		where += fmt.Sprintf(" AND actor = $%d", len(args))
	}
	if action != "" {
		args = append(args, action)
		where += fmt.Sprintf(" AND action = $%d", len(args))
	}
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM i18n_audit_logs WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, `
		SELECT id, actor, action, locale_code, namespace, message_key, metadata, created_at
		FROM i18n_audit_logs WHERE `+where+`
		ORDER BY created_at DESC LIMIT $`+itoa(len(args)-1)+` OFFSET $`+itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var a AuditRow
		if err := rows.Scan(&a.ID, &a.Actor, &a.Action, &a.LocaleCode, &a.Namespace, &a.MessageKey, &a.Metadata, &a.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
