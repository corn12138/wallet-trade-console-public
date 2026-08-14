package txreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"regexp"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
)

// SwapPreparer is the swap-side contract the orchestrator needs.
// Implemented by an adapter over swap.Service so this package doesn't
// import swap (which would create a cycle via httpx).
type SwapPreparer interface {
	// GetQuote returns the live or fallback quote for a swap pair.
	GetQuote(req SwapQuoteRequest) (SwapQuote, error)
	// BuildSwapTx encodes swapExactTokensForTokens(...)
	BuildSwapTx(req SwapBuildRequest) (TxShape, error)
	// BuildApproveTx encodes approve(spender, amount). The custom
	// spender param routes through to the existing swap.BuildApproveTx.
	BuildApproveTx(req SwapApproveRequest) (TxShape, error)
}

// SwapQuoteRequest mirrors swap.QuoteRequest minus the chi-binding layer.
type SwapQuoteRequest struct {
	ChainID          int
	TokenIn          string
	TokenOut         string
	AmountIn         string
	TokenInDecimals  int
	TokenOutDecimals int
	SlippageBps      *int
}

// SwapQuote carries the fields tx-review consumes from a quote.
type SwapQuote struct {
	RouterAddress   string // "" when fallback / no Router
	MinimumReceived string
	SlippageBps     int
	Warnings        []string
}

// SwapBuildRequest mirrors swap.BuildSwapRequest in the form the
// adapter consumes.
type SwapBuildRequest struct {
	ChainID          int
	TokenIn          string
	TokenOut         string
	AmountIn         string
	AmountOutMin     string
	TokenInDecimals  int
	TokenOutDecimals int
	Recipient        string
	DeadlineSeconds  *int
}

// SwapApproveRequest mirrors swap.BuildApproveRequest fields.
type SwapApproveRequest struct {
	ChainID       int
	TokenAddress  string
	Amount        string
	TokenDecimals int
	Spender       string // "" → use router default
}

// EarnPreparer is the earn-side contract.
type EarnPreparer interface {
	BuildDepositTx(ctx context.Context, productID, amount string, tokenDecimals int) (TxShape, error)
	BuildWithdrawTx(ctx context.Context, productID, amount string, tokenDecimals int) (TxShape, error)
	FindProductTokenAddress(ctx context.Context, productID string, chainID int) (string, error)
}

// TokenIntelLookup looks up token registry rows by chain+address.
// Returns (status, isOfficial, tags) so the orchestrator can compute
// the unsupported / warning flags.
type TokenIntelLookup interface {
	GetTokenIntel(ctx context.Context, chainID int, addresses []string) (map[string]TokenIntelRow, error)
}

// TokenIntelRow is the minimal projection from the tokens table.
type TokenIntelRow struct {
	Address    string
	Status     string
	IsOfficial bool
	Tags       []string
}

// ConnectedSiteLookup looks up a connected-site entry by owner + origin.
type ConnectedSiteLookup interface {
	FindConnectedSite(ctx context.Context, ownerAddress string, chainID int, origin string) (*ConnectedSiteDetail, error)
}

// ConnectedSiteDetail extends ConnectedSiteSummary with the
// permissions slice (needed for recommended actions).
type ConnectedSiteDetail struct {
	ID          string
	Origin      string
	SiteName    string
	RiskLevel   string
	Permissions []string
}

// KnownSpendersLookup returns the deployed contract addresses on a
// chain (used for the spender allow-list rule).
type KnownSpendersLookup interface {
	KnownSpenders(chainID int) []string
}

// Service is the orchestrator. Each Lookup / Preparer is optional —
// nil collapses the corresponding code path into a sensible default
// (e.g., no known-spenders means every spender flags as "unknown").
type Service struct {
	rpc      RPCCaller
	swapPrep SwapPreparer
	earnPrep EarnPreparer
	tokens   TokenIntelLookup
	sites    ConnectedSiteLookup
	known    KnownSpendersLookup
	bridge   BridgeStateReader
}

