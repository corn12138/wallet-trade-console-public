// Package earn is the Go port of legacy NestJS earn/earn.service.ts.
// Phase 4b ports only GET /api/earn/products — the read endpoint
// powering the FE earn products list. The two POST build-tx endpoints
// (build-deposit, build-withdraw) require ABI encoding and land in a
// follow-up; they're not on any user-facing rendering path today.
package earn

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/go-chi/chi/v5"
)

// Product is one row of /api/earn/products. Field shape matches the
// NestJS earnService.getProducts() return — contractAddress is the
// chain-resolved StakingPool deployment address (null when missing).
type Product struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	ProductType     string  `json:"productType"`
	ChainID         int     `json:"chainId"`
	TokenAddress    string  `json:"tokenAddress"`
	RewardToken     *string `json:"rewardToken"`
	APY             float64 `json:"apy"`
	TVL             float64 `json:"tvl"`
	Status          string  `json:"status"`
	ContractAddress *string `json:"contractAddress"`
}

// PoolLister is the contract earn needs from staking. Keeping it as an
// interface lets tests inject a fake without spinning a pgxpool.
type PoolLister interface {
	ListActivePools(ctx context.Context, chainID *int) ([]staking.PoolView, error)
}

// PoolFinder resolves a single staking_pools row by id. Used by the
// build-deposit / build-withdraw endpoints to look up the chain
// before resolving the deployment-address.
type PoolFinder interface {
	FindPool(ctx context.Context, id string) (staking.PoolView, error)
}

// DeploymentsLoader resolves chain → contract address lookups. The
// markets.DirLoader satisfies this interface, so reuse the existing
// runtime loader rather than threading a second one through.
type DeploymentsLoader interface {
	Load() (map[int]deployments.ChainConfig, error)
}

// Service composes the staking pool list with deployment-address
// lookup. nil-pool errors from staking degrade to an empty list.
// finder is optional — only the build-tx endpoints need it.
type Service struct {
	pools  PoolLister
	finder PoolFinder
	loader DeploymentsLoader
}

// NewService binds the read-side dependencies. Build-tx endpoints
// require a PoolFinder; use NewServiceWithBuilder.
func NewService(pools PoolLister, loader DeploymentsLoader) *Service {
	return &Service{pools: pools, loader: loader}
}

// NewServiceWithBuilder also wires the PoolFinder needed by the
// stake/unstake build-tx endpoints.
func NewServiceWithBuilder(pools PoolLister, finder PoolFinder, loader DeploymentsLoader) *Service {
	return &Service{pools: pools, finder: finder, loader: loader}
}

// GetProducts returns the active staking pools mapped to the FE shape.
// chainID nil means "all chains".
func (s *Service) GetProducts(ctx context.Context, chainID *int) ([]Product, error) {
	if s.pools == nil {
		return []Product{}, nil
	}
	rows, err := s.pools.ListActivePools(ctx, chainID)
	if err != nil {
		if errors.Is(err, staking.ErrPoolUnavailable) {
			return []Product{}, nil
		}
		return nil, err
	}

	addressByChain := s.resolveStakingAddresses()

	out := make([]Product, 0, len(rows))
	for _, p := range rows {
		apy := 0.0
		if p.APY != nil {
			apy = *p.APY
		}
		tvl := 0.0
		if p.TVL != nil {
			tvl = *p.TVL
		}
		var contract *string
		if addr, ok := addressByChain[p.ChainID]; ok && addr != "" {
			a := addr
			contract = &a
		}
		out = append(out, Product{
			ID:              p.ID,
			Name:            p.Name,
			ProductType:     p.PoolType,
			ChainID:         p.ChainID,
			TokenAddress:    p.TokenAddress,
			RewardToken:     p.RewardToken,
			APY:             apy,
			TVL:             tvl,
			Status:          p.Status,
			ContractAddress: contract,
		})
	}
	return out, nil
}

// resolveStakingAddresses returns a chainID → StakingPool address map.
// A nil loader or a load error degrades to an empty map — the response
// just gets null contractAddress per row, matching getDeploymentAddress
// returning ” / undefined in the NestJS path.
func (s *Service) resolveStakingAddresses() map[int]string {
	if s.loader == nil {
		return map[int]string{}
	}
	configs, err := s.loader.Load()
	if err != nil {
		slog.Warn("earn: deployments load failed", "err", err)
		return map[int]string{}
	}
	out := make(map[int]string, len(configs))
	for chainID, cfg := range configs {
		if addr := deployments.LookupAddress(cfg, "StakingPool"); addr != "" {
			out[chainID] = addr
		}
	}
	return out
}

// BuildTxRequest is the POST body for both build-deposit and
// build-withdraw. tokenDecimals scales `amount` (a human-readable
// decimal string) into the uint256 wei the contract expects.
type BuildTxRequest struct {
	ProductID     string `json:"productId"`
	Amount        string `json:"amount"`
	TokenDecimals int    `json:"tokenDecimals"`
}

// BuildTxResponse is the unsigned transaction the FE wallet signs.
type BuildTxResponse struct {
	ChainID int    `json:"chainId"`
	To      string `json:"to"`
	Value   string `json:"value"`
	Data    string `json:"data"`
}

