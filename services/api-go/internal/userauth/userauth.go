// Package userauth is the Go port of legacy NestJS auth/.
// It composes internal/auth's UserVerifier (JWT issue/verify) with
// internal/users (DB) to power the /api/auth/* routes.
package userauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"os"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/users"
	"github.com/go-chi/chi/v5"
)

// ErrInvalidCredentials surfaces 401 on login failure.
var ErrInvalidCredentials = errors.New("用户名或密码错误")

// ErrUserExists surfaces 400 when register hits a duplicate.
var ErrUserExists = errors.New("该电子邮件已被注册")

// ErrValidation wraps any 400-class problem with the request body
// (bad email, short password, missing field). Handler maps to 400.
var ErrValidation = errors.New("validation failed")

// LoginInput mirrors LoginDto.
type LoginInput struct {
	UsernameOrEmail string `json:"usernameOrEmail"`
	Password        string `json:"password"`
}

// RegisterInput mirrors RegisterDto.
type RegisterInput struct {
	Email    string  `json:"email"`
	Username string  `json:"username"`
	Password string  `json:"password"`
	FullName *string `json:"fullName,omitempty"`
}

// LoginResponse mirrors AuthService.login's return shape (minus
// refreshToken which the handler sets as an HttpOnly cookie).
type LoginResponse struct {
	AccessToken  string           `json:"accessToken"`
	RefreshToken string           `json:"refreshToken,omitempty"`
	CsrfToken    string           `json:"csrfToken,omitempty"`
	User         users.PublicView `json:"user"`
}

// Service composes the user-auth flow.
type Service struct {
	users    *users.Repository
	verifier *auth.UserVerifier
	csrf     *auth.CsrfSigner
}

// NewService accepts nil components for the degraded contract.
func NewService(usersRepo *users.Repository, verifier *auth.UserVerifier, csrf *auth.CsrfSigner) *Service {
	return &Service{users: usersRepo, verifier: verifier, csrf: csrf}
}

// Login validates credentials, issues tokens, persists the (hashed)
// refresh token, returns the response. Caller sets the cookies.
func (s *Service) Login(ctx context.Context, in LoginInput) (LoginResponse, error) {
	if s.users == nil || s.verifier == nil {
		return LoginResponse{}, fmt.Errorf("auth service not configured")
	}
	user, err := s.users.FindByUsernameOrEmail(ctx, in.UsernameOrEmail)
	if errors.Is(err, users.ErrNotFound) {
		return LoginResponse{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResponse{}, err
	}
	if err := auth.ComparePassword(user.Password, in.Password); err != nil {
		return LoginResponse{}, ErrInvalidCredentials
	}
	return s.issueAndPersist(ctx, user)
}

// Register validates uniqueness, hashes the password, creates the row,
// issues tokens, persists the refresh token, returns the response.
func (s *Service) Register(ctx context.Context, in RegisterInput) (LoginResponse, error) {
	// Validate FIRST so callers get a clean 400 even when the service
	// is in a degraded (nil-repo) configuration.
	in.Email = strings.TrimSpace(in.Email)
	in.Username = strings.TrimSpace(in.Username)
	if !validEmail(in.Email) {
		return LoginResponse{}, fmt.Errorf("%w: invalid email", ErrValidation)
	}
	if in.Username == "" {
		return LoginResponse{}, fmt.Errorf("%w: username is required", ErrValidation)
	}
	if len(in.Password) < 8 {
		return LoginResponse{}, fmt.Errorf("%w: password must be at least 8 characters", ErrValidation)
	}
	if s.users == nil || s.verifier == nil {
		return LoginResponse{}, fmt.Errorf("auth service not configured")
	}
	hashed, err := auth.HashPassword(in.Password)
	if err != nil {
		return LoginResponse{}, fmt.Errorf("hash password: %w", err)
	}
	created, err := s.users.Create(ctx, users.CreateInput{
		Email:    in.Email,
		Username: in.Username,
		Password: hashed,
		FullName: in.FullName,
		Roles:    []string{"user"},
	})
	if errors.Is(err, users.ErrDuplicate) {
		return LoginResponse{}, ErrUserExists
	}
	if err != nil {
		return LoginResponse{}, err
	}
	return s.issueAndPersist(ctx, created)
}

func (s *Service) issueAndPersist(ctx context.Context, u users.User) (LoginResponse, error) {
	access, refresh, err := s.verifier.IssueTokens(u.ID, u.Username, u.Email, u.Roles)
	if err != nil {
		return LoginResponse{}, fmt.Errorf("issue tokens: %w", err)
	}
	hashed, err := auth.HashPassword(refresh)
	if err != nil {
		return LoginResponse{}, fmt.Errorf("hash refresh token: %w", err)
	}
	if err := s.users.UpdateRefreshToken(ctx, u.ID, &hashed); err != nil {
		return LoginResponse{}, fmt.Errorf("persist refresh token: %w", err)
	}
	csrf := ""
	if s.csrf != nil {
		csrf = s.csrf.GenerateToken(u.ID)
	}
	return LoginResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		CsrfToken:    csrf,
		User:         u.Public(),
	}, nil
}

