package i18n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// CatalogResponse is the public catalog payload.
type CatalogResponse struct {
	Locale      string          `json:"locale"`
	Version     int             `json:"version"`
	RevisionID  string          `json:"revisionId,omitempty"`
	Checksum    string          `json:"checksum"`
	Source      string          `json:"source"` // "database" | "embedded-baseline"
	PublishedAt *time.Time      `json:"publishedAt,omitempty"`
	Messages    json.RawMessage `json:"messages"`
}

// PublicLocale is the public locale metadata payload (no admin fields).
type PublicLocale struct {
	Code        string `json:"code"`
	EnglishName string `json:"englishName"`
	NativeName  string `json:"nativeName"`
	IsDefault   bool   `json:"isDefault"`
}

// ValidationError carries the blocking issues for a 422 response.
type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	return fmt.Sprintf("i18n: catalog validation failed with %d issue(s)", len(e.Issues))
}

// ErrLocaleDisabled marks an existing-but-disabled (or unknown) public locale.
var ErrLocaleDisabled = errors.New("i18n: locale disabled or unknown")

type cachedCatalog struct {
	resp CatalogResponse
	body []byte // canonical JSON of resp.Messages for ETag reuse
}

// Service owns catalog reads (with a revision cache), draft/publication
// rules and the locale invariants.
type Service struct {
	store Store

	mu    sync.RWMutex
	cache map[string]*cachedCatalog // locale → latest published catalog
}

func NewService(store Store) *Service {
	return &Service{store: store, cache: map[string]*cachedCatalog{}}
}

// invalidate drops the cached catalog for a locale (after publish/rollback/
// locale mutation).
func (s *Service) invalidate(locale string) {
	s.mu.Lock()
	delete(s.cache, locale)
	s.mu.Unlock()
}

// ─── Public reads ────────────────────────────────────────────────────────────

// PublicLocales lists enabled locales. When the database is unavailable or
// empty it degrades to the embedded baseline metadata.
func (s *Service) PublicLocales(ctx context.Context) ([]PublicLocale, string, error) {
	locales, err := s.store.ListLocales(ctx)
	if err == nil && len(locales) > 0 {
		out := make([]PublicLocale, 0, len(locales))
		for _, l := range locales {
			if !l.Enabled {
				continue
			}
			out = append(out, PublicLocale{Code: l.Code, EnglishName: l.EnglishName, NativeName: l.NativeName, IsDefault: l.IsDefault})
		}
		if len(out) > 0 {
			return out, "database", nil
		}
	}
	out := make([]PublicLocale, 0, 2)
	for _, b := range BaselineLocales() {
		out = append(out, PublicLocale{Code: b.Code, EnglishName: b.EnglishName, NativeName: b.NativeName, IsDefault: b.IsDefault})
	}
	return out, "embedded-baseline", nil
}

// DefaultLocale returns the enabled default locale code ("en" bootstrap
// fallback when the database has none).
func (s *Service) DefaultLocale(ctx context.Context) string {
	locales, err := s.store.ListLocales(ctx)
	if err == nil {
		for _, l := range locales {
			if l.Enabled && l.IsDefault {
				return l.Code
			}
		}
	}
	return "en"
}

