package application

import (
	"context"
	"testing"
	"time"
)

// The operator client (adminclient.permanentRefusals) treats a state-decision
// 409 on a same-key resend as proof that an earlier uncertain attempt never
// committed. That inference is sound only while Execute looks the key up in the
// ledger before the first-submission window check, and checks the window before
// any CAS or transition decision. Changing that order would silently break the
// client's recovery guarantee, so it is pinned here.
func TestCommandLedgerLookupPrecedesWindowAndDecision(t *testing.T) {
	store, _, service := fixture(t)
	pause := adminCommand("pause")
	first, err := service.Execute(context.Background(), actorA, pause)
	if err != nil || first.Replayed {
		t.Fatalf("first pause: %+v %v", first, err)
	}
	// Later commands move the publication well past the pause's expectation.
	pub := store.tx.pubs["campaign-1"]
	pub.Version, pub.Mode = 7, "enabled"
	store.tx.pubs["campaign-1"] = pub

	replay, err := service.Execute(context.Background(), actorA, pause)
	if err != nil || !replay.Replayed || replay.Original != first.Original {
		t.Fatalf("a committed key must replay before any CAS check: %+v %v", replay, err)
	}

	stale := adminCommand("resume")
	stale.ClientCommandID = commandID(testNow.Add(time.Second))
	*store.calls = (*store.calls)[:0]
	_, err = service.Execute(context.Background(), actorA, stale)
	assertCode(t, err, "CAMPAIGN_VERSION_CONFLICT")
	if !before(*store.calls, "command", "clock") {
		t.Fatalf("an absent key must be looked up before the window and CAS checks: %v", *store.calls)
	}

	expired := adminCommand("resume")
	expired.ClientCommandID = commandID(testNow.Add(-8 * 24 * time.Hour))
	_, err = service.Execute(context.Background(), actorA, expired)
	assertCode(t, err, "CAMPAIGN_COMMAND_EXPIRED")
	if len(store.tx.saved) != 1 {
		t.Fatal("refused commands must not be saved")
	}
}

func before(calls []string, first, second string) bool {
	a, b := -1, -1
	for i, call := range calls {
		if call == first && a < 0 {
			a = i
		}
		if call == second && b < 0 {
			b = i
		}
	}
	return a >= 0 && b >= 0 && a < b
}
