// Package security is the Go port of legacy NestJS security
// (security.service.ts + connected-sites.service.ts), plus the revoke
// tx-builder. All seven frontend-used routes are now Go: GET
// approvals/alerts/connected-sites (reads), POST/DELETE connected-sites
// (mutations.go), POST revoke/build-tx, and POST tx-review — the last
// folded into this /security mount by httpx/server.go via
// txreview.RegisterGuarded when deps.TxReview is wired (the txreview
// package owns that handler).
//
// Matching the deployed NestJS SecurityController (class-level @Public +
// @UseGuards(Web3AuthGuard); every handler resolves the owner via
// resolveAuthenticatedOwnerAddress), EVERY route is web3-guarded, and the
// data reads + connected-sites mutations are owner-pinned to the
// authenticated wallet. The reads previously mounted guarded-but-not-
// owner-pinned (an IDOR: any token + a foreign ?address= returned that
// owner's data) — now owner-pinned via auth.ResolveOwner. See Router and
// guard-parity.md.
package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// addressRE mirrors common/utils/web3-address.ts isValidAddress.
var addressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

// Severity / category enums (closed sets matching security.types.ts).
const (
	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"

	CategoryApproval = "approval"
	CategorySpender  = "spender"
	CategoryActivity = "activity"

	ActionRevoke = "revoke-approval"
	ActionReview = "review-spender"
)

// Thresholds from security.service.ts (1:1 with NestJS constants).
const (
	staleApprovalDays          = 30
	multiTokenSpenderThreshold = 2
	approvalChurnThreshold     = 3
	approvalChurnWindow        = 24 * time.Hour
	defaultWeb3ChainID         = 11155111
)

var (
	// MAX_UINT256 / 100. JS uses BigInt; Go uses *big.Int.
	highAllowanceThreshold = new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil) // 10^24
	maxUint256             = func() *big.Int {
		// (1 << 256) - 1
		v := new(big.Int).Lsh(big.NewInt(1), 256)
		return v.Sub(v, big.NewInt(1))
	}()
	unlimitedThreshold = func() *big.Int {
		return new(big.Int).Div(maxUint256, big.NewInt(100))
	}()
)

// ApprovalRecord is the GET /api/security/approvals row shape.
type ApprovalRecord struct {
	TokenAddress  string `json:"tokenAddress"`
	Spender       string `json:"spender"`
	Allowance     string `json:"allowance"`
	ChainID       int    `json:"chainId"`
	LastUpdatedAt string `json:"lastUpdatedAt"`
	TxHash        string `json:"txHash"`
}

// Alert is the GET /api/security/alerts row shape. tokenAddress /
// spender / txHash / actionLabel / actionType are nullable depending
// on category, mirroring the NestJS optional fields.
type Alert struct {
	ID           string  `json:"id"`
	Severity     string  `json:"severity"`
	Category     string  `json:"category"`
	Title        string  `json:"title"`
	Summary      string  `json:"summary"`
	ChainID      int     `json:"chainId"`
	OccurredAt   string  `json:"occurredAt"`
	TokenAddress *string `json:"tokenAddress,omitempty"`
	Spender      *string `json:"spender,omitempty"`
	TxHash       *string `json:"txHash,omitempty"`
	ActionLabel  *string `json:"actionLabel,omitempty"`
	ActionType   *string `json:"actionType,omitempty"`
}

