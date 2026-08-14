// Package papertrade is the Go port of legacy NestJS paper-trade/ —
// a tiny "fake order book" used by mobile/admin to seed perp_orders
// without going through the live router. Two routes only:
//
//	POST /api/paper-orders  — INSERT a new PerpOrder row
//	GET  /api/paper-orders  — list newest 50 orders, optional filters.
//
// DEV/ADMIN-ONLY BOUNDARY: paper rows are fake by definition, so the whole
// module is gated behind PAPER_TRADE_ENABLED=1. When disabled (the default,
// including production) the routes answer 503 with an explicit message, and
// the trading read models exclude paper rows (txHash IS NULL) so a fake book
// can never feed the live product surface.
package papertrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Enabled reports whether the paper-trade seeder (and paper rows in trading
// reads) are allowed. Off unless PAPER_TRADE_ENABLED is explicitly 1/true.
func Enabled() bool {
	v := strings.TrimSpace(os.Getenv("PAPER_TRADE_ENABLED"))
	return v == "1" || strings.EqualFold(v, "true")
}

// DisabledRouter answers every paper-order route with an explicit 503 so a
// disabled deployment is distinguishable from a missing route.
func DisabledRouter() chi.Router {
	r := chi.NewRouter()
	h := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusServiceUnavailable,
			"paper trading is disabled (dev-only feature; set PAPER_TRADE_ENABLED=1 to enable locally)")
	}
	r.Get("/", h)
	r.Post("/", h)
	return r
}

// ErrPoolUnavailable surfaces the degraded contract.
var ErrPoolUnavailable = fmt.Errorf("papertrade repository: database pool not configured")

var addressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

// orderTypeAllowed is the closed set the NestJS service implicitly
// allowed by upper-casing the input. We validate explicitly to reject
// junk before reaching the DB.
var orderTypeAllowed = map[string]struct{}{
	"MARKET": {},
	"LIMIT":  {},
	"STOP":   {},
}

const defaultChainID = 11155111

