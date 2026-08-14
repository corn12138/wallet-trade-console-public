// Package bridge serves REAL cross-chain transfer routes and status, backed by
// the deployed BridgeGateway contracts (contracts-foundry/src/bridge) and the
// bridge_transfers projection.
//
// This replaced a simulated route engine that invented amounts, fees and ETAs
// and a status endpoint that returned a constant "35% in progress" fixture.
// Nothing here is invented:
//
//   - a route exists only when the SOURCE gateway's `route()` says so on-chain;
//   - it is executable only when the route is supported, the gateway is not
//     paused, a counterpart gateway is deployed, and the DESTINATION gateway
//     actually holds enough liquidity to deliver;
//   - status comes from bridge_transfers rows, which are written only from
//     observed on-chain events — never optimistically by the relayer;
//   - the API BUILDS deposit calldata but never signs or submits it, so custody
//     stays with the user's wallet.
//
// TRUST MODEL. This is a trusted-relayer bridge, not a proof-based one. Every
// route response carries `trustModel` and the relayer/admin addresses so the UI
// can state plainly what the user is trusting. See BridgeGateway.sol.
package bridge

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// TrustModel is a stable machine-readable constant. It is not a disclaimer the
// UI may drop: a client that cannot render it should not offer execution.
const TrustModel = "trusted-relayer"

// RoutesRequest is the POST /routes body. Amount is in BASE UNITS (integer
// string) so no float ever touches a transfer amount.
type RoutesRequest struct {
	FromChainID int    `json:"fromChainId"`
	ToChainID   int    `json:"toChainId"`
	TokenSymbol string `json:"tokenSymbol"`
	SrcToken    string `json:"srcToken"`
	Amount      string `json:"amount"`
}

// Route is one real, on-chain-verified route.
type Route struct {
	ID          string `json:"id"`
	FromChainID int    `json:"fromChainId"`
	ToChainID   int    `json:"toChainId"`
	TokenSymbol string `json:"tokenSymbol"`
	SrcToken    string `json:"srcToken"`
	DstToken    string `json:"dstToken"`
	SrcGateway  string `json:"srcGateway"`
	DstGateway  string `json:"dstGateway"`

	AmountIn string `json:"amountIn"`
	// AmountOut equals AmountIn: the gateway is lock-and-release and takes no
	// protocol fee. Gas on both chains is paid separately (by the user on the
	// source, by the relayer on the destination) and is deliberately NOT
	// folded into a made-up USD number here.
	AmountOut string `json:"amountOut"`
	MinAmount string `json:"minAmount"`

	Executable bool `json:"executable"`
	// Blockers lists every reason execution is unavailable. Empty iff Executable.
	Blockers []string `json:"blockers"`

	// DstLiquidity is the destination gateway's balance of DstToken, or "" when
	// it could not be read (no RPC for that chain) — never a guessed 0.
	DstLiquidity string `json:"dstLiquidity"`
	Paused       bool   `json:"paused"`
	TrustModel   string `json:"trustModel"`
}

// RoutesResponse is the POST /routes envelope.
type RoutesResponse struct {
	RequestedAt   string  `json:"requestedAt"`
	ExecutionMode string  `json:"executionMode"` // "live" | "unavailable"
	Executable    bool    `json:"executable"`
	TrustModel    string  `json:"trustModel"`
	Chains        []int   `json:"chains"` // chains with a deployed gateway
	Routes        []Route `json:"routes"`
}

// StatusResponse is GET /status/{transferId}.
type StatusResponse struct {
	TransferID  string  `json:"transferId"`
	Status      string  `json:"status"`
	SrcChainID  int     `json:"srcChainId"`
	DstChainID  int     `json:"dstChainId"`
	Sender      string  `json:"sender"`
	Recipient   string  `json:"recipient"`
	SrcToken    string  `json:"srcToken"`
	DstToken    string  `json:"dstToken"`
	Amount      string  `json:"amount"`
	DepositTx   string  `json:"depositTxHash"`
	DepositedAt string  `json:"depositedAt"`
	FulfillTx   string  `json:"fulfillTxHash,omitempty"`
	FulfilledAt *string `json:"fulfilledAt"`
	RefundTx    string  `json:"refundTxHash,omitempty"`
	RefundedAt  *string `json:"refundedAt"`
	// LastError is surfaced verbatim so a stuck transfer explains itself
	// instead of showing an indefinite "pending".
	LastError string `json:"lastError,omitempty"`
	Attempts  int    `json:"attempts"`
	UpdatedAt string `json:"updatedAt"`
}

