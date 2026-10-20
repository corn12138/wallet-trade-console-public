package adminclient

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// Attempt outcomes describe one observation; intent statuses describe what is
// known about the command as a whole. They are deliberately separate: a later
// refusal proves only that the later attempt did not execute.
const (
	attemptCommitted  = "committed"
	attemptRefused    = "refused"
	attemptUncertain  = "uncertain"
	attemptAbsent     = "absent"
	attemptUnreadable = "unreadable"
)

// Intent transitions (the classifiers below implement exactly this table):
//
//	intent before   observation                                        intent after
//	any             verified ledger record (send 2xx or ledger read)   committed
//	certain         send uncertain (transport, 5xx, unbound 2xx)       unknown
//	certain         send refused 401/403                               rejected, retryable (same key)
//	certain         send refused by any other campaign 4xx             rejected
//	uncertain       ledger read absent                                 unknown, then resend same key
//	uncertain       ledger read refused or unreadable                  unknown, nothing sent
//	uncertain       resend refused with a permanent in-window 409      rejected (proven not executed)
//	uncertain       any other resend refusal or uncertain resend       unknown
//
// "Certain" means no earlier send could have committed (planned, or a refusal
// that proved non-execution). Only the two settling rows clear uncertainty.

// permanentRefusals are 409 codes the server can only return after it has
// looked the key up in the ledger and found nothing, and after the key passed
// the first-submission window check (an out-of-window key gets 410 first).
// Inside that window the ledger keeps every committed key, so absence is
// authoritative; and each condition is monotonic for this frozen payload:
// versions only grow, archive is terminal, term locks only accumulate, an
// invalid transition at the expected version stays invalid (any change bumps
// the version), and slugs are never released. A concurrent in-flight copy of
// the same request therefore cannot commit later either. This ordering is
// pinned by TestCommandLedgerLookupPrecedesWindowAndDecision in application.
// 401/403/404/410/422 and IDEMPOTENCY_CONFLICT are not here: they are decided
// before the lookup, outside the retention guarantee, or depend on mutable
// capabilities, so they cannot prove that an earlier attempt did not commit.
var permanentRefusals = map[string]bool{
	"CAMPAIGN_VERSION_CONFLICT":        true,
	"CAMPAIGN_ARCHIVED":                true,
	"CAMPAIGN_TERMS_LOCKED":            true,
	"CAMPAIGN_LEGACY_TERMS_UNVERIFIED": true,
	"CAMPAIGN_INVALID_TRANSITION":      true,
	"CAMPAIGN_SLUG_CONFLICT":           true,
}

// classifySubmit maps one send observation to the intent's next status. Only a
// contract-valid campaign 4xx proves that this attempt did not execute; whether
// that also settles the intent depends on what earlier attempts left uncertain.
func classifySubmit(intent *Intent, reply Reply) (Attempt, string, bool, *v2.AdminCommandResponse) {
	attempt := Attempt{RequestID: reply.RequestID, HTTPStatus: reply.Status}
	// Loaded intents are normalized so that unknown or interrupted sends set
	// Uncertain; the status check keeps directly constructed records honest.
	uncertainBefore := intent.Uncertain || intent.Status == StatusUnknown
	if reply.Err != nil {
		attempt.Outcome, attempt.Note = attemptUncertain, reply.Err.Error()
		return attempt, StatusUnknown, false, nil
	}
	if reply.Status == http.StatusOK || reply.Status == http.StatusCreated {
		result, note := validateCommandResponse(intent, reply.Status, reply.Body, "send")
		if result == nil {
			attempt.Outcome, attempt.Note = attemptUncertain, note
			return attempt, StatusUnknown, false, nil
		}
		attempt.Outcome, attempt.Note = attemptCommitted, note
		return attempt, StatusCommitted, false, result
	}
	code, ok := reply.ErrorCode()
	attempt.Code = code
	if !ok || reply.Status < 400 || reply.Status >= 500 {
		attempt.Outcome, attempt.Note = attemptUncertain, reply.Describe()
		return attempt, StatusUnknown, false, nil
	}
	attempt.Outcome = attemptRefused
	if uncertainBefore {
		if reply.Status == http.StatusConflict && permanentRefusals[code] {
			attempt.Note = "proves the key absent inside its acceptance window and the payload permanently inadmissible, so no earlier attempt can have committed"
			return attempt, StatusRejected, false, nil
		}
		attempt.Note = "this attempt did not execute, but that says nothing about the earlier uncertain attempt"
		return attempt, StatusUnknown, false, nil
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		attempt.Note = "session rejected before execution; log in again and resubmit this same intent"
		return attempt, StatusRejected, true, nil
	case http.StatusForbidden:
		attempt.Note = "not a campaign administrator; resubmit this intent only after authorization is restored"
		return attempt, StatusRejected, true, nil
	case http.StatusConflict, http.StatusUnprocessableEntity:
		attempt.Note = "server refused under current state; re-read state and plan a new intent (never reuse this key for another payload)"
	case http.StatusGone:
		attempt.Note = "key absent and outside the first-submission window; plan a new intent"
	default:
		attempt.Note = "request refused before execution"
	}
	return attempt, StatusRejected, false, nil
}