// ConnectedSite is the GET /api/security/connected-sites row shape.
type ConnectedSite struct {
	ID               string   `json:"id"`
	OwnerAddress     string   `json:"ownerAddress"`
	ChainID          *int     `json:"chainId"`
	Origin           string   `json:"origin"`
	Domain           string   `json:"domain"`
	SiteName         string   `json:"siteName"`
	IconURL          *string  `json:"iconUrl"`
	Category         *string  `json:"category"`
	RiskLevel        string   `json:"riskLevel"`
	Permissions      []string `json:"permissions"`
	FirstConnectedAt string   `json:"firstConnectedAt"`
	LastConnectedAt  string   `json:"lastConnectedAt"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
}

// approvalEventRow is the internal projection from web3_events.
type approvalEventRow struct {
	chainID         int
	contractAddress string
	args            []byte
	txHash          string
	occurredAt      *time.Time
	createdAt       time.Time
}

// ErrPoolUnavailable mirrors the sibling pattern.
var ErrPoolUnavailable = errors.New("security repository: database pool not configured")

// Repository owns the SQL surface. nil pool yields ErrPoolUnavailable.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository binds a repo. nil pool tolerated.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// listApprovalEvents pulls the last 100 Approval rows for an actor.
// Ordering matches NestJS: blockNumber DESC, logIndex DESC.
func (r *Repository) listApprovalEvents(ctx context.Context, actorAddress string, chainID *int) ([]approvalEventRow, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	args := []any{"Approval", actorAddress}
	where := `event_name = $1 AND actor_address = $2`
	if chainID != nil {
		args = append(args, *chainID)
		where += ` AND chain_id = $3`
	}
	rows, err := r.pool.Query(ctx, `
		SELECT chain_id, contract_address, args, tx_hash, occurred_at, created_at
		FROM web3_events
		WHERE `+where+`
		ORDER BY block_number DESC, log_index DESC
		LIMIT 100
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query web3_events (Approval): %w", err)
	}
	defer rows.Close()
	out := make([]approvalEventRow, 0)
	for rows.Next() {
		var row approvalEventRow
		if err := rows.Scan(&row.chainID, &row.contractAddress, &row.args,
			&row.txHash, &row.occurredAt, &row.createdAt); err != nil {
			return nil, fmt.Errorf("scan web3_events: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate web3_events: %w", err)
	}
	return out, nil
}