// PublicCatalog serves the published catalog for an enabled locale. Priority:
// database revision (source "database"); otherwise the embedded baseline
// (source "embedded-baseline"). Unknown or disabled locales return
// ErrLocaleDisabled; database *errors* degrade to the baseline so a healthy
// web tier keeps rendering while the incident is visible via the source field.
func (s *Service) PublicCatalog(ctx context.Context, rawLocale string) (CatalogResponse, error) {
	locale, ok := NormalizeLocale(rawLocale)
	if !ok {
		return CatalogResponse{}, ErrLocaleDisabled
	}

	s.mu.RLock()
	if c, hit := s.cache[locale]; hit {
		s.mu.RUnlock()
		return c.resp, nil
	}
	s.mu.RUnlock()

	dbHealthy := true
	if l, found, err := s.store.GetLocale(ctx, locale); err != nil {
		dbHealthy = false
	} else if found && !l.Enabled {
		return CatalogResponse{}, ErrLocaleDisabled
	} else if !found {
		// Fresh database (no locale rows yet): only baseline locales exist.
		if !hasBaseline(locale) {
			return CatalogResponse{}, ErrLocaleDisabled
		}
	}

	if dbHealthy {
		rev, err := s.store.LatestRevision(ctx, locale)
		if err == nil && rev != nil {
			resp := CatalogResponse{
				Locale: locale, Version: rev.Version, RevisionID: rev.ID,
				Checksum: rev.Checksum, Source: "database",
				PublishedAt: &rev.PublishedAt, Messages: rev.Catalog,
			}
			s.mu.Lock()
			s.cache[locale] = &cachedCatalog{resp: resp, body: rev.Catalog}
			s.mu.Unlock()
			return resp, nil
		}
		if err != nil {
			dbHealthy = false
		}
	}

	// Degraded / fresh-database path: embedded baseline.
	nested, err := BaselineCatalog(locale)
	if err != nil {
		return CatalogResponse{}, ErrLocaleDisabled
	}
	body, err := CanonicalJSON(nested)
	if err != nil {
		return CatalogResponse{}, err
	}
	resp := CatalogResponse{
		Locale: locale, Version: 0, Checksum: Checksum(body),
		Source: "embedded-baseline", Messages: body,
	}
	// Cache only healthy-database emptiness; a DB outage must retry.
	if dbHealthy {
		s.mu.Lock()
		s.cache[locale] = &cachedCatalog{resp: resp, body: body}
		s.mu.Unlock()
	}
	return resp, nil
}

func hasBaseline(locale string) bool {
	for _, b := range BaselineLocales() {
		if b.Code == locale {
			return true
		}
	}
	return false
}

// ─── Admin operations (actor = verified SIWE wallet from context) ───────────

func (s *Service) AdminLocales(ctx context.Context) ([]Locale, error) {
	locales, err := s.store.ListLocales(ctx)
	if err != nil {
		return nil, err
	}
	return locales, nil
}

// CreateLocale registers a new disabled-by-default locale.
func (s *Service) CreateLocale(ctx context.Context, code, englishName, nativeName string, enabled bool, actor string) (Locale, error) {
	norm, ok := NormalizeLocale(code)
	if !ok {
		return Locale{}, &ValidationError{Issues: []Issue{{Code: "invalid-locale", Detail: "locale code must be BCP-47-compatible"}}}
	}
	if strings.TrimSpace(englishName) == "" || strings.TrimSpace(nativeName) == "" {
		return Locale{}, &ValidationError{Issues: []Issue{{Code: "missing-name", Detail: "englishName and nativeName are required"}}}
	}
	loc := Locale{Code: norm, EnglishName: englishName, NativeName: nativeName, Enabled: enabled}
	if err := s.store.CreateLocale(ctx, loc, actor); err != nil {
		return Locale{}, err
	}
	s.invalidate(norm)
	return loc, nil
}

