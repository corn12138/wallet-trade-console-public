package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

// PublishedReader is the only versioned read boundary. A legacy adapter never
// reads revision, publication or personal-state tables directly. Baselines
// returns the original values of imported legacy campaigns, which the public
// base contract does not carry (docs/campaign-legacy-activation-protocol-2026-10-09.md §1.2).
// The adapter always reads the base representation (zero Variants).
type PublishedReader interface {
	List(context.Context, application.ListQuery) (application.ListResponse, error)
	Detail(context.Context, string, v2.Variants) (application.DetailResponse, error)
	Me(context.Context, string, []string) (application.MeResponse, error)
	Baselines(context.Context, []application.RevisionRef) (map[application.RevisionRef]v2.LegacyBaseline, error)
}

type publishedCompatibility struct{ reader PublishedReader }

// NewCompatibleService keeps the bounded v2 sortOrder/ID prefix ahead of the
// legacy startDate-descending/ID prefix. The old API has no pagination cursor;
// detail remains addressable by stable ID for records outside this prefix.
// The reader is required independently of the versioned write rollout flag.
func NewCompatibleService(repo *Repository, reader PublishedReader) *Service {
	return &Service{repo: repo, compat: &publishedCompatibility{reader: reader}}
}

func compatibilityUnavailable(detail string) error {
	return &v2.DomainError{Code: "CAMPAIGN_COMPATIBILITY_UNAVAILABLE", Status: 503, Detail: detail}
}
func compatibilityUnsupported(detail string) error {
	return &v2.DomainError{Code: "CAMPAIGN_CLIENT_UPGRADE_REQUIRED", Status: 422, Detail: detail}
}
func validateReadContract(name string, value any) error {
	raw, err := json.Marshal(value)
	if err == nil {
		var checked any
		err = v2.DecodeContractResponse(name, raw, &checked)
	}
	if err != nil {
		return compatibilityUnavailable("versioned read contract invalid")
	}
	return nil
}
func (p *publishedCompatibility) list(ctx context.Context, svc *Service, phase string, limit int, wallet string) ([]Campaign, error) {
	if p.reader == nil {
		return nil, compatibilityUnavailable("published reader required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	views, baselines, hidden, err := p.publicPrefix(ctx, phase, limit)
	if err != nil {
		return nil, err
	}
	out, err := p.mapViews(ctx, views, baselines, wallet)
	if err != nil {
		return nil, err
	}
	if len(out) < limit {
		rows, err := svc.repo.ListAll(ctx, phase, limit-len(out))
		if err != nil {
			return nil, err
		}
		rows, err = svc.enrichForWallet(ctx, rows, wallet)
		if err != nil {
			return nil, err
		}
		// Omitted versioned campaigns still own their IDs: a legacy row with
		// one of them is an ownership conflict, not a campaign to show.
		seen := map[string]bool{}
		for _, row := range out {
			seen[row.ID] = true
		}
		for _, id := range hidden {
			seen[id] = true
		}
		for _, row := range rows {
			if seen[row.ID] || validStatus(row.Status) != nil || row.Status == "" || (phase != "" && row.Status != phase) {
				return nil, compatibilityUnavailable("legacy ownership or phase changed")
			}
			seen[row.ID] = true
			out = append(out, row)
		}
	}
	if len(out) > limit || ctx.Err() != nil {
		return nil, compatibilityUnavailable("legacy read budget exceeded")
	}
	return out, nil
}

// listScanBudget bounds how many versioned campaigns one legacy list request
// may examine while skipping ones the old DTO cannot express.
const listScanBudget = 4 * maxListLimit

// publicPrefix collects up to limit representable published campaigns in
// version-store order and returns the IDs it omitted. The legacy list protocol
// (docs/campaign-legacy-list-protocol-2026-10-09.md):
//   - representable (enabled, renderable content, upcoming/active/ended phase,
//     known count): listed;
//   - unrepresentable but known (paused, archived, unsupported content):
//     omitted without consuming the limit, so later campaigns still appear;
//     detail answers CAMPAIGN_CLIENT_UPGRADE_REQUIRED and personal history and
//     cancellation stay on the versioned endpoints;
//   - unknown or invalid (counts, times, snapshot or contract drift): the whole
//     request fails with 503 rather than hiding data;
//   - scan or page budget exhausted before the prefix is complete: 503, never
//     a shorter list presented as complete.
//
// The source is asked for LegacyRepresentable rows, so paused and archived
// campaigns are filtered in SQL and never consume the scan budget; the in-loop
// skip remains for what only Go can judge (a template this build cannot
// render). Legacy rows are appended only when the versioned source is
// exhausted, which keeps every versioned campaign ahead of every legacy one.
func (p *publishedCompatibility) publicPrefix(ctx context.Context, phase string, limit int) ([]application.PublicCampaignView, baselineSet, []string, error) {
	views := []application.PublicCampaignView{}
	kept := baselineSet{}
	hidden := []string{}
	cursor := ""
	scanned := 0
	seenIDs, seenCursors := map[string]bool{}, map[string]bool{}
	for page := 0; page < maxListLimit; page++ {
		if ctx.Err() != nil {
			return nil, nil, nil, compatibilityUnavailable("published read deadline exceeded")
		}
		need := limit - len(views)
		value, err := p.reader.List(ctx, application.ListQuery{Phase: phase, Limit: need, Cursor: cursor, LegacyRepresentable: true})
		if err != nil {
			return nil, nil, nil, err
		}
		if err = validateReadContract("QueryListResponse", value); err != nil {
			return nil, nil, nil, err
		}
		if len(value.Items) > need {
			return nil, nil, nil, compatibilityUnavailable("published page exceeds requested limit")
		}
		baselines, err := p.baselines(ctx, value.Items)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, row := range value.Items {
			if seenIDs[row.ID] || (phase != "" && row.EffectivePhase != phase) {
				return nil, nil, nil, compatibilityUnavailable("published pagination identity or phase changed")
			}
			seenIDs[row.ID] = true
			scanned++
			keep, err := listable(row, baselines.of(row))
			if err != nil {
				return nil, nil, nil, err
			}
			if keep {
				views = append(views, row)
				if baseline := baselines.of(row); baseline != nil {
					kept[application.RevisionRef{CampaignID: row.ID, RevisionID: row.RevisionID}] = *baseline
				}
			} else {
				hidden = append(hidden, row.ID)
			}
		}
		if len(views) == limit || value.NextCursor == nil {
			return views, kept, hidden, nil
		}
		if scanned >= listScanBudget {
			return nil, nil, nil, compatibilityUnavailable("legacy list scan budget exhausted before the requested prefix was complete")
		}
		next := *value.NextCursor
		if len(value.Items) == 0 || next == "" || next == cursor || seenCursors[next] {
			return nil, nil, nil, compatibilityUnavailable("published cursor does not advance")
		}
		seenCursors[next] = true
		cursor = next
	}
	return nil, nil, nil, compatibilityUnavailable("published page budget exceeded")
}

// baselineSet holds the verified legacy baselines of the views being mapped.
type baselineSet map[application.RevisionRef]v2.LegacyBaseline

func (b baselineSet) of(view application.PublicCampaignView) *v2.LegacyBaseline {
	baseline, ok := b[application.RevisionRef{CampaignID: view.ID, RevisionID: view.RevisionID}]
	if !ok {
		return nil
	}
	return &baseline
}

// baselines fetches the legacy baselines of views whose content the public
// contract cannot carry; views with renderable content need none.
func (p *publishedCompatibility) baselines(ctx context.Context, views []application.PublicCampaignView) (baselineSet, error) {
	refs := []application.RevisionRef{}
	for _, view := range views {
		if view.ContentState != "ready" {
			refs = append(refs, application.RevisionRef{CampaignID: view.ID, RevisionID: view.RevisionID})
		}
	}
	if len(refs) == 0 {
		return baselineSet{}, nil
	}
	found, err := p.reader.Baselines(ctx, refs)
	if err != nil {
		return nil, err
	}
	out := baselineSet{}
	for ref, baseline := range found {
		if baseline.CampaignID != ref.CampaignID || baseline.RevisionID != ref.RevisionID {
			return nil, compatibilityUnavailable("legacy baseline identity mismatch")
		}
		out[ref] = baseline
	}
	return out, nil
}

func (p *publishedCompatibility) detail(ctx context.Context, svc *Service, id, wallet string) (Campaign, error) {
	if p.reader == nil {
		return Campaign{}, compatibilityUnavailable("published reader required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	value, err := p.reader.Detail(ctx, id, v2.Variants{})
	if err == nil {
		if err = validateReadContract("QueryDetailResponse", value); err != nil {
			return Campaign{}, err
		}
		if value.Item.ID != id {
			return Campaign{}, compatibilityUnavailable("published detail identity mismatch")
		}
		baselines, err := p.baselines(ctx, []application.PublicCampaignView{value.Item})
		if err != nil {
			return Campaign{}, err
		}
		rows, err := p.mapViews(ctx, []application.PublicCampaignView{value.Item}, baselines, wallet)
		if err != nil {
			return Campaign{}, err
		}
		if ctx.Err() != nil {
			return Campaign{}, compatibilityUnavailable("published read deadline exceeded")
		}
		return rows[0], nil
	}
	var domain *v2.DomainError
	if !errors.As(err, &domain) || domain.Code != "CAMPAIGN_NOT_FOUND" || domain.Status != 404 {
		return Campaign{}, err
	}
	row, err := svc.repo.FindByID(ctx, id)
	if err != nil {
		return Campaign{}, err
	}
	if row.ID != id || row.Status == "" || validStatus(row.Status) != nil {
		return Campaign{}, compatibilityUnavailable("legacy detail identity or phase invalid")
	}
	rows, err := svc.enrichForWallet(ctx, []Campaign{row}, wallet)
	if err != nil {
		return Campaign{}, err
	}
	if ctx.Err() != nil {
		return Campaign{}, compatibilityUnavailable("legacy read deadline exceeded")
	}
	return rows[0], nil
}

// listable reports whether the legacy DTO can express a published campaign.
// Only CAMPAIGN_CLIENT_UPGRADE_REQUIRED (a known state the old DTO has no field
// for) is omitted; every other mapping failure is returned and fails the read.
func listable(view application.PublicCampaignView, baseline *v2.LegacyBaseline) (bool, error) {
	_, err := publicCompatibilityView(view, baseline)
	var domain *v2.DomainError
	if errors.As(err, &domain) && domain.Code == "CAMPAIGN_CLIENT_UPGRADE_REQUIRED" {
		return false, nil
	}
	return err == nil, err
}
