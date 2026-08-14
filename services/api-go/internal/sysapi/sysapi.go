// Package sysapi serves the /api/health and /api/metrics aliases that
// the NestJS health.controller.ts and metrics.controller.ts expose.
// The Go service already provides /healthz, /readyz, and /metrics at
// the root — sysapi adds the /api/-prefixed routes the frontend +
// monitoring stack expect, plus the JSON-shaped responses NestJS
// returns (which differ from Prometheus's plain-text /metrics).
package sysapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// startedAt is captured at package init so the uptime field is meaningful
// (process.uptime() on Node).
var startedAt = time.Now()

// DBChecker is the interface health uses to ping the database. pgxpool.Pool
// satisfies it; tests pass a stub.
type DBChecker interface {
	Ping(ctx context.Context) error
}

// HealthResponse mirrors NestJS GET /api/health.
type HealthResponse struct {
	Status    string         `json:"status"`
	Timestamp string         `json:"timestamp"`
	Uptime    string         `json:"uptime"`
	Database  DatabaseStatus `json:"database"`
	Memory    MemoryStats    `json:"memory"`
}

// DatabaseStatus matches the Nest `database` field of GET /api/health.
type DatabaseStatus struct {
	Status  string `json:"status"`
	Latency *int64 `json:"latency,omitempty"`
	Driver  string `json:"driver,omitempty"`
	Message string `json:"message,omitempty"`
}

// MemoryStats matches Nest's `memory` field (rss / heapTotal / heapUsed
// in human-readable MB strings; closest Go analog is the runtime stats).
type MemoryStats struct {
	RSS       string `json:"rss"`
	HeapTotal string `json:"heapTotal"`
	HeapUsed  string `json:"heapUsed"`
}

// DBResponse matches NestJS GET /api/health/db.
type DBResponse struct {
	Database  string `json:"database"`
	Timestamp string `json:"timestamp"`
}

// CustomMetricsResponse matches NestJS GET /api/metrics/custom.
// custom_metrics is JSON-pass-through so future Go-side counters can
// add fields without breaking the wire shape.
type CustomMetricsResponse struct {
	Timestamp     string           `json:"timestamp"`
	Uptime        float64          `json:"uptime"`
	Memory        map[string]int64 `json:"memory"`
	CustomMetrics json.RawMessage  `json:"custom_metrics,omitempty"`
}

// Router mounts /health, /health/db (GETs) and /metrics, /metrics/custom
// (GETs). The pool arg may be nil — health degrades to "disconnected"
// and metrics still serve.
func Router(pool DBChecker) chi.Router {
	r := chi.NewRouter()

	r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
		dbStatus := pingDB(req.Context(), pool)
		writeJSON(w, http.StatusOK, HealthResponse{
			Status:    "ok",
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Uptime:    formatUptime(time.Since(startedAt)),
			Database:  dbStatus,
			Memory:    memoryStats(),
		})
	})

	r.Get("/health/db", func(w http.ResponseWriter, req *http.Request) {
		db := pingDB(req.Context(), pool)
		status := "unhealthy"
		if db.Status == "connected" {
			status = "healthy"
		}
		writeJSON(w, http.StatusOK, DBResponse{
			Database:  status,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})
	})

	// /api/metrics → Prometheus text format, same handler the root
	// /metrics endpoint uses. Mounting both keeps existing scrapers
	// working while the migration is in flight.
	r.Handle("/metrics", promhttp.Handler())

	r.Get("/metrics/custom", func(w http.ResponseWriter, _ *http.Request) {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		writeJSON(w, http.StatusOK, CustomMetricsResponse{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Uptime:    time.Since(startedAt).Seconds(),
			Memory: map[string]int64{
				"alloc":      int64(mem.Alloc),
				"totalAlloc": int64(mem.TotalAlloc),
				"sys":        int64(mem.Sys),
				"heapAlloc":  int64(mem.HeapAlloc),
				"heapInUse":  int64(mem.HeapInuse),
				"numGC":      int64(mem.NumGC),
			},
		})
	})

	return r
}

func pingDB(ctx context.Context, pool DBChecker) DatabaseStatus {
	if pool == nil {
		return DatabaseStatus{Status: "disconnected", Message: "no pool configured", Driver: "pgx"}
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := pool.Ping(pingCtx); err != nil {
		return DatabaseStatus{Status: "error", Message: err.Error(), Driver: "pgx"}
	}
	latency := time.Since(start).Milliseconds()
	return DatabaseStatus{
		Status:  "connected",
		Driver:  "pgx",
		Latency: &latency,
	}
}

func memoryStats() MemoryStats {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return MemoryStats{
		RSS:       fmt.Sprintf("%dMB", m.Sys/1024/1024),
		HeapTotal: fmt.Sprintf("%dMB", m.HeapSys/1024/1024),
		HeapUsed:  fmt.Sprintf("%dMB", m.HeapInuse/1024/1024),
	}
}

func formatUptime(d time.Duration) string {
	secs := int(d.Seconds())
	mins := secs / 60
	secs %= 60
	return fmt.Sprintf("%dm %ds", mins, secs)
}

// PingerFromPool wraps a *pgxpool.Pool as a DBChecker. Returns nil if
// the pool is nil so the caller can pass the result straight into
// Router without an if-check at the call site.
func PingerFromPool(pool *pgxpool.Pool) DBChecker {
	if pool == nil {
		return nil
	}
	return pool
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("sysapi response encode failed", "err", err)
	}
}

// ensure stdlib imports are used (linters can complain on rewrites
// when a constant ends up unused).
var _ = os.Args