// NewService builds the orchestrator. Nil deps are tolerated.
func NewService(
	rpc RPCCaller,
	swap SwapPreparer,
	earn EarnPreparer,
	tokens TokenIntelLookup,
	sites ConnectedSiteLookup,
	known KnownSpendersLookup,
	bridgeReader BridgeStateReader,
) *Service {
	return &Service{rpc: rpc, swapPrep: swap, earnPrep: earn, tokens: tokens, sites: sites, known: known, bridge: bridgeReader}
}

const DefaultChainID = 11155111

// ReviewTransaction is the public entry point. Mirrors NestJS
// TxReviewService.reviewTransaction.
func (s *Service) ReviewTransaction(ctx context.Context, in ReviewInput) (Result, error) {
	if s == nil {
		return Result{}, errors.New("txreview: service nil")
	}
	op := in.OperationType
	if op == "" {
		op = OpCustom
	}
	chainID := DefaultChainID
	if in.ChainID != nil && *in.ChainID > 0 {
		chainID = *in.ChainID
	}
	fromAddress, err := ValidateAddress(in.FromAddress, "from address")
	if err != nil {
		return Result{}, err
	}

	ctxBase := ReviewContext{
		OperationType: op,
		ChainID:       chainID,
		FromAddress:   fromAddress,
	}
	if in.SiteOrigin != nil {
		origin, err := NormalizeOptionalOrigin(*in.SiteOrigin)
		if err != nil {
			return Result{}, err
		}
		ctxBase.SiteOrigin = origin
	}
	if in.NativeBalanceWei != nil {
		ctxBase.NativeBalanceHex = NormalizeBigIntString(*in.NativeBalanceWei)
	}

	prepared, err := s.prepare(ctx, in, ctxBase)
	if err != nil {
		return Result{}, err
	}

	var site *ConnectedSiteDetail
	if prepared.SiteOrigin != "" && s.sites != nil {
		site, _ = s.sites.FindConnectedSite(ctx, prepared.FromAddress, chainID, prepared.SiteOrigin)
	}

	sim := Simulate(ctx, s.rpc, prepared.FromAddress, prepared.GeneratedTx, prepared.NativeBalanceHex)

	allowanceChange := s.allowanceChange(ctx, prepared)

	tokenIntel := s.lookupTokenIntel(ctx, chainID, prepared.TokenAddresses, prepared.TokenWarnings)

	rulesIn := RulesInput{
		OperationType:    op,
		Spender:          prepared.Spender,
		KnownSpenders:    s.knownSpenders(chainID),
		IsSupportedToken: tokenIntel.isSupportedToken,
		TokenWarnings:    tokenIntel.warnings,
		SlippageBps:      prepared.SlippageBps,
		QuoteExpiresAt:   prepared.QuoteExpiresAt,
		NativeBalanceWei: sim.NativeBalanceWei,
		EstimatedFeeWei:  sim.EstimatedFeeWei,
		AllowanceRequest: prepared.AllowanceRequest,
		SimulationError:  ptrToString(sim.ErrorMessage),
	}
	checks := EvaluateRules(rulesIn)

	// Bridge-deposit reviews carry deterministic bridge checks on top of the
	// generic rules — appended (not replacing) so gas/token/spender rules
	// still apply to the deposit transaction itself.
	if prepared.Bridge != nil {
		checks = append(checks, EvaluateBridgeRules(*prepared.Bridge)...)
	}

	if site != nil && riskLevelRE.MatchString(site.RiskLevel) {
		checks = append(checks, Check{
			ID:       "connected-site-risk",
			Severity: SeverityMedium,
			Status:   StatusWarn,
			Title:    "Connected site is flagged",
			Summary:  fmt.Sprintf("%s is marked as %s in the connected-site ledger.", site.SiteName, site.RiskLevel),
		})
	}

	checks = DedupeChecks(checks)
	status := DeriveReviewStatus(checks, sim.Mode)
	connectedSummary := connectedSiteSummary(site)
	permissions := []string{}
	if site != nil {
		permissions = site.Permissions
	}

	return Result{
		ReviewStatus:       status,
		OperationType:      op,
		ChainID:            chainID,
		RiskScore:          ComputeRiskScore(checks, connectedSummary),
		Simulation:         sim,
		GeneratedTx:        prepared.GeneratedTx,
		AllowanceChange:    allowanceChange,
		ConnectedSite:      connectedSummary,
		RecommendedActions: BuildRecommendedActions(checks, connectedSummary, sim.Mode, permissions),
		Checks:             checks,
	}, nil
}