// Refresh validates the refresh token (both JWT signature AND bcrypt
// match against the DB-stored hash), issues new tokens, persists the
// new refresh token.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (LoginResponse, error) {
	if s.users == nil || s.verifier == nil {
		return LoginResponse{}, fmt.Errorf("auth service not configured")
	}
	claims, err := s.verifier.VerifyRefresh(refreshToken)
	if err != nil {
		return LoginResponse{}, ErrInvalidCredentials
	}
	user, err := s.users.FindByID(ctx, claims.Sub)
	if errors.Is(err, users.ErrNotFound) {
		return LoginResponse{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResponse{}, err
	}
	if user.RefreshToken == nil || *user.RefreshToken == "" {
		return LoginResponse{}, ErrInvalidCredentials
	}
	if err := auth.ComparePassword(*user.RefreshToken, refreshToken); err != nil {
		return LoginResponse{}, ErrInvalidCredentials
	}
	return s.issueAndPersist(ctx, user)
}

// Logout clears the stored refresh token.
func (s *Service) Logout(ctx context.Context, userID string) error {
	if s.users == nil {
		return fmt.Errorf("auth service not configured")
	}
	return s.users.UpdateRefreshToken(ctx, userID, nil)
}

// ValidateToken parses an access token, looks up the user, returns the
// public projection. JWT signature check happens first so bad tokens
// always surface as 401 even when the user repo isn't configured.
func (s *Service) ValidateToken(ctx context.Context, token string) (users.PublicView, error) {
	if s.verifier == nil {
		return users.PublicView{}, fmt.Errorf("auth service not configured")
	}
	claims, err := s.verifier.VerifyAccess(token)
	if err != nil {
		return users.PublicView{}, ErrInvalidCredentials
	}
	if s.users == nil {
		return users.PublicView{}, fmt.Errorf("auth service not configured")
	}
	user, err := s.users.FindByID(ctx, claims.Sub)
	if errors.Is(err, users.ErrNotFound) {
		return users.PublicView{}, ErrInvalidCredentials
	}
	if err != nil {
		return users.PublicView{}, err
	}
	return user.Public(), nil
}

// GetProfile returns the public view for the authenticated user.
func (s *Service) GetProfile(ctx context.Context, userID string) (users.PublicView, error) {
	if s.users == nil {
		return users.PublicView{}, fmt.Errorf("auth service not configured")
	}
	user, err := s.users.FindByID(ctx, userID)
	if errors.Is(err, users.ErrNotFound) {
		return users.PublicView{}, ErrInvalidCredentials
	}
	if err != nil {
		return users.PublicView{}, err
	}
	return user.Public(), nil
}

// Router mounts /api/auth/* with all 7 NestJS routes. The guarded
// middleware is invoked at the handler level for /logout, /refresh,
// and /profile.
func Router(svc *Service, accessGuard, refreshGuard func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	RegisterRoutes(r, svc, accessGuard, refreshGuard)
	return r
}

