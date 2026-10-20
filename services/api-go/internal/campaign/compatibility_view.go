package campaign

import (
	"context"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

func publicCompatibilityView(value application.PublicCampaignView, baseline *v2.LegacyBaseline) (Campaign, error) {
	if value.ContentState != "ready" && baseline != nil {
		return baselineCompatibilityView(value, *baseline)
	}
	if value.ContentState != "ready" || value.Content == nil || value.Mode != "enabled" || value.EffectivePhase == "" || validStatus(value.EffectivePhase) != nil {
		return Campaign{}, compatibilityUnsupported("legacy DTO cannot express content or publication mode")
	}
	count, err := compatibleCount(value)
	if err != nil {
		return Campaign{}, err
	}
	terms := value.Content.MaterialTerms
	start, err := time.Parse(time.RFC3339, terms.StartAt)
	if err != nil {
		return Campaign{}, compatibilityUnavailable("published start time invalid")
	}
	end, err := time.Parse(time.RFC3339, terms.EndAt)
	if err != nil || !end.After(start) {
		return Campaign{}, compatibilityUnavailable("published end time invalid")
	}
	copy := terms.Copy.En
	// The public port intentionally has no database lifecycle timestamps or
	// media URL resolver. Neither may be invented from a request clock or ID.
	return Campaign{ID: value.ID, Title: copy.Title, Description: &copy.Description,
		StartDate: start, EndDate: end, Status: value.EffectivePhase, Reward: &copy.RewardDescription,
		Participants: int(count), ParticipantCount: int(count)}, nil
}

func compatibleCount(value application.PublicCampaignView) (int64, error) {
	if value.StatisticsState != "ready" || value.ParticipantCount == nil {
		return 0, compatibilityUnavailable("participant count is unknown")
	}
	count, err := strconv.ParseInt(*value.ParticipantCount, 10, strconv.IntSize)
	if err != nil || count < 0 || count > 1<<53-1 || strconv.FormatInt(count, 10) != *value.ParticipantCount {
		return 0, compatibilityUnavailable("participant count cannot be represented")
	}
	return count, nil
}

// baselineCompatibilityView answers an imported legacy campaign exactly as the
// legacy repository answered its row: every original value comes from the
// verified baseline (metadata through the same decoder), the phase and the
// live participant count from the versioned model.
func baselineCompatibilityView(value application.PublicCampaignView, baseline v2.LegacyBaseline) (Campaign, error) {
	if baseline.CampaignID != value.ID || baseline.RevisionID != value.RevisionID ||
		baseline.MaterialTermsHash != value.MaterialTermsHash || baseline.ContentHash != value.ContentHash {
		return Campaign{}, compatibilityUnavailable("legacy baseline does not match the published revision")
	}
	if value.Mode != "enabled" || value.EffectivePhase == "" || validStatus(value.EffectivePhase) != nil {
		return Campaign{}, compatibilityUnsupported("legacy DTO cannot express publication mode")
	}
	count, err := compatibleCount(value)
	if err != nil {
		return Campaign{}, err
	}
	terms, record := baseline.Input.MaterialTerms.Legacy, baseline.Input.LegacyRecord
	created, err := v2.ParseLegacyTime(record.CreatedAt)
	if err != nil {
		return Campaign{}, compatibilityUnavailable("legacy baseline creation time invalid")
	}
	updated, err := v2.ParseLegacyTime(record.UpdatedAt)
	if err != nil {
		return Campaign{}, compatibilityUnavailable("legacy baseline update time invalid")
	}
	var metadata any
	if record.MetadataJSON != nil {
		metadata = decodeMetadata([]byte(*record.MetadataJSON))
	}
	return Campaign{ID: value.ID, Title: terms.Title, Description: terms.Description, Banner: record.Banner,
		StartDate: baseline.Start, EndDate: baseline.End, Status: value.EffectivePhase, Reward: terms.Reward,
		Participants: int(record.Participants), Metadata: metadata, CreatedAt: &created, UpdatedAt: &updated,
		ParticipantCount: int(count)}, nil
}

func (p *publishedCompatibility) mapViews(ctx context.Context, views []application.PublicCampaignView, baselines baselineSet, wallet string) ([]Campaign, error) {
	out := make([]Campaign, 0, len(views))
	ids := make([]string, 0, len(views))
	for _, view := range views {
		row, err := publicCompatibilityView(view, baselines.of(view))
		if err != nil {
			return nil, err
		}
		out, ids = append(out, row), append(ids, row.ID)
	}
	if wallet == "" || len(ids) == 0 {
		return out, nil
	}
	value, err := p.reader.Me(ctx, wallet, ids)
	if err != nil {
		return nil, err
	}
	if err = validateReadContract("QueryMeResponse", value); err != nil {
		return nil, err
	}
	states := map[string]application.MyCampaignView{}
	for _, row := range value.Items {
		if _, duplicate := states[row.ID]; duplicate {
			return nil, compatibilityUnavailable("duplicate personal response")
		}
		states[row.ID] = row
	}
	if len(states) != len(ids) {
		return nil, compatibilityUnavailable("personal response incomplete")
	}
	for i, view := range views {
		state, ok := states[view.ID]
		if !ok || state.PublicationVersion != view.PublicationVersion || state.InteractionVersion != view.InteractionVersion {
			return nil, compatibilityUnavailable("public and personal snapshots changed")
		}
		joined, err := compatiblePersonal(state.Participation, "joined", "left")
		if err != nil {
			return nil, err
		}
		reminded, err := compatiblePersonal(state.Reminder, "set", "removed")
		if err != nil {
			return nil, err
		}
		out[i].IsParticipating, out[i].HasReminder = &joined, &reminded
	}
	return out, nil
}

func compatiblePersonal(value application.PersonalStateView, positive, negative string) (bool, error) {
	if value.State == "absent" && value.StateVersion != nil && *value.StateVersion == "0" && value.Status == nil && value.AcceptanceProvenance == nil && value.AcceptedRevisionID == nil && value.AcceptedPublicationVersion == nil && value.AcceptedTermsHash == nil && value.AcceptedAt == nil && value.ChangedAt == nil {
		return false, nil
	}
	if value.State != "known" || value.StateVersion == nil || value.Status == nil || value.AcceptanceProvenance == nil || value.ChangedAt == nil {
		return false, compatibilityUnavailable("personal state is unknown")
	}
	version, err := strconv.ParseUint(*value.StateVersion, 10, 63)
	if err != nil || version == 0 || strconv.FormatUint(version, 10) != *value.StateVersion {
		return false, compatibilityUnavailable("personal version invalid")
	}
	if *value.Status != positive && *value.Status != negative {
		return false, compatibilityUnavailable("personal status is unknown")
	}
	switch *value.AcceptanceProvenance {
	case "verified":
		if value.AcceptedRevisionID == nil || value.AcceptedPublicationVersion == nil || value.AcceptedTermsHash == nil || value.AcceptedAt == nil {
			return false, compatibilityUnavailable("personal acceptance incomplete")
		}
		acceptedVersion, err := strconv.ParseUint(*value.AcceptedPublicationVersion, 10, 63)
		if err != nil || acceptedVersion == 0 || strconv.FormatUint(acceptedVersion, 10) != *value.AcceptedPublicationVersion {
			return false, compatibilityUnavailable("accepted publication version invalid")
		}
	case "neverAccepted":
		if *value.Status == positive || value.AcceptedRevisionID != nil || value.AcceptedPublicationVersion != nil || value.AcceptedTermsHash != nil || value.AcceptedAt != nil {
			return false, compatibilityUnavailable("personal tombstone invalid")
		}
	case "legacyUnknown":
		// An imported legacy state: participation without proven terms.
		if value.AcceptedRevisionID != nil || value.AcceptedPublicationVersion != nil || value.AcceptedTermsHash != nil {
			return false, compatibilityUnavailable("legacy acceptance invalid")
		}
	default:
		return false, compatibilityUnavailable("legacy DTO cannot express acceptance provenance")
	}
	return *value.Status == positive, nil
}