// prepare routes to the per-operation handler. Returns ErrInvalidInput
// (BadRequestException-equivalent) on missing required fields.
func (s *Service) prepare(ctx context.Context, in ReviewInput, base ReviewContext) (ReviewContext, error) {
	switch base.OperationType {
	case OpCustom:
		return s.prepareCustom(in, base)
	case OpApprove:
		return s.prepareApprove(in, base)
	case OpRevokeApproval:
		return s.prepareRevoke(in, base)
	case OpSwap:
		return s.prepareSwap(in, base)
	case OpEarnDeposit:
		return s.prepareEarnDeposit(ctx, in, base)
	case OpEarnWithdraw:
		return s.prepareEarnWithdraw(ctx, in, base)
	case OpBridgeDeposit:
		return s.prepareBridgeDeposit(ctx, in, base)
	default:
		// Unknown op type → treat as custom (matches NestJS default).
		base.OperationType = OpCustom
		return s.prepareCustom(in, base)
	}
}

func (s *Service) prepareCustom(in ReviewInput, base ReviewContext) (ReviewContext, error) {
	if in.Tx == nil || in.Tx.To == "" || in.Tx.Data == "" {
		return ReviewContext{}, fmt.Errorf("%w: custom tx review requires tx.to and tx.data", ErrInvalidInput)
	}
	to, err := ValidateAddress(in.Tx.To, "tx.to")
	if err != nil {
		return ReviewContext{}, err
	}
	value := NormalizeBigIntString(in.Tx.Value)
	if value == "" {
		value = "0"
	}
	if in.QuoteExpiresAt != nil {
		base.QuoteExpiresAt = *in.QuoteExpiresAt
	}
	base.SlippageBps = in.SlippageBps
	base.Spender = to
	base.GeneratedTx = TxShape{ChainID: base.ChainID, To: to, Value: value, Data: in.Tx.Data}
	return base, nil
}

func (s *Service) prepareApprove(in ReviewInput, base ReviewContext) (ReviewContext, error) {
	if in.TokenAddress == nil || in.Amount == nil || in.TokenDecimals == nil {
		return ReviewContext{}, fmt.Errorf("%w: approve requires tokenAddress, amount, tokenDecimals", ErrInvalidInput)
	}
	tokenAddress, err := ValidateAddress(*in.TokenAddress, "token address")
	if err != nil {
		return ReviewContext{}, err
	}
	spender := ""
	if in.Spender != nil && *in.Spender != "" {
		spender, err = ValidateAddress(*in.Spender, "spender")
		if err != nil {
			return ReviewContext{}, err
		}
	}
	if s.swapPrep == nil {
		return ReviewContext{}, errors.New("txreview: swap preparer not configured")
	}
	tx, err := s.swapPrep.BuildApproveTx(SwapApproveRequest{
		ChainID:       base.ChainID,
		TokenAddress:  tokenAddress,
		Amount:        *in.Amount,
		TokenDecimals: *in.TokenDecimals,
		Spender:       spender,
	})
	if err != nil {
		return ReviewContext{}, err
	}
	// The tx.to is the token contract; the actual spender is encoded
	// in tx.data. Recover it from the request (spender override) or
	// from the tx we just built — for known-spender lookup, we use
	// the spender argument when provided.
	resolvedSpender := spender
	if resolvedSpender == "" {
		// best effort: try to extract last 20 bytes of the first
		// argument slot in data (offset 0x4 + 12 = 0x10 for left-padded
		// address). Parsing is "tx.data" = "0x" + selector(4) +
		// address(32 left-padded) + amount(32).
		if extracted := extractAddressFromApproveData(tx.Data); extracted != "" {
			resolvedSpender = extracted
		}
	}
	requested, _ := abi.ParseUnits(*in.Amount, *in.TokenDecimals)
	base.Spender = resolvedSpender
	base.TokenAddresses = []string{tokenAddress}
	base.AllowanceRequest = &AllowanceRequest{
		TokenAddress:       tokenAddress,
		Spender:            resolvedSpender,
		RequestedAllowance: requested,
		IsUnlimited:        IsUnlimitedAllowance(requested),
	}
	base.GeneratedTx = tx
	if base.GeneratedTx.ChainID == 0 {
		base.GeneratedTx.ChainID = base.ChainID
	}
	return base, nil
}

