package article

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

type stubReader struct {
	listFn  func(context.Context, ListQuery) (ListResult, error)
	findFn  func(context.Context, string) (Article, error)
	listErr error
	findErr error
}

func (s stubReader) List(ctx context.Context, q ListQuery) (ListResult, error) {
	if s.listFn != nil {
		return s.listFn(ctx, q)
	}
	if s.listErr != nil {
		return ListResult{}, s.listErr
	}
	return ListResult{Items: []Article{}, Meta: Meta{Page: q.Page, Limit: q.Limit}}, nil
}
func (s stubReader) FindByID(ctx context.Context, id string) (Article, error) {
	if s.findFn != nil {
		return s.findFn(ctx, id)
	}
	if s.findErr != nil {
		return Article{}, s.findErr
	}
	return Article{ID: id, Title: "T"}, nil
}

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.List(context.Background(), ListQuery{}); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("List err = %v", err)
	}
	if _, err := r.FindByID(context.Background(), "id"); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("FindByID err = %v", err)
	}
}

func TestHandler_ListDegradedReturnsEmptyEnvelope(t *testing.T) {
	mux := Router(NewService(nil, nil), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?limit=3", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body ListResult
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 0 || body.Meta.Limit != 3 {
		t.Errorf("body = %+v, want empty with limit=3", body)
	}
}

func TestHandler_ListRejectsBadInput(t *testing.T) {
	mux := Router(NewService(stubReader{}, nil), nil)
	cases := []struct {
		name, url string
	}{
		{"bad page", "/?page=abc"},
		{"page=0", "/?page=0"},
		{"page too big", "/?page=1001"},
		{"limit too big", "/?limit=999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_ListPassesFilters(t *testing.T) {
	var captured ListQuery
	stub := stubReader{listFn: func(_ context.Context, q ListQuery) (ListResult, error) {
		captured = q
		return ListResult{}, nil
	}}
	mux := Router(NewService(stub, nil), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?category=cat-1&tag=tag-2&search=hello&page=3&limit=20", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if captured.Category != "cat-1" {
		t.Errorf("Category = %q, want cat-1", captured.Category)
	}
	if captured.Tag != "tag-2" {
		t.Errorf("Tag = %q, want tag-2", captured.Tag)
	}
	if captured.Search != "hello" {
		t.Errorf("Search = %q, want hello", captured.Search)
	}
	if captured.Page != 3 || captured.Limit != 20 {
		t.Errorf("page/limit = %d/%d, want 3/20", captured.Page, captured.Limit)
	}
}

func TestHandler_FindOneNotFound(t *testing.T) {
	stub := stubReader{findErr: ErrNotFound}
	mux := Router(NewService(stub, nil), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandler_FindOneDegradedReturns503(t *testing.T) {
	mux := Router(NewService(nil, nil), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/abc", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandler_FindOnePoolErr503(t *testing.T) {
	stub := stubReader{findErr: ErrPoolUnavailable}
	mux := Router(NewService(stub, nil), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/abc", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

// --- mutation handler tests ---

type stubWriter struct {
	createFn       func(context.Context, string, CreateInput) (Article, error)
	updateFn       func(context.Context, string, string, UpdateInput) (Article, error)
	deleteFn       func(context.Context, string, string) error
	setPublishedFn func(context.Context, string, string, bool) (Article, error)
}

func (s stubWriter) Create(ctx context.Context, authorID string, in CreateInput) (Article, error) {
	if s.createFn != nil {
		return s.createFn(ctx, authorID, in)
	}
	return Article{ID: "new", Title: in.Title, AuthorID: authorID, Tags: []Tag{}}, nil
}
func (s stubWriter) Update(ctx context.Context, authorID, id string, in UpdateInput) (Article, error) {
	if s.updateFn != nil {
		return s.updateFn(ctx, authorID, id, in)
	}
	return Article{ID: id, AuthorID: authorID, Tags: []Tag{}}, nil
}
func (s stubWriter) Delete(ctx context.Context, authorID, id string) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, authorID, id)
	}
	return nil
}
func (s stubWriter) SetPublished(ctx context.Context, authorID, id string, published bool) (Article, error) {
	if s.setPublishedFn != nil {
		return s.setPublishedFn(ctx, authorID, id, published)
	}
	return Article{ID: id, AuthorID: authorID, Published: published, Tags: []Tag{}}, nil
}

// authStubGuard pre-populates userID + roles on context so handlers
// can read them via auth.UserIDFromContext.
func authStubGuard(userID string, roles []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithUser(r.Context(), auth.UserClaims{Sub: userID, Roles: roles})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func TestRouter_MutationsRequireGuard(t *testing.T) {
	mux := Router(NewService(stubReader{}, stubWriter{}), nil)
	cases := []struct{ method, path string }{
		{http.MethodPost, "/"},
		{http.MethodPatch, "/a-1"},
		{http.MethodDelete, "/a-1"},
		{http.MethodPost, "/a-1/publish"},
		{http.MethodPost, "/a-1/unpublish"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader("{}")))
		if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404/405 when guard not wired", c.method, c.path, rec.Code)
		}
	}
}

func TestRouter_Create_HappyPath(t *testing.T) {
	stub := stubWriter{}
	mux := Router(NewService(stubReader{}, stub), authStubGuard("u-1", []string{"admin"}))
	body := `{"title":"Hello","content":"World"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestRouter_Create_400OnMissingFields(t *testing.T) {
	mux := Router(NewService(stubReader{}, stubWriter{}), authStubGuard("u-1", []string{"admin"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"only"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestRouter_Update_404OnAuthorMismatch(t *testing.T) {
	stub := stubWriter{updateFn: func(context.Context, string, string, UpdateInput) (Article, error) {
		return Article{}, ErrAuthorMismatch
	}}
	mux := Router(NewService(stubReader{}, stub), authStubGuard("u-1", []string{"editor"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/a-1", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRouter_Delete_404OnAuthorMismatch(t *testing.T) {
	stub := stubWriter{deleteFn: func(context.Context, string, string) error {
		return ErrAuthorMismatch
	}}
	mux := Router(NewService(stubReader{}, stub), authStubGuard("u-1", []string{"admin"}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/a-1", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRouter_PublishUnpublish_Happy(t *testing.T) {
	var captured bool
	stub := stubWriter{setPublishedFn: func(_ context.Context, _ string, _ string, p bool) (Article, error) {
		captured = p
		return Article{ID: "a-1", Published: p, Tags: []Tag{}}, nil
	}}
	mux := Router(NewService(stubReader{}, stub), authStubGuard("u-1", []string{"admin"}))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/a-1/publish", nil))
	if rec.Code != http.StatusOK || captured != true {
		t.Errorf("publish: status=%d captured=%v", rec.Code, captured)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/a-1/unpublish", nil))
	if rec.Code != http.StatusOK || captured != false {
		t.Errorf("unpublish: status=%d captured=%v", rec.Code, captured)
	}
}

func TestHandler_FindOneHappyPath(t *testing.T) {
	stub := stubReader{findFn: func(_ context.Context, id string) (Article, error) {
		return Article{ID: id, Title: "Hello", Tags: []Tag{}}, nil
	}}
	mux := Router(NewService(stub, nil), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/art-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var body Article
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.ID != "art-1" || body.Title != "Hello" {
		t.Errorf("body = %+v, want art-1/Hello", body)
	}
}
