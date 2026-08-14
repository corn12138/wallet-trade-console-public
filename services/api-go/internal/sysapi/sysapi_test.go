package sysapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubDB struct {
	err error
}

func (s stubDB) Ping(context.Context) error { return s.err }

func TestHealth_NoPool_Disconnected(t *testing.T) {
	mux := Router(nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("Status = %q, want ok", body.Status)
	}
	if body.Database.Status != "disconnected" {
		t.Errorf("Database.Status = %q, want disconnected", body.Database.Status)
	}
}

func TestHealth_PoolErrors_ErrorStatus(t *testing.T) {
	mux := Router(stubDB{err: errors.New("boom")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body HealthResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Database.Status != "error" {
		t.Errorf("Database.Status = %q, want error", body.Database.Status)
	}
	if body.Database.Message != "boom" {
		t.Errorf("Database.Message = %q, want boom", body.Database.Message)
	}
}

func TestHealth_PoolHealthy_Connected(t *testing.T) {
	mux := Router(stubDB{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body HealthResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Database.Status != "connected" {
		t.Errorf("Database.Status = %q, want connected", body.Database.Status)
	}
	if body.Database.Latency == nil {
		t.Error("Latency should be set when connected")
	}
}

func TestHealthDB_HealthyOrNot(t *testing.T) {
	cases := []struct {
		name string
		db   DBChecker
		want string
	}{
		{"healthy", stubDB{}, "healthy"},
		{"unhealthy_no_pool", nil, "unhealthy"},
		{"unhealthy_err", stubDB{err: errors.New("boom")}, "unhealthy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mux := Router(c.db)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/db", nil))
			var body DBResponse
			_ = json.NewDecoder(rec.Body).Decode(&body)
			if body.Database != c.want {
				t.Errorf("Database = %q, want %q", body.Database, c.want)
			}
		})
	}
}

func TestMetrics_ReturnsPrometheusText(t *testing.T) {
	mux := Router(nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct == "" {
		t.Errorf("Content-Type empty, want a Prometheus text format")
	}
}

func TestMetricsCustom_ReturnsJSONWithUptime(t *testing.T) {
	mux := Router(nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics/custom", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body CustomMetricsResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Uptime < 0 {
		t.Errorf("Uptime = %f, want >= 0", body.Uptime)
	}
	if body.Memory["alloc"] == 0 {
		t.Errorf("Memory[alloc] = 0, expected runtime stat populated")
	}
}