func (s *Service) prepareRevoke(in ReviewInput, base ReviewContext) (ReviewContext, error) {
	if in.TokenAddress == nil || in.Spender == nil {
		return ReviewContext{}, fmt.Errorf("%w: revoke-approval requires tokenAddress + spender", ErrInvalidInput)
	}
	tokenAddress, err := ValidateAddress(*in.TokenAddress, "token address")
	if err != nil {
		return ReviewContext{}, err
	}
	spender, err := ValidateAddress(*in.Spender, "spender")
	if err != nil {
		return ReviewContext{}, err
	}
	// Encode approve(spender, 0) inline — equivalent to the existing
	// security.BuildRevokeTx but keeping this layer self-contained.
	spenderArg, _ := abi.EncodeAddress(spender)
	zeroArg, _ := abi.EncodeUint256(big.NewInt(0))
	data := abi.EncodeCall("approve(address,uint256)", spenderArg, zeroArg)
	base.Spender = spender
	base.TokenAddresses = []string{tokenAddress}
	base.AllowanceRequest = &AllowanceRequest{
		TokenAddress:       tokenAddress,
		Spender:            spender,
		RequestedAllowance: big.NewInt(0),
		IsUnlimited:        false,
	}
	base.GeneratedTx = TxShape{ChainID: base.ChainID, To: tokenAddress, Value: "0", Data: data}
	return base, nil
}

func (s *Service) prepareSwap(in ReviewInput, base ReviewContext) (ReviewContext, error) {
	if s.swapPrep == nil {
		return ReviewContext{}, errors.New("txreview: swap preparer not configured")
	}
	if in.TokenIn == nil || in.TokenOut == nil || in.Amount == nil || in.TokenInDecimals == nil || in.TokenOutDecimals == nil {
		return ReviewContext{}, fmt.Errorf("%w: swap requires tokenIn/tokenOut/amount + decimals", ErrInvalidInput)
	}
	tokenIn, err := ValidateAddress(*in.TokenIn, "tokenIn")
	if err != nil {
		return ReviewContext{}, err
	}
	tokenOut, err := ValidateAddress(*in.TokenOut, "tokenOut")
	if err != nil {
		return ReviewContext{}, err
	}
	quote, err := s.swapPrep.GetQuote(SwapQuoteRequest{
		ChainID:          base.ChainID,
		TokenIn:          tokenIn,
		TokenOut:         tokenOut,
		AmountIn:         *in.Amount,
		TokenInDecimals:  *in.TokenInDecimals,
		TokenOutDecimals: *in.TokenOutDecimals,
		SlippageBps:      in.SlippageBps,
	})
	if err != nil {
		return ReviewContext{}, err
	}
	recipient := base.FromAddress
	if in.Recipient != nil && *in.Recipient != "" {
		recipient, err = ValidateAddress(*in.Recipient, "recipient")
		if err != nil {
			return ReviewContext{}, err
		}
	}
	amountOutMin := quote.MinimumReceived
	if in.AmountOutMin != nil && *in.AmountOutMin != "" {
		amountOutMin = *in.AmountOutMin
	}
	tx, err := s.swapPrep.BuildSwapTx(SwapBuildRequest{
		ChainID:          base.ChainID,
		TokenIn:          tokenIn,
		TokenOut:         tokenOut,
		AmountIn:         *in.Amount,
		AmountOutMin:     amountOutMin,
		TokenInDecimals:  *in.TokenInDecimals,
		TokenOutDecimals: *in.TokenOutDecimals,
		Recipient:        recipient,
		DeadlineSeconds:  in.DeadlineSeconds,
	})
	if err != nil {
		return ReviewContext{}, err
	}
	bps := quote.SlippageBps
	base.Spender = NormalizeOptionalAddressLiteral(quote.RouterAddress)
	if in.QuoteExpiresAt != nil {
		base.QuoteExpiresAt = *in.QuoteExpiresAt
	}
	base.SlippageBps = &bps
	base.TokenAddresses = []string{tokenIn, tokenOut}
	base.TokenWarnings = quote.Warnings
	base.GeneratedTx = tx
	if base.GeneratedTx.ChainID == 0 {
		base.GeneratedTx.ChainID = base.ChainID
	}
	return base, nil
}

