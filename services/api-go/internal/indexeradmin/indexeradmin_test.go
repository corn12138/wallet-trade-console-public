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

type stubResyncer struct{ res ResyncResult }

func (s stubResyncer) ResyncTx(context.Context, string) (ResyncResult, error) { return s.res, nil }

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

func TestStartStopToggleRunning(t *testing.T) {
	svc := newSvc()
	h := Router(svc)
	if rec := req(t, h, http.MethodPost, "/stop", ""); rec.Code != http.StatusOK {
		t.Fatalf("stop got %d", rec.Code)
	}
	rec := req(t, h, http.MethodGet, "/status", "")
	var st Status
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.IsRunning {
		t.Fatalf("after stop, isRunning should be false")
	}
	_ = req(t, h, http.MethodPost, "/start", "")
	rec = req(t, h, http.MethodGet, "/status", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.IsRunning {
		t.Fatalf("after start, isRunning should be true")
	}
}

func TestBackfillNotConfigured(t *testing.T) {
	h := Router(newSvc())
	rec := req(t, h, http.MethodPost, "/backfill", `{"contractAddress":"0xabc"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("backfill without RPC got %d want 503", rec.Code)
	}
}

func TestResyncValidationAndDegradation(t *testing.T) {
	h := Router(newSvc())
	// missing txHash -> 400
	if rec := req(t, h, http.MethodPost, "/resync-tx", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing txHash got %d want 400", rec.Code)
	}
	// txHash present but no resyncer -> 503
	if rec := req(t, h, http.MethodPost, "/resync-tx", `{"txHash":"0xdead"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("resync without RPC got %d want 503", rec.Code)
	}
}

func TestResyncWired(t *testing.T) {
	svc := newSvc().WithResyncer(stubResyncer{res: ResyncResult{TxHash: "0xdead", Status: "pending", Indexed: false}})
	h := Router(svc)
	rec := req(t, h, http.MethodPost, "/resync-tx", `{"txHash":"0xdead"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("wired resync got %d want 200", rec.Code)
	}
	var res ResyncResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.TxHash != "0xdead" || res.Indexed {
		t.Fatalf("unexpected resync result: %+v", res)
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