// UpdateLocale patches locale metadata under the invariants: the default
// locale cannot be disabled; making a locale default requires it enabled;
// enabling (or defaulting) a locale requires its drafts to validate.
func (s *Service) UpdateLocale(ctx context.Context, code string, englishName, nativeName *string, enabled, isDefault *bool, actor string) (Locale, error) {
	norm, ok := NormalizeLocale(code)
	if !ok {
		return Locale{}, ErrNotFound
	}
	cur, found, err := s.store.GetLocale(ctx, norm)
	if err != nil {
		return Locale{}, err
	}
	if !found {
		return Locale{}, ErrNotFound
	}
	next := cur
	if englishName != nil && strings.TrimSpace(*englishName) != "" {
		next.EnglishName = *englishName
	}
	if nativeName != nil && strings.TrimSpace(*nativeName) != "" {
		next.NativeName = *nativeName
	}
	if enabled != nil {
		next.Enabled = *enabled
	}
	if isDefault != nil {
		next.IsDefault = *isDefault
	}
	if cur.IsDefault && !next.IsDefault {
		return Locale{}, &ValidationError{Issues: []Issue{{Code: "default-required",
			Detail: "cannot unset the default locale directly — set another enabled locale as default instead"}}}
	}
	if cur.IsDefault && !next.Enabled {
		return Locale{}, &ValidationError{Issues: []Issue{{Code: "default-disabled",
			Detail: "the default locale cannot be disabled"}}}
	}
	if next.IsDefault && !next.Enabled {
		return Locale{}, &ValidationError{Issues: []Issue{{Code: "default-disabled",
			Detail: "a disabled locale cannot be the default"}}}
	}
	// Enabling / defaulting requires a currently-valid catalog.
	if (next.Enabled && !cur.Enabled) || (next.IsDefault && !cur.IsDefault) {
		if issues, err := s.Validate(ctx, norm); err != nil {
			return Locale{}, err
		} else if len(issues) > 0 {
			return Locale{}, &ValidationError{Issues: issues}
		}
	}
	audit := AuditEntry{Actor: actor, Action: "locale.update", LocaleCode: norm,
		Metadata: map[string]any{
			"before": map[string]any{"enabled": cur.Enabled, "isDefault": cur.IsDefault, "englishName": cur.EnglishName, "nativeName": cur.NativeName},
			"after":  map[string]any{"enabled": next.Enabled, "isDefault": next.IsDefault, "englishName": next.EnglishName, "nativeName": next.NativeName},
		}}
	if next.Enabled != cur.Enabled {
		audit.Action = "locale.enable"
		if !next.Enabled {
			audit.Action = "locale.disable"
		}
	}
	if err := s.store.UpdateLocale(ctx, next, next.IsDefault && !cur.IsDefault, audit); err != nil {
		return Locale{}, err
	}
	s.invalidate(norm)
	return next, nil
}

// Messages returns one page of drafts (with default-locale values for the
// side-by-side editor).
func (s *Service) Messages(ctx context.Context, locale, namespace, search string, page, pageSize int) ([]DraftRow, int, error) {
	norm, ok := NormalizeLocale(locale)
	if !ok {
		return nil, 0, ErrNotFound
	}
	page, pageSize = boundPage(page, pageSize)
	return s.store.PageDrafts(ctx, norm, s.DefaultLocale(ctx), namespace, search, page, pageSize)
}

func (s *Service) NamespaceStats(ctx context.Context, locale string) ([]NamespaceStat, error) {
	norm, ok := NormalizeLocale(locale)
	if !ok {
		return nil, ErrNotFound
	}
	return s.store.Namespaces(ctx, norm, s.DefaultLocale(ctx))
}

// UpdateDraft validates and writes a single draft value with optimistic
// concurrency (expectedVersion 0 means "create new key").
func (s *Service) UpdateDraft(ctx context.Context, locale, namespace, key, value string, expectedVersion int, actor string) (DraftRow, error) {
	norm, ok := NormalizeLocale(locale)
	if !ok {
		return DraftRow{}, ErrNotFound
	}
	if err := ValidKeyParts(namespace, key); err != nil {
		return DraftRow{}, &ValidationError{Issues: []Issue{{Namespace: namespace, Key: key, Code: "invalid-key", Detail: err.Error()}}}
	}
	if issues := ValidateValue(namespace, key, value); len(issues) > 0 {
		return DraftRow{}, &ValidationError{Issues: issues}
	}
	return s.store.UpsertDraft(ctx, norm, namespace, key, value, expectedVersion, actor)
}