// RegisterRoutes attaches /api/auth/* user/password auth routes to an
// existing auth router. This lets httpx compose these routes with the
// SIWE auth aliases under the same /api/auth prefix.
func RegisterRoutes(r chi.Router, svc *Service, accessGuard, refreshGuard func(http.Handler) http.Handler) {
	r.Post("/login", svc.handleLogin)
	r.Post("/register", svc.handleRegister)
	r.Post("/refresh-token", svc.handleRefreshToken)
	r.Post("/validate-token", svc.handleValidateToken)

	// /logout + /profile require an access token.
	if accessGuard != nil {
		r.With(accessGuard).Post("/logout", svc.handleLogout)
		r.With(accessGuard).Get("/profile", svc.handleProfile)
	}
	// /refresh requires a refresh token (separate middleware).
	if refreshGuard != nil {
		r.With(refreshGuard).Post("/refresh", svc.handleRefreshGuarded)
	}
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in LoginInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(in.UsernameOrEmail) == "" || in.Password == "" {
		writeError(w, http.StatusBadRequest, "usernameOrEmail and password are required")
		return
	}
	resp, err := s.Login(r.Context(), in)
	if errors.Is(err, ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, ErrInvalidCredentials.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "userauth login failed", "err", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	setAuthCookies(w, resp)
	resp.RefreshToken = ""
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) handleRegister(w http.ResponseWriter, r *http.Request) {
	var in RegisterInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.Register(r.Context(), in)
	if errors.Is(err, ErrUserExists) {
		writeError(w, http.StatusBadRequest, ErrUserExists.Error())
		return
	}
	if errors.Is(err, ErrValidation) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "userauth register failed", "err", err)
		writeError(w, http.StatusInternalServerError, "register failed")
		return
	}
	setAuthCookies(w, resp)
	resp.RefreshToken = ""
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Service) handleRefreshToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.Refresh(r.Context(), body.RefreshToken)
	if errors.Is(err, ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "无效的刷新令牌")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "userauth refresh failed", "err", err)
		writeError(w, http.StatusInternalServerError, "refresh failed")
		return
	}
	setAuthCookies(w, resp)
	writeJSON(w, http.StatusOK, map[string]any{
		"accessToken":  resp.AccessToken,
		"refreshToken": resp.RefreshToken,
	})
}

func (s *Service) handleRefreshGuarded(w http.ResponseWriter, r *http.Request) {
	// Refresh-guard middleware put userID in context; we still need
	// the raw refresh token (cookie OR header) to compare to the DB
	// hash. NestJS reads it from req.user.refreshToken (added by
	// jwt-refresh.strategy.ts) — we read it from the cookie.
	refresh := readCookie(r, "refreshToken")
	resp, err := s.Refresh(r.Context(), refresh)
	if errors.Is(err, ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "无效的刷新令牌")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "userauth refresh-guarded failed", "err", err)
		writeError(w, http.StatusInternalServerError, "refresh failed")
		return
	}
	setRefreshCookie(w, resp.RefreshToken)
	writeJSON(w, http.StatusOK, map[string]any{
		"accessToken": resp.AccessToken,
	})
}

func (s *Service) handleValidateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	view, err := s.ValidateToken(r.Context(), body.Token)
	if errors.Is(err, ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "无效的令牌")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "userauth validate failed", "err", err)
		writeError(w, http.StatusInternalServerError, "validate failed")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "用户未认证")
		return
	}
	if err := s.Logout(r.Context(), userID); err != nil && !errors.Is(err, users.ErrNotFound) {
		slog.ErrorContext(r.Context(), "userauth logout failed", "err", err)
		writeError(w, http.StatusInternalServerError, "logout failed")
		return
	}
	clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (s *Service) handleProfile(w http.ResponseWriter, r *http.Request) {
	userID := auth.UserIDFromContext(r.Context())
	if userID == "" {
		writeError(w, http.StatusUnauthorized, "用户未认证")
		return
	}
	view, err := s.GetProfile(r.Context(), userID)
	if errors.Is(err, ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "用户不存在")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "userauth profile failed", "err", err)
		writeError(w, http.StatusInternalServerError, "profile failed")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// AccessUserChecker looks up the access-token subject so the middleware can
