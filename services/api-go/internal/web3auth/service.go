package web3auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// DefaultNonceTTL matches DEFAULT_NONCE_TTL_MS from NestJS (5 minutes).
const DefaultNonceTTL = 5 * time.Minute

// DefaultSessionTTL matches DEFAULT_SESSION_TTL_SECONDS (24h).
const DefaultSessionTTL = 24 * time.Hour

// DefaultSIWEStatement matches legacy NestJS web3-auth/web3-auth.constants.ts.
const DefaultSIWEStatement = "Sign in with Ethereum to Wallet Trade Console"

var (
	DefaultAllowedDomains  = []string{"localhost:3000", "127.0.0.1:3000", "localhost:3002", "127.0.0.1:3002"}
	DefaultAllowedChainIDs = []int{1, 11155111, 31337}
)

// NonceResponse mirrors NestJS NonceResponseDto.
type NonceResponse struct {
	Nonce           string `json:"nonce"`
	Domain          string `json:"domain"`
	URI             string `json:"uri"`
	Statement       string `json:"statement"`
	IssuedAt        string `json:"issuedAt"`
	ExpirationTime  string `json:"expirationTime"`
	AllowedChainIDs []int  `json:"allowedChainIds"`
}

// VerifyResponse mirrors VerifyResponseDto.
type VerifyResponse struct {
	Token            string `json:"token"`
	Address          string `json:"address"`
	ChainID          int    `json:"chainId"`
	SessionExpiresAt string `json:"sessionExpiresAt"`
}

// VerifyInput mirrors VerifyDto.
type VerifyInput struct {
	Message   string `json:"message"`
	Signature string `json:"signature"`
}

// WalletInfo mirrors WalletInfoDto.
type WalletInfo struct {
	Address     string  `json:"address"`
	ChainID     int     `json:"chainId"`
	LastLoginAt *string `json:"lastLoginAt,omitempty"`
	CreatedAt   *string `json:"createdAt,omitempty"`
}

// storedChallenge keeps the issued nonce alongside its expiry + (optional)
// address binding the nonce was minted for.
type storedChallenge struct {
	issuedAt        time.Time
	expiresAt       time.Time
	address         string
	domain          string
	uri             string
	statement       string
	allowedChainIDs []int
}

// Service holds the nonce store + JWT signer config. The nonce store
// is in-memory only (matches NestJS) — production deployments behind
// multiple replicas would need a shared store (Redis), which is a
// later concern.
type Service struct {
	jwtSecret       []byte
	sessionTTL      time.Duration
	nonceTTL        time.Duration
	allowedChains   map[int]bool
	allowedChainIDs []int
	domain          string
	uri             string
	statement       string
	mu              sync.Mutex
	nonces          map[string]storedChallenge
	consumed        map[string]time.Time
}

type Config struct {
	SessionTTL      time.Duration
	NonceTTL        time.Duration
	AllowedDomains  []string
	AllowedURIs     []string
	AllowedChainIDs []int
	Statement       string
}

// NewService builds the service. jwtSecret empty → /verify always
// 500-equivalent ("not configured"). allowedChainIDs empty → no
// restriction (every chain accepted, matches NestJS dev default).
func NewService(jwtSecret string, sessionTTL, nonceTTL time.Duration, allowedChainIDs []int) *Service {
	return NewServiceWithConfig(jwtSecret, Config{
		SessionTTL:      sessionTTL,
		NonceTTL:        nonceTTL,
		AllowedChainIDs: allowedChainIDs,
	})
}

func NewServiceFromEnv(jwtSecret string) *Service {
	return NewServiceWithConfig(jwtSecret, Config{
		SessionTTL:      secondsEnv("SIWE_SESSION_TTL_SECONDS", DefaultSessionTTL),
		NonceTTL:        secondsEnv("SIWE_NONCE_TTL_SECONDS", DefaultNonceTTL),
		AllowedDomains:  csvEnv("SIWE_ALLOWED_DOMAINS"),
		AllowedURIs:     firstNonEmptyCSVEnv("SIWE_ALLOWED_URIS", "CORS_ALLOWED_ORIGINS", "MOBILE_FIRST_PARTY_WEB_ORIGINS"),
		AllowedChainIDs: intCSVEnv("SIWE_ALLOWED_CHAIN_IDS"),
		Statement:       os.Getenv("SIWE_STATEMENT"),
	})
}

