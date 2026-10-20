package adminclient

import (
	"encoding/json"
	"fmt"
	"strings"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

func findingsLine(findings []v2.LegacyFindingSummary) string {
	if len(findings) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(findings))
	for _, finding := range findings {
		parts = append(parts, fmt.Sprintf("%s/%s×%d", finding.Kind, finding.Severity, finding.Count))
	}
	return strings.Join(parts, " ")
}

// RenderLegacyPlan prints one page of a run's plan.
func RenderLegacyPlan(page v2.LegacyImportPlanResponse) string {
	var b strings.Builder
	run := page.Run
	captured, source := "not captured", "none"
	if run.CapturedAt != nil {
		captured = *run.CapturedAt
	}
	if run.SourceSha256 != nil {
		source = *run.SourceSha256
	}
	fmt.Fprintf(&b, "run %s: %s by %s, captured %s, %d source rows (sha256 %s), kept until %s\n", run.RunID, run.Status, run.Operator, captured, run.Rows, source, run.RetainUntil)
	for _, item := range page.Items {
		fence, review, activation := "not fenced", "", ""
		if item.Fence != nil {
			fence = "fenced " + item.Fence.FencedAt
			if item.Fence.LiftedAt != nil {
				fence = "fence lifted " + *item.Fence.LiftedAt
			}
		}
		if item.Review != nil {
			review = fmt.Sprintf(", review %s (%s, accepts %s)", item.Review.ReviewID, item.Review.Decision, orNone(item.Review.AcceptedDifferences))
		}
		if item.Activation != nil {
			activation = fmt.Sprintf(", ACTIVATED %s as %s (differences %s)", item.Activation.ActivationID, item.Activation.Decision, orNone(item.Activation.Classes))
		}
		fmt.Fprintf(&b, "- %s  %s  row %s  interactions %s  participants %d reminders %d  %s%s%s\n    findings: %s\n",
			item.CampaignID, item.Decision, item.LegacyRowSha256, item.InteractionsSha256, item.ParticipantRows, item.ReminderRows, fence, review, activation, findingsLine(item.Findings))
	}
	if page.UnlistedItems > 0 {
		fmt.Fprintf(&b, "%d items have campaign IDs longer than 160 bytes; they cannot be fenced or activated and appear only in the capture report\n", page.UnlistedItems)
	}
	if page.NextCursor != nil {
		fmt.Fprintf(&b, "more items: --cursor %s\n", *page.NextCursor)
	}
	return b.String()
}

// RenderLegacyIntent prints the frozen request and the item it was frozen
// against; fresh, when set, is the item read just before a send.
func RenderLegacyIntent(intent *LegacyIntent, fresh *v2.LegacyPlanItemView) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("== legacy import %s %s", intent.Operation, intent.ID)
	line("run: %s   campaign: %s   actor: %s", intent.RunID, intent.CampaignID, intent.Actor)
	line("api: %s   request: POST %s   body sha256: %s", intent.API, intent.Path, intent.BodySHA256)
	item := intent.Item
	line("plan item as reviewed (as of %s): %s, row %s, interactions %s, participants %d, reminders %d", item.AsOf, item.Decision, item.LegacyRowSHA256, item.InteractionsSHA256, item.ParticipantRows, item.ReminderRows)
	line("findings: %s", findingsLine(item.Findings))
	if item.ReviewID != "" {
		line("recorded review: %s (%s)", item.ReviewID, item.ReviewDecision)
	}
	if intent.Operation == LegacyReview {
		var body v2.LegacyReviewCommandInput
		if json.Unmarshal([]byte(intent.Body), &body) == nil {
			line("decision: %s   accepts differences: %s   acknowledges: %s", body.Decision, orNone(body.AcceptedDifferences), orNone(body.AcknowledgedFindings))
			line("reason: %s", body.Reason)
		}
	} else {
		var body v2.LegacyActivationCommandInput
		if json.Unmarshal([]byte(intent.Body), &body) == nil {
			line("activates with the reviewed digests; the server refuses if the plan item or the legacy rows changed")
			line("reason: %s", body.Reason)
		}
	}
	if fresh != nil {
		state := "unchanged"
		if fresh.LegacyRowSha256 != item.LegacyRowSHA256 || fresh.InteractionsSha256 != item.InteractionsSHA256 || fresh.Decision != item.Decision {
			state = "CHANGED"
		}
		line("plan item now: %s (%s)", fresh.Decision, state)
	}
	line("status: %s, previous attempts: %d", intent.Status, len(intent.Attempts))
	if intent.Uncertain {
		line("an earlier attempt may already have committed; this resend uses the same key and identical bytes, so it can only replay or perform this one command")
	}
	return b.String()
}

