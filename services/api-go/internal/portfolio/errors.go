package portfolio

import (
	"errors"
	"net/http"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/settings"
)

var ErrSourcesUnavailable = errors.New("portfolio asset sources unavailable")

type visibilityReadError struct{ cause error }

func (e *visibilityReadError) Error() string {
	return "portfolio visibility unavailable: " + e.cause.Error()
}
func (e *visibilityReadError) Unwrap() error { return e.cause }

func writeInventoryError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "PORTFOLIO_READ_FAILED"
	message := "failed to load portfolio"
	var visibilityErr *visibilityReadError
	switch {
	case errors.As(err, &visibilityErr):
		code = "PORTFOLIO_VISIBILITY_UNAVAILABLE"
		message = "portfolio visibility rules unavailable"
		if errors.Is(err, settings.ErrPoolUnavailable) {
			status = http.StatusServiceUnavailable
		}
	case errors.Is(err, ErrSourcesUnavailable):
		status = http.StatusServiceUnavailable
		code = "PORTFOLIO_SOURCES_UNAVAILABLE"
		message = "portfolio asset sources unavailable"
	}
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
