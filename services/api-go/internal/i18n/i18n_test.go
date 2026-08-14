package i18n

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ─── baseline ────────────────────────────────────────────────────────────────

func TestBaselineLoadsAndChecksumIsDeterministic(t *testing.T) {
	for _, code := range []string{"en", "zh"} {
		nested, err := BaselineCatalog(code)
		if err != nil {
			t.Fatalf("baseline %s: %v", code, err)
		}
		flat, err := Flatten(nested)
		if err != nil {
			t.Fatalf("flatten %s: %v", code, err)
		}
		if len(flat) < 1000 {
			t.Fatalf("baseline %s suspiciously small: %d keys", code, len(flat))
		}
		rebuilt, err := Nest(flat)
		if err != nil {
			t.Fatalf("nest %s: %v", code, err)
		}
		b1, _ := CanonicalJSON(nested)
		b2, _ := CanonicalJSON(rebuilt)
		if Checksum(b1) != Checksum(b2) {
			t.Fatalf("%s: flatten→nest is not lossless", code)
		}
		b3, _ := CanonicalJSON(nested)
		if Checksum(b1) != Checksum(b3) {
			t.Fatal("checksum not deterministic across marshals")
		}
	}
}

func TestBaselineLocalesHaveExactlyOneDefault(t *testing.T) {
	defaults := 0
	for _, b := range BaselineLocales() {
		if b.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("want exactly one default baseline locale, got %d", defaults)
	}
}

func TestNestRejectsConflicts(t *testing.T) {
	_, err := Nest([]FlatMessage{
		{Namespace: "a", Key: "x", Value: "1"},
		{Namespace: "a", Key: "x.y", Value: "2"},
	})
	if err == nil {
		t.Fatal("leaf/parent conflict must be rejected")
	}
	_, err = Nest([]FlatMessage{
		{Namespace: "a", Key: "x", Value: "1"},
		{Namespace: "a", Key: "x", Value: "2"},
	})
	if err == nil {
		t.Fatal("duplicate key must be rejected")
	}
}

// ─── ICU parser ──────────────────────────────────────────────────────────────

func TestParseICU(t *testing.T) {
	ok := []string{
		"Plain text",
		"Hello {name}",
		"{count, plural, one {# item} other {# items}}",
		"{kind, select, buy {Buy} sell {Sell} other {Trade}}",
		"{n, number} at {when, date, short}",
		"It''s {n, plural, =1 {one} other {many}}",
		"'{literal}' braces",
		"{a, plural, offset:1 =0 {none} other {#}}",
	}
	for _, m := range ok {
		if _, err := ParseICU(m); err != nil {
			t.Errorf("ParseICU(%q) unexpected error: %v", m, err)
		}
	}
	bad := []string{
		"{unclosed",
		"unopened}",
		"{count, plural, one {x}}",       // missing other
		"{kind, select, buy {Buy}}",      // missing other
		"{x, frobnicate}",                // unknown type
		"{count, plural, one {unclosed}", // unterminated
		"{, plural, other {x}}",          // empty arg name
	}
	for _, m := range bad {
		if _, err := ParseICU(m); err == nil {
			t.Errorf("ParseICU(%q) should fail", m)
		}
	}
	parsed, err := ParseICU("{name} bought {count, plural, one {# token} other {# tokens}} for {price, number}")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(parsed.ArgNames(), ",")
	if got != "count,name,price" {
		t.Fatalf("args = %s", got)
	}
}

// ─── validation ──────────────────────────────────────────────────────────────