// Validate runs full catalog validation for a locale against the default
// locale's drafts.
func (s *Service) Validate(ctx context.Context, locale string) ([]Issue, error) {
	norm, ok := NormalizeLocale(locale)
	if !ok {
		return nil, ErrNotFound
	}
	def := s.DefaultLocale(ctx)
	target, err := s.store.ListDrafts(ctx, norm)
	if err != nil {
		return nil, err
	}
	defMsgs, err := s.store.ListDrafts(ctx, def)
	if err != nil {
		return nil, err
	}
	if norm == def {
		// The default locale validates against itself (syntax/shape only).
		defMsgs = target
	}
	return ValidateCatalog(target, defMsgs), nil
}

// Publish snapshots the locale's drafts into a new immutable revision after
// validation. Atomic: revision + audit land together or not at all.
func (s *Service) Publish(ctx context.Context, locale, actor string) (Revision, error) {
	norm, ok := NormalizeLocale(locale)
	if !ok {
		return Revision{}, ErrNotFound
	}
	issues, err := s.Validate(ctx, norm)
	if err != nil {
		return Revision{}, err
	}
	if len(issues) > 0 {
		return Revision{}, &ValidationError{Issues: issues}
	}
	drafts, err := s.store.ListDrafts(ctx, norm)
	if err != nil {
		return Revision{}, err
	}
	if len(drafts) == 0 {
		return Revision{}, &ValidationError{Issues: []Issue{{Code: "empty-catalog", Detail: "no drafts to publish"}}}
	}
	nested, err := Nest(drafts)
	if err != nil {
		return Revision{}, &ValidationError{Issues: []Issue{{Code: "structure", Detail: err.Error()}}}
	}
	body, err := CanonicalJSON(nested)
	if err != nil {
		return Revision{}, err
	}
	sum := Checksum(body)
	rev, err := s.store.CreateRevision(ctx, norm, body, sum, actor, nil, nil, AuditEntry{
		Actor: actor, Action: "publish", LocaleCode: norm,
		Metadata: map[string]any{"checksum": sum, "keyCount": len(drafts)},
	})
	if err != nil {
		return Revision{}, err
	}
	s.invalidate(norm)
	return rev, nil
}

// Rollback publishes a NEW revision whose catalog is an old revision's
// snapshot (history is never mutated) and resets drafts to that snapshot so
// the editor reflects what production now serves.
func (s *Service) Rollback(ctx context.Context, locale, revisionID, actor string) (Revision, error) {
	norm, ok := NormalizeLocale(locale)
	if !ok {
		return Revision{}, ErrNotFound
	}
	old, err := s.store.GetRevision(ctx, revisionID)
	if err != nil {
		return Revision{}, err
	}
	if old == nil || old.LocaleCode != norm {
		return Revision{}, ErrNotFound
	}
	var nested map[string]any
	if err := json.Unmarshal(old.Catalog, &nested); err != nil {
		return Revision{}, fmt.Errorf("i18n rollback: stored catalog is corrupt: %w", err)
	}
	flat, err := Flatten(nested)
	if err != nil {
		return Revision{}, fmt.Errorf("i18n rollback: %w", err)
	}
	src := old.ID
	rev, err := s.store.CreateRevision(ctx, norm, old.Catalog, old.Checksum, actor, &src, flat, AuditEntry{
		Actor: actor, Action: "rollback", LocaleCode: norm,
		Metadata: map[string]any{"sourceRevisionId": old.ID, "sourceVersion": old.Version, "checksum": old.Checksum},
	})
	if err != nil {
		return Revision{}, err
	}
	s.invalidate(norm)
	return rev, nil
}

func (s *Service) Revisions(ctx context.Context, locale string, page, pageSize int) ([]Revision, int, error) {
	norm, ok := NormalizeLocale(locale)
	if !ok {
		return nil, 0, ErrNotFound
	}
	page, pageSize = boundPage(page, pageSize)
	return s.store.PageRevisions(ctx, norm, page, pageSize)
}

func (s *Service) Audit(ctx context.Context, locale, actor, action string, page, pageSize int) ([]AuditRow, int, error) {
	page, pageSize = boundPage(page, pageSize)
	return s.store.PageAudit(ctx, locale, actor, action, page, pageSize)
}

func boundPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}
