package campaign

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/go-chi/chi/v5"
)

// Router keeps legacy reads available independently of the v2 feature flag.
// Every mutation verb is blocked before routing, including unknown old paths.
func Router(svc *Service, resolveWallet WalletResolver, guard func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	})
	blocked := guardedUpgrade(guard)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				blocked.ServeHTTP(w, r)
			default:
				if len(r.Header.Values("Authorization")) != 0 {
					if guard == nil {
						denyAll(nil).ServeHTTP(w, r)
						return
					}
					guard(next).ServeHTTP(w, r)
					return
				}
				next.ServeHTTP(w, r)
			}
		})
	})
	r.Get("/", makeListHandler(svc, resolveWallet, false))
	r.Get("/active", makeListHandler(svc, resolveWallet, true))
	r.Get("/{id}", makeByIDHandler(svc, resolveWallet))
	return r
}

func guardedUpgrade(guard func(http.Handler) http.Handler) http.Handler {
	if guard == nil {
		return denyAll(nil)
	}
	return guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		if auth.AddressFromContext(r.Context()) == "" {
			writeErr(w, http.StatusUnauthorized, "Missing or invalid web3 authorization")
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"statusCode": http.StatusConflict, "code": "CAMPAIGN_CLIENT_UPGRADE_REQUIRED",
			"message": "Campaign changes require the versioned campaign client",
		})
	}))
}

// RegisterInteractionRoutes preserves callers of the route helper while
// preventing those callers from exposing the retired repository mutations.
func RegisterInteractionRoutes(r chi.Router, _ *Service, guard func(http.Handler) http.Handler) {
	blocked := guardedUpgrade(guard)
	for _, path := range []string{"/{id}/join", "/{id}/reminder"} {
		r.Method(http.MethodPost, path, blocked)
		r.Method(http.MethodDelete, path, blocked)
	}
}

func optionalWallet(w http.ResponseWriter, r *http.Request, resolve WalletResolver) (string, error) {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return "", nil
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if len(values) != 1 || resolve == nil {
		return "", auth.ErrInvalidToken
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", auth.ErrMissingToken
	}
	wallet := strings.ToLower(resolve(r))
	if !walletRE.MatchString(wallet) || wallet == "0x0000000000000000000000000000000000000000" || auth.AddressFromContext(r.Context()) != wallet {
		return "", auth.ErrInvalidToken
	}
	return wallet, nil
}

func listQuery(r *http.Request) (string, int, error) {
	query := r.URL.Query()
	if len(query["status"]) > 1 {
		return "", 0, ErrInvalidStatus
	}
	status := query.Get("status")
	if err := validStatus(status); err != nil {
		return "", 0, err
	}
	limit := defaultListLimit
	if values, present := query["limit"]; present {
		if len(values) != 1 {
			return "", 0, ErrInvalidLimit
		}
		var err error
		limit, err = strconv.Atoi(values[0])
		if err != nil {
			return "", 0, ErrInvalidLimit
		}
	}
	limit, err := boundedLimit(limit)
	return status, limit, err
}

func makeListHandler(svc *Service, resolve WalletResolver, active bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wallet, err := optionalWallet(w, r, resolve)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "Missing or invalid web3 authorization")
			return
		}
		status, limit, err := listQuery(r)
		if err != nil {
			writeReadError(w, r, err)
			return
		}
		var out []Campaign
		if active {
			out, err = svc.active(r.Context(), time.Now(), limit, wallet)
		} else {
			out, err = svc.list(r.Context(), status, limit, wallet)
		}
		if err != nil {
			writeReadError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeByIDHandler(svc *Service, resolve WalletResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wallet, err := optionalWallet(w, r, resolve)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "Missing or invalid web3 authorization")
			return
		}
		out, err := svc.byID(r.Context(), chi.URLParam(r, "id"), wallet)
		if err != nil {
			writeReadError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeReadError(w http.ResponseWriter, r *http.Request, err error) {
	var domain *v2.DomainError
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "campaign not found")
	case errors.Is(err, ErrInvalidStatus), errors.Is(err, ErrInvalidLimit):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &domain) && domain.Status >= 400 && domain.Status <= 599:
		writeJSON(w, domain.Status, map[string]any{"statusCode": domain.Status, "code": domain.Code, "message": "Campaign read unavailable for this client"})
	default:
		slog.ErrorContext(r.Context(), "campaign read dependency failed", "err", err)
		writeErr(w, http.StatusServiceUnavailable, "campaign store unavailable")
	}
}

const AdminWalletsEnv = "CAMPAIGN_ADMIN_WALLETS"

func ParseAdminWallets(raw string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		address := strings.ToLower(strings.TrimSpace(part))
		if walletRE.MatchString(address) && address != "0x0000000000000000000000000000000000000000" {
			out[address] = true
		}
	}
	return out
}

func denyAll(http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		writeErr(w, http.StatusUnauthorized, "Missing or invalid web3 authorization")
	})
}

func writeErr(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"statusCode": status, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("campaign response encode failed", "err", err)
	}
}
