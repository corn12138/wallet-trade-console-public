// Package indexeradmin exposes only the read-only indexer status surface. Worker
// lifecycle and repair hooks remain internal and are never mounted on HTTP.
package indexeradmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrRPCNotConfigured is returned by backfill/resync when no chain-RPC-backed
// hook is wired into the admin path.
var ErrRPCNotConfigured = errors.New("indexer admin: chain RPC not configured for this operation")

// Checkpoint mirrors one web3_indexer_state row in the status payload.
type Checkpoint struct {
	Contract string `json:"contract"`
	Block    string `json:"block"`
}

// Status mirrors IndexerService.getStatus()'s shape exactly.
type Status struct {
	IsRunning      bool         `json:"isRunning"`
	ChainID        int          `json:"chainId"`
	WatchTransport string       `json:"watchTransport"`
	Contracts      []string     `json:"contracts"`
	Checkpoints    []Checkpoint `json:"checkpoints"`
}

// ResyncResult mirrors syncTransactionByHash's "not indexed" return branch.
type ResyncResult struct {
	TxHash       string  `json:"txHash"`
	Status       string  `json:"status"`
	SyncedEvents int     `json:"syncedEvents"`
	Indexed      bool    `json:"indexed"`
	OccurredAt   *string `json:"occurredAt"`
}

// StateReader reads the indexer checkpoint table; a nil pool degrades to an
// empty checkpoint list (status still reports config).
type StateReader interface {
	ListCheckpoints(ctx context.Context, chainID int) ([]Checkpoint, error)
}

// WorkerControl is an optional hook to drive the real worker on start/stop.
type WorkerControl interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// Backfiller is an optional hook for POST /backfill (needs chain RPC).
type Backfiller interface {
	Backfill(ctx context.Context, contractAddress string) error
}

// Resyncer is an optional hook for POST /resync-tx (needs chain RPC).
type Resyncer interface {
	ResyncTx(ctx context.Context, txHash string) (ResyncResult, error)
}

// Repository reads web3_indexer_state via pgx.
type Repository struct{ pool *pgxpool.Pool }

// NewRepository tolerates a nil pool.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// ListCheckpoints returns one entry per tracked contract on the chain.
func (r *Repository) ListCheckpoints(ctx context.Context, chainID int) ([]Checkpoint, error) {
	out := []Checkpoint{}
	if r == nil || r.pool == nil {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT contract_address, last_processed_block
		FROM web3_indexer_state
		WHERE chain_id = $1
		ORDER BY contract_address ASC`, chainID)
	if err != nil {
		return out, fmt.Errorf("query web3_indexer_state: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var contract string
		var block int64
		if err := rows.Scan(&contract, &block); err != nil {
			return out, fmt.Errorf("scan checkpoint: %w", err)
		}
		out = append(out, Checkpoint{Contract: contract, Block: fmt.Sprintf("%d", block)})
	}
	return out, rows.Err()
}

// Config carries the static facts status reports.
type Config struct {
	ChainID        int
	WatchTransport string   // "wss" | "http"
	Contracts      []string // tracked contract addresses (e.g. factory)
}

// Service wires the read repo + config + optional control hooks.
type Service struct {
	reader     StateReader
	cfg        Config
	worker     WorkerControl // optional
	backfiller Backfiller    // optional
	resyncer   Resyncer      // optional

	mu      sync.Mutex
	running bool
}

// NewService builds the admin service. running starts true (the worker runs at
// boot). Optional hooks may be nil.
func NewService(reader StateReader, cfg Config) *Service {
	if cfg.Contracts == nil {
		cfg.Contracts = []string{}
	}
	return &Service{reader: reader, cfg: cfg, running: true}
}

// WithWorker wires the real worker lifecycle into start/stop (optional).
func (s *Service) WithWorker(w WorkerControl) *Service { s.worker = w; return s }

// WithBackfiller enables POST /backfill (optional; needs chain RPC).
func (s *Service) WithBackfiller(b Backfiller) *Service { s.backfiller = b; return s }

// WithResyncer enables POST /resync-tx (optional; needs chain RPC).
func (s *Service) WithResyncer(r Resyncer) *Service { s.resyncer = r; return s }

func (s *Service) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Status reads checkpoints + reports config/running.
func (s *Service) Status(ctx context.Context) (Status, error) {
	cps, err := s.reader.ListCheckpoints(ctx, s.cfg.ChainID)
	if err != nil {
		return Status{}, err
	}
	return Status{
		IsRunning:      s.isRunning(),
		ChainID:        s.cfg.ChainID,
		WatchTransport: s.cfg.WatchTransport,
		Contracts:      s.cfg.Contracts,
		Checkpoints:    cps,
	}, nil
}

// Router exposes monitoring state without publishing worker control operations.
func Router(s *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/status", s.handleStatus)
	return r
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.Status(r.Context())
	if err != nil {
		slog.ErrorContext(r.Context(), "indexer status failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load indexer status")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Service) handleStart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	if s.worker != nil {
		if err := s.worker.Start(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to start indexer")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "Indexer started"})
}

func (s *Service) handleStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	if s.worker != nil {
		if err := s.worker.Stop(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to stop indexer")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "Indexer stopped"})
}

func (s *Service) handleBackfill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ContractAddress string `json:"contractAddress"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if s.backfiller == nil {
		writeError(w, http.StatusServiceUnavailable,
			"backfill requires chain RPC (SEPOLIA_RPC_URL) — not wired into the admin path")
		return
	}
	if err := s.backfiller.Backfill(r.Context(), strings.TrimSpace(body.ContractAddress)); err != nil {
		writeError(w, http.StatusInternalServerError, "backfill failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "Backfill triggered"})
}

func (s *Service) handleResync(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TxHash string `json:"txHash"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	txHash := strings.TrimSpace(body.TxHash)
	if txHash == "" {
		writeError(w, http.StatusBadRequest, "txHash is required")
		return
	}
	if s.resyncer == nil {
		writeError(w, http.StatusServiceUnavailable,
			"resync-tx requires chain RPC (SEPOLIA_RPC_URL) — not wired into the admin path")
		return
	}
	res, err := s.resyncer.ResyncTx(r.Context(), txHash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "resync failed")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("indexeradmin response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": status, "message": message})
}