// RenderLegacyOutcome reports the latest observation, what is known about the
// intent, and the next safe step.
func RenderLegacyOutcome(intent *LegacyIntent) string {
	var b strings.Builder
	if n := len(intent.Attempts); n > 0 {
		last := intent.Attempts[n-1]
		observed := "no HTTP response"
		if last.HTTPStatus != 0 {
			observed = strings.TrimSpace(fmt.Sprintf("HTTP %d %s", last.HTTPStatus, last.Code))
			if last.RequestID != "" {
				observed += ", request " + last.RequestID
			}
		}
		fmt.Fprintf(&b, "%s: %s (%s) %s\n", last.Kind, last.Outcome, observed, last.Note)
	}
	switch {
	case intent.Status == StatusCommitted && intent.Review != nil:
		r := intent.Review
		fmt.Fprintf(&b, "committed (%s): review %s records %s for %s, accepting %s\n", intent.Settlement, r.ReviewID, r.Decision, r.CampaignID, orNone(r.AcceptedDifferences))
	case intent.Status == StatusCommitted && intent.Activation != nil:
		a := intent.Activation
		fmt.Fprintf(&b, "committed (%s): activation %s imported %s as %s at publication version %s with %d interactions; compared %d wallets, differences %s\n",
			intent.Settlement, a.ActivationID, a.CampaignID, a.Decision, a.PublicationVersion, a.ImportedInteractions, a.Comparison.Wallets, orNone(a.Comparison.Classes))
	case intent.Status == StatusCommitted:
		fmt.Fprintf(&b, "committed (%s)\n", intent.Settlement)
	case intent.Status == StatusRejected && intent.Retryable:
		fmt.Fprintf(&b, "refused before execution; after fixing the session or role, run legacy submit %s again (same key)\n", intent.ID)
	case intent.Status == StatusRejected && strings.HasPrefix(intent.Settlement, "proven-not-executed"):
		fmt.Fprintln(&b, "NOT EXECUTED (proven): the server holds no record of this key and cannot accept it later. Re-read the plan before freezing a new intent")
	case intent.Status == StatusRejected:
		fmt.Fprintln(&b, "refused; nothing was written. Re-read the plan before freezing a new intent")
	default:
		fmt.Fprintf(&b, "OUTCOME UNKNOWN: an earlier attempt may have committed. Do not freeze a replacement; run legacy status %s, or legacy submit %s again (same key and bytes)\n", intent.ID, intent.ID)
	}
	if r := intent.Refusal; r != nil && intent.Status != StatusCommitted {
		for _, drift := range r.Drift {
			after := ""
			if drift.AfterRef != nil {
				after = " -> " + *drift.AfterRef
			}
			fmt.Fprintf(&b, "drift: %s %s %s%s\n", drift.Table, drift.Change, drift.RowRef, after)
		}
		if r.DriftTruncated {
			fmt.Fprintln(&b, "drift: more entries than shown; capture a new run")
		}
		if c := r.Comparison; c != nil {
			fmt.Fprintf(&b, "comparison at %s over %d wallets: differences %s, accepted %s\n", c.Instant, c.Wallets, orNone(c.Classes), orNone(c.Accepted))
			for _, group := range c.Groups {
				fmt.Fprintf(&b, "  %s %s: %s -> %s ×%d wallets %s\n", group.Class, group.Field, group.Before, group.After, group.Count, orNone(group.WalletRefs))
			}
			if c.GroupsTruncated {
				fmt.Fprintln(&b, "  more groups than shown")
			}
		}
	}
	return b.String()
}