func TestValidateCatalog(t *testing.T) {
	def := []FlatMessage{
		{Namespace: "shop", Key: "title", Value: "Buy {count, plural, one {# token} other {# tokens}}"},
		{Namespace: "shop", Key: "cta", Value: "Go"},
	}
	issuesOf := func(target []FlatMessage) map[string]bool {
		out := map[string]bool{}
		for _, i := range ValidateCatalog(target, def) {
			out[i.Code] = true
		}
		return out
	}

	if got := issuesOf([]FlatMessage{
		{Namespace: "shop", Key: "title", Value: "买{count, plural, other {#}}"},
		{Namespace: "shop", Key: "cta", Value: "去"},
	}); len(got) != 0 {
		t.Fatalf("valid catalog flagged: %v", got)
	}
	if got := issuesOf([]FlatMessage{{Namespace: "shop", Key: "cta", Value: "去"}}); !got["missing-key"] {
		t.Fatalf("missing key not flagged: %v", got)
	}
	if got := issuesOf([]FlatMessage{
		{Namespace: "shop", Key: "title", Value: "买{amount}"},
		{Namespace: "shop", Key: "cta", Value: "去"},
	}); !got["icu-arg-mismatch"] {
		t.Fatalf("arg mismatch not flagged: %v", got)
	}
	if got := issuesOf([]FlatMessage{
		{Namespace: "shop", Key: "title", Value: "  "},
		{Namespace: "shop", Key: "cta", Value: "去"},
	}); !got["empty-value"] {
		t.Fatalf("empty value not flagged: %v", got)
	}
	if got := issuesOf([]FlatMessage{
		{Namespace: "shop", Key: "title", Value: "<script>alert(1)</script>"},
		{Namespace: "shop", Key: "cta", Value: "去"},
	}); !got["html-not-allowed"] {
		t.Fatalf("markup not flagged: %v", got)
	}
	// Placeholder tokens like /profile/<address> are legal copy.
	if issues := ValidateValue("p", "kicker", "Open /profile/<address> to view"); len(issues) != 0 {
		t.Fatalf("placeholder token wrongly flagged: %v", issues)
	}
}

// ─── fake store ──────────────────────────────────────────────────────────────

type fakeStore struct {
	locales   map[string]Locale
	drafts    map[string][]FlatMessage // locale → messages
	revisions []Revision
	audits    []AuditEntry
	failList  bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		locales: map[string]Locale{
			"en": {Code: "en", EnglishName: "English", NativeName: "English", Enabled: true, IsDefault: true},
			"zh": {Code: "zh", EnglishName: "Chinese", NativeName: "中文", Enabled: true},
			"fr": {Code: "fr", EnglishName: "French", NativeName: "Français", Enabled: false},
		},
		drafts: map[string][]FlatMessage{
			"en": {{Namespace: "app", Key: "hello", Value: "Hello {name}"}},
			"zh": {{Namespace: "app", Key: "hello", Value: "你好 {name}"}},
		},
	}
}

func (f *fakeStore) ListLocales(context.Context) ([]Locale, error) {
	if f.failList {
		return nil, fmt.Errorf("db down")
	}
	out := []Locale{}
	for _, l := range f.locales {
		out = append(out, l)
	}
	return out, nil
}
func (f *fakeStore) GetLocale(_ context.Context, code string) (Locale, bool, error) {
	if f.failList {
		return Locale{}, false, fmt.Errorf("db down")
	}
	l, ok := f.locales[code]
	return l, ok, nil
}
func (f *fakeStore) CreateLocale(_ context.Context, loc Locale, actor string) error {
	if _, exists := f.locales[loc.Code]; exists {
		return ErrLocaleExists
	}
	f.locales[loc.Code] = loc
	f.audits = append(f.audits, AuditEntry{Actor: actor, Action: "locale.create", LocaleCode: loc.Code})
	return nil
}
func (f *fakeStore) UpdateLocale(_ context.Context, loc Locale, clearOther bool, audit AuditEntry) error {
	if clearOther {
		for c, l := range f.locales {
			if l.IsDefault && c != loc.Code {
				l.IsDefault = false
				f.locales[c] = l
			}
		}
	}
	f.locales[loc.Code] = loc
	f.audits = append(f.audits, audit)
	return nil
}
func (f *fakeStore) ListDrafts(_ context.Context, locale string) ([]FlatMessage, error) {
	return f.drafts[locale], nil
}
func (f *fakeStore) PageDrafts(_ context.Context, locale, _, _, _ string, page, pageSize int) ([]DraftRow, int, error) {
	msgs := f.drafts[locale]
	rows := make([]DraftRow, 0, len(msgs))
	for _, m := range msgs {
		rows = append(rows, DraftRow{Namespace: m.Namespace, Key: m.Key, Value: m.Value, Version: 1, UpdatedAt: time.Now()})
	}
	if pageSize > 200 {
		return nil, 0, fmt.Errorf("page size not bounded: %d", pageSize)
	}
	return rows, len(rows), nil
}
func (f *fakeStore) Namespaces(context.Context, string, string) ([]NamespaceStat, error) {
	return []NamespaceStat{{Namespace: "app", Keys: 1}}, nil
}
func (f *fakeStore) UpsertDraft(_ context.Context, locale, ns, key, value string, expectedVersion int, actor string) (DraftRow, error) {
	if expectedVersion == 7 {
		return DraftRow{}, ErrVersionConflict
	}
	f.drafts[locale] = append(f.drafts[locale], FlatMessage{Namespace: ns, Key: key, Value: value})
	f.audits = append(f.audits, AuditEntry{Actor: actor, Action: "draft.update", LocaleCode: locale, Namespace: ns, MessageKey: key})
	return DraftRow{Namespace: ns, Key: key, Value: value, Version: expectedVersion + 1, UpdatedAt: time.Now()}, nil
}
func (f *fakeStore) LatestRevision(_ context.Context, locale string) (*Revision, error) {
	var latest *Revision
	for i := range f.revisions {
		r := f.revisions[i]
		if r.LocaleCode == locale && (latest == nil || r.Version > latest.Version) {
			latest = &f.revisions[i]
		}
	}
	return latest, nil
}
func (f *fakeStore) GetRevision(_ context.Context, id string) (*Revision, error) {
	for i := range f.revisions {
		if f.revisions[i].ID == id {
			return &f.revisions[i], nil
		}
	}
	return nil, nil
}
func (f *fakeStore) PageRevisions(_ context.Context, locale string, _, _ int) ([]Revision, int, error) {
	out := []Revision{}
	for _, r := range f.revisions {
		if r.LocaleCode == locale {
			out = append(out, r)
		}
	}
	return out, len(out), nil
}
func (f *fakeStore) CreateRevision(_ context.Context, locale string, catalog json.RawMessage, checksum, publishedBy string, source *string, resetDrafts []FlatMessage, audit AuditEntry) (Revision, error) {
	version := 1
	for _, r := range f.revisions {
		if r.LocaleCode == locale && r.Version >= version {
			version = r.Version + 1
		}
	}
	rev := Revision{ID: fmt.Sprintf("rev-%s-%d", locale, version), LocaleCode: locale, Version: version,
		Catalog: catalog, Checksum: checksum, PublishedBy: publishedBy, PublishedAt: time.Now(), SourceRevisionID: source}
	f.revisions = append(f.revisions, rev)
	if resetDrafts != nil {
		f.drafts[locale] = resetDrafts
	}
	f.audits = append(f.audits, audit)
	return rev, nil
}
func (f *fakeStore) InsertAudit(_ context.Context, e AuditEntry) error {
	f.audits = append(f.audits, e)
	return nil
}
func (f *fakeStore) PageAudit(context.Context, string, string, string, int, int) ([]AuditRow, int, error) {
	return nil, len(fakeAudits), nil
}

