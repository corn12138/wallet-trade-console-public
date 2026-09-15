package web3auth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
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

// VerifyInput mirrors VerifyDto. Origin is set by the handler from the request
// header and is deliberately not decodable from the body — a caller must not be
// able to choose the origin it is checked against.
type VerifyInput struct {
	Message   string `json:"message"`
	Signature string `json:"signature"`
	Origin    string `json:"-"`
}

// WalletInfo mirrors WalletInfoDto.
type WalletInfo struct {
	Address     string  `json:"address"`
	ChainID     int     `json:"chainId"`
	LastLoginAt *string `json:"lastLoginAt,omitempty"`
	CreatedAt   *string `json:"createdAt,omitempty"`
}

// DefaultMaxClockSkew bounds how far a wallet's "Issued At" may lead the
// server. Wallets stamp it from the browser clock, which is routinely a few
// seconds off; anything beyond this is a message that was not minted for the
// challenge we just issued.
const DefaultMaxClockSkew = 2 * time.Minute

// Service holds the challenge store, the optional contract-wallet verifier and
// the JWT signer config. The store is an interface because a per-process map
// cannot make a nonce single-use across replicas — see ADR 0007.
type Service struct {
	jwtSecret        []byte
	sessionTTL       time.Duration
	nonceTTL         time.Duration
	maxClockSkew     time.Duration
	allowedChains    map[int]bool
	allowedChainIDs  []int
	domain           string
	uri              string
	statement        string
	allowedOrigins   map[string]struct{}
	nonces           NonceStore
	contractVerifier ContractSignatureVerifier
}

