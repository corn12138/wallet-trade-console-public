package tags

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubReader struct {
	all       []Tag
	byID      map[string]TagDetail
	bySlug    map[string]TagDetail
	allErr    error
	byIDErr   error
	bySlugErr error
}

func (s stubReader) FindAll(context.Context) ([]Tag, error) {
	if s.allErr != nil {
		return nil, s.allErr
	}
	return s.all, nil
}
func (s stubReader) FindByID(_ context.Context, id string) (TagDetail, error) {
	if s.byIDErr != nil {
		return TagDetail{}, s.byIDErr
	}
	t, ok := s.byID[id]
	if !ok {
		return TagDetail{}, ErrNotFound
	}
	return t, nil
}
func (s stubReader) FindBySlug(_ context.Context, slug string) (TagDetail, error) {
	if s.bySlugErr != nil {
		return TagDetail{}, s.bySlugErr
	}
	t, ok := s.bySlug[slug]
	if !ok {
		return TagDetail{}, ErrNotFound
	}
	return t, nil
}

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.FindAll(context.Background()); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("FindAll err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.FindByID(context.Background(), "id"); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("FindByID err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.FindBySlug(context.Background(), "slug"); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("FindBySlug err = %v, want ErrPoolUnavailable", err)
	}
}

func TestHandler_ListDegradedReader(t *testing.T) {
	mux := Router(NewService(nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body []Tag
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %+v, want empty", body)
	}
}

func TestHandler_DetailNotFound(t *testing.T) {
	stub := stubReader{byID: map[string]TagDetail{}, bySlug: map[string]TagDetail{}}
	mux := Router(NewService(stub))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing-id", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("byID status = %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slug/missing-slug", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("bySlug status = %d, want 404", rec.Code)
	}
}

func TestHandler_DetailDegradedReader503(t *testing.T) {
	mux := Router(NewService(nil))
	// /{id}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/abc", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("byID status = %d, want 503 (degraded)", rec.Code)
	}
	// /slug/{slug}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slug/abc", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("bySlug status = %d, want 503 (degraded)", rec.Code)
	}
}

func TestHandler_DetailPoolErrPropagates503(t *testing.T) {
	stub := stubReader{byIDErr: ErrPoolUnavailable, bySlugErr: ErrPoolUnavailable}
	mux := Router(NewService(stub))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/abc", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("byID status = %d, want 503", rec.Code)
	}
}

func TestHandler_DetailReturnsBody(t *testing.T) {
	slug := "go"
	t1 := TagDetail{
		Tag:      Tag{ID: "tag-1", Name: "Go", Slug: &slug, ArticleCount: 2},
		Articles: []ArticleSummary{{ID: "a1", Title: "Hello"}, {ID: "a2", Title: "World"}},
	}
	stub := stubReader{
		byID:   map[string]TagDetail{"tag-1": t1},
		bySlug: map[string]TagDetail{"go": t1},
	}
	mux := Router(NewService(stub))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tag-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body TagDetail
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID != "tag-1" || len(body.Articles) != 2 {
		t.Errorf("body = %+v, want tag-1 with 2 articles", body)
	}
}