// BuildDepositRequest is the POST /build-deposit body.
type BuildDepositRequest struct {
	FromChainID int    `json:"fromChainId"`
	ToChainID   int    `json:"toChainId"`
	SrcToken    string `json:"srcToken"`
	Amount      string `json:"amount"`
	Recipient   string `json:"recipient"`
}

// BuildDepositResponse is unsigned calldata for the user's wallet.
type BuildDepositResponse struct {
	ChainID int    `json:"chainId"`
	To      string `json:"to"`
	Data    string `json:"data"`
	Value   string `json:"value"`
	// ApprovalTarget is the gateway the user must ERC20-approve first.
	ApprovalTarget string `json:"approvalTarget"`
	TrustModel     string `json:"trustModel"`
}

var (
	// ErrInvalidAmount rejects a non-positive / non-integer base-unit amount.
	ErrInvalidAmount = errors.New("amount must be a positive integer in base units")
	// ErrRouteUnavailable means no executable route exists for the request.
	ErrRouteUnavailable = errors.New("no executable bridge route for this pair")
	// ErrNoTransfer means no transfer row matches the id.
	ErrNoTransfer = errors.New("bridge: no transfer exists for this id")
)

// Service wires the on-chain registry and the transfer store.
type Service struct {
	registry *Registry
	store    *Store
	now      func() time.Time
}

// NewService builds the service. A nil registry or store degrades to an honest
// "unavailable" — never to a fabricated route or an empty-looking status.
func NewService(registry *Registry, store *Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{registry: registry, store: store, now: now}
}

// GetRoutes resolves the requested pair against on-chain state.
func (s *Service) GetRoutes(ctx context.Context, req RoutesRequest) (RoutesResponse, error) {
	amount, ok := ParseAmount(req.Amount)
	if !ok {
		return RoutesResponse{}, ErrInvalidAmount
	}

	out := RoutesResponse{
		RequestedAt:   s.now().UTC().Format(time.RFC3339Nano),
		ExecutionMode: "unavailable",
		TrustModel:    TrustModel,
		Chains:        s.registry.Chains(),
		Routes:        []Route{},
	}
	if out.Chains == nil {
		out.Chains = []int{}
	}

	route, blockers := s.resolveRoute(ctx, req.FromChainID, req.SrcToken, req.ToChainID, amount)
	route.TokenSymbol = req.TokenSymbol
	route.AmountIn = req.Amount
	route.AmountOut = req.Amount // lock-and-release: no protocol fee
	route.Blockers = blockers
	route.Executable = len(blockers) == 0
	route.TrustModel = TrustModel
	route.ID = buildRouteID(req.FromChainID, req.ToChainID, req.SrcToken)

	out.Routes = append(out.Routes, route)
	out.Executable = route.Executable
	if route.Executable {
		out.ExecutionMode = "live"
	}
	return out, nil
}

// resolveRoute reads the chain and returns the route plus every blocker found.
// Blockers accumulate instead of short-circuiting so the UI can show the whole
// truth at once rather than one problem per retry.
func (s *Service) resolveRoute(ctx context.Context, srcChainID int, srcToken string, dstChainID int, amount *big.Int) (Route, []string) {
	r := Route{FromChainID: srcChainID, ToChainID: dstChainID, SrcToken: strings.ToLower(srcToken)}
	blockers := []string{}

	if srcChainID == dstChainID {
		return r, append(blockers, "SAME_CHAIN")
	}
	if strings.TrimSpace(srcToken) == "" {
		return r, append(blockers, "SRC_TOKEN_REQUIRED")
	}

	srcGW, hasSrc := s.registry.Gateway(srcChainID)
	if !hasSrc {
		blockers = append(blockers, "NO_SOURCE_GATEWAY")
	} else {
		r.SrcGateway = srcGW.Address
	}
	dstGW, hasDst := s.registry.Gateway(dstChainID)
	if !hasDst {
		// This is the expected state until a counterpart chain is deployed.
		blockers = append(blockers, "NO_DESTINATION_GATEWAY")
	} else {
		r.DstGateway = dstGW.Address
	}
	if !hasSrc {
		return r, blockers
	}

	info, err := s.registry.Route(ctx, srcChainID, srcToken, dstChainID)
	if err != nil {
		if errors.Is(err, ErrNoRPC) {
			return r, append(blockers, "SOURCE_RPC_UNAVAILABLE")
		}
		return r, append(blockers, "ROUTE_READ_FAILED")
	}

	r.DstToken = info.DstToken
	r.Paused = info.Paused
	if info.MinAmount != nil {
		r.MinAmount = info.MinAmount.String()
	}

	if !info.Supported {
		blockers = append(blockers, "ROUTE_NOT_CONFIGURED")
	}
	if info.Paused {
		blockers = append(blockers, "GATEWAY_PAUSED")
	}
	if info.MinAmount != nil && amount.Cmp(info.MinAmount) < 0 {
		blockers = append(blockers, "BELOW_MIN_AMOUNT")
	}

	// Liquidity is only meaningful once the route names a destination token.
	if hasDst && info.Supported && info.DstToken != "" {
		liq, liqErr := s.registry.Liquidity(ctx, dstChainID, info.DstToken)
		switch {
		case liqErr != nil:
			// Unknown, not zero — say so rather than blocking on a guess.
			blockers = append(blockers, "DESTINATION_LIQUIDITY_UNKNOWN")
		default:
			r.DstLiquidity = liq.String()
			if liq.Cmp(amount) < 0 {
				blockers = append(blockers, "INSUFFICIENT_DESTINATION_LIQUIDITY")
			}
		}
	}
	return r, blockers
}

