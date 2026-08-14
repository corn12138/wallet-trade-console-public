package web3events

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
)

// addressRE / txHashRE mirror common/utils/web3-address.ts /
// web3-transactions.service.ts validators.
var (
	addressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)
	txHashRE  = regexp.MustCompile(`^0x[a-fA-F0-9]{64}$`)
)

// defaultChainID matches DEFAULT_WEB3_CHAIN_ID (sepolia).
const defaultChainID = 11155111

// Reader is the read surface the HTTP handlers depend on. Repository
// satisfies this interface; tests pass a stub.
type Reader interface {
	ListEvents(ctx context.Context, q ListEventsQuery) (ListEventsResult, error)
	GetFullStats(ctx context.Context, chainID int) (FullStats, error)
	EventsByTxHash(ctx context.Context, txHash string, chainID int) ([]EventRow, error)
	UserEvents(ctx context.Context, address string, chainID, limit int) ([]EventRow, error)
	RecentTransactions(ctx context.Context, chainID, limit int) ([]TransactionRow, error)
}

// Writer is the write surface for the 2 mutation endpoints (SIWE-guarded tx
// reporting). Repository satisfies it; tests pass a stub.
type Writer interface {
	UpsertSubmittedTransaction(ctx context.Context, in SubmitTxInput) (TransactionRow, error)
	UpsertTransactionReceipt(ctx context.Context, in ReceiptTxInput) (TransactionRow, error)
}

// Service holds the Reader the handlers use, plus an optional Writer + web3
// guard for the mutation endpoints.
type Service struct {
	reader         Reader
	writer         Writer
	web3Middleware func(http.Handler) http.Handler
}

// NewService builds a Service. A nil reader is allowed; routes return
// degraded responses (empty payloads, "0" for stats counters) just
// like sibling modules behave when their repository is unwired.
func NewService(reader Reader) *Service {
	return &Service{reader: reader}
}

// WithWrites enables the 2 mutation endpoints (POST /transactions[/receipt]) on
// the Service, guarded by web3Middleware when non-nil. Without a writer the
// POSTs are not mounted (the module stays read-only, as before).
func (s *Service) WithWrites(writer Writer, web3Middleware func(http.Handler) http.Handler) *Service {
	s.writer = writer
	s.web3Middleware = web3Middleware
	return s
}

// Router mounts the 5 read endpoints + the 2 mutation endpoints under
// /api/web3-events. The mutations are always mounted (so route parity sees a
// constant topology) under the web3 guard when one is wired — mirroring the
// NestJS @Public controller whose POSTs add @UseGuards(Web3AuthGuard). Without a
// Writer (no DB) the handlers degrade to 503, like sibling modules.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/", svc.listEvents)
	r.Get("/stats", svc.stats)
	r.Get("/tx/{txHash}", svc.eventsByTxHash)
	r.Get("/user/{address}", svc.userEvents)
	r.Get("/transactions", svc.recentTransactions)

	mountWrites := func(r chi.Router) {
		r.Post("/transactions", svc.reportSubmittedTransaction)
		r.Post("/transactions/{txHash}/receipt", svc.reportTransactionReceipt)
	}
	if svc.web3Middleware != nil {
		r.Group(func(r chi.Router) {
			r.Use(svc.web3Middleware)
			mountWrites(r)
		})
	} else {
		mountWrites(r)
	}
	return r
}

func (s *Service) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	page, ok := parsePositiveInt(q.Get("page"), 1, 1, 0)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid page")
		return
	}
	limit, ok := parsePositiveInt(q.Get("limit"), 20, 1, 100)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return
	}
	chainID, ok := parseChainID(q.Get("chainId"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid chainId")
		return
	}

	fromBlock, ok := parseOptionalInt64(q.Get("fromBlock"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid fromBlock")
		return
	}
	toBlock, ok := parseOptionalInt64(q.Get("toBlock"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid toBlock")
		return
	}

	query := ListEventsQuery{
		Page:            page,
		Limit:           limit,
		EventName:       strings.TrimSpace(q.Get("eventName")),
		ActorAddress:    strings.TrimSpace(q.Get("actorAddress")),
		ContractAddress: strings.TrimSpace(q.Get("contractAddress")),
		ChainID:         chainID,
		FromBlock:       fromBlock,
		ToBlock:         toBlock,
	}

	if s.reader == nil {
		writeJSON(w, http.StatusOK, ListEventsResult{
			Data: []EventRow{},
			Pagination: Pagination{
				Page:       query.Page,
				Limit:      query.Limit,
				Total:      0,
				TotalPages: 0,
			},
		})
		return
	}

	out, err := s.reader.ListEvents(r.Context(), query)
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, ListEventsResult{
			Data: []EventRow{},
			Pagination: Pagination{
				Page:       query.Page,
				Limit:      query.Limit,
				Total:      0,
				TotalPages: 0,
			},
		})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "web3events list failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load web3 events")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) stats(w http.ResponseWriter, r *http.Request) {
	chainID, ok := parseChainID(r.URL.Query().Get("chainId"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid chainId")
		return
	}

	if s.reader == nil {
		writeJSON(w, http.StatusOK, emptyFullStats())
		return
	}

	out, err := s.reader.GetFullStats(r.Context(), chainID)
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, emptyFullStats())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "web3events stats failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load web3 stats")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func emptyFullStats() FullStats {
	return FullStats{
		EventCounts:  []EventCount{},
		LatestBlock:  "0",
		IndexerBlock: "0",
		TotalEvents:  0,
	}
}

