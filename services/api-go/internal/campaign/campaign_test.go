package campaign

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	ctx := context.Background()
	if _, err := r.ListAll(ctx, ""); err != ErrPoolUnavailable {
		t.Errorf("ListAll err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.GetActive(ctx, time.Now()); err != ErrPoolUnavailable {
		t.Errorf("GetActive err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.FindByID(ctx, "x"); err != ErrPoolUnavailable {
		t.Errorf("FindByID err = %v, want ErrPoolUnavailable", err)
	}
	// Writes must surface the error too (never silently "succeed").
	if _, err := r.Create(ctx, CreateInput{Title: "x"}); err != ErrPoolUnavailable {
		t.Errorf("Create err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.Update(ctx, "x", map[string]any{"title": "y"}); err != ErrPoolUnavailable {
		t.Errorf("Update err = %v, want ErrPoolUnavailable", err)
	}
}

func TestHandler_CreateRequiresTitle(t *testing.T) {
	svc := NewService(NewRepository(nil))
	body := `{"startDate":"2026-06-01T00:00:00Z","endDate":"2026-06-30T00:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	Router(svc, nil, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandler_CreateRejectsBadDate(t *testing.T) {
	svc := NewService(NewRepository(nil))
	body := `{"title":"Launch","startDate":"not-a-date","endDate":"2026-06-30T00:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	Router(svc, nil, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandler_Create503WhenDegraded(t *testing.T) {
	// Valid body but nil pool: the write must 503, not pretend success.
	svc := NewService(NewRepository(nil))
	body := `{"title":"Launch","startDate":"2026-06-01T00:00:00Z","endDate":"2026-06-30T00:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	Router(svc, nil, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandler_UpdateRejectsMalformedBody(t *testing.T) {
	svc := NewService(NewRepository(nil))
	req := httptest.NewRequest(http.MethodPut, "/some-id", strings.NewReader("{not json"))
	rr := httptest.NewRecorder()
	Router(svc, nil, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandler_Update503WhenDegraded(t *testing.T) {
	svc := NewService(NewRepository(nil))
	req := httptest.NewRequest(http.MethodPut, "/some-id", strings.NewReader(`{"status":"active"}`))
	rr := httptest.NewRecorder()
	Router(svc, nil, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rr.Code, rr.Body.String())
	}
}

func TestParseUpdateFields(t *testing.T) {
	t.Run("known keys + unknown ignored", func(t *testing.T) {
		f, err := parseUpdateFields([]byte(`{"title":"x","participants":5,"bogus":1}`))
		if err != nil {
			t.Fatal(err)
		}
		if f["title"] != "x" || f["participants"] != 5 {
			t.Errorf("got %#v", f)
		}
		if _, ok := f["bogus"]; ok {
			t.Error("unknown key should be ignored")
		}
	})
	t.Run("present-null preserved as nil", func(t *testing.T) {
		f, err := parseUpdateFields([]byte(`{"description":null}`))
		if err != nil {
			t.Fatal(err)
		}
		v, ok := f["description"]
		if !ok || v != nil {
			t.Errorf("description: ok=%v v=%#v, want present nil", ok, v)
		}
	})
	t.Run("metadata kept as raw JSON string", func(t *testing.T) {
		f, err := parseUpdateFields([]byte(`{"metadata":{"a":1}}`))
		if err != nil {
			t.Fatal(err)
		}
		if f["metadata"] != `{"a":1}` {
			t.Errorf("metadata = %#v, want raw json string", f["metadata"])
		}
	})
	t.Run("type mismatch on known key errors", func(t *testing.T) {
		if _, err := parseUpdateFields([]byte(`{"participants":"nope"}`)); err == nil {
			t.Error("expected error on non-int participants")
		}
	})
	t.Run("malformed json errors", func(t *testing.T) {
		if _, err := parseUpdateFields([]byte(`{`)); err == nil {
			t.Error("expected error on malformed json")
		}
	})
	t.Run("empty object yields empty map", func(t *testing.T) {
		f, err := parseUpdateFields([]byte(`{}`))
		if err != nil || len(f) != 0 {
			t.Errorf("f=%#v err=%v", f, err)
		}
	})
}

func TestParseDate(t *testing.T) {
	ok := []string{
		"2026-06-01T00:00:00Z",
		"2026-06-01T12:30:00.123456789Z",
		"2026-06-01",
		"2026-06-01T12:30:00+08:00",
	}
	for _, s := range ok {
		if _, err := parseDate(s); err != nil {
			t.Errorf("parseDate(%q) errored: %v", s, err)
		}
	}
	for _, s := range []string{"", "not-a-date", "06/01/2026"} {
		if _, err := parseDate(s); err == nil {
			t.Errorf("parseDate(%q) should have errored", s)
		}
	}
}

func TestService_ListDegradesOnPoolUnavailable(t *testing.T) {
	svc := NewService(NewRepository(nil))
	out, err := svc.List(context.Background(), "")
	if err != nil {
		t.Fatalf("expected nil err on degraded mode; got %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty slice; got %d", len(out))
	}
}

func TestService_FindByIDDegradesAsNotFound(t *testing.T) {
	// Pool-unavailable should surface as ErrNotFound so the handler
	// returns 404 not 500 when DB envs are missing.
	svc := NewService(NewRepository(nil))
	_, err := svc.FindByID(context.Background(), "abc")
	if err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestHandler_ListReturnsEmptyArrayWhenDegraded(t *testing.T) {
	svc := NewService(NewRepository(nil))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	Router(svc, nil, nil).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out []Campaign
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty array; got %+v", out)
	}
}

func TestHandler_ActiveReturnsEmptyArrayWhenDegraded(t *testing.T) {
	svc := NewService(NewRepository(nil))
	req := httptest.NewRequest(http.MethodGet, "/active", nil)
	rr := httptest.NewRecorder()
	Router(svc, nil, nil).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out []Campaign
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty array; got %+v", out)
	}
}

func TestHandler_ByID404OnDegradedMode(t *testing.T) {
	svc := NewService(NewRepository(nil))
	req := httptest.NewRequest(http.MethodGet, "/somecuid", nil)
	rr := httptest.NewRecorder()
	Router(svc, nil, nil).ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 on degraded mode; got %d", rr.Code)
	}
}

func TestDecodeMetadata(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want any
	}{
		{"nil", nil, nil},
		{"empty", []byte{}, nil},
		{"bad json", []byte("{not json"), nil},
		{"valid", []byte(`{"k":1}`), map[string]any{"k": float64(1)}},
	}
	for _, tc := range cases {
		got := decodeMetadata(tc.in)
		switch want := tc.want.(type) {
		case nil:
			if got != nil {
				t.Errorf("%s: got %v, want nil", tc.name, got)
			}
		case map[string]any:
			gotMap, ok := got.(map[string]any)
			if !ok {
				t.Errorf("%s: got %T, want map", tc.name, got)
				continue
			}
			if gotMap["k"] != want["k"] {
				t.Errorf("%s: got %v, want %v", tc.name, gotMap, want)
			}
		}
	}
}