// reject a token whose user has been deleted or disabled — mirroring the NestJS
// JwtStrategy.validate (userService.findOne(sub) + a `disabled`-role check).
// *users.Repository satisfies it. A nil checker skips the lookup (dev/tests, or
// any environment without a user store), preserving the prior pure-JWT behaviour.
type AccessUserChecker interface {
	FindByID(ctx context.Context, id string) (users.User, error)
}

// AccessMiddleware verifies the access JWT and stores user claims on
// the request context. Used by /logout, /profile, plus future routes
// that need the user identity (article mutations, /users/me, etc.).
//
// When checker is non-nil it also confirms the subject is still a valid,
// non-disabled user (guard-parity.md addendum 2026-06-08): a valid token for a
// deleted/disabled user is rejected with 401, matching the deployed NestJS.
func AccessMiddleware(v *auth.UserVerifier, checker AccessUserChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
			claims, err := v.VerifyAccess(token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid access token")
				return
			}
			if checker != nil {
				user, err := checker.FindByID(r.Context(), claims.Sub)
				if err != nil {
					writeError(w, http.StatusUnauthorized, "用户不存在")
					return
				}
				for _, role := range user.Roles {
					if role == "disabled" {
						writeError(w, http.StatusUnauthorized, "账户已被禁用")
						return
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), claims)))
		})
	}
}

// RequireRoles wraps AccessMiddleware and additionally checks the
// user's role list against allowedRoles (any-match). Mirrors NestJS
// RolesGuard. Use to gate routes that need @Roles('admin','editor').
func RequireRoles(v *auth.UserVerifier, checker AccessUserChecker, allowedRoles ...string) func(http.Handler) http.Handler {
	access := AccessMiddleware(v, checker)
	return func(next http.Handler) http.Handler {
		return access(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userRoles := auth.UserRolesFromContext(r.Context())
			if !hasAnyRole(userRoles, allowedRoles) {
				writeError(w, http.StatusForbidden, "insufficient role")
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

func hasAnyRole(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, h := range have {
		for _, w := range want {
			if h == w {
				return true
			}
		}
	}
	return false
}

// RefreshMiddleware verifies the refresh JWT (cookie or bearer) and
// stores claims on the context.
func RefreshMiddleware(v *auth.UserVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := readCookie(r, "refreshToken")
			if token == "" {
				token = extractBearer(r)
			}
			if token == "" {
				writeError(w, http.StatusUnauthorized, "missing refresh token")
				return
			}
			claims, err := v.VerifyRefresh(token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid refresh token")
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), claims)))
		})
	}
}

// --- cookie + helpers ---

func setAuthCookies(w http.ResponseWriter, resp LoginResponse) {
	setRefreshCookie(w, resp.RefreshToken)
	if resp.CsrfToken != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "XSRF-TOKEN",
			Value:    resp.CsrfToken,
			Path:     "/",
			Secure:   isProduction(),
			SameSite: http.SameSiteStrictMode,
			MaxAge:   24 * 60 * 60,
		})
	}
}

func setRefreshCookie(w http.ResponseWriter, token string) {
	if token == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "refreshToken",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isProduction(),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   7 * 24 * 60 * 60,
	})
}

func clearAuthCookies(w http.ResponseWriter) {
	for _, name := range []string{"refreshToken", "XSRF-TOKEN"} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1})
	}
}

func readCookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func validEmail(s string) bool {
	if s == "" {
		return false
	}
	addr, err := mail.ParseAddress(s)
	return err == nil && addr.Address == s
}

func isProduction() bool {
	return strings.EqualFold(os.Getenv("NODE_ENV"), "production")
}

// _ keeps net imported; future hooks may attach IP-based throttling.
var _ = net.IPv4

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("userauth response encode failed", "err", err)
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
