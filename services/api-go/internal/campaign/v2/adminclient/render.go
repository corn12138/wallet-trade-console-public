package adminclient

import (
	"errors"
	"fmt"
	"strings"
)

// ConfirmationTarget is what the operator must type to send. Typing the
// campaign ID (rather than "yes") ties the confirmation to the reviewed target.
func ConfirmationTarget(intent *Intent) string {
	if intent.Operation == "create" {
		return "create"
	}
	return intent.CampaignID
}

// ValidatePathIDs keeps read-only commands from composing arbitrary paths.
func ValidatePathIDs(values ...string) error {
	if len(values) > 0 && !campaignIDPattern.MatchString(values[0]) {
		return errors.New("malformed campaign ID")
	}
	for _, value := range values[1:] {
		if !revisionIDPattern.MatchString(value) {
			return errors.New("malformed revision ID")
		}
	}
	return nil
}

// RenderReview prints the frozen command and its review. fresh is the state
// read just before a first send; nil means the review snapshot is shown alone.
func RenderReview(intent *Intent, fresh *CurrentState) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	r := intent.Review
	line("== campaign administrator command %s", intent.ID)
	line("operation: %s   target: %s   actor: %s", intent.Operation, ConfirmationTarget(intent), intent.Actor)
	line("api: %s   request: %s %s   body sha256: %s", intent.API, intent.Method, intent.Path, intent.BodySHA256)
	if intent.LedgerPayloadHash != "" {
		line("ledger payload hash every result must report: %s", intent.LedgerPayloadHash)
	}
	line("reason: %s", r.Reason)
	if r.ExpectedPublicationVersion != nil {
		line("expected publication version: %s", *r.ExpectedPublicationVersion)
	}
	for _, entry := range []struct {
		label string
		state *CurrentState
	}{{"reviewed state", r.Current}, {"state now", fresh}} {
		label, state := entry.label, entry.state
		if state == nil {
			continue
		}
		active := "none"
		if state.ActiveRevisionID != nil {
			active = *state.ActiveRevisionID
		}
		line("%s (as of %s): version %s, mode %s, phase %s, active %s, terms %s", label, state.AsOf, state.PublicationVersion, state.Mode, state.EffectivePhase, active, state.MaterialTermsLock)
	}
	if c := r.Candidate; c != nil {
		id := c.RevisionID
		if id == "" {
			id = "(new revision)"
		}
		line("candidate %s: terms %s, content %s [%s]", id, c.MaterialTermsHash, c.ContentHash, c.HashSource)
		if c.TitleEn != "" || c.StartAt != "" {
			line("candidate window %s .. %s, title %q", c.StartAt, c.EndAt, c.TitleEn)
		}
	}
	for _, value := range r.Impact {
		line("impact: %s", value)
	}
	for _, value := range r.Warnings {
		line("WARNING: %s", value)
	}
	line("status: %s, previous attempts: %d", intent.Status, len(intent.Attempts))
	if intent.Uncertain {
		line("an earlier attempt may already have committed; this resend uses the same key and identical bytes, so it can only replay or perform this one command")
	} else if intent.Status != StatusPlanned {
		line("earlier attempts were refused before execution; this resend uses the same key and identical bytes")
	}
	return b.String()
}

// RenderOutcome reports the latest observation, what is known about the
// command as a whole, and the next safe action. An uncertain intent is never
// described as refused: a later refusal says nothing about an earlier attempt.
func RenderOutcome(intent *Intent) string {
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
	case intent.Status == StatusCommitted && intent.Result != nil:
		o, c := intent.Result.Original, intent.Result.Current
		revision := "none"
		if o.RevisionID != nil {
			revision = *o.RevisionID
		}
		fmt.Fprintf(&b, "committed (%s): campaign %s revision %s at publication version %s; current version %s\n", intent.Settlement, o.CampaignID, revision, o.PublicationVersion, c.PublicationVersion)
		if c.PublicationVersion != o.PublicationVersion {
			fmt.Fprintln(&b, "the campaign has changed since this command; later commands are not part of this result")
		}
	case intent.Status == StatusRejected && intent.Retryable:
		fmt.Fprintf(&b, "refused before execution; after fixing the session or role, resubmit %s (same key)\n", intent.ID)
	case intent.Status == StatusRejected && strings.HasPrefix(intent.Settlement, "proven-not-executed"):
		fmt.Fprintln(&b, "NOT EXECUTED (proven): the server found no record of this key inside its acceptance window and refused the payload permanently. Re-read state and plan a new intent")
	case intent.Status == StatusRejected:
		fmt.Fprintln(&b, "refused; the command did not execute. Re-read state and plan a new intent")
	default:
		fmt.Fprintf(&b, "OUTCOME UNKNOWN: an earlier attempt may have committed and nothing has settled it. Do not plan a replacement; once access is available run reconcile %s, or submit %s again (same key and bytes)\n", intent.ID, intent.ID)
	}
	return b.String()
}
