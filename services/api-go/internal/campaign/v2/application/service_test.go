package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

func TestAuthorizationBeforeResourceLedgerAndAgainAfterLock(t *testing.T) {
	for _, denyAt := range []int{1, 2} {
		store, auth, service := fixture(t)
		auth.denyAt = denyAt
		_, err := service.Execute(context.Background(), actorA, joinCommand())
		assertCode(t, err, "FORBIDDEN")
		if countCall(*store.calls, "command") != 0 || countCall(*store.calls, "publication") != 0 || len(store.tx.saved) != 0 {
			t.Fatalf("revoked actor reached protected state: %v", *store.calls)
		}
		want := []string{"authorize"}
		if denyAt == 2 {
			want = []string{"authorize", "lock", "authorize"}
		}
		if !reflect.DeepEqual(*store.calls, want) {
			t.Fatalf("wrong authorization order %v", *store.calls)
		}
	}
	store, auth, service := fixture(t)
	if _, err := service.Execute(context.Background(), actorA, joinCommand()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual((*store.calls)[:6], []string{"authorize", "lock", "authorize", "publication", "interaction", "command"}) {
		t.Fatal(*store.calls)
	}
	if !reflect.DeepEqual(auth.adminFlags, []bool{false, false}) {
		t.Fatal("owner operation requested unrelated administrator scope")
	}
}

func TestUnpublishedUnknownOwnerCannotReadLedger(t *testing.T) {
	store, _, service := fixture(t)
	store.tx.pubs["campaign-1"] = v2.Publication{CampaignID: "campaign-1", Mode: "enabled"}
	_, err := service.Execute(context.Background(), actorA, joinCommand())
	assertCode(t, err, "CAMPAIGN_NOT_FOUND")
	if countCall(*store.calls, "command") != 0 {
		t.Fatal("resource access checked after ledger")
	}
}

func TestExpiredReplayReturnsOriginalAndCurrentWithoutReexecution(t *testing.T) {
	store, _, service := fixture(t)
	command := joinCommand()
	first, err := service.Execute(context.Background(), actorA, command)
	if err != nil {
		t.Fatal(err)
	}
	state := *store.tx.state
	state.Status = "left"
	state.Version++
	store.tx.state = &state
	pub := store.tx.pubs["campaign-1"]
	pub.Version = 2
	pub.InteractionVersion = 2
	store.tx.pubs[pub.CampaignID] = pub
	store.tx.now = testNow.Add(8 * 24 * time.Hour)
	command.RequestID = "different-trace"
	service.capabilities = nil
	replay, err := service.Execute(context.Background(), actorA, command)
	if err != nil || !replay.Replayed || !reflect.DeepEqual(first.Original, replay.Original) || replay.Current.State.Status != "left" || replay.Current.State.StateVersion != "2" {
		t.Fatalf("bad replay: %+v %v", replay, err)
	}
	if len(store.tx.saved) != 1 || countCall(*store.calls, "clock") != 1 {
		t.Fatal("retry reexecuted or applied new-key age check")
	}
	command.Reason = "different intent"
	_, err = service.Execute(context.Background(), actorA, command)
	assertCode(t, err, "IDEMPOTENCY_CONFLICT")
}

func TestExpiredAbsentCommandCheckedAfterAuthorizedLedgerLookup(t *testing.T) {
	store, _, service := fixture(t)
	command := joinCommand()
	command.ClientCommandID = commandID(testNow.Add(-8 * 24 * time.Hour))
	_, err := service.Execute(context.Background(), actorA, command)
	assertCode(t, err, "CAMPAIGN_COMMAND_EXPIRED")
	if countCall(*store.calls, "command") != 1 || countCall(*store.calls, "clock") != 1 || len(store.tx.saved) != 0 {
		t.Fatal(*store.calls)
	}
}

func TestCreateRetryUsesFixedCollectionAndOriginalIdentity(t *testing.T) {
	store, auth, service := fixture(t)
	input := store.tx.revisions[activeID].Input
	command := Command{Operation: "create", ClientCommandID: commandID(testNow), Revision: &input, Slug: "stable-name", Reason: "new campaign", RequestID: "trace-1"}
	first, err := service.Execute(context.Background(), actorA, command)
	if err != nil {
		t.Fatal(err)
	}
	store.tx.now = testNow.Add(8 * 24 * time.Hour)
	command.RequestID = "trace-2"
	replay, err := service.Execute(context.Background(), actorA, command)
	if err != nil || !replay.Replayed || first.Original.CampaignID != replay.Original.CampaignID || len(store.tx.saved) != 1 {
		t.Fatalf("duplicate create: %+v %v", replay, err)
	}
	if store.tx.saved[0].Key.TargetID != "campaigns" || store.scopes[0].CampaignID != "" || store.scopes[0].CreationKey == "" || store.scopes[0] != store.scopes[1] {
		t.Fatal("create used a fresh aggregate as retry identity")
	}
	for _, admin := range auth.adminFlags {
		if !admin {
			t.Fatal("create lacked administrator scope")
		}
	}
}

func TestCommandShapeRejectsPostgresCastFailuresBeforeStore(t *testing.T) {
	for _, edit := range []func(*Command){
		func(c *Command) { c.ClientCommandID = "not-a-uuid" },
		func(c *Command) { c.ClientCommandID = activeID },
		func(c *Command) {
			c.Operation = "publish"
			c.ExpectedState = nil
			c.Reason = "publish"
			c.CandidateRevisionID = "not-a-uuid"
		},
		func(c *Command) { c.RequestID = "bad\x00trace" },
	} {
		store, _, service := fixture(t)
		command := joinCommand()
		edit(&command)
		_, err := service.Execute(context.Background(), actorA, command)
		assertCode(t, err, "CAMPAIGN_INVALID_INPUT")
		if len(store.scopes) != 0 {
			t.Fatal("invalid UUID or metadata reached PostgreSQL")
		}
	}
}

func TestFrozenPointersCannotDriftWhileWaitingForLock(t *testing.T) {
	store, _, service := fixture(t)
	command := joinCommand()
	store.beforeLock = func() { *command.ExpectedPublication = 9 }
	if _, err := service.Execute(context.Background(), actorA, command); err != nil {
		t.Fatal(err)
	}
	if *store.tx.saved[0].ExpectedPublication != 1 {
		t.Fatal("pointer drift changed reviewed command")
	}
	store, _, service = fixture(t)
	input := store.tx.revisions[activeID].Input
	command = Command{Operation: "create", ClientCommandID: commandID(testNow), Revision: &input, Reason: "new", RequestID: "trace"}
	original := input.MaterialTerms.Copy.En.Title
	store.beforeLock = func() { input.MaterialTerms.Copy.En.Title = "changed while waiting" }
	if _, err := service.Execute(context.Background(), actorA, command); err != nil {
		t.Fatal(err)
	}
	if store.tx.mutations[0].Revision.Input.MaterialTerms.Copy.En.Title != original {
		t.Fatal("revision changed after payload hash")
	}
}

func TestCommitFailureDoesNotReturnSuccessfulOutcome(t *testing.T) {
	store, _, service := fixture(t)
	store.commitErr = errors.New("commit outcome unavailable")
	out, err := service.Execute(context.Background(), actorA, joinCommand())
	if err == nil || !reflect.DeepEqual(out, Outcome{}) {
		t.Fatalf("uncommitted result returned as success: %+v %v", out, err)
	}
}

func TestVersionsStayExactDecimalStringsAtWireBoundary(t *testing.T) {
	command := joinCommand()
	command.ExpectedState = u64(9007199254740993)
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"expectedStateVersion":"9007199254740993"`) {
		t.Fatalf("version lost string representation: %s", raw)
	}
	var decoded Command
	if err = json.Unmarshal(raw, &decoded); err != nil || *decoded.ExpectedState != 9007199254740993 {
		t.Fatalf("version changed: %+v %v", decoded, err)
	}
	if err = json.Unmarshal([]byte(`{"expectedStateVersion":9007199254740993}`), &decoded); err == nil {
		t.Fatal("unquoted numeric version accepted")
	}
}

func TestMismatchedLedgerIdentityFailsClosed(t *testing.T) {
	store, _, service := fixture(t)
	command := joinCommand()
	if _, err := service.Execute(context.Background(), actorA, command); err != nil {
		t.Fatal(err)
	}
	for key, record := range store.tx.ledger {
		record.Key.Actor = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		store.tx.ledger[key] = record
	}
	out, err := service.Execute(context.Background(), actorA, command)
	assertCode(t, err, "CAMPAIGN_DATA_UNAVAILABLE")
	if !reflect.DeepEqual(out, Outcome{}) || len(store.tx.saved) != 1 {
		t.Fatal("foreign ledger response exposed or reexecuted")
	}
}