type Config struct {
	SessionTTL      time.Duration
	NonceTTL        time.Duration
	MaxClockSkew    time.Duration
	AllowedDomains  []string
	AllowedURIs     []string
	AllowedChainIDs []int
	Statement       string
	// NonceStore defaults to an in-process map. Production must pass a shared
	// store; ResolveNonceStore enforces that.
	NonceStore NonceStore
	// ContractVerifier enables EIP-1271 logins. Nil means EOA-only: a contract
	// wallet is rejected rather than assumed valid.
	ContractVerifier ContractSignatureVerifier
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

// NewServiceFromEnv resolves the challenge store before building the service,
// so a production process with no reachable shared store fails at startup
// instead of serving replayable logins.
func NewServiceFromEnv(ctx context.Context, jwtSecret string, verifier ContractSignatureVerifier) (*Service, error) {
	store, err := ResolveNonceStore(ctx, NonceStoreConfigFromEnv())
	if err != nil {
		return nil, err
	}
	return NewServiceWithConfig(jwtSecret, Config{
		SessionTTL:       secondsEnv("SIWE_SESSION_TTL_SECONDS", DefaultSessionTTL),
		NonceTTL:         secondsEnv("SIWE_NONCE_TTL_SECONDS", DefaultNonceTTL),
		MaxClockSkew:     secondsEnv("SIWE_MAX_CLOCK_SKEW_SECONDS", DefaultMaxClockSkew),
		AllowedDomains:   csvEnv("SIWE_ALLOWED_DOMAINS"),
		AllowedURIs:      firstNonEmptyCSVEnv("SIWE_ALLOWED_URIS", "CORS_ALLOWED_ORIGINS", "MOBILE_FIRST_PARTY_WEB_ORIGINS"),
		AllowedChainIDs:  intCSVEnv("SIWE_ALLOWED_CHAIN_IDS"),
		Statement:        os.Getenv("SIWE_STATEMENT"),
		NonceStore:       store,
		ContractVerifier: verifier,
	}), nil
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
	skew := cfg.MaxClockSkew
	if skew <= 0 {
		skew = DefaultMaxClockSkew
	}
	store := cfg.NonceStore
	if store == nil {
		store = NewMemoryNonceStore()
	}
	origins := buildAllowedOrigins(allowedDomains, cfg.AllowedURIs, uri)
	return &Service{
		jwtSecret:        []byte(jwtSecret),
		sessionTTL:       sessionTTL,
		nonceTTL:         nonceTTL,
		maxClockSkew:     skew,
		allowedChains:    allowed,
		allowedChainIDs:  append([]int(nil), allowedChainIDs...),
		domain:           domain,
		uri:              uri,
		statement:        statement,
		allowedOrigins:   origins,
		nonces:           store,
		contractVerifier: cfg.ContractVerifier,
	}
}

// NonceStoreKind names the challenge backend for operator diagnostics. It never
// carries a connection target.
func (s *Service) NonceStoreKind() string {
	if s == nil || s.nonces == nil {
		return "none"
	}
	return s.nonces.Kind()
}

// Close releases the challenge store.
func (s *Service) Close() error {
	if s == nil || s.nonces == nil {
		return nil
	}
	return s.nonces.Close()
}

// GenerateNonceFor returns a fresh nonce and stores it. If address is
// provided (and well-formed), the nonce is bound to that address; a
// subsequent verify must come from the same address.
//
// The challenge carries this replica's domain/URI/statement/chain set so the
// replica that consumes it validates against the configuration the signature
// was actually produced under.
func (s *Service) GenerateNonceFor(ctx context.Context, address string) (NonceResponse, error) {
	addr := ""
	if address != "" {
		addr = NormalizeAddress(address)
		if !addressRE.MatchString(addr) {
			return NonceResponse{}, fmt.Errorf("%w: address", ErrBadMessage)
		}
	}
	now := Now()
	challenge := Challenge{
		IssuedAt:        now,
		ExpiresAt:       now.Add(s.nonceTTL),
		Address:         addr,
		Domain:          s.domain,
		URI:             s.uri,
		Statement:       s.statement,
		AllowedChainIDs: append([]int(nil), s.allowedChainIDs...),
	}
	nonce := GenerateNonce()
	if err := s.nonces.Issue(ctx, nonce, challenge); err != nil {
		return NonceResponse{}, err
	}
	return s.serializeChallenge(nonce, challenge), nil
}

// VerifySiwe validates the signed SIWE message and issues a JWT.
//
// Order matters. The nonce is consumed before the signature is checked, so one
// issued challenge funds exactly one verification attempt: a failed attempt
// cannot be repeated against the same challenge, which also bounds the number
// of EIP-1271 eth_calls an unauthenticated caller can provoke per nonce.
func (s *Service) VerifySiwe(ctx context.Context, in VerifyInput) (VerifyResponse, error) {
	msg, err := ParseSiweMessage(in.Message)
	if err != nil {
		return VerifyResponse{}, err
	}

	now := Now()
	challenge, err := s.nonces.Consume(ctx, msg.Nonce)
	if err != nil {
		return VerifyResponse{}, err
	}
	if now.After(challenge.ExpiresAt) {
		return VerifyResponse{}, ErrNonceExpired
	}
	if msg.Domain != challenge.Domain || msg.URI != challenge.URI {
		return VerifyResponse{}, ErrDomainMismatch
	}
	if msg.Statement != challenge.Statement {
		return VerifyResponse{}, fmt.Errorf("%w: statement mismatch", ErrBadMessage)
	}
	if len(challenge.AllowedChainIDs) > 0 && !containsInt(challenge.AllowedChainIDs, msg.ChainID) {
		return VerifyResponse{}, ErrChainNotAllowed
	}
	if msg.ExpirationTime != challenge.ExpiresAt.UTC().Format(time.RFC3339) {
		return VerifyResponse{}, ErrNonceExpired
	}
	if err := s.checkMessageTimes(msg, now); err != nil {
		return VerifyResponse{}, err
	}
	if err := s.checkRequestOrigin(in.Origin); err != nil {
		return VerifyResponse{}, err
	}

	owner := NormalizeAddress(msg.Address)
	if err := s.authenticateOwner(ctx, msg, in.Signature, owner); err != nil {
		return VerifyResponse{}, err
	}
	if challenge.Address != "" && challenge.Address != owner {
		return VerifyResponse{}, ErrNonceMismatch
	}

	if len(s.jwtSecret) == 0 {
		return VerifyResponse{}, fmt.Errorf("auth secret not configured")
	}
	expires := now.Add(s.sessionTTL)
	claims := auth.Claims{
		Sub:     owner,
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
		Address:          owner,
		ChainID:          msg.ChainID,
		SessionExpiresAt: expires.UTC().Format(time.RFC3339),
	}, nil
}

// authenticateOwner proves that `owner` authorised this message. An EOA proves
// it by secp256k1 recovery; a contract wallet proves it by EIP-1271. The
// contract path is a fallback, never a bypass: it runs only when recovery did
// not already produce the claimed owner, and it authenticates the identical
// EIP-191 digest.
func (s *Service) authenticateOwner(ctx context.Context, msg SiweMessage, signature, owner string) error {
	recovered, recoverErr := RecoverAddress(msg.Raw, signature)
	if recoverErr == nil && recovered == owner {
		return nil
	}

	if s.contractVerifier == nil {
		if recoverErr != nil {
			return recoverErr
		}
		return ErrAddressMismatch
	}
	if !s.contractVerifier.SupportsChain(msg.ChainID) {
		return ErrContractVerificationUnavailable
	}
	sigBytes, err := decodeSignatureBytes(signature)
	if err != nil {
		return err
	}
	ok, err := s.contractVerifier.IsValidSignature(
		ctx, msg.ChainID, owner, PersonalSignDigest(msg.Raw), sigBytes)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadSignature
	}
	return nil
}

// checkMessageTimes enforces the EIP-4361 time fields the parser previously
// only collected.
func (s *Service) checkMessageTimes(msg SiweMessage, now time.Time) error {
	issuedAt, err := time.Parse(time.RFC3339, msg.IssuedAt)
	if err != nil {
		return fmt.Errorf("%w: issuedAt not RFC3339", ErrBadMessage)
	}
	if issuedAt.After(now.Add(s.maxClockSkew)) {
		return fmt.Errorf("%w: issuedAt is in the future", ErrBadMessage)
	}
	if msg.NotBefore != "" {
		notBefore, err := time.Parse(time.RFC3339, msg.NotBefore)
		if err != nil {
			return fmt.Errorf("%w: notBefore not RFC3339", ErrBadMessage)
		}
		if now.Add(s.maxClockSkew).Before(notBefore) {
			return fmt.Errorf("%w: notBefore has not passed", ErrBadMessage)
		}
	}
	return nil
}

