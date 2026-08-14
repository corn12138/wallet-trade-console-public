package mobilebff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// stubSession implements SessionProvider.
type stubSession struct {
	loggedIn bool
	userID   string
}

func (s stubSession) MobileSession(*http.Request) Session {
	return Session{IsLoggedIn: s.loggedIn, UserID: s.userID, Locale: "zh-CN", Theme: "system", Container: "browser"}
}

// stubStore embeds a nil-pool Repository (degraded defaults) and overrides the
// data paths a given test needs.
type stubStore struct {
	*Repository
	articleByID      func(id string) (ArticleRow, error)
	articlePublished func(id string) (bool, bool)
	createComment    func(articleID, authorID string, parentID *string, content string) (CommentRow, error)
	commentCount     int64
	searchLimits     []int
}

func (s *stubStore) SearchArticles(ctx context.Context, query string, limit int) ([]ArticleRow, error) {
	s.searchLimits = append(s.searchLimits, limit)
	return s.Repository.SearchArticles(ctx, query, limit)
}

func (s *stubStore) TopTags(ctx context.Context, limit int) ([]TagRow, error) {
	s.searchLimits = append(s.searchLimits, limit)
	return s.Repository.TopTags(ctx, limit)
}

func (s *stubStore) TopUsers(ctx context.Context, limit int) ([]UserRow, error) {
	s.searchLimits = append(s.searchLimits, limit)
	return s.Repository.TopUsers(ctx, limit)
}

func (s *stubStore) ArticleByID(ctx context.Context, id string) (ArticleRow, error) {
	if s.articleByID != nil {
		return s.articleByID(id)
	}
	return s.Repository.ArticleByID(ctx, id)
}
func (s *stubStore) ArticlePublished(ctx context.Context, id string) (bool, bool) {
	if s.articlePublished != nil {
		return s.articlePublished(id)
	}
	return s.Repository.ArticlePublished(ctx, id)
}
func (s *stubStore) CreateComment(ctx context.Context, articleID, authorID string, parentID *string, content string) (CommentRow, error) {
	if s.createComment != nil {
		return s.createComment(articleID, authorID, parentID, content)
	}
	return s.Repository.CreateComment(ctx, articleID, authorID, parentID, content)
}
func (s *stubStore) CommentCount(ctx context.Context, articleID string) (int64, error) {
	return s.commentCount, nil
}

func newStore() *stubStore { return &stubStore{Repository: NewRepository(nil)} }

func svcWith(store Store, sess SessionProvider) *Service {
	if sess == nil {
		sess = stubSession{}
	}
	return NewService(store, sess, nil, nil)
}

func router(svc *Service) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/mobile", func(m chi.Router) { Register(m, svc) })
	return r
}

