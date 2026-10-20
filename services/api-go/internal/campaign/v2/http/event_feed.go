package campaignhttp

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (h handler) events(private bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		allowed := []string{"cursor", "limit"}
		if !private {
			allowed = append(allowed, "family")
		}
		q, err := query(r, allowed...)
		if err != nil {
			writeFailure(w, r, err)
			return
		}
		n, err := limit(q)
		if err != nil {
			writeFailure(w, r, err)
			return
		}
		if h.Events == nil {
			writeFailure(w, r, unavailable("CAMPAIGN_UNAVAILABLE"))
			return
		}
		family := q.Get("family")
		if private {
			family = "personal"
		}
		value, err := h.Events.Read(r.Context(), chi.URLParam(r, "id"), family, q.Get("cursor"), n, private)
		if err != nil {
			writeFailure(w, r, err)
			return
		}
		writeContract(w, r, "EventFeedResponse", value, http.StatusOK)
	}
}
