package campaignhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"unicode"
	"unicode/utf8"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/go-chi/chi/v5/middleware"
)

func unavailable(code string) error {
	return &v2.DomainError{Code: code, Status: http.StatusServiceUnavailable}
}

type requestIDKey struct{}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := normalizedRequestID(r)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}
func normalizedRequestID(r *http.Request) string {
	id := middleware.GetReqID(r.Context())
	valid := id != "" && len(id) <= 96 && utf8.ValidString(id)
	for _, ch := range id {
		if unicode.IsControl(ch) {
			valid = false
		}
	}
	if !valid {
		id, _ = application.NewID()
	}
	return id
}
func writeFailure(w http.ResponseWriter, r *http.Request, err error) {
	code, status := "CAMPAIGN_UNAVAILABLE", http.StatusServiceUnavailable
	var domain *v2.DomainError
	if errors.As(err, &domain) && domain.Status >= 400 && domain.Status <= 599 {
		code, status = domain.Code, domain.Status
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v2.ErrorResponse{SchemaVersion: 1, Code: code, RequestID: requestID(r)})
}
func writeContract(w http.ResponseWriter, r *http.Request, name string, value any, status int) {
	raw, err := json.Marshal(value)
	if err == nil {
		var checked any
		err = v2.DecodeContractResponse(name, raw, &checked)
	}
	if err != nil {
		writeFailure(w, r, unavailable("CAMPAIGN_RESPONSE_INVALID"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
func decodeBody(w http.ResponseWriter, r *http.Request, name string, target any) error {
	if r.URL.RawQuery != "" {
		return v2.InvalidInput("query parameters not accepted")
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return &v2.DomainError{Code: "CAMPAIGN_CONTENT_TYPE", Status: http.StatusUnsupportedMediaType}
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return &v2.DomainError{Code: "CAMPAIGN_BODY_TOO_LARGE", Status: http.StatusRequestEntityTooLarge}
		}
		return v2.InvalidInput("body unreadable")
	}
	return v2.DecodeContract(name, raw, target)
}
func query(r *http.Request, allowed ...string) (url.Values, error) {
	if len(r.URL.RawQuery) > 4096 {
		return nil, v2.InvalidInput("query too large")
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, v2.InvalidInput("invalid query")
	}
	for key, entries := range values {
		found := false
		for _, name := range allowed {
			if key == name {
				found = true
			}
		}
		if !found || len(entries) != 1 {
			return nil, v2.InvalidInput("unknown or duplicate query parameter")
		}
	}
	return values, nil
}
func limit(values url.Values) (int, error) {
	if !values.Has("limit") {
		return 20, nil
	}
	n, err := strconv.Atoi(values.Get("limit"))
	if err != nil || n < 1 || n > 50 || strconv.Itoa(n) != values.Get("limit") {
		return 0, v2.InvalidInput("invalid limit")
	}
	return n, nil
}
func version(raw string) (*uint64, error) { return application.ParseVersion(raw) }