func (s *Service) prepareEarnDeposit(ctx context.Context, in ReviewInput, base ReviewContext) (ReviewContext, error) {
	return s.prepareEarn(ctx, in, base, true)
}

func (s *Service) prepareEarnWithdraw(ctx context.Context, in ReviewInput, base ReviewContext) (ReviewContext, error) {
	return s.prepareEarn(ctx, in, base, false)
}

func (s *Service) prepareEarn(ctx context.Context, in ReviewInput, base ReviewContext, isDeposit bool) (ReviewContext, error) {
	if s.earnPrep == nil {
		return ReviewContext{}, errors.New("txreview: earn preparer not configured")
	}
	if in.ProductID == nil || in.Amount == nil || in.TokenDecimals == nil {
		return ReviewContext{}, fmt.Errorf("%w: earn op requires productId + amount + tokenDecimals", ErrInvalidInput)
	}
	var (
		tx  TxShape
		err error
	)
	if isDeposit {
		tx, err = s.earnPrep.BuildDepositTx(ctx, *in.ProductID, *in.Amount, *in.TokenDecimals)
	} else {
		tx, err = s.earnPrep.BuildWithdrawTx(ctx, *in.ProductID, *in.Amount, *in.TokenDecimals)
	}
	if err != nil {
		return ReviewContext{}, err
	}
	tokenAddress, _ := s.earnPrep.FindProductTokenAddress(ctx, *in.ProductID, tx.ChainID)
	tokens := []string{}
	if tokenAddress != "" {
		tokens = []string{tokenAddress}
	}
	base.Spender = tx.To
	base.TokenAddresses = tokens
	base.GeneratedTx = tx
	if base.GeneratedTx.ChainID == 0 {
		base.GeneratedTx.ChainID = base.ChainID
	}
	return base, nil
}

func (s *Service) allowanceChange(ctx context.Context, prepared ReviewContext) *AllowanceChange {
	req := prepared.AllowanceRequest
	if req == nil {
		return nil
	}
	current := FetchAllowance(ctx, s.rpc, req.TokenAddress, prepared.FromAddress, req.Spender)
	out := &AllowanceChange{
		TokenAddress: req.TokenAddress,
		Spender:      req.Spender,
		IsUnlimited:  req.IsUnlimited,
	}
	if current != nil {
		s := current.String()
		out.CurrentAllowance = &s
	}
	if req.RequestedAllowance != nil {
		s := req.RequestedAllowance.String()
		out.RequestedAllowance = &s
	}
	return out
}

type tokenIntelOutcome struct {
	isSupportedToken *bool
	warnings         []string
}

var tokenWarningTagRE = regexp.MustCompile(`(?i)spam|scam|phishing|suspicious|airdrop`)

