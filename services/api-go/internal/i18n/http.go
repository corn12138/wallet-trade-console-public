package i18n

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

// AdminWalletsEnv is the allowlist env var: comma-separated wallet addresses.
const AdminWalletsEnv = "I18N_ADMIN_WALLETS"

// ParseAdminWallets normalizes the configured allowlist. An empty result
// means every admin endpoint answers 403 — absence of configuration never
// grants access.
func ParseAdminWallets(raw string) map[string]bool {
	out := map[string]bool{}
	for part := range strings.SplitSeq(raw, ",") {
		addr := strings.ToLower(strings.TrimSpace(part))
		if len(addr) == 42 && strings.HasPrefix(addr, "0x") {
			out[addr] = true
		}
	}
	return out
}

// Router mounts the public catalog endpoints and the SIWE-admin-guarded
// management endpoints. guardedAuth is the shared web3 JWT middleware; nil
// (no JWT secret configured) leaves the admin subtree answering 401.
func Router(svc *Service, guardedAuth func(http.Handler) http.Handler) http.Handler {
	admins := ParseAdminWallets(os.Getenv(AdminWalletsEnv))
	r := chi.NewRouter()

	// ── public, read-only ────────────────────────────────────────────────
	r.Get("/locales", func(w http.ResponseWriter, req *http.Request) {
		locales, source, err := svc.PublicLocales(req.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "locale metadata unavailable")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("X-I18n-Source", source)
		writeJSON(w, http.StatusOK, map[string]any{"locales": locales, "source": source})
	})

	r.Get("/catalog/{locale}", func(w http.ResponseWriter, req *http.Request) {
		resp, err := svc.PublicCatalog(req.Context(), chi.URLParam(req, "locale"))
		if err != nil {
			if errors.Is(err, ErrLocaleDisabled) {
				writeErr(w, http.StatusNotFound, "locale is not enabled")
				return
			}
			writeErr(w, http.StatusInternalServerError, "catalog unavailable")
			return
		}
		etag := `"` + resp.Checksum + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
		w.Header().Set("X-I18n-Source", resp.Source)
		if match := req.Header.Get("If-None-Match"); match != "" && strings.Contains(match, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})

	// ── admin: SIWE JWT + wallet allowlist ───────────────────────────────
	r.Route("/admin", func(ar chi.Router) {
		if guardedAuth != nil {
			ar.Use(guardedAuth)
		} else {
			ar.Use(func(http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					writeErr(w, http.StatusUnauthorized, "Missing or invalid web3 authorization")
				})
			})
		}
		ar.Use(adminOnly(admins))

		ar.Get("/locales", func(w http.ResponseWriter, req *http.Request) {
			locales, err := svc.AdminLocales(req.Context())
			if err != nil {
				writeErr(w, http.StatusInternalServerError, "locales unavailable")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"locales": locales})
		})

		ar.Post("/locales", func(w http.ResponseWriter, req *http.Request) {
			var body struct {
				Code        string `json:"code"`
				EnglishName string `json:"englishName"`
				NativeName  string `json:"nativeName"`
				Enabled     bool   `json:"enabled"`
			}
			if !decodeBody(w, req, &body) {
				return
			}
			loc, err := svc.CreateLocale(req.Context(), body.Code, body.EnglishName, body.NativeName, body.Enabled, actor(req))
			if err != nil {
				writeServiceErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, loc)
		})

		ar.Patch("/locales/{code}", func(w http.ResponseWriter, req *http.Request) {
			var body struct {
				EnglishName *string `json:"englishName"`
				NativeName  *string `json:"nativeName"`
				Enabled     *bool   `json:"enabled"`
				IsDefault   *bool   `json:"isDefault"`
			}
			if !decodeBody(w, req, &body) {
				return
			}
			loc, err := svc.UpdateLocale(req.Context(), chi.URLParam(req, "code"),
				body.EnglishName, body.NativeName, body.Enabled, body.IsDefault, actor(req))
			if err != nil {
				writeServiceErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, loc)
		})

		ar.Get("/messages", func(w http.ResponseWriter, req *http.Request) {
			q := req.URL.Query()
			items, total, err := svc.Messages(req.Context(), q.Get("locale"), q.Get("namespace"),
				q.Get("search"), atoiDefault(q.Get("page"), 1), atoiDefault(q.Get("pageSize"), 50))
			if err != nil {
				writeServiceErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
		})

		ar.Get("/namespaces", func(w http.ResponseWriter, req *http.Request) {
			stats, err := svc.NamespaceStats(req.Context(), req.URL.Query().Get("locale"))
			if err != nil {
				writeServiceErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"namespaces": stats})
		})

		ar.Patch("/messages/{locale}/{namespace}/{key}", func(w http.ResponseWriter, req *http.Request) {
			var body struct {
				Value           string `json:"value"`
				ExpectedVersion int    `json:"expectedVersion"`
			}
			if !decodeBody(w, req, &body) {
				return
			}
			row, err := svc.UpdateDraft(req.Context(), chi.URLParam(req, "locale"),
				chi.URLParam(req, "namespace"), chi.URLParam(req, "key"),
				body.Value, body.ExpectedVersion, actor(req))
			if err != nil {
				writeServiceErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, row)
		})

		ar.Post("/validate/{locale}", func(w http.ResponseWriter, req *http.Request) {
			issues, err := svc.Validate(req.Context(), chi.URLParam(req, "locale"))
			if err != nil {
				writeServiceErr(w, err)
				return
			}
			if issues == nil {
				issues = []Issue{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"valid": len(issues) == 0, "issues": issues})
		})

		ar.Post("/publish/{locale}", func(w http.ResponseWriter, req *http.Request) {
			rev, err := svc.Publish(req.Context(), chi.URLParam(req, "locale"), actor(req))
			if err != nil {
				writeServiceErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, rev)
		})

		ar.Get("/revisions", func(w http.ResponseWriter, req *http.Request) {
			q := req.URL.Query()
			items, total, err := svc.Revisions(req.Context(), q.Get("locale"),
				atoiDefault(q.Get("page"), 1), atoiDefault(q.Get("pageSize"), 20))
			if err != nil {
				writeServiceErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
		})

		ar.Post("/rollback/{locale}/{revisionId}", func(w http.ResponseWriter, req *http.Request) {
			rev, err := svc.Rollback(req.Context(), chi.URLParam(req, "locale"),
				chi.URLParam(req, "revisionId"), actor(req))
			if err != nil {
				writeServiceErr(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, rev)
		})

		ar.Get("/audit", func(w http.ResponseWriter, req *http.Request) {
			q := req.URL.Query()
			items, total, err := svc.Audit(req.Context(), q.Get("locale"), q.Get("actor"), q.Get("action"),
				atoiDefault(q.Get("page"), 1), atoiDefault(q.Get("pageSize"), 50))
			if err != nil {
				writeServiceErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
		})
	})

	return r
}

// adminOnly rejects any authenticated wallet outside the allowlist. An empty
// allowlist rejects everyone (403), in every environment.
func adminOnly(admins map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			addr := strings.ToLower(auth.AddressFromContext(req.Context()))
			if addr == "" {
				writeErr(w, http.StatusUnauthorized, "Missing or invalid web3 authorization")
				return
			}
			if !admins[addr] {
				writeErr(w, http.StatusForbidden, "wallet is not an i18n administrator")
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

// actor returns the verified wallet from the auth context. Handlers never
// trust an actor supplied in the request body.
func actor(req *http.Request) string {
	return strings.ToLower(auth.AddressFromContext(req.Context()))
}

func decodeBody(w http.ResponseWriter, req *http.Request, dst any) bool {
	req.Body = http.MaxBytesReader(w, req.Body, 1<<20) // 1 MiB bound
	dec := json.NewDecoder(req.Body)
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeServiceErr(w http.ResponseWriter, err error) {
	var vErr *ValidationError
	switch {
	case errors.As(err, &vErr):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"statusCode": http.StatusUnprocessableEntity,
			"message":    "validation failed",
			"issues":     vErr.Issues,
		})
	case errors.Is(err, ErrVersionConflict):
		writeErr(w, http.StatusConflict, "draft changed since you loaded it — reload and retry")
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrLocaleDisabled):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrLocaleExists):
		writeErr(w, http.StatusConflict, "locale already exists")
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"statusCode": status, "message": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func atoiDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
