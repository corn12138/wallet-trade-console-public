package campaignhttp

import (
	"context"
	"net/http"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/go-chi/chi/v5"
)

type Commander interface {
	Execute(context.Context, string, application.Command) (application.Outcome, error)
}
type Reader interface {
	List(context.Context, application.ListQuery) (application.ListResponse, error)
	Detail(context.Context, string, v2.Variants) (application.DetailResponse, error)
	Me(context.Context, string, []string) (application.MeResponse, error)
	History(context.Context, string, application.HistoryQuery) (application.HistoryResponse, error)
	Command(context.Context, string, string, string, string) (application.CommandResponse, error)
	Preview(context.Context, string, string, string) (application.PreviewResponse, error)
	AdminCampaign(context.Context, string, string) (application.AdminCampaignResponse, error)
}
type EventReader interface {
	Read(context.Context, string, string, string, int, bool) (v2.EventFeedResponse, error)
}
type Config struct {
	Events   EventReader
	Commands Commander
	Queries  Reader
	Verifier *auth.Verifier
	Enabled  bool
	// Legacy and Access serve the controlled legacy import's administration;
	// Access re-checks the session and administrator grant on every step.
	Legacy LegacyImports
	Access application.Authorizer
}
type handler struct{ Config }

func Router(config Config) chi.Router {
	h := handler{config}
	r := chi.NewRouter()
	r.Use(noStore)
	r.Use(withRequestID)
	r.Group(func(public chi.Router) {
		public.Use(h.ready)
		public.Get("/campaigns", h.list)
		public.Get("/campaigns/{id}", h.detail)
		public.Get("/campaigns/{id}/events", h.events(false))
	})
	r.Group(func(private chi.Router) {
		private.Use(auth.StrictMiddlewareWithFailure(config.Verifier, func(w http.ResponseWriter, r *http.Request, err error) {
			writeFailure(w, r, authFailure(err))
		}))
		private.Use(h.ready)
		private.Get("/campaigns/me", h.me)
		private.Get("/campaigns/{id}/me/events", h.events(true))
		private.Get("/campaigns/me/history", h.history)
		private.Get("/campaigns/{id}/commands/{commandId}", h.commandQuery(false, false))
		private.Post("/campaigns/{id}/join", h.interact("join"))
		private.Delete("/campaigns/{id}/join", h.interact("leave"))
		private.Post("/campaigns/{id}/reminder", h.interact("set-reminder"))
		private.Delete("/campaigns/{id}/reminder", h.interact("remove-reminder"))
		private.Post("/campaign-admin/campaigns", h.create)
		private.Get("/campaign-admin/campaigns/{id}", h.adminCampaign)
		private.Post("/campaign-admin/campaigns/{id}/revisions", h.revision)
		private.Post("/campaign-admin/campaigns/{id}/publications", h.publication)
		private.Get("/campaign-admin/campaigns/{id}/revisions/{revisionId}/preview", h.preview)
		private.Get("/campaign-admin/campaigns/{id}/commands/{commandId}", h.commandQuery(true, false))
		private.Get("/campaign-admin/commands/{commandId}", h.commandQuery(true, true))
		private.Get("/campaign-admin/legacy-imports/runs/{runId}", h.legacyPlan)
		private.Post("/campaign-admin/legacy-imports/runs/{runId}/campaigns/{id}/review", h.legacyReview)
		private.Post("/campaign-admin/legacy-imports/runs/{runId}/campaigns/{id}/activation", h.legacyActivate)
		private.Get("/campaign-admin/legacy-imports/campaigns/{id}/activation", h.legacyActivation)
	})
	return r
}
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
func (h handler) ready(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.Enabled || h.Queries == nil {
			writeFailure(w, r, unavailable("CAMPAIGN_UNAVAILABLE"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
