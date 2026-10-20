package adminclient

import (
	"fmt"
	"strconv"
)

// describeImpact turns the reviewed snapshot into plain statements and
// predicted rejections. Predictions mirror the server's published rules only to
// save a pointless round trip; they never block or replace the locked decision.
func describeImpact(intent *Intent, in PlanInput) {
	r := &intent.Review
	impact := func(format string, args ...any) { r.Impact = append(r.Impact, fmt.Sprintf(format, args...)) }
	warn := func(format string, args ...any) { r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...)) }
	if in.Operation == "create" {
		impact("creates a private campaign draft at publicationVersion 0; nothing is public until a publish command")
		if in.Slug != "" {
			impact("fixes slug %q permanently; the stable campaign ID remains the lookup identity", in.Slug)
		}
		return
	}
	current := r.Current
	expected, _ := strconv.ParseUint(current.PublicationVersion, 10, 63)
	if current.Mode == "archived" {
		warn("campaign is archived (terminal); the server will refuse publication changes with CAMPAIGN_ARCHIVED")
	}
	termsChange := func(candidate *Candidate) bool {
		return current.ActiveTermsHash != nil && candidate.MaterialTermsHash != *current.ActiveTermsHash
	}
	lockWarnings := func(candidate *Candidate) {
		switch {
		case current.MaterialTermsLock == "legacyUnverified":
			warn("legacy terms are unverified; any publication is refused (CAMPAIGN_LEGACY_TERMS_UNVERIFIED) — use a new campaign ID")
		case current.MaterialTermsLock == "locked" && termsChange(candidate):
			warn("material terms are locked (started or previously accepted); publishing this candidate will be refused with CAMPAIGN_TERMS_LOCKED — a material change needs a new campaign ID")
		case current.MaterialTermsLock == "unknown":
			warn("active terms cannot be interpreted by this server; publication may be refused as unsupported")
		}
	}
	switch in.Operation {
	case "revision":
		impact("adds an immutable candidate revision; the publication stays at version %d", expected)
		if termsChange(r.Candidate) {
			impact("candidate changes material terms relative to the active revision")
		} else if current.ActiveTermsHash != nil {
			impact("candidate keeps the active material terms (display-only change)")
		}
		lockWarnings(r.Candidate)
	case "publish", "rollback":
		from := "none"
		if current.ActiveRevisionID != nil {
			from = *current.ActiveRevisionID
		}
		impact("publication version %d -> %d; active revision %s -> %s", expected, expected+1, from, r.Candidate.RevisionID)
		if termsChange(r.Candidate) {
			impact("changes the material terms users accept")
		}
		if current.ActiveRevisionID != nil && *current.ActiveRevisionID == r.Candidate.RevisionID {
			warn("candidate is already active; this only creates a new version with identical content")
		}
		if r.Candidate.ContentState != "ready" {
			warn("this server cannot render the candidate with its current capabilities; it will be refused or stay read-only")
		}
		if in.Operation == "rollback" && (r.Candidate.PreviouslyActive == nil || !*r.Candidate.PreviouslyActive) {
			warn("candidate was never published; rollback will be refused — use publish after review")
		}
		if in.Operation == "rollback" {
			impact("rollback is a new monotonic version; participation and acceptance history are kept")
		}
		lockWarnings(r.Candidate)
	case "pause":
		impact("publication version %d -> %d; new joins and reminders stop, cancellation stays available", expected, expected+1)
		if current.ActiveRevisionID == nil || current.Mode != "enabled" {
			warn("only an enabled, published campaign can be paused; expect CAMPAIGN_INVALID_TRANSITION")
		}
	case "resume":
		impact("publication version %d -> %d; admission reopens under the unchanged active terms", expected, expected+1)
		if current.ActiveRevisionID == nil || current.Mode != "paused" {
			warn("only a paused campaign can be resumed; expect CAMPAIGN_INVALID_TRANSITION")
		}
	case "archive":
		impact("publication version %d -> %d; TERMINAL: archive cannot be resumed or rolled back", expected, expected+1)
		impact("existing participation history and cancellation remain available")
	}
}
