// Package token's create path is the Go port of
// legacy NestJS token/token.service.ts `async create(dto, address)`.
// This is the first mutation port in the strangler-fig migration — it
// writes a new row into the `tokens` table after checking for symbol
// uniqueness on the requested chain.
package token

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/jackc/pgx/v5"
)

// CreateRequest mirrors CreateTokenDto in legacy NestJS token/dto.
// Pointer-typed optional fields preserve "field absent" vs "field set
// to zero/empty" distinctions when serializing back to the response.
type CreateRequest struct {
	Symbol              string   `json:"symbol"`
	Name                string   `json:"name"`
	Description         *string  `json:"description,omitempty"`
	Image               *string  `json:"image,omitempty"`
	Banner              *string  `json:"banner,omitempty"`
	Tags                []string `json:"tags,omitempty"`
	Twitter             *string  `json:"twitter,omitempty"`
	Discord             *string  `json:"discord,omitempty"`
	Telegram            *string  `json:"telegram,omitempty"`
	Website             *string  `json:"website,omitempty"`
	Whitepaper          *string  `json:"whitepaper,omitempty"`
	LaunchType          string   `json:"launchType"`
	PreBuyPercent       *float64 `json:"preBuyPercent,omitempty"`
	HasMargin           *bool    `json:"hasMargin,omitempty"`
	HasReservation      *bool    `json:"hasReservation,omitempty"`
	IsOfficial          *bool    `json:"isOfficial,omitempty"`
	CustomAddress       *string  `json:"customAddress,omitempty"`
	ChainID             int      `json:"chainId"`
	ContractAddress     *string  `json:"contractAddress,omitempty"`
	BondingCurveAddress *string  `json:"bondingCurveAddress,omitempty"`
	TransactionHash     *string  `json:"transactionHash,omitempty"`
}

// Validation sentinels — handler maps each to 400/409 as appropriate.
var (
	ErrSymbolRequired      = errors.New("token: symbol is required")
	ErrSymbolTooLong       = errors.New("token: symbol exceeds 20 chars")
	ErrNameRequired        = errors.New("token: name is required")
	ErrNameTooLong         = errors.New("token: name exceeds 100 chars")
	ErrChainIDRequired     = errors.New("token: chainId is required")
	ErrInvalidLaunchType   = errors.New("token: launchType must be NEW_COIN, IDO, or BURNING")
	ErrPreBuyOutOfRange    = errors.New("token: preBuyPercent must be in [0, 99.9]")
	ErrCustomAddrTooLong   = errors.New("token: customAddress exceeds 4 chars")
	ErrSymbolExistsOnChain = errors.New("token: symbol already exists on this chain")
	ErrCreateRequiresPool  = errors.New("token: cannot create with no database pool")
)

// validLaunchTypes mirrors the NestJS enum (LaunchType in CreateTokenDto).
var validLaunchTypes = map[string]struct{}{
	"NEW_COIN": {},
	"IDO":      {},
	"BURNING":  {},
}

// Validate runs the same constraints class-validator enforces in the
// NestJS DTO. Kept in the service rather than the handler so other
// callers (future internal pipelines) get the same checks.
func (r CreateRequest) Validate() error {
	if strings.TrimSpace(r.Symbol) == "" {
		return ErrSymbolRequired
	}
	if len(r.Symbol) > 20 {
		return ErrSymbolTooLong
	}
	if strings.TrimSpace(r.Name) == "" {
		return ErrNameRequired
	}
	if len(r.Name) > 100 {
		return ErrNameTooLong
	}
	if r.ChainID == 0 {
		return ErrChainIDRequired
	}
	if _, ok := validLaunchTypes[r.LaunchType]; !ok {
		return ErrInvalidLaunchType
	}
	if r.PreBuyPercent != nil && (*r.PreBuyPercent < 0 || *r.PreBuyPercent > 99.9) {
		return ErrPreBuyOutOfRange
	}
	if r.CustomAddress != nil && len(*r.CustomAddress) > 4 {
		return ErrCustomAddrTooLong
	}
	return nil
}