var fakeAudits []AuditRow

// ─── service behavior ────────────────────────────────────────────────────────

func TestPublicCatalogSourcesAndCache(t *testing.T) {
	fs := newFakeStore()
	svc := NewService(fs)
	ctx := context.Background()

	// No revision yet → embedded baseline for baseline locales.
	resp, err := svc.PublicCatalog(ctx, "en")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != "embedded-baseline" || resp.Version != 0 {
		t.Fatalf("want embedded-baseline v0, got %s v%d", resp.Source, resp.Version)
	}

	// Publish → database source; cache must be invalidated by Publish.
	if _, err := svc.Publish(ctx, "en", "0xAdmin000000000000000000000000000000000001"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	resp, err = svc.PublicCatalog(ctx, "en")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != "database" || resp.Version != 1 {
		t.Fatalf("want database v1 after publish, got %s v%d", resp.Source, resp.Version)
	}

	// Disabled locale → ErrLocaleDisabled.
	if _, err := svc.PublicCatalog(ctx, "fr"); err != ErrLocaleDisabled {
		t.Fatalf("disabled locale: want ErrLocaleDisabled, got %v", err)
	}
	// Unknown locale → ErrLocaleDisabled (never an empty catalog).
	if _, err := svc.PublicCatalog(ctx, "tlh"); err != ErrLocaleDisabled {
		t.Fatalf("unknown locale: want ErrLocaleDisabled, got %v", err)
	}
}

func TestPublishValidatesAndIsAudited(t *testing.T) {
	fs := newFakeStore()
	svc := NewService(fs)
	ctx := context.Background()

	// Break zh: ICU arg not present in the default message.
	fs.drafts["zh"] = []FlatMessage{{Namespace: "app", Key: "hello", Value: "你好 {nom}"}}
	_, err := svc.Publish(ctx, "zh", "0xAdmin000000000000000000000000000000000001")
	var vErr *ValidationError
	if err == nil || !asValidation(err, &vErr) {
		t.Fatalf("want ValidationError, got %v", err)
	}
	if len(fs.revisions) != 0 {
		t.Fatal("failed publish must not create a revision")
	}

	fs.drafts["zh"] = []FlatMessage{{Namespace: "app", Key: "hello", Value: "你好 {name}"}}
	rev, err := svc.Publish(ctx, "zh", "0xAdmin000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if rev.Version != 1 || rev.Checksum == "" {
		t.Fatalf("bad revision %+v", rev)
	}
	found := false
	for _, a := range fs.audits {
		if a.Action == "publish" && a.LocaleCode == "zh" && a.Actor != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("publish must write an audit entry with the actor")
	}
}

func TestRollbackCreatesNewRevisionAndKeepsHistory(t *testing.T) {
	fs := newFakeStore()
	svc := NewService(fs)
	ctx := context.Background()
	admin := "0xAdmin000000000000000000000000000000000001"

	if _, err := svc.Publish(ctx, "en", admin); err != nil {
		t.Fatal(err)
	}
	fs.drafts["en"] = []FlatMessage{{Namespace: "app", Key: "hello", Value: "Hi {name}"}}
	if _, err := svc.Publish(ctx, "en", admin); err != nil {
		t.Fatal(err)
	}
	v1, _ := fs.GetRevision(ctx, "rev-en-1")
	rb, err := svc.Rollback(ctx, "en", v1.ID, admin)
	if err != nil {
		t.Fatal(err)
	}
	if rb.Version != 3 {
		t.Fatalf("rollback must create a NEW revision (v3), got v%d", rb.Version)
	}
	if rb.SourceRevisionID == nil || *rb.SourceRevisionID != v1.ID {
		t.Fatal("rollback must record its source revision")
	}
	if len(fs.revisions) != 3 {
		t.Fatalf("history must be append-only, got %d revisions", len(fs.revisions))
	}
	if rb.Checksum != v1.Checksum {
		t.Fatal("rollback catalog must equal the source snapshot")
	}
	// Drafts were reset to the snapshot.
	if fs.drafts["en"][0].Value != "Hello {name}" {
		t.Fatalf("drafts not reset to snapshot: %+v", fs.drafts["en"])
	}
	// Rolling back to a revision of ANOTHER locale must fail.
	if _, err := svc.Rollback(ctx, "zh", v1.ID, admin); err == nil {
		t.Fatal("cross-locale rollback must be rejected")
	}
}

func TestLocaleInvariants(t *testing.T) {
	fs := newFakeStore()
	svc := NewService(fs)
	ctx := context.Background()
	admin := "0xAdmin000000000000000000000000000000000001"
	f := false

	if _, err := svc.UpdateLocale(ctx, "en", nil, nil, &f, nil, admin); err == nil {
		t.Fatal("disabling the default locale must be rejected")
	}
	tr := true
	if _, err := svc.UpdateLocale(ctx, "fr", nil, nil, nil, &tr, admin); err == nil {
		t.Fatal("a disabled locale cannot become default")
	}
	// Enabling a locale with invalid drafts is rejected.
	fs.drafts["fr"] = []FlatMessage{{Namespace: "app", Key: "hello", Value: "Bonjour {nom}"}}
	if _, err := svc.UpdateLocale(ctx, "fr", nil, nil, &tr, nil, admin); err == nil {
		t.Fatal("enabling a locale with ICU-arg mismatches must be rejected")
	}
	// Fix drafts → enable works, and setting default clears the old default.
	fs.drafts["fr"] = []FlatMessage{{Namespace: "app", Key: "hello", Value: "Bonjour {name}"}}
	if _, err := svc.UpdateLocale(ctx, "fr", nil, nil, &tr, nil, admin); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := svc.UpdateLocale(ctx, "fr", nil, nil, nil, &tr, admin); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if fs.locales["en"].IsDefault {
		t.Fatal("old default must be cleared")
	}
}

func TestUpdateDraftValidation(t *testing.T) {
	fs := newFakeStore()
	svc := NewService(fs)
	ctx := context.Background()
	if _, err := svc.UpdateDraft(ctx, "zh", "app", "x", "{broken", 0, "0xA"); err == nil {
		t.Fatal("invalid ICU must be rejected at draft save")
	}
	if _, err := svc.UpdateDraft(ctx, "zh", "bad ns!", "x", "ok", 0, "0xA"); err == nil {
		t.Fatal("invalid namespace must be rejected")
	}
	if _, err := svc.UpdateDraft(ctx, "zh", "app", "x", "好", 7, "0xA"); err != ErrVersionConflict {
		t.Fatalf("stale version must surface ErrVersionConflict, got %v", err)
	}
}

func asValidation(err error, target **ValidationError) bool {
	v, ok := err.(*ValidationError)
	if ok {
		*target = v
	}
	return ok
}