// checkRequestOrigin rejects a browser caller from an origin this deployment
// does not serve.
//
// It is checked against the whole configured origin set rather than the single
// challenge URI: a deployment may legitimately serve several origins (a
// production domain and a preview domain), and the challenge carries only the
// first. The signed message's own domain/URI are already pinned to the
// server-issued challenge, so this is defence in depth against a page on
// another origin driving the flow — not the primary control.
//
// A missing or opaque Origin is accepted: non-browser clients (the mobile
// surface, the authenticated smoke, curl) legitimately omit it, and refusing
// them would break the API for every caller that is not a browser.
func (s *Service) checkRequestOrigin(requestOrigin string) error {
	origin := strings.TrimSpace(requestOrigin)
	if origin == "" || strings.EqualFold(origin, "null") {
		return nil
	}
	if len(s.allowedOrigins) == 0 {
		return nil
	}
	if _, ok := s.allowedOrigins[normalizeOrigin(origin)]; !ok {
		return fmt.Errorf("%w: request origin", ErrDomainMismatch)
	}
	return nil
}

// buildAllowedOrigins collapses the configured URIs and domains into the set of
// scheme://host values a browser may present.
func buildAllowedOrigins(domains, uris []string, challengeURI string) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(raw string) {
		if normalized := normalizeOrigin(raw); normalized != "" {
			out[normalized] = struct{}{}
		}
	}
	for _, uri := range uris {
		add(uri)
	}
	for _, domain := range domains {
		add(resolvePreferredURI(domain, nil))
	}
	add(challengeURI)
	return out
}

// normalizeOrigin reduces a URL or bare host to a lowercase scheme://host.
func normalizeOrigin(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || value == "*" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = resolvePreferredURI(value, nil)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host)
}

// decodeSignatureBytes accepts any length: contract-wallet signatures are not
// constrained to 65 bytes.
func decodeSignatureBytes(signature string) ([]byte, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(signature), "0x")
	if raw == "" || len(raw)%2 != 0 {
		return nil, fmt.Errorf("%w: signature format", ErrBadSignature)
	}
	out, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: signature hex", ErrBadSignature)
	}
	return out, nil
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
	s.respondWithNonce(w, r, addr)
}

func (s *Service) handlePostNonce(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Address string `json:"address"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.respondWithNonce(w, r, strings.TrimSpace(body.Address))
}

// respondWithNonce separates a caller mistake from a store outage: a bad
// address is 400, an unreachable shared store is 503 with no target detail.
func (s *Service) respondWithNonce(w http.ResponseWriter, r *http.Request, address string) {
	out, err := s.GenerateNonceFor(r.Context(), address)
	if err != nil {
		if errors.Is(err, ErrBadMessage) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.ErrorContext(r.Context(), "web3auth nonce issue failed",
			"err", err, "store", s.NonceStoreKind())
		writeError(w, http.StatusServiceUnavailable, "sign-in challenge store unavailable")
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
	in.Origin = r.Header.Get("Origin")
	out, err := s.VerifySiwe(r.Context(), in)
	if err != nil {
		switch {
		case errors.Is(err, ErrBadMessage), errors.Is(err, ErrBadSignature), errors.Is(err, ErrAddressMismatch), errors.Is(err, ErrDomainMismatch):
			writeError(w, http.StatusUnauthorized, err.Error())
		case errors.Is(err, ErrNonceUnknown), errors.Is(err, ErrNonceExpired), errors.Is(err, ErrNonceMismatch):
			writeError(w, http.StatusUnauthorized, err.Error())
		case errors.Is(err, ErrChainNotAllowed), errors.Is(err, ErrContractVerificationUnavailable):
			writeError(w, http.StatusUnauthorized, err.Error())
		case errors.Is(err, ErrContractVerificationUnreachable):
			slog.ErrorContext(r.Context(), "web3auth contract verification unreachable", "err", err)
			writeError(w, http.StatusServiceUnavailable, ErrContractVerificationUnreachable.Error())
		default:
			// The message is deliberately fixed: a store or signing failure
			// must not put its target, driver text or SQL in a public body.
			slog.ErrorContext(r.Context(), "web3auth verify failed",
				"err", err, "store", s.NonceStoreKind())
			writeError(w, http.StatusServiceUnavailable, "sign-in temporarily unavailable")
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

func (s *Service) serializeChallenge(nonce string, challenge Challenge) NonceResponse {
	return NonceResponse{
		Nonce:           nonce,
		Domain:          challenge.Domain,
		URI:             challenge.URI,
		Statement:       challenge.Statement,
		IssuedAt:        challenge.IssuedAt.UTC().Format(time.RFC3339),
		ExpirationTime:  challenge.ExpiresAt.UTC().Format(time.RFC3339),
		AllowedChainIDs: append([]int(nil), challenge.AllowedChainIDs...),
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