func NewServiceWithConfig(jwtSecret string, cfg Config) *Service {
	sessionTTL := cfg.SessionTTL
	if sessionTTL <= 0 {
		sessionTTL = DefaultSessionTTL
	}
	nonceTTL := cfg.NonceTTL
	if nonceTTL <= 0 {
		nonceTTL = DefaultNonceTTL
	}
	allowedChainIDs := cfg.AllowedChainIDs
	if len(allowedChainIDs) == 0 {
		allowedChainIDs = append([]int(nil), DefaultAllowedChainIDs...)
	}
	allowedDomains := cfg.AllowedDomains
	if len(allowedDomains) == 0 {
		allowedDomains = append([]string(nil), DefaultAllowedDomains...)
	}
	domain := allowedDomains[0]
	uri := resolvePreferredURI(domain, cfg.AllowedURIs)
	statement := strings.TrimSpace(cfg.Statement)
	if statement == "" {
		statement = DefaultSIWEStatement
	}
	allowed := map[int]bool{}
	for _, id := range allowedChainIDs {
		allowed[id] = true
	}
	return &Service{
		jwtSecret:       []byte(jwtSecret),
		sessionTTL:      sessionTTL,
		nonceTTL:        nonceTTL,
		allowedChains:   allowed,
		allowedChainIDs: append([]int(nil), allowedChainIDs...),
		domain:          domain,
		uri:             uri,
		statement:       statement,
		nonces:          map[string]storedChallenge{},
		consumed:        map[string]time.Time{},
	}
}

// GenerateNonceFor returns a fresh nonce and stores it. If address is
// provided (and well-formed), the nonce is bound to that address; a
// subsequent verify must come from the same address.
func (s *Service) GenerateNonceFor(address string) (NonceResponse, error) {
	addr := ""
	if address != "" {
		addr = NormalizeAddress(address)
		if !addressRE.MatchString(addr) {
			return NonceResponse{}, fmt.Errorf("%w: address", ErrBadMessage)
		}
	}
	nonce := GenerateNonce()
	now := Now()
	expires := now.Add(s.nonceTTL)
	challenge := storedChallenge{
		issuedAt:        now,
		expiresAt:       expires,
		address:         addr,
		domain:          s.domain,
		uri:             s.uri,
		statement:       s.statement,
		allowedChainIDs: append([]int(nil), s.allowedChainIDs...),
	}
	s.mu.Lock()
	s.cleanupLocked(now)
	s.nonces[nonce] = challenge
	s.mu.Unlock()
	return s.serializeChallenge(nonce, challenge), nil
}

// VerifySiwe validates the signed SIWE message and issues a JWT.
func (s *Service) VerifySiwe(ctx context.Context, in VerifyInput) (VerifyResponse, error) {
	msg, err := ParseSiweMessage(in.Message)
	if err != nil {
		return VerifyResponse{}, err
	}
	recovered, err := RecoverAddress(in.Message, in.Signature)
	if err != nil {
		return VerifyResponse{}, err
	}
	if NormalizeAddress(msg.Address) != recovered {
		return VerifyResponse{}, ErrAddressMismatch
	}

	now := Now()

	// Consume nonce.
	s.mu.Lock()
	challenge, ok := s.nonces[msg.Nonce]
	if ok {
		delete(s.nonces, msg.Nonce)
		s.consumed[msg.Nonce] = challenge.expiresAt
	}
	s.mu.Unlock()
	if !ok {
		return VerifyResponse{}, ErrNonceUnknown
	}
	if now.After(challenge.expiresAt) {
		return VerifyResponse{}, ErrNonceExpired
	}
	if challenge.address != "" && challenge.address != recovered {
		return VerifyResponse{}, ErrNonceMismatch
	}
	if msg.Domain != challenge.domain || msg.URI != challenge.uri {
		return VerifyResponse{}, ErrDomainMismatch
	}
	if msg.Statement != challenge.statement {
		return VerifyResponse{}, fmt.Errorf("%w: statement mismatch", ErrBadMessage)
	}
	if len(challenge.allowedChainIDs) > 0 && !containsInt(challenge.allowedChainIDs, msg.ChainID) {
		return VerifyResponse{}, ErrChainNotAllowed
	}
	if msg.ExpirationTime != challenge.expiresAt.UTC().Format(time.RFC3339) {
		return VerifyResponse{}, ErrNonceExpired
	}

	if len(s.jwtSecret) == 0 {
		return VerifyResponse{}, fmt.Errorf("auth secret not configured")
	}
	expires := now.Add(s.sessionTTL)
	claims := auth.Claims{
		Sub:     recovered,
		ChainID: msg.ChainID,
		Type:    "web3",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return VerifyResponse{}, fmt.Errorf("sign jwt: %w", err)
	}
	return VerifyResponse{
		Token:            signed,
		Address:          recovered,
		ChainID:          msg.ChainID,
		SessionExpiresAt: expires.UTC().Format(time.RFC3339),
	}, nil
}

