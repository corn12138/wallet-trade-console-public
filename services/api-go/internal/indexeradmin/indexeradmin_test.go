package indexeradmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubReader struct{ cps []Checkpoint }

func (s stubReader) ListCheckpoints(context.Context, int) ([]Checkpoint, error) { return s.cps, nil }

func newSvc() *Service {
	return NewService(stubReader{cps: []Checkpoint{{Contract: "0xabc", Block: "42"}}},
		Config{ChainID: 11155111, WatchTransport: "wss", Contracts: []string{"0xfactory"}})
}

func req(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestStatusShape(t *testing.T) {
	h := Router(newSvc())
	rec := req(t, h, http.MethodGet, "/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status got %d", rec.Code)
	}
	var st Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !st.IsRunning || st.ChainID != 11155111 || st.WatchTransport != "wss" {
		t.Fatalf("unexpected status: %+v", st)
	}
	if len(st.Contracts) != 1 || st.Contracts[0] != "0xfactory" {
		t.Fatalf("contracts: %+v", st.Contracts)
	}
	if len(st.Checkpoints) != 1 || st.Checkpoints[0].Block != "42" {
		t.Fatalf("checkpoints: %+v", st.Checkpoints)
	}
}

func TestControlRoutesAreNotMounted(t *testing.T) {
	h := Router(newSvc())
	for _, path := range []string{"/start", "/stop", "/backfill", "/resync-tx"} {
		if rec := req(t, h, http.MethodPost, path, `{}`); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s got %d, want 404", path, rec.Code)
		}
	}
}

func TestNilPoolRepoEmptyCheckpoints(t *testing.T) {
	r := NewRepository(nil)
	cps, err := r.ListCheckpoints(context.Background(), 11155111)
	if err != nil {
		t.Fatalf("nil pool must not error, got %v", err)
	}
	if cps == nil || len(cps) != 0 {
		t.Fatalf("nil pool must return empty slice, got %+v", cps)
	}
}
