package application

import (
	"context"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

type ListQuery struct {
	Phase, Cursor string
	Limit         int
	// LegacyRepresentable restricts the page to enabled campaigns with
	// recognized content, the only ones the legacy DTO can express. It is set
	// by the in-process compatibility adapter only; HTTP never exposes it. The
	// filter runs in SQL so archived campaigns, which accumulate forever, never
	// consume the adapter's bounded scan budget.
	LegacyRepresentable bool
	// Variants are the response representations the client negotiated. The
	// zero value is the base representation every strict client decodes.
	Variants v2.Variants
}

// ListScope binds a list cursor to its filter, so a cursor from the public
// list cannot continue a legacy-representable list or the reverse.
func ListScope(query ListQuery) string {
	if query.LegacyRepresentable {
		return "legacy-representable"
	}
	return ""
}

type HistoryQuery struct {
	Cursor   string
	Limit    int
	Variants v2.Variants
}

// Every read method owns one repeatable-read, read-only snapshot and DB clock.
// Actors are explicit only on private ports; public queries have no owner input.
type ReadStore interface {
	List(context.Context, ListQuery, v2.Capabilities) (ListResponse, error)
	Detail(context.Context, string, v2.Variants, v2.Capabilities) (DetailResponse, error)
	Me(context.Context, string, []string) (MeResponse, error)
	History(context.Context, string, HistoryQuery) (HistoryResponse, error)
	CommandResult(context.Context, CommandKey) (CommandResponse, error)
	Preview(context.Context, string, string, v2.Capabilities) (PreviewResponse, error)
	AdminCampaign(context.Context, string, v2.Capabilities) (AdminCampaignResponse, error)
	Baselines(context.Context, []RevisionRef) (map[RevisionRef]v2.LegacyBaseline, error)
}

// RevisionRef names one immutable revision of a campaign.
type RevisionRef struct{ CampaignID, RevisionID string }