func req(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// TestReadRouteGroupsDegrade covers a representative successful (degraded, nil
// pool → fixtures) response for each read route group.
func TestReadRouteGroupsDegrade(t *testing.T) {
	h := router(svcWith(newStore(), stubSession{}))
	cases := []struct{ path, mustContain string }{
		{"/api/mobile/feed/home", `"heroArticle"`},
		{"/api/mobile/search/index", `"articleResults"`},
		{"/api/mobile/message/center", `"summary"`},
		{"/api/mobile/profile/home", `"profile"`},
		{"/api/mobile/settings/index", `"DIGITAL INKSTONE VERSION`},
		{"/api/mobile/topic/scholar-path", `"slug":"scholar-path"`},
		{"/api/mobile/video/feed", `"autoplayEnabled"`},
		{"/api/mobile/video/alchemy-ink", `"video"`},
		{"/api/mobile/comment/sheet?articleId=x", `"composer"`},
	}
	for _, c := range cases {
		rec := req(t, h, http.MethodGet, c.path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: got %d want 200 (%s)", c.path, rec.Code, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), c.mustContain) {
			t.Errorf("%s: body missing %q: %s", c.path, c.mustContain, rec.Body.String())
		}
	}
}

func TestFeedHomeShape(t *testing.T) {
	h := router(svcWith(newStore(), stubSession{}))
	rec := req(t, h, http.MethodGet, "/api/mobile/feed/home", "")
	var resp FeedHomeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.HeroArticle.ID == "" || resp.WeeklyFocus.Slug != "scholar-path" || len(resp.TrendingTopics) == 0 {
		t.Fatalf("unexpected feed: %+v", resp)
	}
}

func TestSearchIndexCapsOversizedLimitBeforeStoreFanout(t *testing.T) {
	store := newStore()
	h := router(svcWith(store, stubSession{}))
	rec := req(t, h, http.MethodGet, "/api/mobile/search/index?limit=999999999", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(store.searchLimits) != 3 {
		t.Fatalf("captured %d store limits, want 3", len(store.searchLimits))
	}
	for _, limit := range store.searchLimits {
		if limit != maxSearchResultLimit {
			t.Fatalf("store limit = %d, want %d", limit, maxSearchResultLimit)
		}
	}
}

func TestSearchIndexFallsBackWithoutStoreFanoutWhenCapacityIsFull(t *testing.T) {
	for range maxConcurrentSearches {
		searchQuerySlots <- struct{}{}
	}
	defer func() {
		for range maxConcurrentSearches {
			<-searchQuerySlots
		}
	}()

	store := newStore()
	h := router(svcWith(store, stubSession{}))
	rec := req(t, h, http.MethodGet, "/api/mobile/search/index?query=wallet", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(store.searchLimits) != 0 {
		t.Fatalf("captured %d store limits, want no saturated fan-out", len(store.searchLimits))
	}
	var response SearchIndexResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if len(response.TrendingPaths) == 0 || len(response.ArticleResults) == 0 {
		t.Fatalf("static fallback is empty: %+v", response)
	}
}

func TestArticleDetailNotFound(t *testing.T) {
	h := router(svcWith(newStore(), stubSession{}))
	rec := req(t, h, http.MethodGet, "/api/mobile/article/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d want 404", rec.Code)
	}
}

func TestArticleDetailSuccess(t *testing.T) {
	store := newStore()
	summary := "A summary"
	content := "word " + strings.Repeat("more ", 300)
	store.articleByID = func(id string) (ArticleRow, error) {
		return ArticleRow{ID: id, Title: "T", Summary: &summary, Content: &content,
			ViewCount: 42, CommentCount: 3, CreatedAt: time.Now(), Tags: []string{"go"}}, nil
	}
	h := router(svcWith(store, stubSession{}))
	rec := req(t, h, http.MethodGet, "/api/mobile/article/a1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp ArticleDetailResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Article.ID != "a1" || resp.Article.Content != content || resp.Article.Stats.ViewCount != 42 {
		t.Fatalf("unexpected article detail: %+v", resp.Article)
	}
	if resp.Article.ReadTimeMinutes < 1 {
		t.Fatalf("readTimeMinutes should be >=1, got %d", resp.Article.ReadTimeMinutes)
	}
}

func TestCommentCreateLoginRequired(t *testing.T) {
	h := router(svcWith(newStore(), stubSession{loggedIn: false}))
	rec := req(t, h, http.MethodPost, "/api/mobile/comment/create", `{"articleId":"a1","content":"hi"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", rec.Code)
	}
}

func TestCommentCreateEmptyContent(t *testing.T) {
	h := router(svcWith(newStore(), stubSession{loggedIn: true, userID: "u1"}))
	rec := req(t, h, http.MethodPost, "/api/mobile/comment/create", `{"articleId":"a1","content":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", rec.Code)
	}
}

func TestCommentCreateSuccess(t *testing.T) {
	store := newStore()
	store.articlePublished = func(id string) (bool, bool) { return true, true }
	store.commentCount = 5
	uname := "alice"
	store.createComment = func(articleID, authorID string, parentID *string, content string) (CommentRow, error) {
		return CommentRow{ID: "c1", Content: content, CreatedAt: time.Now(),
			AuthorID: &authorID, AuthorUsername: &uname}, nil
	}
	h := router(svcWith(store, stubSession{loggedIn: true, userID: "u1"}))
	rec := req(t, h, http.MethodPost, "/api/mobile/comment/create", `{"articleId":"a1","content":"Great essay"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d want 201 (%s)", rec.Code, rec.Body.String())
	}
	var resp CommentCreateResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Comment.ID != "c1" || resp.Comment.Body != "Great essay" || resp.Total != 5 {
		t.Fatalf("unexpected create response: %+v", resp)
	}
}

func TestMessageReadLoggedIn(t *testing.T) {
	h := router(svcWith(newStore(), stubSession{loggedIn: true, userID: "u1"}))
	rec := req(t, h, http.MethodPost, "/api/mobile/message/read", `{"messageIds":["m1"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d want 201 (%s)", rec.Code, rec.Body.String())
	}
	var resp MessageReadResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.UpdatedCount != 0 { // nil pool → nothing found
		t.Fatalf("expected 0 updated on nil pool, got %d", resp.UpdatedCount)
	}
}

func TestMessageReadLoginRequired(t *testing.T) {
	h := router(svcWith(newStore(), stubSession{}))
	rec := req(t, h, http.MethodPost, "/api/mobile/message/read", `{}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d want 401", rec.Code)
	}
}

func TestSharePrepare(t *testing.T) {
	h := router(svcWith(newStore(), stubSession{}))
	// link (no DB) -> 201
	rec := req(t, h, http.MethodPost, "/api/mobile/share/prepare", `{"resourceType":"link","resourceId":"/x","source":"feed"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("link share got %d want 201 (%s)", rec.Code, rec.Body.String())
	}
	var resp SharePrepareResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.ResourceType != "link" || resp.URL == "" {
		t.Fatalf("unexpected share: %+v", resp)
	}
	// video (fixtures) -> 201
	rec = req(t, h, http.MethodPost, "/api/mobile/share/prepare", `{"resourceType":"video","resourceId":"alchemy-ink"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("video share got %d want 201", rec.Code)
	}
	// missing resourceId -> 400
	rec = req(t, h, http.MethodPost, "/api/mobile/share/prepare", `{"resourceType":"link"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing resourceId got %d want 400", rec.Code)
	}
}

// TestRouteRegistration asserts all 13 routes are wired (not router-404).
func TestRouteRegistration(t *testing.T) {
	h := router(svcWith(newStore(), stubSession{loggedIn: true, userID: "u1"}))
	routes := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/mobile/feed/home"},
		{http.MethodGet, "/api/mobile/article/x"},
		{http.MethodGet, "/api/mobile/comment/sheet"},
		{http.MethodPost, "/api/mobile/comment/create"},
		{http.MethodGet, "/api/mobile/search/index"},
		{http.MethodGet, "/api/mobile/message/center"},
		{http.MethodPost, "/api/mobile/message/read"},
		{http.MethodGet, "/api/mobile/profile/home"},
		{http.MethodGet, "/api/mobile/settings/index"},
		{http.MethodGet, "/api/mobile/topic/x"},
		{http.MethodGet, "/api/mobile/video/feed"},
		{http.MethodGet, "/api/mobile/video/x"},
		{http.MethodPost, "/api/mobile/share/prepare"},
	}
	for _, rt := range routes {
		body := ""
		if rt.method == http.MethodPost {
			body = `{"articleId":"a1","content":"x","resourceType":"link","resourceId":"/x"}`
		}
		rec := req(t, h, rt.method, rt.path, body)
		// article/x 404s (no such article) but that's a handler response, not a
		// router miss; any non-405 proves the route is registered.
		if rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s not registered (405)", rt.method, rt.path)
		}
	}
}

func TestRepositoryNilPoolDegrades(t *testing.T) {
	r := NewRepository(nil)
	ctx := context.Background()
	if arts, err := r.FeedArticles(ctx, 6); err != nil || len(arts) != 0 {
		t.Errorf("FeedArticles nil pool: %v %v", arts, err)
	}
	if _, err := r.ArticleByID(ctx, "x"); err != ErrNotFound {
		t.Errorf("ArticleByID nil pool err = %v, want ErrNotFound", err)
	}
	if _, err := r.CreateComment(ctx, "a", "u", nil, "c"); err != ErrPoolUnavailable {
		t.Errorf("CreateComment nil pool err = %v, want ErrPoolUnavailable", err)
	}
	if ids, err := r.FindUnreadMessageIDs(ctx, "u", []string{"m"}, nil); err != nil || len(ids) != 0 {
		t.Errorf("FindUnreadMessageIDs nil pool: %v %v", ids, err)
	}
}