func (s *Service) eventsByTxHash(w http.ResponseWriter, r *http.Request) {
	txHash := strings.TrimSpace(chi.URLParam(r, "txHash"))
	if !txHashRE.MatchString(txHash) {
		writeError(w, http.StatusBadRequest, "invalid txHash")
		return
	}
	chainID, ok := parseChainID(r.URL.Query().Get("chainId"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid chainId")
		return
	}

	if s.reader == nil {
		writeJSON(w, http.StatusOK, []EventRow{})
		return
	}

	out, err := s.reader.EventsByTxHash(r.Context(), txHash, chainID)
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, []EventRow{})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "web3events by tx failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load web3 events")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) userEvents(w http.ResponseWriter, r *http.Request) {
	address := strings.TrimSpace(chi.URLParam(r, "address"))
	if !addressRE.MatchString(address) {
		writeError(w, http.StatusBadRequest, "invalid address")
		return
	}
	q := r.URL.Query()
	chainID, ok := parseChainID(q.Get("chainId"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid chainId")
		return
	}
	limit, ok := parsePositiveInt(q.Get("limit"), 50, 1, 200)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return
	}

	if s.reader == nil {
		writeJSON(w, http.StatusOK, []EventRow{})
		return
	}

	out, err := s.reader.UserEvents(r.Context(), address, chainID, limit)
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, []EventRow{})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "web3events user events failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load web3 user events")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) recentTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	chainID, ok := parseChainID(q.Get("chainId"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid chainId")
		return
	}
	limit, ok := parsePositiveInt(q.Get("limit"), 20, 1, 100)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return
	}

	if s.reader == nil {
		writeJSON(w, http.StatusOK, []TransactionRow{})
		return
	}

	out, err := s.reader.RecentTransactions(r.Context(), chainID, limit)
	if errors.Is(err, ErrPoolUnavailable) {
		writeJSON(w, http.StatusOK, []TransactionRow{})
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "web3events recent txs failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load web3 transactions")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// submittedBody / receiptBody mirror the NestJS @Body shapes. bigint-like
// fields are json.RawMessage so both "123" (string) and 123 (number) parse.
type submittedBody struct {
	ChainID         *int            `json:"chainId"`
	TxHash          string          `json:"txHash"`
	FromAddress     *string         `json:"fromAddress"`
	ToAddress       *string         `json:"toAddress"`
	ContractAddress *string         `json:"contractAddress"`
	Value           *string         `json:"value"`
	TxType          *string         `json:"txType"`
	Metadata        json.RawMessage `json:"metadata"`
}

type receiptBody struct {
	ChainID         *int            `json:"chainId"`
	FromAddress     *string         `json:"fromAddress"`
	Status          *string         `json:"status"`
	BlockNumber     json.RawMessage `json:"blockNumber"`
	GasUsed         json.RawMessage `json:"gasUsed"`
	GasPrice        json.RawMessage `json:"gasPrice"`
	ToAddress       *string         `json:"toAddress"`
	ContractAddress *string         `json:"contractAddress"`
	Value           *string         `json:"value"`
	TxType          *string         `json:"txType"`
	Metadata        json.RawMessage `json:"metadata"`
}