func (s *Service) lookupTokenIntel(ctx context.Context, chainID int, addresses, seed []string) tokenIntelOutcome {
	warnings := append([]string{}, seed...)
	if len(addresses) == 0 {
		return tokenIntelOutcome{warnings: NormalizeTokenWarnings(warnings)}
	}
	addr := make([]string, 0, len(addresses))
	seen := map[string]bool{}
	for _, a := range addresses {
		la := strings.ToLower(a)
		if seen[la] {
			continue
		}
		seen[la] = true
		addr = append(addr, la)
	}
	if s.tokens == nil {
		return tokenIntelOutcome{warnings: NormalizeTokenWarnings(warnings)}
	}
	rows, err := s.tokens.GetTokenIntel(ctx, chainID, addr)
	if err != nil {
		slog.WarnContext(ctx, "txreview: token intel lookup failed", "err", err)
		return tokenIntelOutcome{warnings: NormalizeTokenWarnings(warnings)}
	}
	unsupported := false
	for _, a := range addr {
		row, ok := rows[a]
		if !ok {
			warnings = append(warnings, "Token "+TruncateAddress(a)+" is outside the tracked registry.")
			continue
		}
		if !row.IsOfficial && row.Status != "LAUNCHED" {
			unsupported = true
		}
		for _, tag := range row.Tags {
			if tokenWarningTagRE.MatchString(tag) {
				warnings = append(warnings, "Token "+TruncateAddress(a)+" carries "+tag+" tag.")
			}
		}
	}
	out := tokenIntelOutcome{warnings: NormalizeTokenWarnings(warnings)}
	supported := !unsupported
	out.isSupportedToken = &supported
	return out
}

func (s *Service) knownSpenders(chainID int) []string {
	if s.known == nil {
		return nil
	}
	out := s.known.KnownSpenders(chainID)
	for i, v := range out {
		out[i] = strings.ToLower(v)
	}
	return out
}

func connectedSiteSummary(site *ConnectedSiteDetail) *ConnectedSiteSummary {
	if site == nil {
		return nil
	}
	return &ConnectedSiteSummary{
		ID:        site.ID,
		Origin:    site.Origin,
		SiteName:  site.SiteName,
		RiskLevel: site.RiskLevel,
	}
}

func ptrToString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// extractAddressFromApproveData parses the first arg slot of an
// `approve(address,uint256)` encoded data string. Returns "" on any
// shape mismatch — best-effort only.
func extractAddressFromApproveData(data string) string {
	d := strings.TrimPrefix(data, "0x")
	// selector(4) + arg1 head(32) + arg2 head(32) = 68 bytes = 136 hex.
	if len(d) < 8+64 {
		return ""
	}
	// First arg occupies bytes 4..36; address is the right-most 20 bytes.
	arg := d[8 : 8+64]
	addr := "0x" + arg[24:]
	if !addressRE.MatchString(addr) {
		return ""
	}
	return strings.ToLower(addr)
}

// Router mounts POST /tx-review. The authMiddleware (when non-nil)
// wraps the route — same pattern as the security and wallets packages.
func Router(svc *Service, authMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	if authMiddleware != nil {
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware)
			r.Post("/tx-review", makeReviewHandler(svc))
		})
	} else {
		r.Post("/tx-review", makeReviewHandler(svc))
	}
	return r
}

// RegisterGuarded is the registrar form for httpx to fold the
// tx-review route into the existing /security mount (chi rejects two
// Mount() calls on the same prefix). Pass it to security.Router as
// the extraGuardedRoutes argument.
func RegisterGuarded(svc *Service) func(r chi.Router) {
	return func(r chi.Router) {
		r.Post("/tx-review", makeReviewHandler(svc))
	}
}

func makeReviewHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in ReviewInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		// Owner-pin fromAddress to the authenticated wallet, mirroring NestJS
		// resolveAuthenticatedOwnerAddress(authenticated, body.fromAddress,
		// 'fromAddress'): no fromAddress → the JWT wallet; a mismatched one →
		// 403; no authenticated address → 401. A caller can't review a tx as
		// another wallet.
		owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), in.FromAddress)
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		in.FromAddress = owner
		out, err := svc.ReviewTransaction(r.Context(), in)
		if errors.Is(err, ErrInvalidInput) {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "txreview failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statusCode": status,
		"message":    message,
	})
}

// mapResolveOwnerErr maps an auth.ResolveOwner sentinel to the matching status
// (401 missing auth / 403 owner mismatch / 400 invalid requested address),
// using the same {statusCode,message} JSON body as the rest of tx-review.
func mapResolveOwnerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrMissingAuthenticated):
		writeJSONError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, auth.ErrOwnerMismatch):
		writeJSONError(w, http.StatusForbidden, err.Error())
	default:
		writeJSONError(w, http.StatusBadRequest, err.Error())
	}
}