// BuildDeposit returns unsigned calldata for an executable route. It refuses to
// build for a route that is not executable, so the UI cannot hand a user a
// transaction that would revert or strand funds.
func (s *Service) BuildDeposit(ctx context.Context, req BuildDepositRequest) (BuildDepositResponse, error) {
	amount, ok := ParseAmount(req.Amount)
	if !ok {
		return BuildDepositResponse{}, ErrInvalidAmount
	}
	if !addressRE.MatchString(strings.TrimSpace(req.Recipient)) {
		return BuildDepositResponse{}, errors.New("recipient must be a 0x-prefixed address")
	}

	_, blockers := s.resolveRoute(ctx, req.FromChainID, req.SrcToken, req.ToChainID, amount)
	if len(blockers) > 0 {
		return BuildDepositResponse{}, ErrRouteUnavailable
	}
	gw, ok := s.registry.Gateway(req.FromChainID)
	if !ok {
		return BuildDepositResponse{}, ErrRouteUnavailable
	}

	return BuildDepositResponse{
		ChainID:        req.FromChainID,
		To:             gw.Address,
		Data:           BuildDepositCalldata(req.SrcToken, amount, req.ToChainID, req.Recipient),
		Value:          "0",
		ApprovalTarget: gw.Address,
		TrustModel:     TrustModel,
	}, nil
}

// GetStatus reads a real transfer row.
func (s *Service) GetStatus(ctx context.Context, transferID string) (StatusResponse, error) {
	if s.store == nil || !s.store.Available() {
		return StatusResponse{}, ErrStoreUnavailable
	}
	t, err := s.store.FindByTransferID(ctx, 0, transferID)
	if errors.Is(err, ErrTransferNotFound) {
		return StatusResponse{}, ErrNoTransfer
	}
	if err != nil {
		return StatusResponse{}, err
	}
	return toStatus(t), nil
}

// ListTransfers returns a wallet's transfers.
func (s *Service) ListTransfers(ctx context.Context, address string, limit int) ([]StatusResponse, error) {
	if s.store == nil || !s.store.Available() {
		return nil, ErrStoreUnavailable
	}
	rows, err := s.store.ListByAddress(ctx, address, limit)
	if err != nil {
		return nil, err
	}
	out := make([]StatusResponse, 0, len(rows))
	for _, t := range rows {
		out = append(out, toStatus(t))
	}
	return out, nil
}

func toStatus(t Transfer) StatusResponse {
	s := StatusResponse{
		TransferID:  t.TransferID,
		Status:      t.Status,
		SrcChainID:  t.SrcChainID,
		DstChainID:  t.DstChainID,
		Sender:      t.Sender,
		Recipient:   t.Recipient,
		SrcToken:    t.SrcToken,
		DstToken:    t.DstToken,
		Amount:      t.Amount,
		DepositTx:   t.DepositTxHash,
		DepositedAt: t.DepositedAt.UTC().Format(time.RFC3339),
		FulfillTx:   t.FulfillTxHash,
		RefundTx:    t.RefundTxHash,
		LastError:   t.LastError,
		Attempts:    t.Attempts,
		UpdatedAt:   t.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if t.FulfilledAt != nil {
		v := t.FulfilledAt.UTC().Format(time.RFC3339)
		s.FulfilledAt = &v
	}
	if t.RefundedAt != nil {
		v := t.RefundedAt.UTC().Format(time.RFC3339)
		s.RefundedAt = &v
	}
	return s
}

func buildRouteID(from, to int, token string) string {
	return "gw-" + strconv.Itoa(from) + "-" + strconv.Itoa(to) + "-" + strings.ToLower(strings.TrimPrefix(token, "0x"))
}
