package adminclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// Confirm shows the frozen review (and, for a first send, freshly read state)
// to the operator. Returning an error aborts before anything is sent.
type Confirm func(intent *Intent, fresh *CurrentState) error

// Submit sends a planned intent, or settles an uncertain one with the same key
// and bytes. A certain intent re-reads the publication first and refuses to
// send if it moved since review. An uncertain intent is reconciled first: a
// verified ledger record settles it without sending; a refused or unreadable
// read stops (nothing is sent, the intent stays unknown); only a read that
// finds the key absent leads to a resend, which can then only replay or
// perform this one reviewed command.
func Submit(ctx context.Context, home Home, client *Client, credential Credential, id string, now func() time.Time, confirm Confirm) (*Intent, error) {
	unlock, err := home.LockIntent(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	intent, err := home.LoadIntent(id)
	if err != nil {
		return nil, err
	}
	if err = sameOwner(intent, client, credential); err != nil {
		return intent, err
	}
	intent.normalize()
	var fresh *CurrentState
	switch {
	case intent.Status == StatusCommitted:
		return intent, errors.New("intent already committed; use reconcile to refresh its current state")
	case intent.Status == StatusAbandoned:
		return intent, errors.New("intent was abandoned before sending")
	case intent.Status == StatusRejected && !intent.Retryable:
		return intent, errors.New("server refused this intent; review current state and plan a new one")
	case intent.Uncertain:
		attempt, result := readLedger(ctx, client, intent, now)
		intent.Attempts = append(intent.Attempts, attempt)
		if result != nil {
			settle(intent, StatusCommitted, "ledger-record", result)
			return intent, home.SaveIntent(intent)
		}
		if err = home.SaveIntent(intent); err != nil {
			return intent, err
		}
		if attempt.Outcome != attemptAbsent {
			return intent, fmt.Errorf("cannot settle: %s; an earlier attempt may have committed, so do not plan a replacement", attempt.Note)
		}
	case intent.Operation != "create":
		var view v2.AdminCampaignResponse
		if err = client.GetContract(ctx, "/v2/campaign-admin/campaigns/"+intent.CampaignID, "AdminCampaignResponse", &view); err != nil {
			return intent, err
		}
		if fresh, err = currentState(ctx, client, view); err != nil {
			return intent, err
		}
		if fresh.PublicationVersion != *intent.Review.ExpectedPublicationVersion {
			return intent, fmt.Errorf("publication moved from %s to %s since review; abandon this intent and plan again", *intent.Review.ExpectedPublicationVersion, fresh.PublicationVersion)
		}
	}
	if err = confirm(intent, fresh); err != nil {
		return intent, err
	}
	uncertainBefore := intent.Uncertain
	intent.Status = StatusSending
	if err = home.SaveIntent(intent); err != nil {
		return intent, err
	}
	requestID := "campaign-admin-" + intent.ID + "-" + strconv.Itoa(len(intent.Attempts)+1)
	reply := client.Do(ctx, intent.Method, intent.Path, []byte(intent.Body), requestID)
	attempt, status, retryable, result := classifySubmit(intent, reply)
	attempt.At, attempt.Kind = now().UTC().Format(time.RFC3339Nano), "send"
	intent.Attempts = append(intent.Attempts, attempt)
	switch {
	case status == StatusCommitted:
		settle(intent, StatusCommitted, map[bool]string{true: "ledger-record", false: "executed"}[result.Replayed], result)
	case status == StatusRejected && uncertainBefore:
		settle(intent, StatusRejected, "proven-not-executed:"+attempt.Code, nil)
	case status == StatusRejected:
		settle(intent, StatusRejected, "refused-before-execution:"+attempt.Code, nil)
		intent.Retryable = retryable
	default:
		intent.Status, intent.Uncertain, intent.Retryable = StatusUnknown, true, false
	}
	return intent, home.SaveIntent(intent)
}

// settle records a terminal fact and clears uncertainty. Callers reach it only
// through the transitions documented in outcome.go.
func settle(intent *Intent, status, how string, result *v2.AdminCommandResponse) {
	intent.Status, intent.Settlement, intent.Uncertain, intent.Retryable = status, how, false, false
	if result != nil {
		intent.Result = result
	}
}

func sameOwner(intent *Intent, client *Client, credential Credential) error {
	if intent.API != client.Base() {
		return fmt.Errorf("intent targets %s, not %s", intent.API, client.Base())
	}
	// The ledger key includes the actor, so another administrator's session
	// would create a second command rather than retry this one.
	if intent.Actor != credential.Address {
		return fmt.Errorf("intent belongs to administrator %s; current session is %s", intent.Actor, credential.Address)
	}
	return nil
}

// Reconcile reads the authoritative ledger for an intent's key. Only a
// verified record changes the intent (to committed); absence, refusal or an
// unreadable answer is recorded as an observation and leaves the status as is.
func Reconcile(ctx context.Context, home Home, client *Client, credential Credential, id string, now func() time.Time) (*Intent, error) {
	unlock, err := home.LockIntent(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	intent, err := home.LoadIntent(id)
	if err != nil {
		return nil, err
	}
	if err = sameOwner(intent, client, credential); err != nil {
		return intent, err
	}
	intent.normalize()
	if intent.Status == StatusPlanned || intent.Status == StatusAbandoned {
		return intent, errors.New("intent was never sent; nothing to reconcile")
	}
	attempt, result := readLedger(ctx, client, intent, now)
	if result != nil && intent.Status == StatusRejected {
		attempt.Note += "; the ledger shows this key committed although a send was classified as refused"
	}
	intent.Attempts = append(intent.Attempts, attempt)
	if result != nil {
		settle(intent, StatusCommitted, "ledger-record", result)
	}
	return intent, home.SaveIntent(intent)
}

// Abandon retires an intent that was never sent. Anything that may have reached
// the server must be settled through reconcile or a same-key resubmit instead.
func Abandon(home Home, id string, now time.Time) (*Intent, error) {
	unlock, err := home.LockIntent(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	intent, err := home.LoadIntent(id)
	if err != nil {
		return nil, err
	}
	if intent.Status != StatusPlanned {
		return intent, fmt.Errorf("only a never-sent intent can be abandoned (status %s)", intent.Status)
	}
	intent.Attempts = append(intent.Attempts, Attempt{At: now.UTC().Format(time.RFC3339Nano), Kind: "abandon", Outcome: StatusAbandoned})
	intent.Status = StatusAbandoned
	return intent, home.SaveIntent(intent)
}