// Create writes a new tokens row after a chain-scoped symbol uniqueness
// check. Mirrors the NestJS service: status defaults to PENDING unless
// contractAddress is supplied (then LAUNCHED + launched_at = now).
// hasMargin/hasReservation/isOfficial default to false, tags to [].
func (r *Repository) Create(ctx context.Context, req CreateRequest, creatorAddress string) (Token, error) {
	if r.pool == nil {
		return Token{}, ErrCreateRequiresPool
	}
	// Chain-scoped symbol check — same predicate as
	// prisma.token.findFirst({where: {symbol, chainId}}).
	var existingID string
	err := r.pool.QueryRow(ctx, `
		SELECT id FROM tokens
		WHERE symbol = $1 AND chain_id = $2
		LIMIT 1
	`, req.Symbol, req.ChainID).Scan(&existingID)
	if err == nil {
		return Token{}, ErrSymbolExistsOnChain
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Token{}, fmt.Errorf("check existing token: %w", err)
	}

	tagsJSON, err := json.Marshal(defaultEmptySlice(req.Tags))
	if err != nil {
		return Token{}, fmt.Errorf("encode tags: %w", err)
	}

	status := "PENDING"
	var launchedAt *time.Time
	if req.ContractAddress != nil && *req.ContractAddress != "" {
		status = "LAUNCHED"
		now := time.Now()
		launchedAt = &now
	}

	var t Token
	var tagsRaw []byte
	err = r.pool.QueryRow(ctx, `
		INSERT INTO tokens (
			id,
			symbol, name, description, image, banner, tags,
			twitter, discord, telegram, website, whitepaper,
			launch_type, pre_buy_percent, has_margin, has_reservation, is_official,
			custom_address, chain_id, creator_address, status,
			address, bonding_curve, launched_at,
			created_at, updated_at
		) VALUES (
			gen_random_uuid()::text,
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16,
			$17, $18, $19, $20,
			$21, $22, $23,
			NOW(), NOW()
		)
		RETURNING id, address, chain_id, symbol, name, image, banner, tags, status,
		          launch_type, is_official, market_cap, volume_24h, price_change_24h,
		          creator_address, launched_at, created_at
	`,
		req.Symbol, req.Name, req.Description, req.Image, req.Banner, tagsJSON,
		req.Twitter, req.Discord, req.Telegram, req.Website, req.Whitepaper,
		req.LaunchType, req.PreBuyPercent,
		boolOrFalse(req.HasMargin), boolOrFalse(req.HasReservation), boolOrFalse(req.IsOfficial),
		req.CustomAddress, req.ChainID, strings.ToLower(creatorAddress), status,
		req.ContractAddress, req.BondingCurveAddress, launchedAt,
	).Scan(&t.ID, &t.Address, &t.ChainID, &t.Symbol, &t.Name, &t.Image, &t.Banner,
		&tagsRaw, &t.Status, &t.LaunchType, &t.IsOfficial, &t.MarketCap, &t.Volume24h,
		&t.PriceChange24h, &t.CreatorAddress, &t.LaunchedAt, &t.CreatedAt)
	if err != nil {
		return Token{}, fmt.Errorf("insert token: %w", err)
	}
	t.Tags = decodeTags(tagsRaw)
	return t, nil
}

func defaultEmptySlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func boolOrFalse(p *bool) bool {
	return p != nil && *p
}

// CreateToken is the service-layer entry point used by the handler.
// Runs DTO validation, calls the repo, and propagates errors. The
// caller (handler) supplies the creator address from the auth context.
func (s *Service) CreateToken(ctx context.Context, req CreateRequest, creatorAddress string) (Token, error) {
	if err := req.Validate(); err != nil {
		return Token{}, err
	}
	return s.repo.Create(ctx, req, creatorAddress)
}

// makeCreateHandler reads the auth context for the creator address,
// parses CreateRequest from the body, and returns the new Token row.
// Auth middleware MUST have run before this handler — otherwise the
// 401 fallback below fires (defense in depth against router miswiring).
func makeCreateHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		creator := auth.AddressFromContext(r.Context())
		if creator == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req CreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		out, err := svc.CreateToken(r.Context(), req, creator)
		switch {
		case errors.Is(err, ErrSymbolExistsOnChain):
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		case errors.Is(err, ErrSymbolRequired),
			errors.Is(err, ErrSymbolTooLong),
			errors.Is(err, ErrNameRequired),
			errors.Is(err, ErrNameTooLong),
			errors.Is(err, ErrChainIDRequired),
			errors.Is(err, ErrInvalidLaunchType),
			errors.Is(err, ErrPreBuyOutOfRange),
			errors.Is(err, ErrCustomAddrTooLong):
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		case errors.Is(err, ErrCreateRequiresPool):
			// Degraded mode: no DB pool means the service is in a
			// reduced state. NestJS would 500 here; we return 503 to
			// match the readyz contract on /readyz under nil pool.
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		case err != nil:
			slog.ErrorContext(r.Context(), "token create failed", "err", err, "symbol", req.Symbol)
			http.Error(w, "failed to create token", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, out)
	}
}
