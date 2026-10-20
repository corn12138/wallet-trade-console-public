package application

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

func TestCancellationAndContainmentDoNotReauthorizeHistoricalCapabilities(t *testing.T) {
	store, _, service := fixture(t)
	if _, err := service.Execute(context.Background(), actorA, joinCommand()); err != nil {
		t.Fatal(err)
	}
	pub := store.tx.pubs["campaign-1"]
	pub.Active.Input.SchemaVersion = 2
	store.tx.pubs[pub.CampaignID] = pub
	store.tx.revisionErr = errors.New("withdrawn historical decoder")
	service.capabilities = nil
	command := Command{Operation: "leave", CampaignID: "campaign-1", ClientCommandID: commandID(testNow), ExpectedState: u64(1), RequestID: "leave-trace"}
	left, err := service.Execute(context.Background(), actorA, command)
	if err != nil || left.Current.State.Status != "left" || left.Current.State.AcceptanceProvenance != "verified" {
		t.Fatalf("historical configuration prevented exit: %+v %v", left, err)
	}
	if countCall(*store.calls, "revision") != 1 {
		t.Fatal("cancellation loaded withdrawn revision")
	}
	for _, operation := range []string{"pause", "archive"} {
		store, _, service = fixture(t)
		service.capabilities = nil
		store.tx.revisionErr = errors.New("historical content unavailable")
		if _, err := service.Execute(context.Background(), actorA, adminCommand(operation)); err != nil {
			t.Fatalf("%s containment failed: %v", operation, err)
		}
		if countCall(*store.calls, "revision") != 0 {
			t.Fatal("containment required historical decoding")
		}
	}
}

func TestNewAdmissionChecksCurrentCapabilitiesAndRevisionIntegrity(t *testing.T) {
	store, _, service := fixture(t)
	service.capabilities = func() v2.Capabilities { return v2.Capabilities{} }
	_, err := service.Execute(context.Background(), actorA, joinCommand())
	assertCode(t, err, "CAMPAIGN_UNSUPPORTED")
	if len(store.tx.saved) != 0 {
		t.Fatal("withdrawn template allowed admission")
	}
	store, _, service = fixture(t)
	revision := store.tx.revisions[activeID]
	revision.Input.MaterialTerms.Copy.En.Title = "changed terms"
	store.tx.revisions[activeID] = revision
	_, err = service.Execute(context.Background(), actorA, joinCommand())
	assertCode(t, err, "CAMPAIGN_STORED_STATE_INVALID")
	if len(store.tx.saved) != 0 {
		t.Fatal("unverified revision became accepted")
	}
}

func TestForeignCandidateCannotReachPublicationHistoryLookup(t *testing.T) {
	store, _, service := fixture(t)
	candidate := store.tx.revisions[activeID]
	candidate.ID = candidateID
	candidate.CampaignID = "other-campaign"
	store.tx.revisions[candidateID] = candidate
	command := adminCommand("publish")
	command.CandidateRevisionID = candidateID
	_, err := service.Execute(context.Background(), actorA, command)
	assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
	if countCall(*store.calls, "published") != 0 || len(store.tx.saved) != 0 {
		t.Fatal("foreign revision reached mutation or history lookup")
	}
}

func TestExpectedStatePrecedesPublicationConflict(t *testing.T) {
	store, _, service := fixture(t)
	if _, err := service.Execute(context.Background(), actorA, joinCommand()); err != nil {
		t.Fatal(err)
	}
	command := joinCommand()
	command.ClientCommandID = commandID(testNow.Add(time.Millisecond))
	command.ExpectedPublication = u64(0)
	service.capabilities = nil
	_, err := service.Execute(context.Background(), actorA, command)
	assertCode(t, err, "CAMPAIGN_STATE_CONFLICT")
	command.ExpectedState = u64(1)
	_, err = service.Execute(context.Background(), actorA, command)
	assertCode(t, err, "CAMPAIGN_VERSION_CONFLICT")
	if len(store.tx.saved) != 1 {
		t.Fatal("stale commands mutated state")
	}
}

func TestNoopDoesNotAdvanceCountersAndOverflowNeverSaves(t *testing.T) {
	store, _, service := fixture(t)
	if _, err := service.Execute(context.Background(), actorA, joinCommand()); err != nil {
		t.Fatal(err)
	}
	pub := store.tx.pubs["campaign-1"]
	pub.InteractionVersion = math.MaxInt64
	store.tx.pubs[pub.CampaignID] = pub
	store.tx.state.Version = math.MaxInt64
	command := joinCommand()
	command.ClientCommandID = commandID(testNow.Add(time.Millisecond))
	command.ExpectedState = u64(math.MaxInt64)
	noop, err := service.Execute(context.Background(), actorA, command)
	if err != nil || noop.Current.Changed || noop.Current.InteractionVersion != math.MaxInt64 || store.tx.mutations[1].Interaction.Changed {
		t.Fatalf("noop exhausted counters: %+v %v", noop, err)
	}
	command.Operation = "leave"
	command.ExpectedPublication = nil
	_, err = service.Execute(context.Background(), actorA, command)
	assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
	if len(store.tx.saved) != 2 {
		t.Fatal("row version overflow mutated state")
	}
	store.tx.state.Version = 1
	command.ExpectedState = u64(1)
	_, err = service.Execute(context.Background(), actorA, command)
	assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
	if len(store.tx.saved) != 2 {
		t.Fatal("interaction version overflow mutated state")
	}
	pub.Version = math.MaxInt64
	store.tx.pubs[pub.CampaignID] = pub
	command = adminCommand("pause")
	command.ExpectedPublication = u64(math.MaxInt64)
	_, err = service.Execute(context.Background(), actorA, command)
	assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
}

func TestAdmissionUsesDatabaseClockAfterLockWait(t *testing.T) {
	store, _, service := fixture(t)
	store.beforeLock = func() { store.tx.now = time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC) }
	_, err := service.Execute(context.Background(), actorA, joinCommand())
	assertCode(t, err, "CAMPAIGN_NOT_OPEN")
	if len(store.tx.saved) != 0 {
		t.Fatal("request start time bypassed deadline")
	}
}

func TestPointerSnapshotPreservesTextAndRejectsInvalidEncoding(t *testing.T) {
	for _, title := range []string{"bad\x00title", string([]byte{0xff})} {
		store, _, service := fixture(t)
		input := store.tx.revisions[activeID].Input
		input.MaterialTerms.Copy.En.Title = title
		command := Command{Operation: "create", ClientCommandID: commandID(testNow), Revision: &input, Reason: "new", RequestID: "trace"}
		_, err := service.Execute(context.Background(), actorA, command)
		assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
		if len(store.scopes) != 0 {
			t.Fatal("invalid JSONB text reached store")
		}
	}
}

func TestMatchedPositiveNoopDoesNotRequireWithdrawnCapability(t *testing.T) {
	store, _, service := fixture(t)
	if _, err := service.Execute(context.Background(), actorA, joinCommand()); err != nil {
		t.Fatal(err)
	}
	command := joinCommand()
	command.ClientCommandID = commandID(testNow.Add(time.Millisecond))
	command.ExpectedState = u64(1)
	service.capabilities = nil
	out, err := service.Execute(context.Background(), actorA, command)
	if err != nil || out.Current.Changed || out.Current.State.StateVersion != "1" || out.Current.InteractionVersion != 1 {
		t.Fatalf("no-op reopened admission: %+v %v", out, err)
	}
	if len(store.tx.saved) != 2 || store.tx.mutations[1].Interaction.Changed {
		t.Fatal("no-op must record command without state transition")
	}
}