// reportSubmittedTransaction is POST /web3-events/transactions — the Go port of
// web3-events.controller.ts reportSubmittedTransaction (SIWE-guarded). The
// fromAddress is pinned to the authenticated wallet (resolveAuthenticatedOwnerAddress).
func (s *Service) reportSubmittedTransaction(w http.ResponseWriter, r *http.Request) {
	if s.writer == nil {
		writeError(w, http.StatusServiceUnavailable, "transaction reporting not configured")
		return
	}
	var body submittedBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	from, ok := resolveOwner(w, r, body.FromAddress)
	if !ok {
		return
	}
	txHash, ok := validateTxHash(body.TxHash)
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid txHash")
		return
	}
	toAddr, ok := normOptAddr(w, body.ToAddress, "toAddress")
	if !ok {
		return
	}
	contractAddr, ok := normOptAddr(w, body.ContractAddress, "contractAddress")
	if !ok {
		return
	}
	out, err := s.writer.UpsertSubmittedTransaction(r.Context(), SubmitTxInput{
		ChainID:         chainIDOrDefault(body.ChainID),
		TxHash:          txHash,
		FromAddress:     from,
		ToAddress:       toAddr,
		ContractAddress: contractAddr,
		Value:           normOptStr(body.Value),
		TxType:          normOptStr(body.TxType),
		Metadata:        body.Metadata,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "report submitted tx failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to record transaction")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// reportTransactionReceipt is POST /web3-events/transactions/:txHash/receipt.
func (s *Service) reportTransactionReceipt(w http.ResponseWriter, r *http.Request) {
	if s.writer == nil {
		writeError(w, http.StatusServiceUnavailable, "transaction reporting not configured")
		return
	}
	var body receiptBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	from, ok := resolveOwner(w, r, body.FromAddress)
	if !ok {
		return
	}
	txHash, ok := validateTxHash(chi.URLParam(r, "txHash"))
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid txHash")
		return
	}
	toAddr, ok := normOptAddr(w, body.ToAddress, "toAddress")
	if !ok {
		return
	}
	contractAddr, ok := normOptAddr(w, body.ContractAddress, "contractAddress")
	if !ok {
		return
	}
	status, ok := normalizeStatus(body.Status)
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid transaction status")
		return
	}
	blockNumber, ok := parseBigIntLike(body.BlockNumber)
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid bigint field")
		return
	}
	gasUsed, ok := parseBigIntLike(body.GasUsed)
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid bigint field")
		return
	}
	gasPrice, ok := parseBigIntLike(body.GasPrice)
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid bigint field")
		return
	}
	var bn int64
	if blockNumber != nil {
		bn = *blockNumber
	}
	out, err := s.writer.UpsertTransactionReceipt(r.Context(), ReceiptTxInput{
		ChainID:         chainIDOrDefault(body.ChainID),
		TxHash:          txHash,
		FromAddress:     from,
		ToAddress:       toAddr,
		ContractAddress: contractAddr,
		Value:           normOptStr(body.Value),
		TxType:          normOptStr(body.TxType),
		Metadata:        body.Metadata,
		Status:          status,
		BlockNumber:     bn,
		GasUsed:         gasUsed,
		GasPrice:        gasPrice,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "report tx receipt failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to record transaction")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveOwner mirrors resolveAuthenticatedOwnerAddress: default the address to
// the authenticated wallet, reject a mismatched one (403).
func resolveOwner(w http.ResponseWriter, r *http.Request, requested *string) (string, bool) {
	req := ""
	if requested != nil {
		req = *requested
	}
	owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), req)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrMissingAuthenticated):
			writeError(w, http.StatusUnauthorized, err.Error())
		case errors.Is(err, auth.ErrOwnerMismatch):
			writeError(w, http.StatusForbidden, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return "", false
	}
	return owner, true
}

func validateTxHash(raw string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if !txHashRE.MatchString(v) {
		return "", false
	}
	return v, true
}

// normOptAddr mirrors normalizeOptionalAddress: nil / empty → nil (kept on
// upsert); a valid address → lowercased; otherwise a 400.
func normOptAddr(w http.ResponseWriter, p *string, label string) (*string, bool) {
	if p == nil {
		return nil, true
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil, true
	}
	if !addressRE.MatchString(v) {
		writeError(w, http.StatusBadRequest, "Invalid "+label)
		return nil, false
	}
	lower := strings.ToLower(v)
	return &lower, true
}

func normOptStr(p *string) *string {
	if p == nil {
		return nil
	}
	v := strings.TrimSpace(*p)
	if v == "" {
		return nil
	}
	return &v
}

func chainIDOrDefault(p *int) int {
	if p == nil {
		return defaultChainID
	}
	return *p
}

// normalizeStatus mirrors normalizeTransactionStatus (pending/confirmed/failed).
func normalizeStatus(p *string) (string, bool) {
	v := ""
	if p != nil {
		v = strings.ToLower(strings.TrimSpace(*p))
	}
	switch v {
	case "", "pending":
		return "pending", true
	case "success", "confirmed":
		return "confirmed", true
	case "reverted", "failed":
		return "failed", true
	default:
		return "", false
	}
}

// parseBigIntLike mirrors normalizeBigIntLike: accepts a JSON string ("123") or
// number (123) or null; returns nil for absent/null, ok=false on garbage.
func parseBigIntLike(raw json.RawMessage) (*int64, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil, true
	}
	s = strings.Trim(s, `"`)
	if s == "" {
		return nil, true
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, false
	}
	return &v, true
}

// parsePositiveInt returns (value, ok). Empty input → fallback. min/max
// are inclusive. maxVal == 0 means no upper bound.
func parsePositiveInt(raw string, fallback, minVal, maxVal int) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	if v < minVal {
		return 0, false
	}
	if maxVal > 0 && v > maxVal {
		return 0, false
	}
	return v, true
}

func parseChainID(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultChainID, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

func parseOptionalInt64(raw string) (*int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, false
	}
	return &v, true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("web3events response encode failed", "err", err)
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