// Me decodes the bearer + returns a WalletInfo. The verifier is the
// same auth.Verifier that's already wired for guarded routes.
func (s *Service) Me(verifier *auth.Verifier, bearer string) (WalletInfo, error) {
	if verifier == nil {
		return WalletInfo{}, errors.New("auth verifier not configured")
	}
	claims, err := verifier.Verify(bearer)
	if err != nil {
		return WalletInfo{}, err
	}
	return WalletInfo{Address: claims.Sub, ChainID: claims.ChainID}, nil
}

// cleanupLocked removes expired nonces. Caller holds s.mu.
func (s *Service) cleanupLocked(now time.Time) {
	for n, c := range s.nonces {
		if now.After(c.expiresAt) {
			delete(s.nonces, n)
		}
	}
	for n, exp := range s.consumed {
		if now.After(exp.Add(s.nonceTTL)) {
			delete(s.consumed, n)
		}
	}
}

// Router mounts /api/web3-auth/{nonce,verify,me}.
func Router(svc *Service, verifier *auth.Verifier) chi.Router {
	r := chi.NewRouter()
	r.Get("/nonce", svc.handleGetNonce)
	r.Post("/nonce", svc.handlePostNonce)
	r.Post("/verify", svc.handleVerify)
	r.Get("/me", svc.makeMeHandler(verifier))
	return r
}

// AuthAliasRouter mounts the NestJS SiweAuthController aliases under
// /api/auth/{nonce,verify,me}. User/password auth owns the other /auth
// routes, so httpx composes both routers under one prefix.
func AuthAliasRouter(svc *Service, verifier *auth.Verifier) chi.Router {
	r := chi.NewRouter()
	RegisterAuthAliases(r, svc, verifier)
	return r
}

// RegisterAuthAliases attaches the NestJS SiweAuthController aliases to an
// existing /api/auth router.
func RegisterAuthAliases(r chi.Router, svc *Service, verifier *auth.Verifier) {
	r.Post("/nonce", svc.handlePostNonce)
	r.Post("/verify", svc.handleVerify)
	r.Get("/me", svc.makeMeHandler(verifier))
}

func (s *Service) handleGetNonce(w http.ResponseWriter, r *http.Request) {
	addr := strings.TrimSpace(r.URL.Query().Get("address"))
	out, err := s.GenerateNonceFor(addr)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handlePostNonce(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address string `json:"address"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	out, err := s.GenerateNonceFor(strings.TrimSpace(body.Address))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleVerify(w http.ResponseWriter, r *http.Request) {
	var in VerifyInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	out, err := s.VerifySiwe(r.Context(), in)
	if err != nil {
		switch {
		case errors.Is(err, ErrBadMessage), errors.Is(err, ErrBadSignature), errors.Is(err, ErrAddressMismatch), errors.Is(err, ErrDomainMismatch):
			writeError(w, http.StatusUnauthorized, err.Error())
		case errors.Is(err, ErrNonceUnknown), errors.Is(err, ErrNonceExpired), errors.Is(err, ErrNonceMismatch):
			writeError(w, http.StatusUnauthorized, err.Error())
		case errors.Is(err, ErrChainNotAllowed):
			writeError(w, http.StatusUnauthorized, err.Error())
		default:
			slog.ErrorContext(r.Context(), "web3auth verify failed", "err", err)
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) makeMeHandler(verifier *auth.Verifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "Missing or invalid authorization header")
			return
		}
		out, err := s.Me(verifier, strings.TrimSpace(h[7:]))
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("web3auth response encode failed", "err", err)
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

func (s *Service) serializeChallenge(nonce string, challenge storedChallenge) NonceResponse {
	return NonceResponse{
		Nonce:           nonce,
		Domain:          challenge.domain,
		URI:             challenge.uri,
		Statement:       challenge.statement,
		IssuedAt:        challenge.issuedAt.UTC().Format(time.RFC3339),
		ExpirationTime:  challenge.expiresAt.UTC().Format(time.RFC3339),
		AllowedChainIDs: append([]int(nil), challenge.allowedChainIDs...),
	}
}

func containsInt(values []int, want int) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func csvEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func firstNonEmptyCSVEnv(keys ...string) []string {
	for _, key := range keys {
		if values := csvEnv(key); len(values) > 0 {
			return values
		}
	}
	return nil
}

func intCSVEnv(key string) []int {
	values := csvEnv(key)
	out := make([]int, 0, len(values))
	for _, value := range values {
		n, err := strconv.Atoi(value)
		if err == nil {
			out = append(out, n)
		}
	}
	return out
}

func secondsEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func resolvePreferredURI(domain string, allowedURIs []string) string {
	if len(allowedURIs) > 0 {
		return allowedURIs[0]
	}
	protocol := "https"
	if strings.Contains(domain, "localhost") || strings.HasPrefix(domain, "127.0.0.1") {
		protocol = "http"
	}
	return protocol + "://" + domain
}