// countRecentApprovalEvents is the alert-rule input for "approval
// churn detected". Mirrors the prisma count with createdAt >= now-24h.
func (r *Repository) countRecentApprovalEvents(ctx context.Context, actorAddress string, chainID *int, since time.Time) (int, error) {
	if r.pool == nil {
		return 0, ErrPoolUnavailable
	}
	args := []any{"Approval", actorAddress, since}
	where := `event_name = $1 AND actor_address = $2 AND created_at >= $3`
	if chainID != nil {
		args = append(args, *chainID)
		where += ` AND chain_id = $4`
	}
	var count int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM web3_events WHERE `+where+`
	`, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count web3_events: %w", err)
	}
	return count, nil
}

// listConnectedSites queries the connected_sites table. chainId nil
// means all chains; non-nil matches both the chain and rows with NULL
// chainId (matches the NestJS OR clause).
func (r *Repository) listConnectedSites(ctx context.Context, ownerAddress string, chainID *int) ([]ConnectedSite, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	args := []any{ownerAddress}
	where := `"ownerAddress" = $1`
	if chainID != nil {
		args = append(args, *chainID)
		where += ` AND ("chainId" = $2 OR "chainId" IS NULL)`
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, "ownerAddress", "chainId", origin, domain, "siteName",
		       "iconUrl", category, "riskLevel", permissions,
		       "firstConnectedAt", "lastConnectedAt", "createdAt", "updatedAt"
		FROM connected_sites
		WHERE `+where+`
		ORDER BY "lastConnectedAt" DESC, "createdAt" DESC
		LIMIT 50
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query connected_sites: %w", err)
	}
	defer rows.Close()
	out := make([]ConnectedSite, 0)
	for rows.Next() {
		var (
			s                             ConnectedSite
			permissionsRaw                []byte
			firstConnected, lastConnected time.Time
			created, updated              time.Time
		)
		if err := rows.Scan(&s.ID, &s.OwnerAddress, &s.ChainID, &s.Origin, &s.Domain, &s.SiteName,
			&s.IconURL, &s.Category, &s.RiskLevel, &permissionsRaw,
			&firstConnected, &lastConnected, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan connected_sites: %w", err)
		}
		s.Permissions = decodeStringArray(permissionsRaw)
		s.FirstConnectedAt = firstConnected.UTC().Format(time.RFC3339Nano)
		s.LastConnectedAt = lastConnected.UTC().Format(time.RFC3339Nano)
		s.CreatedAt = created.UTC().Format(time.RFC3339Nano)
		s.UpdatedAt = updated.UTC().Format(time.RFC3339Nano)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connected_sites: %w", err)
	}
	return out, nil
}

func decodeStringArray(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var arr []any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// --- service ---

// Service wraps the repo with the alert-composition logic. Empty
// address and nil-pool both degrade to empty slices, matching the
// FE-rendering pattern from sibling packages.
type Service struct {
	repo *Repository
	// now is injected so tests can pin the churn-window comparison.
	now func() time.Time
}

// NewService builds a Service that uses real wall-clock time.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo, now: time.Now}
}

// GetApprovals returns the deduped approval records (latest only per
// (token, spender) pair). Empty/invalid address surfaces as [].
func (s *Service) GetApprovals(ctx context.Context, address string, chainID *int) ([]ApprovalRecord, error) {
	addr, ok := normalizeIfValid(address)
	if !ok {
		return []ApprovalRecord{}, nil
	}
	return s.listApprovalRecords(ctx, addr, chainID)
}

func (s *Service) listApprovalRecords(ctx context.Context, normalizedAddress string, chainID *int) ([]ApprovalRecord, error) {
	rows, err := s.repo.listApprovalEvents(ctx, normalizedAddress, chainID)
	if errors.Is(err, ErrPoolUnavailable) {
		return []ApprovalRecord{}, nil
	}
	if err != nil {
		return nil, err
	}

	// Dedupe by token+spender, keeping the first-seen row (which is
	// the latest because of the ORDER BY).
	type approvalKey struct{ token, spender string }
	seen := make(map[approvalKey]struct{}, len(rows))
	out := make([]ApprovalRecord, 0, len(rows))
	for _, row := range rows {
		args := decodeArgs(row.args)
		spender := asAddress(args["spender"])
		if spender == "" {
			continue
		}
		key := approvalKey{token: row.contractAddress, spender: spender}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		allowance := stringifyBigIntLike(firstNonNil(args["value"], args["amount"]))
		ts := row.createdAt
		if row.occurredAt != nil {
			ts = *row.occurredAt
		}
		out = append(out, ApprovalRecord{
			TokenAddress:  row.contractAddress,
			Spender:       spender,
			Allowance:     allowance,
			ChainID:       row.chainID,
			LastUpdatedAt: ts.UTC().Format(time.RFC3339Nano),
			TxHash:        row.txHash,
		})
	}
	return out, nil
}

// GetAlerts composes the alert feed from approvals + churn count. The
// rules mirror security.service.ts: unlimited / large / stale /
// multi-token spender / approval-churn.
func (s *Service) GetAlerts(ctx context.Context, address string, chainID *int) ([]Alert, error) {
	addr, ok := normalizeIfValid(address)
	if !ok {
		return []Alert{}, nil
	}
	approvals, err := s.listApprovalRecords(ctx, addr, chainID)
	if err != nil {
		return nil, err
	}

	alerts := make([]Alert, 0)
	spenderTokens := make(map[string]map[string]struct{})

	for _, a := range approvals {
		if _, ok := spenderTokens[a.Spender]; !ok {
			spenderTokens[a.Spender] = make(map[string]struct{})
		}
		spenderTokens[a.Spender][a.TokenAddress] = struct{}{}

		allowance := parseBigIntLike(a.Allowance)
		ageDays := ageInDays(a.LastUpdatedAt, s.now())

		token := a.TokenAddress
		spender := a.Spender
		txHash := a.TxHash
		revokeLabel := "Build revoke tx"
		revokeAction := ActionRevoke
		reviewLabel := "Review approval"
		reviewAction := ActionReview

		if isUnlimited(allowance) {
			alerts = append(alerts, Alert{
				ID:           fmt.Sprintf("approval:unlimited:%s:%s", a.TokenAddress, a.Spender),
				Severity:     SeverityHigh,
				Category:     CategoryApproval,
				Title:        "Unlimited token approval detected",
				Summary:      fmt.Sprintf("%s can spend this token without an effective cap.", a.Spender),
				ChainID:      a.ChainID,
				OccurredAt:   a.LastUpdatedAt,
				TokenAddress: &token,
				Spender:      &spender,
				TxHash:       &txHash,
				ActionLabel:  &revokeLabel,
				ActionType:   &revokeAction,
			})
		} else if allowance.Cmp(highAllowanceThreshold) >= 0 {
			alerts = append(alerts, Alert{
				ID:           fmt.Sprintf("approval:large:%s:%s", a.TokenAddress, a.Spender),
				Severity:     SeverityMedium,
				Category:     CategoryApproval,
				Title:        "Large token approval still active",
				Summary:      fmt.Sprintf("A large approval remains active for %s. Review whether it is still needed.", a.Spender),
				ChainID:      a.ChainID,
				OccurredAt:   a.LastUpdatedAt,
				TokenAddress: &token,
				Spender:      &spender,
				TxHash:       &txHash,
				ActionLabel:  &revokeLabel,
				ActionType:   &revokeAction,
			})
		}

		if allowance.Sign() > 0 && ageDays >= staleApprovalDays {
			alerts = append(alerts, Alert{
				ID:           fmt.Sprintf("approval:stale:%s:%s", a.TokenAddress, a.Spender),
				Severity:     SeverityLow,
				Category:     CategoryApproval,
				Title:        "Stale approval should be reviewed",
				Summary:      fmt.Sprintf("This approval has been open for %d days without being rotated.", ageDays),
				ChainID:      a.ChainID,
				OccurredAt:   a.LastUpdatedAt,
				TokenAddress: &token,
				Spender:      &spender,
				TxHash:       &txHash,
				ActionLabel:  &reviewLabel,
				ActionType:   &reviewAction,
			})
		}
	}

	for spender, tokens := range spenderTokens {
		if len(tokens) < multiTokenSpenderThreshold {
			continue
		}
		// Find the latest approval (first in slice — already ORDER BY
		// blockNumber DESC) for this spender to source chainId / txHash.
		var latest *ApprovalRecord
		for i := range approvals {
			if approvals[i].Spender == spender {
				latest = &approvals[i]
				break
			}
		}
		if latest == nil {
			continue
		}
		sp := spender
		tx := latest.TxHash
		label := "Review spender"
		action := ActionReview
		alerts = append(alerts, Alert{
			ID:          fmt.Sprintf("spender:spread:%s", spender),
			Severity:    SeverityMedium,
			Category:    CategorySpender,
			Title:       "One spender controls multiple token approvals",
			Summary:     fmt.Sprintf("%s currently has active approvals across %d assets.", spender, len(tokens)),
			ChainID:     latest.ChainID,
			OccurredAt:  latest.LastUpdatedAt,
			Spender:     &sp,
			TxHash:      &tx,
			ActionLabel: &label,
			ActionType:  &action,
		})
	}

	churnCount, err := s.repo.countRecentApprovalEvents(ctx, addr, chainID, s.now().Add(-approvalChurnWindow))
	if err != nil && !errors.Is(err, ErrPoolUnavailable) {
		return nil, err
	}
	if churnCount >= approvalChurnThreshold {
		chain := defaultWeb3ChainID
		if chainID != nil {
			chain = *chainID
		}
		label := "Review activity"
		action := ActionReview
		key := "all"
		if chainID != nil {
			key = strconv.Itoa(*chainID)
		}
		alerts = append(alerts, Alert{
			ID:          fmt.Sprintf("activity:approval-churn:%s:%s", addr, key),
			Severity:    SeverityMedium,
			Category:    CategoryActivity,
			Title:       "Unusual approval churn detected",
			Summary:     fmt.Sprintf("%d approval updates were recorded in the last 24 hours.", churnCount),
			ChainID:     chain,
			OccurredAt:  s.now().UTC().Format(time.RFC3339Nano),
			ActionLabel: &label,
			ActionType:  &action,
		})
	}

	return dedupeAndSortAlerts(alerts), nil
}

// GetConnectedSites lists the sites linked to ownerAddress.
func (s *Service) GetConnectedSites(ctx context.Context, ownerAddress string, chainID *int) ([]ConnectedSite, error) {
	addr, ok := normalizeIfValid(ownerAddress)
	if !ok {
		return []ConnectedSite{}, nil
	}
	rows, err := s.repo.listConnectedSites(ctx, addr, chainID)
	if errors.Is(err, ErrPoolUnavailable) {
		return []ConnectedSite{}, nil
	}
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// --- helpers ---

func normalizeIfValid(addr string) (string, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" || !addressRE.MatchString(addr) {
		return "", false
	}
	return strings.ToLower(addr), true
}

func decodeArgs(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// asAddress returns a normalized 0x-prefixed address if v is a string
// that passes the address regex, else "".
func asAddress(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	if !addressRE.MatchString(s) {
		return ""
	}
	return strings.ToLower(s)
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// stringifyBigIntLike mirrors the JS helper: number → trunc string,
// string → as-is, anything else → "0".
func stringifyBigIntLike(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// JSON numbers decode as float64; truncate toward zero.
		if x >= 0 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	default:
		return "0"
	}
}

func parseBigIntLike(value string) *big.Int {
	// Only allow base-10 digits — matches NestJS's /^\d+$/ guard.
	for _, c := range value {
		if c < '0' || c > '9' {
			return big.NewInt(0)
		}
	}
	if value == "" {
		return big.NewInt(0)
	}
	v, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return big.NewInt(0)
	}
	return v
}

func isUnlimited(v *big.Int) bool {
	return v.Cmp(unlimitedThreshold) >= 0
}

func ageInDays(iso string, now time.Time) int {
	ts, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		// Try without nanos.
		ts, err = time.Parse(time.RFC3339, iso)
		if err != nil {
			return 0
		}
	}
	days := int(now.Sub(ts).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

// dedupeAndSortAlerts removes duplicate IDs (keeping the first
// occurrence) and sorts by occurredAt DESC.
func dedupeAndSortAlerts(alerts []Alert) []Alert {
	seen := make(map[string]struct{}, len(alerts))
	out := make([]Alert, 0, len(alerts))
	for _, a := range alerts {
		if _, dup := seen[a.ID]; dup {
			continue
		}
		seen[a.ID] = struct{}{}
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].OccurredAt > out[j].OccurredAt
	})
	return out
}

// --- router ---

// RevokeTxRequest is the POST body for /revoke/build-tx.
type RevokeTxRequest struct {
	TokenAddress string `json:"tokenAddress"`
	Spender      string `json:"spender"`
	ChainID      *int   `json:"chainId,omitempty"`
}

// RevokeTxResponse is the unsigned approve(spender, 0) transaction.
type RevokeTxResponse struct {
	ChainID int    `json:"chainId"`
	To      string `json:"to"`
	Value   string `json:"value"`
	Data    string `json:"data"`
}

// ErrInvalidAddress is returned when either token or spender fails
// the basic shape check.
var ErrInvalidAddress = errors.New("invalid token or spender address")

// BuildRevokeTx mirrors NestJS securityService.buildRevokeTx. ABI-
// encodes ERC-20 approve(spender, 0) against the token's contract
// address. ChainID defaults to defaultWeb3ChainID (Sepolia).
func (s *Service) BuildRevokeTx(req RevokeTxRequest) (RevokeTxResponse, error) {
	if !addressRE.MatchString(req.TokenAddress) || !addressRE.MatchString(req.Spender) {
		return RevokeTxResponse{}, ErrInvalidAddress
	}
	spenderEncoded, err := abi.EncodeAddress(req.Spender)
	if err != nil {
		return RevokeTxResponse{}, ErrInvalidAddress
	}
	zero, err := abi.EncodeUint256(big.NewInt(0))
	if err != nil {
		return RevokeTxResponse{}, err
	}
	chainID := defaultWeb3ChainID
	if req.ChainID != nil && *req.ChainID > 0 {
		chainID = *req.ChainID
	}
	return RevokeTxResponse{
		ChainID: chainID,
		To:      req.TokenAddress,
		Value:   "0",
		Data:    abi.EncodeCall("approve(address,uint256)", spenderEncoded, zero),
	}, nil
}

// Router mounts /api/security. Mirrors the deployed NestJS
// SecurityController: class-level @Public + @UseGuards(Web3AuthGuard), so
// EVERY route requires a valid web3 token in production. When an auth
// middleware is wired, all routes mount under it; when none is (dev/smoke
// without a JWT secret) the reads + revoke tx-builder stay reachable and the
// connected-sites mutations don't mount (no caller to attribute the write to).
//
// The three data reads (approvals/alerts/connected-sites) are owner-pinned to
// the authenticated wallet via auth.ResolveOwner (no ?address= → JWT wallet;
// mismatch → 403), matching NestJS resolveAuthenticatedOwnerAddress and the
// connected-sites mutations. POST /revoke/build-tx is guarded (under the
// controller's Web3AuthGuard) but NOT owner-pinned — it's pure ABI encoding
// with no owner data, and NestJS resolves no owner on it either.
//
// POST /security/tx-review is NOT defined here: httpx/server.go folds it into
// this same /security mount via txreview.RegisterGuarded(deps.TxReview) (passed
// as extraGuardedRoutes) when the tx-review service is wired, so it lands inside
// the guarded group. The txreview package owns that handler (it owner-pins
// fromAddress the same way). No security route can be cut to Go until the
// runtime mount is proven (go run ./cmd/routes).
func Router(svc *Service, authMiddleware func(http.Handler) http.Handler, extraGuardedRoutes ...func(r chi.Router)) chi.Router {
	r := chi.NewRouter()

	// In the deployed NestJS the whole SecurityController is @Public() +
	// Web3AuthGuard, so EVERY route — the GET reads and the revoke tx-builder
	// included — requires a valid web3 token in production. The live golden run
	// (golden-run-2026-06-08.md) caught Go serving the reads publicly (NestJS 401
	// vs Go 200), an over-exposure of per-owner security data. Reads + the revoke
	// tx-builder therefore mount under the web3 guard when one is wired, and stay
	// public only when no guard is present (dev/smoke without a JWT secret) — the
	// same nil-guard fallback the token/trading modules use.
	registerReads := func(r chi.Router) {
		r.Get("/approvals", makeApprovalsHandler(svc))
		r.Get("/alerts", makeAlertsHandler(svc))
		r.Get("/connected-sites", makeConnectedSitesHandler(svc))
		r.Post("/revoke/build-tx", makeRevokeBuildTxHandler(svc))
	}
	// The connected-sites mutations never mount without a guard — they have no
	// authenticated caller to attribute the write to.
	registerMutations := func(r chi.Router) {
		r.Post("/connected-sites", makeUpsertConnectedSiteHandler(svc))
		r.Delete("/connected-sites/{id}", makeRemoveConnectedSiteHandler(svc))
	}

	if authMiddleware != nil {
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware)
			registerReads(r)
			registerMutations(r)
			for _, register := range extraGuardedRoutes {
				register(r)
			}
		})
	} else {
		// No auth middleware (dev/smoke, no JWT secret) → keep the reads
		// reachable so local runs and the deploy smoke still work; mutations
		// stay unmounted.
		registerReads(r)
		for _, register := range extraGuardedRoutes {
			register(r)
		}
	}
	return r
}

func makeRevokeBuildTxHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body RevokeTxRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		out, err := svc.BuildRevokeTx(body)
		if errors.Is(err, ErrInvalidAddress) {
			http.Error(w, "Invalid token or spender address", http.StatusBadRequest)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "security revoke build-tx failed", "err", err)
			http.Error(w, "failed to build transaction", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeApprovalsHandler owner-pins the read to the authenticated wallet, mirroring
// NestJS resolveAuthenticatedOwnerAddress(authenticated, ?address=, 'address'):
// no ?address= → the JWT wallet; a mismatched ?address= → 403; no authenticated
// address → 401.
func makeApprovalsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), r.URL.Query().Get("address"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		chainID := parseChainID(r.URL.Query().Get("chainId"))
		out, err := svc.GetApprovals(r.Context(), owner, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "security approvals failed", "err", err)
			http.Error(w, "failed to load approvals", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeAlertsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), r.URL.Query().Get("address"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		chainID := parseChainID(r.URL.Query().Get("chainId"))
		out, err := svc.GetAlerts(r.Context(), owner, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "security alerts failed", "err", err)
			http.Error(w, "failed to load alerts", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeConnectedSitesHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// NestJS owner-pins via @Query('address'); the FE also passes
		// `ownerAddress` on some connected-sites calls — accept either as the
		// requested owner, then owner-pin it against the authenticated wallet.
		requested := q.Get("address")
		if requested == "" {
			requested = q.Get("ownerAddress")
		}
		owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), requested)
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		chainID := parseChainID(q.Get("chainId"))
		out, err := svc.GetConnectedSites(r.Context(), owner, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "security connected-sites failed", "err", err)
			http.Error(w, "failed to load connected sites", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func parseChainID(raw string) *int {
	if raw == "" {
		return nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	return &v
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("security response encode failed", "err", err)
	}
}