// ErrProductNotFound is the not-configured / not-found case for build-tx.
var ErrProductNotFound = errors.New("staking pool contract is not configured")

// ErrInvalidAmount is returned by parseAmount on bad decimal input.
var ErrInvalidAmount = errors.New("invalid amount")

// BuildDepositTx ABI-encodes stake(uint256). NestJS uses viem's
// parseUnits + encodeFunctionData; we do the equivalent with
// internal/abi. Returns ErrProductNotFound when finder rejects the
// productId or the chain has no StakingPool deployment.
func (s *Service) BuildDepositTx(ctx context.Context, req BuildTxRequest) (BuildTxResponse, error) {
	return s.buildStakeTx(ctx, req, "stake(uint256)")
}

// BuildWithdrawTx ABI-encodes unstake(uint256).
func (s *Service) BuildWithdrawTx(ctx context.Context, req BuildTxRequest) (BuildTxResponse, error) {
	return s.buildStakeTx(ctx, req, "unstake(uint256)")
}

func (s *Service) buildStakeTx(ctx context.Context, req BuildTxRequest, signature string) (BuildTxResponse, error) {
	if s.finder == nil {
		return BuildTxResponse{}, ErrProductNotFound
	}
	pool, err := s.finder.FindPool(ctx, req.ProductID)
	if errors.Is(err, staking.ErrPoolNotFound) || errors.Is(err, staking.ErrPoolUnavailable) {
		return BuildTxResponse{}, ErrProductNotFound
	}
	if err != nil {
		return BuildTxResponse{}, err
	}

	contractAddress := ""
	if s.loader != nil {
		configs, lerr := s.loader.Load()
		if lerr == nil {
			contractAddress = deployments.LookupAddress(configs[pool.ChainID], "StakingPool")
		}
	}
	if contractAddress == "" {
		return BuildTxResponse{}, ErrProductNotFound
	}

	amount, err := parseAmount(req.Amount, req.TokenDecimals)
	if err != nil {
		return BuildTxResponse{}, err
	}
	encoded, err := abi.EncodeUint256(amount)
	if err != nil {
		return BuildTxResponse{}, err
	}
	return BuildTxResponse{
		ChainID: pool.ChainID,
		To:      contractAddress,
		Value:   "0",
		Data:    abi.EncodeCall(signature, encoded),
	}, nil
}

// parseAmount delegates to abi.ParseUnits and remaps the error to
// earn's ErrInvalidAmount so the handler's `errors.Is(err,
// ErrInvalidAmount)` check (which returns 400) continues to work.
func parseAmount(s string, decimals int) (*big.Int, error) {
	v, err := abi.ParseUnits(s, decimals)
	if err != nil {
		return nil, ErrInvalidAmount
	}
	return v, nil
}

// Router mounts /products + the two build-tx endpoints on a chi
// sub-router (mounted at /api/earn).
//
// GET /products is PUBLIC: it returns the product *catalog* (active staking
// pools + chain-resolved contract addresses), not user-owned data, and the FE
// EarnPage loads it on mount before any wallet/user sign-in. This is an
// intentional Go-canonical departure from the deployed NestJS, where the global
// JwtAuthGuard 401s the route — that 401 left the earn page unable to show
// products for normal pre-login browsing. See
// docs/migration/backend-go/cutover/earn-products.md.
//
// build-deposit / build-withdraw stay access-guarded (parity with the NestJS
// global JwtAuthGuard): buildTxGuard wraps them when non-nil. A nil guard (dev
// without JWT_SECRET) mounts them open, exactly as the previous
// mountGuarded(accessGuard, "/earn", …) did — so local dev is unchanged. These
// POSTs are NOT routed to Go in production by this cut.
func Router(svc *Service, buildTxGuard func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Get("/products", makeProductsHandler(svc))
	if buildTxGuard != nil {
		r.Group(func(r chi.Router) {
			r.Use(buildTxGuard)
			r.Post("/build-deposit", makeBuildDepositHandler(svc))
			r.Post("/build-withdraw", makeBuildWithdrawHandler(svc))
		})
	} else {
		r.Post("/build-deposit", makeBuildDepositHandler(svc))
		r.Post("/build-withdraw", makeBuildWithdrawHandler(svc))
	}
	return r
}

func makeBuildDepositHandler(svc *Service) http.HandlerFunc {
	return makeBuildHandler(svc, svc.BuildDepositTx)
}

func makeBuildWithdrawHandler(svc *Service) http.HandlerFunc {
	return makeBuildHandler(svc, svc.BuildWithdrawTx)
}

func makeBuildHandler(svc *Service, fn func(context.Context, BuildTxRequest) (BuildTxResponse, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body BuildTxRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		out, err := fn(r.Context(), body)
		if errors.Is(err, ErrProductNotFound) {
			http.Error(w, "staking pool contract is not configured", http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrInvalidAmount) {
			http.Error(w, "invalid amount", http.StatusBadRequest)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "earn build-tx failed", "err", err)
			http.Error(w, "failed to build transaction", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeProductsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var chainID *int
		if raw := r.URL.Query().Get("chainId"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil {
				chainID = &v
			}
		}
		out, err := svc.GetProducts(r.Context(), chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "earn products failed", "err", err)
			http.Error(w, "failed to load earn products", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("earn response encode failed", "err", err)
	}
}