// validateCommandResponse accepts a success only when it is provably this
// intent's ledger record: same key, actor, operation, target and payload hash,
// the original result at the version and revision this payload implies, and a
// current state for the same campaign that is not older than the original.
// Current may legitimately be newer: later commands advance it after a replay.
// Anything else, however well-formed, stays unknown.
func validateCommandResponse(intent *Intent, status int, body []byte, source string) (*v2.AdminCommandResponse, string) {
	var value v2.AdminCommandResponse
	if err := v2.DecodeContractResponse("AdminCommandResponse", body, &value); err != nil {
		return nil, "success status with a body that violates AdminCommandResponse"
	}
	target := intent.CampaignID
	if intent.Operation == "create" {
		target = "campaigns"
	}
	id := value.Command
	if id.ClientCommandID != intent.ID || id.Actor != intent.Actor || id.Operation != intent.Operation || id.TargetID != target {
		return nil, "response identifies another ledger command"
	}
	command, err := intent.frozenCommand()
	if err != nil {
		return nil, "frozen body cannot be mapped to a ledger command"
	}
	if want, err := intent.ledgerPayloadHash(); err != nil || id.PayloadHash != want {
		return nil, "ledger payload hash does not match this intent's frozen body"
	}
	if source == "reconcile" {
		if status != http.StatusOK || !value.Replayed {
			return nil, "a ledger read must return the stored record as a replay"
		}
	} else {
		if intent.Operation == "create" && (status == http.StatusCreated) == value.Replayed || intent.Operation != "create" && status != http.StatusOK {
			return nil, "status code disagrees with the replay flag"
		}
		if value.Replayed && !intent.sentBefore() {
			return nil, "replay reported for a key this client never sent before"
		}
	}
	original, current := value.Original, value.Current
	originalVersion, err1 := strconv.ParseUint(original.PublicationVersion, 10, 63)
	currentVersion, err2 := strconv.ParseUint(current.PublicationVersion, 10, 63)
	if err1 != nil || err2 != nil || current.CampaignID != original.CampaignID || currentVersion < originalVersion {
		return nil, "current state is not a later view of the same campaign"
	}
	if note := bindOriginal(intent, command.ExpectedPublication, command.CandidateRevisionID, original, originalVersion); note != "" {
		return nil, note
	}
	note := "executed"
	if value.Replayed {
		note = "replayed: the original commit is returned and was not executed again"
	}
	if currentVersion > originalVersion {
		note += fmt.Sprintf("; current publication has since advanced to version %d", currentVersion)
	}
	return &value, note
}

// bindOriginal checks the original result against what this payload must have
// produced. A revision command's new revision ID is server-assigned, so only
// its presence can be checked; every other operation fixes it in advance.
func bindOriginal(intent *Intent, expected *uint64, candidate string, original v2.CommandResult, version uint64) string {
	if intent.Operation == "create" {
		if original.RevisionID == nil || version != 0 {
			return "create result is not a new unpublished draft"
		}
		return ""
	}
	if original.CampaignID != intent.CampaignID || expected == nil {
		return "result belongs to another campaign"
	}
	want := *expected + 1
	if intent.Operation == "revision" {
		want = *expected
	}
	if version != want {
		return fmt.Sprintf("result publication version %d is not the %d this payload implies", version, want)
	}
	switch intent.Operation {
	case "revision":
		if original.RevisionID == nil {
			return "revision result has no revision"
		}
	case "publish", "rollback":
		if original.RevisionID == nil || *original.RevisionID != candidate {
			return "result activated a different revision than the candidate"
		}
	default:
		reviewed := intent.Review.Current
		if original.RevisionID == nil || reviewed == nil || reviewed.ActiveRevisionID == nil || *original.RevisionID != *reviewed.ActiveRevisionID {
			return "result active revision differs from the reviewed active revision"
		}
	}
	return ""
}

// readLedger performs one authoritative ledger read for the intent's key and
// classifies it. Only a verified record settles anything; absence at a read is
// never proof that an in-flight or earlier request will not commit.
func readLedger(ctx context.Context, client *Client, intent *Intent, now func() time.Time) (Attempt, *v2.AdminCommandResponse) {
	path := "/v2/campaign-admin/campaigns/" + intent.CampaignID + "/commands/" + intent.ID + "?operation=" + intent.Operation
	if intent.Operation == "create" {
		path = "/v2/campaign-admin/commands/" + intent.ID + "?operation=create"
	}
	reply := client.Do(ctx, http.MethodGet, path, nil, "campaign-admin-"+intent.ID+"-r"+strconv.Itoa(len(intent.Attempts)+1))
	attempt := Attempt{At: now().UTC().Format(time.RFC3339Nano), Kind: "reconcile", RequestID: reply.RequestID, HTTPStatus: reply.Status}
	attempt.Code, _ = reply.ErrorCode()
	switch {
	case reply.Err == nil && reply.Status == http.StatusOK:
		result, note := validateCommandResponse(intent, reply.Status, reply.Body, "reconcile")
		if result != nil {
			attempt.Outcome, attempt.Note = attemptCommitted, note
			return attempt, result
		}
		attempt.Outcome, attempt.Note = attemptUnreadable, note
	case attempt.Code == "CAMPAIGN_NOT_FOUND":
		attempt.Outcome, attempt.Note = attemptAbsent, "absent from the ledger at this read; not proof that an earlier or in-flight request will not commit"
	case attempt.Code == "UNAUTHORIZED" || attempt.Code == "CAMPAIGN_ADMIN_REQUIRED":
		attempt.Outcome, attempt.Note = attemptRefused, "ledger read refused; restore the session or administrator role before settling this intent"
	default:
		attempt.Outcome, attempt.Note = attemptUnreadable, "ledger unreadable: "+reply.Describe()
	}
	return attempt, nil
}
