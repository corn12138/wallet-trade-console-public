package campaignhttp

import (
	"net/http"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/go-chi/chi/v5"
)

func (h handler) list(w http.ResponseWriter, r *http.Request) {
	q, err := query(r, "phase", "cursor", "limit")
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	n, err := limit(q)
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	variants := negotiate(w, r)
	value, err := h.Queries.List(r.Context(), application.ListQuery{Phase: q.Get("phase"), Cursor: q.Get("cursor"), Limit: n, Variants: variants})
	if err == nil {
		err = checkPublicViews(value.Items, variants)
	}
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	writeContract(w, r, "QueryListResponse", value, http.StatusOK)
}
func (h handler) detail(w http.ResponseWriter, r *http.Request) {
	if _, err := query(r); err != nil {
		writeFailure(w, r, err)
		return
	}
	variants := negotiate(w, r)
	value, err := h.Queries.Detail(r.Context(), chi.URLParam(r, "id"), variants)
	if err == nil {
		err = checkPublicViews([]application.PublicCampaignView{value.Item}, variants)
	}
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	writeContract(w, r, "QueryDetailResponse", value, http.StatusOK)
}
func (h handler) me(w http.ResponseWriter, r *http.Request) {
	q, err := query(r, "ids")
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	value, err := h.Queries.Me(r.Context(), auth.AddressFromContext(r.Context()), strings.Split(q.Get("ids"), ","))
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	writeContract(w, r, "QueryMeResponse", value, http.StatusOK)
}
func (h handler) history(w http.ResponseWriter, r *http.Request) {
	q, err := query(r, "cursor", "limit")
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	n, err := limit(q)
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	variants := negotiate(w, r)
	value, err := h.Queries.History(r.Context(), auth.AddressFromContext(r.Context()), application.HistoryQuery{Cursor: q.Get("cursor"), Limit: n, Variants: variants})
	if err == nil && v2.CheckHistoryResponse(value, variants) != nil {
		err = unavailable("CAMPAIGN_RESPONSE_INVALID")
	}
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	writeContract(w, r, "QueryHistoryResponse", value, http.StatusOK)
}
func (h handler) preview(w http.ResponseWriter, r *http.Request) {
	if _, err := query(r); err != nil {
		writeFailure(w, r, err)
		return
	}
	value, err := h.Queries.Preview(r.Context(), auth.AddressFromContext(r.Context()), chi.URLParam(r, "id"), chi.URLParam(r, "revisionId"))
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	writeContract(w, r, "QueryPreviewResponse", value, http.StatusOK)
}
func (h handler) commandQuery(admin, collection bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, err := query(r, "operation")
		if err != nil {
			writeFailure(w, r, err)
			return
		}
		op, target := q.Get("operation"), chi.URLParam(r, "id")
		valid := false
		if collection {
			valid = op == "create"
			target = "campaigns"
		} else if admin {
			switch op {
			case "revision", "publish", "pause", "resume", "archive", "rollback":
				valid = true
			}
		} else {
			switch op {
			case "join", "leave", "set-reminder", "remove-reminder":
				valid = true
			}
		}
		if !valid {
			writeFailure(w, r, v2.InvalidInput("invalid command operation"))
			return
		}
		value, err := h.Queries.Command(r.Context(), auth.AddressFromContext(r.Context()), target, op, chi.URLParam(r, "commandId"))
		if err != nil {
			writeFailure(w, r, err)
			return
		}
		writeCommand(w, r, op, value, http.StatusOK)
	}
}
func (h handler) adminCampaign(w http.ResponseWriter, r *http.Request) {
	if _, err := query(r); err != nil {
		writeFailure(w, r, err)
		return
	}
	value, err := h.Queries.AdminCampaign(r.Context(), auth.AddressFromContext(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	writeContract(w, r, "AdminCampaignResponse", value, http.StatusOK)
}

// negotiate reads the client's advertised response variants. The response
// varies on the header even when it is absent or ignored.
func negotiate(w http.ResponseWriter, r *http.Request) v2.Variants {
	w.Header().Add("Vary", v2.VariantsHeader)
	return v2.ParseVariants(r.Header.Values(v2.VariantsHeader))
}

// checkPublicViews is the last guard before the bytes reach a strict client: a
// variant shape the client did not negotiate fails the request instead of the
// client's whole decoder.
func checkPublicViews(items []application.PublicCampaignView, variants v2.Variants) error {
	for _, item := range items {
		if v2.CheckPublicCampaignView(item, variants) != nil {
			return unavailable("CAMPAIGN_RESPONSE_INVALID")
		}
	}
	return nil
}