// Order is the row shape returned to clients. createdAt is RFC3339 to
// match how NestJS serializes Prisma DateTime through the global
// transformer.
type Order struct {
	ID           string    `json:"id"`
	ChainID      int       `json:"chainId"`
	Account      string    `json:"account"`
	Token        string    `json:"token"`
	IsLong       bool      `json:"isLong"`
	SizeDelta    string    `json:"sizeDelta"`
	TriggerPrice *string   `json:"triggerPrice"`
	Type         string    `json:"type"`
	Status       string    `json:"status"`
	TxHash       *string   `json:"txHash"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// CreateInput mirrors the request body. triggerPrice is optional —
// missing-or-empty stores NULL.
type CreateInput struct {
	Account      string  `json:"account"`
	Token        string  `json:"token"`
	IsLong       bool    `json:"isLong"`
	SizeDelta    string  `json:"sizeDelta"`
	TriggerPrice *string `json:"triggerPrice"`
	Type         string  `json:"type"`
	ChainID      *int    `json:"chainId"`
}

// ListQuery matches the GET filter knobs (all optional).
type ListQuery struct {
	Account string
	Symbol  string
	ChainID *int
}

// Repository wraps the pgx pool.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository accepts a nil pool — calls return ErrPoolUnavailable.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Create inserts a perp_order row with status NEW (matches NestJS
// "NEW" status which the trading worker later transitions). Returns
// the row as written so clients see DB-generated id / timestamps.
func (r *Repository) Create(ctx context.Context, in CreateInput) (Order, error) {
	if r.pool == nil {
		return Order{}, ErrPoolUnavailable
	}

	account := strings.ToLower(in.Account)
	orderType := strings.ToUpper(in.Type)
	chainID := defaultChainID
	if in.ChainID != nil {
		chainID = *in.ChainID
	}
	var trigger *string
	if in.TriggerPrice != nil && *in.TriggerPrice != "" {
		v := *in.TriggerPrice
		trigger = &v
	}

	var o Order
	err := r.pool.QueryRow(ctx, `
		INSERT INTO perp_orders
			(id, chain_id, account, token, "isLong", "sizeDelta", "triggerPrice", type, status, "createdAt", "updatedAt")
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, 'NEW', NOW(), NOW())
		RETURNING id, chain_id, account, token, "isLong", "sizeDelta", "triggerPrice", type, status, "txHash", "createdAt", "updatedAt"
	`, chainID, account, in.Token, in.IsLong, in.SizeDelta, trigger, orderType).Scan(
		&o.ID, &o.ChainID, &o.Account, &o.Token, &o.IsLong, &o.SizeDelta,
		&o.TriggerPrice, &o.Type, &o.Status, &o.TxHash, &o.CreatedAt, &o.UpdatedAt,
	)
	if err != nil {
		return Order{}, fmt.Errorf("insert perp_order: %w", err)
	}
	return o, nil
}

// List returns up to 50 orders matching the optional filters,
// ordered by createdAt DESC.
func (r *Repository) List(ctx context.Context, q ListQuery) ([]Order, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}

	clauses := []string{}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if q.Account != "" {
		add(`account = $%d`, strings.ToLower(q.Account))
	}
	if q.Symbol != "" {
		add(`token = $%d`, q.Symbol)
	}
	if q.ChainID != nil {
		add(`chain_id = $%d`, *q.ChainID)
	}

	query := `
		SELECT id, chain_id, account, token, "isLong", "sizeDelta", "triggerPrice",
		       type, status, "txHash", "createdAt", "updatedAt"
		FROM perp_orders
	`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += ` ORDER BY "createdAt" DESC LIMIT 50`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_orders: %w", err)
	}
	defer rows.Close()

	out := make([]Order, 0, 50)
	for rows.Next() {
		var o Order
		if err := rows.Scan(
			&o.ID, &o.ChainID, &o.Account, &o.Token, &o.IsLong, &o.SizeDelta,
			&o.TriggerPrice, &o.Type, &o.Status, &o.TxHash, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan perp_order: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_orders: %w", err)
	}
	return out, nil
}

// Store is the surface used by handlers. Tests pass a stub.
type Store interface {
	Create(ctx context.Context, in CreateInput) (Order, error)
	List(ctx context.Context, q ListQuery) ([]Order, error)
}

// Service wires a Store for the HTTP layer.
type Service struct {
	store Store
}

// NewService accepts nil to honor the degraded contract.
func NewService(store Store) *Service {
	return &Service{store: store}
}

// Router mounts /api/paper-orders with POST + GET.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/", svc.list)
	r.Post("/", svc.create)
	return r
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	in.Account = strings.TrimSpace(in.Account)
	in.Token = strings.TrimSpace(in.Token)
	in.SizeDelta = strings.TrimSpace(in.SizeDelta)
	in.Type = strings.ToUpper(strings.TrimSpace(in.Type))

	if !addressRE.MatchString(in.Account) {
		writeError(w, http.StatusBadRequest, "Invalid account address")
		return
	}
	if in.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	if in.SizeDelta == "" {
		writeError(w, http.StatusBadRequest, "sizeDelta is required")
		return
	}
	if _, ok := orderTypeAllowed[in.Type]; !ok {
		writeError(w, http.StatusBadRequest, "type must be MARKET, LIMIT, or STOP")
		return
	}

	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	order, err := s.store.Create(r.Context(), in)
	if errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "paper-trade create failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to create paper order")
		return
	}
	writeJSON(w, http.StatusCreated, order)
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := ListQuery{
		Account: strings.TrimSpace(q.Get("account")),
		Symbol:  strings.TrimSpace(q.Get("symbol")),
	}
	if raw := strings.TrimSpace(q.Get("chainId")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err == nil && v > 0 {
			query.ChainID = &v
		}
		// Match NestJS parseNumberParam: invalid input → undefined, NOT 400.
	}

	if s.store == nil {
		writeJSON(w, http.StatusOK, []Order{})
		return
	}
	out, err := s.store.List(r.Context(), query)
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, []Order{})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "paper-trade list failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to list paper orders")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("papertrade response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statusCode": status,
		"message":    message,
	})
}
