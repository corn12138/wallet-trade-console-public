package application

import v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"

type PublicCampaignView = v2.QueryPublicCampaignView
type PersonalStateView = v2.QueryPersonalStateView
type MyCampaignView = v2.QueryMyCampaignView
type HistoryView = v2.QueryHistoryView
type ListResponse = v2.QueryListResponse
type DetailResponse = v2.QueryDetailResponse
type MeResponse = v2.QueryMeResponse
type HistoryResponse = v2.QueryHistoryResponse
type PreviewView = v2.QueryPreviewView
type PreviewResponse = v2.QueryPreviewResponse

type CommandResponse struct {
	// Identity is the ledger row this response was read from. It is filled by
	// the store and exposed only on administrator responses.
	Identity      CommandIdentity `json:"-"`
	Replayed      bool            `json:"replayed"`
	SchemaVersion int             `json:"schemaVersion"`
	ServerNow     string          `json:"serverNow"`
	AsOf          string          `json:"asOf"`
	Original      Result          `json:"original"`
	Current       Result          `json:"current"`
}
type AdminRevisionSummary = v2.AdminRevisionSummary
type AdminCampaignView = v2.AdminCampaignView
type AdminCampaignResponse = v2.AdminCampaignResponse
