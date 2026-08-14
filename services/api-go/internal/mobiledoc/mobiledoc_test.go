package mobiledoc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// stubStore is an in-memory Store for handler tests.
type stubStore struct {
	docs       map[string]MobileDoc
	seq        int
	failCreate bool
}

func newStub() *stubStore { return &stubStore{docs: map[string]MobileDoc{}} }

func (s *stubStore) Create(_ context.Context, in CreateInput) (MobileDoc, error) {
	if s.failCreate {
		return MobileDoc{}, errors.New("boom")
	}
	s.seq++
	id := fmt.Sprintf("doc-%d", s.seq)
	cat := in.Category
	if cat == "" {
		cat = "FRONTEND"
	}
	tags := in.Tags
	if tags == nil {
		tags = []string{}
	}
	pub := true
	if in.Published != nil {
		pub = *in.Published
	}
	hot := false
	if in.IsHot != nil {
		hot = *in.IsHot
	}
	d := MobileDoc{ID: id, Title: in.Title, Content: in.Content, Summary: in.Summary,
		FilePath: in.FilePath, Category: cat, Tags: tags, IsHot: hot, Published: pub}
	s.docs[id] = d
	return d, nil
}
func (s *stubStore) CreateMany(ctx context.Context, ins []CreateInput) (int64, error) {
	var n int64
	for _, in := range ins {
		if _, err := s.Create(ctx, in); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
func (s *stubStore) FindAll(_ context.Context, q QueryParams) (PaginatedResult, error) {
	out := PaginatedResult{Items: []MobileDoc{}, Page: q.Page, PageSize: q.PageSize}
	for _, d := range s.docs {
		if d.Published == q.Published {
			out.Items = append(out.Items, d)
		}
	}
	out.Total = int64(len(out.Items))
	return out, nil
}
func (s *stubStore) FindOne(_ context.Context, id string) (MobileDoc, error) {
	d, ok := s.docs[id]
	if !ok || !d.Published {
		return MobileDoc{}, ErrNotFound
	}
	return d, nil
}
func (s *stubStore) GetStatsByCategory(context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, d := range s.docs {
		if d.Published {
			out[d.Category]++
		}
	}
	return out, nil
}
func (s *stubStore) GetHotDocs(context.Context, int) ([]MobileDoc, error) {
	out := []MobileDoc{}
	for _, d := range s.docs {
		if d.IsHot && d.Published {
			out = append(out, d)
		}
	}
	return out, nil
}
func (s *stubStore) GetRelatedDocs(ctx context.Context, id string, _ int) ([]MobileDoc, error) {
	if _, err := s.FindOne(ctx, id); err != nil {
		return nil, err
	}
	return []MobileDoc{}, nil
}
func (s *stubStore) Update(ctx context.Context, id string, p UpdateInput) (MobileDoc, error) {
	d, err := s.FindOne(ctx, id)
	if err != nil {
		return MobileDoc{}, err
	}
	if p.Title != nil {
		d.Title = *p.Title
	}
	if p.Published != nil {
		d.Published = *p.Published
	}
	s.docs[id] = d
	return d, nil
}
func (s *stubStore) Remove(ctx context.Context, id string) error {
	if _, err := s.FindOne(ctx, id); err != nil {
		return err
	}
	delete(s.docs, id)
	return nil
}
func (s *stubStore) SoftRemove(ctx context.Context, id string) (MobileDoc, error) {
	f := false
	return s.Update(ctx, id, UpdateInput{Published: &f})
}
func (s *stubStore) ClearAll(context.Context) error { s.docs = map[string]MobileDoc{}; return nil }
func (s *stubStore) GetCategories(context.Context) ([]CategoryCount, error) {
	return []CategoryCount{}, nil
}
func (s *stubStore) GetStats(context.Context) (StatsResult, error) {
	return StatsResult{Categories: []CategoryCount{}}, nil
}

// buildRouter mirrors the production mount (/api/mobile + /api/web/v1).
func buildRouter(svc *Service, guard func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Route("/api", func(api chi.Router) {
		api.Route("/mobile", func(m chi.Router) { RegisterMobile(m, svc, guard) })
		api.Mount("/web/v1", WebV1Router(svc, guard))
	})
	return r
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLegacyCreateValidation(t *testing.T) {
	h := buildRouter(NewService(newStub()), nil)

	// missing title -> 400
	rec := do(t, h, http.MethodPost, "/api/mobile/docs", `{"content":"x","category":"AI"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing title: got %d want 400 (%s)", rec.Code, rec.Body.String())
	}
	// bad category -> 400
	rec = do(t, h, http.MethodPost, "/api/mobile/docs", `{"title":"t","content":"x","category":"NOPE"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad category: got %d want 400", rec.Code)
	}
	// valid -> 201 raw doc
	rec = do(t, h, http.MethodPost, "/api/mobile/docs", `{"title":"t","content":"x","category":"AI"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid create: got %d want 201 (%s)", rec.Code, rec.Body.String())
	}
	var doc MobileDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode raw doc: %v", err)
	}
	if doc.Title != "t" || doc.Category != "AI" || doc.ID == "" {
		t.Fatalf("unexpected doc: %+v", doc)
	}
}

func TestLegacyFindOneNotFound(t *testing.T) {
	h := buildRouter(NewService(newStub()), nil)
	rec := do(t, h, http.MethodGet, "/api/mobile/docs/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", rec.Code)
	}
}

func TestLegacyListRawShape(t *testing.T) {
	st := newStub()
	_, _ = st.Create(context.Background(), CreateInput{Title: "a", Content: "c", Category: "AI"})
	h := buildRouter(NewService(st), nil)
	rec := do(t, h, http.MethodGet, "/api/mobile/docs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200", rec.Code)
	}
	var pr PaginatedResult
	if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil {
		t.Fatalf("list must be raw PaginatedResult, not an envelope: %v (%s)", err, rec.Body.String())
	}
	if pr.Total != 1 || len(pr.Items) != 1 {
		t.Fatalf("unexpected list: %+v", pr)
	}
}

func TestV1EnvelopeShape(t *testing.T) {
	st := newStub()
	h := buildRouter(NewService(st), nil)
	rec := do(t, h, http.MethodPost, "/api/mobile/v1/docs", `{"title":"t","content":"x","category":"AI"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d want 201 (%s)", rec.Code, rec.Body.String())
	}
	var env successEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("v1 must return SuccessResponse envelope: %v", err)
	}
	if !env.Success || env.Message != "文档创建成功" || env.TraceID == "" || env.Timestamp == "" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

func TestV1DeleteNoContent(t *testing.T) {
	st := newStub()
	d, _ := st.Create(context.Background(), CreateInput{Title: "t", Content: "c", Category: "AI"})
	h := buildRouter(NewService(st), nil)
	rec := do(t, h, http.MethodDelete, "/api/mobile/v1/docs/"+d.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("204 must have empty body, got %q", rec.Body.String())
	}
}

func TestWebDetailEnhanced(t *testing.T) {
	st := newStub()
	d, _ := st.Create(context.Background(), CreateInput{Title: "t", Content: "hello world", Category: "AI"})
	h := buildRouter(NewService(st), nil)
	rec := do(t, h, http.MethodGet, "/api/web/v1/docs/"+d.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 (%s)", rec.Code, rec.Body.String())
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			ID  string `json:"id"`
			Web struct {
				WordCount int    `json:"wordCount"`
				ShareURL  string `json:"shareUrl"`
				CanEdit   bool   `json:"canEdit"`
			} `json:"_web"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Web.WordCount != len("hello world") || !env.Data.Web.CanEdit || !strings.Contains(env.Data.Web.ShareURL, "/docs/"+d.ID) {
		t.Fatalf("web enhancement missing/wrong: %+v", env.Data.Web)
	}
}

// TestGuardParity: a 401-ing guard must block the non-@Public mutation but NOT
// the @Public read or the @Public clear.
func TestGuardParity(t *testing.T) {
	st := newStub()
	d, _ := st.Create(context.Background(), CreateInput{Title: "t", Content: "c", Category: "AI"})
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	h := buildRouter(NewService(st), guard)

	// public read -> 200 even without auth
	if rec := do(t, h, http.MethodGet, "/api/mobile/docs", ""); rec.Code != http.StatusOK {
		t.Fatalf("public list blocked: %d", rec.Code)
	}
	// public clear -> 200 even without auth (mirrors @Public)
	if rec := do(t, h, http.MethodDelete, "/api/mobile/docs/clear", ""); rec.Code != http.StatusOK {
		t.Fatalf("public clear blocked: %d", rec.Code)
	}
	// guarded mutation -> 401 without auth
	if rec := do(t, h, http.MethodPatch, "/api/mobile/docs/"+d.ID, `{"title":"x"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("guarded PATCH not blocked: %d", rec.Code)
	}
	// guarded v1 read -> 401 without auth (no @Public on v1)
	if rec := do(t, h, http.MethodGet, "/api/mobile/v1/docs", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("guarded v1 list not blocked: %d", rec.Code)
	}
}

func TestRepositoryNilPoolDegrades(t *testing.T) {
	r := NewRepository(nil)
	ctx := context.Background()
	if _, err := r.Create(ctx, CreateInput{}); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("Create err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.FindOne(ctx, "x"); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("FindOne err = %v, want ErrPoolUnavailable", err)
	}
	pr, err := r.FindAll(ctx, QueryParams{Page: 1, PageSize: 10, Published: true})
	if !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("FindAll err = %v, want ErrPoolUnavailable", err)
	}
	if pr.Items == nil {
		t.Errorf("FindAll degraded result must have non-nil Items slice")
	}
}

func TestCreateInputValidate(t *testing.T) {
	long := strings.Repeat("x", 201)
	cases := []struct {
		name string
		in   CreateInput
		ok   bool
	}{
		{"ok", CreateInput{Title: "t", Content: "c", Category: "DESIGN"}, true},
		{"no title", CreateInput{Content: "c", Category: "AI"}, false},
		{"title too long", CreateInput{Title: long, Content: "c", Category: "AI"}, false},
		{"no content", CreateInput{Title: "t", Category: "AI"}, false},
		{"bad category", CreateInput{Title: "t", Content: "c", Category: "X"}, false},
	}
	for _, c := range cases {
		err := c.in.validate()
		if c.ok && err != nil {
			t.Errorf("%s: want ok, got %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: want error, got nil", c.name)
		}
	}
}
