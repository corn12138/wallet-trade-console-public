package v2

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Response variants a client negotiates with the X-Campaign-Variants header
// (docs/campaign-legacy-presentation-protocol-2026-10-12.md §2). Strict
// clients reject unknown keys and enum values, so a variant shape is sent only
// to a client that asked for it; the zero value is the base representation.
const (
	VariantsHeader             = "X-Campaign-Variants"
	VariantLegacyBaseline      = "legacyBaselineV1"
	VariantLegacyImportHistory = "legacyImportHistoryV1"
	// VariantContentCapabilities marks a client that checks media and trade
	// targets against its own registries before offering new admission.
	VariantContentCapabilities = "contentCapabilitiesV1"
	ContentStateLegacyBaseline = "legacyBaseline"
	HistoryEntryCommand        = "command"
	HistoryEntryLegacyImport   = "legacyImport"
)

const (
	variantHeaderMaxBytes  = 512
	variantHeaderMaxTokens = 16
)

type Variants struct {
	LegacyBaseline      bool
	LegacyImportHistory bool
	ContentCapabilities bool
}

var variantTokenPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)

// ParseVariants reads every value of the header. Unknown well-formed tokens
// are ignored so a newer client can advertise more than this server knows; a
// malformed header is ignored as a whole. Neither is an error: the response
// falls back to the base representation.
func ParseVariants(values []string) Variants {
	var result Variants
	joined := strings.Join(values, ",")
	if len(joined) > variantHeaderMaxBytes {
		return Variants{}
	}
	tokens := 0
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			token = strings.Trim(token, " \t")
			if token == "" {
				continue
			}
			tokens++
			if tokens > variantHeaderMaxTokens || !variantTokenPattern.MatchString(token) {
				return Variants{}
			}
			switch token {
			case VariantLegacyBaseline:
				result.LegacyBaseline = true
			case VariantLegacyImportHistory:
				result.LegacyImportHistory = true
			case VariantContentCapabilities:
				result.ContentCapabilities = true
			}
		}
	}
	return result
}

func variantViolation(detail string) error {
	return errors.New("campaign response variant shape: " + detail)
}

// CheckPublicCampaignView enforces the content cross-field rules the schema
// cannot express: a baseline object exactly with contentState legacyBaseline,
// and that state only when the variant is allowed; ready media or trade
// content only for a client that checks those capabilities itself.
func CheckPublicCampaignView(view QueryPublicCampaignView, allowed Variants) error {
	switch view.ContentState {
	case "ready":
		if view.Content == nil || view.Baseline != nil {
			return variantViolation("ready content must carry content only")
		}
		if !allowed.ContentCapabilities && (view.Content.DisplayContent.MediaAssetID != nil || view.Content.Cta.ActionKind != "none") {
			return variantViolation("media or trade content without the negotiated capability check")
		}
	case "unsupported":
		if view.Content != nil || view.Baseline != nil {
			return variantViolation("unsupported content must carry neither content nor baseline")
		}
	case ContentStateLegacyBaseline:
		if !allowed.LegacyBaseline {
			return variantViolation("baseline content without the negotiated variant")
		}
		if view.Content != nil || view.Baseline == nil {
			return variantViolation("baseline content must carry the baseline only")
		}
		start, startErr := time.Parse(time.RFC3339Nano, view.Baseline.StartAt)
		end, endErr := time.Parse(time.RFC3339Nano, view.Baseline.EndAt)
		if startErr != nil || endErr != nil || !end.After(start) {
			return variantViolation("baseline window")
		}
	default:
		return variantViolation("unknown content state")
	}
	return nil
}

// CheckHistoryResponse enforces the history entry rules: without the variant
// no item carries an entry discriminator; with it, either every item does (a
// variant-aware server) or none does (a server that ignored the header), and
// an imported entry never presents an administrator's command as the user's.
func CheckHistoryResponse(response QueryHistoryResponse, allowed Variants) error {
	tagged := 0
	for _, item := range response.Items {
		if item.Entry != nil {
			tagged++
		}
	}
	if tagged != 0 && (!allowed.LegacyImportHistory || tagged != len(response.Items)) {
		return variantViolation("history entries must all or none carry the negotiated discriminator")
	}
	for _, item := range response.Items {
		if err := checkHistoryView(item); err != nil {
			return err
		}
	}
	return nil
}

func checkHistoryView(item QueryHistoryView) error {
	if item.Entry == nil || *item.Entry == HistoryEntryCommand {
		if item.Operation == nil || item.ClientCommandID == nil || item.LegacyImport != nil {
			return variantViolation("command history entry")
		}
		return nil
	}
	if *item.Entry != HistoryEntryLegacyImport {
		return variantViolation("unknown history entry")
	}
	state, imported := item.State, item.LegacyImport
	positive := "joined"
	if item.Kind == "reminder" {
		positive = "set"
	}
	if item.Operation != nil || item.ClientCommandID != nil || item.BeforeStatus != nil || imported == nil ||
		item.SnapshotState != "legacyUnknown" || item.AcceptedTerms != nil || imported.ImportedAt != item.OccurredAt {
		return variantViolation("imported history entry")
	}
	if state.State != "known" || state.StateVersion == nil || *state.StateVersion != "1" || state.Status == nil || *state.Status != positive ||
		state.AcceptanceProvenance == nil || *state.AcceptanceProvenance != "legacyUnknown" ||
		state.AcceptedRevisionID != nil || state.AcceptedPublicationVersion != nil || state.AcceptedTermsHash != nil ||
		state.ChangedAt == nil || *state.ChangedAt != item.OccurredAt || !sameOptional(state.AcceptedAt, imported.LegacyRecordedAt) {
		return variantViolation("imported history state")
	}
	return nil
}

func sameOptional(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
