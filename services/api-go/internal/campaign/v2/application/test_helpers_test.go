package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

const actorA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const activeID = "11111111-1111-4111-8111-111111111111"
const candidateID = "22222222-2222-4222-8222-222222222222"

var testNow = time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
var testCapabilities = v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}}

func u64(value uint64) *uint64 { return &value }
func commandID(at time.Time) string {
	value := fmt.Sprintf("%012x", at.UnixMilli())
	return value[:8] + "-" + value[8:] + "-7000-8000-000000000001"
}
func joinCommand() Command {
	return Command{Operation: "join", CampaignID: "campaign-1", ClientCommandID: commandID(testNow), ExpectedPublication: u64(1), ExpectedState: u64(0), RequestID: "request-1"}
}
func adminCommand(operation string) Command {
	return Command{Operation: operation, CampaignID: "campaign-1", ClientCommandID: commandID(testNow), ExpectedPublication: u64(1), Reason: "reviewed publication change", RequestID: "request-1"}
}
func fixture(t *testing.T) (*memoryStore, *testAuthorizer, *Service) {
	t.Helper()
	raw, err := os.ReadFile("../../../../../../packages/shared/contracts/fixtures/campaign-v2-revision.json")
	if err != nil {
		t.Fatal(err)
	}
	input, err := v2.DecodeRevision(raw)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := v2.NewRevision(activeID, "campaign-1", input, testCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	tx := &memoryTx{calls: &calls, now: testNow, pubs: map[string]v2.Publication{"campaign-1": {CampaignID: "campaign-1", Version: 1, Mode: "enabled", Active: &revision}}, revisions: map[string]v2.Revision{activeID: revision}, ledger: map[CommandKey]Record{}}
	store := &memoryStore{tx: tx, calls: &calls}
	authorizer := &testAuthorizer{calls: &calls}
	return store, authorizer, NewService(store, authorizer, func() v2.Capabilities { return testCapabilities })
}
func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	var domain *v2.DomainError
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func countCall(calls []string, want string) int {
	n := 0
	for _, call := range calls {
		if call == want {
			n++
		}
	}
	return n
}

type testAuthorizer struct {
	calls      *[]string
	count      int
	denyAt     int
	adminFlags []bool
}

func (a *testAuthorizer) Require(_ context.Context, actor string, admin bool) error {
	*a.calls = append(*a.calls, "authorize")
	a.count++
	a.adminFlags = append(a.adminFlags, admin)
	if actor != actorA {
		return &v2.DomainError{Code: "FORBIDDEN", Status: 403}
	}
	if a.count == a.denyAt {
		return &v2.DomainError{Code: "FORBIDDEN", Status: 403}
	}
	return nil
}

type memoryStore struct {
	tx         *memoryTx
	calls      *[]string
	scopes     []LockScope
	beforeLock func()
	commitErr  error
}

func (s *memoryStore) Transact(_ context.Context, scope LockScope, fn func(Transaction) error) error {
	*s.calls = append(*s.calls, "lock")
	s.scopes = append(s.scopes, scope)
	if s.beforeLock != nil {
		s.beforeLock()
	}
	if err := fn(s.tx); err != nil {
		return err
	}
	return s.commitErr
}

// This fake verifies application orchestration only. PostgreSQL rollback,
// locking, deferred constraints and concurrent writers require adapter tests.
type memoryTx struct {
	calls       *[]string
	now         time.Time
	pubs        map[string]v2.Publication
	revisions   map[string]v2.Revision
	state       *v2.Interaction
	ledger      map[CommandKey]Record
	saved       []Record
	mutations   []Mutation
	published   bool
	revisionErr error
}

func (tx *memoryTx) Now() (time.Time, error) {
	*tx.calls = append(*tx.calls, "clock")
	return tx.now, nil
}
func (tx *memoryTx) Publication(id string) (v2.Publication, error) {
	*tx.calls = append(*tx.calls, "publication")
	pub, ok := tx.pubs[id]
	if !ok {
		return pub, &v2.DomainError{Code: "CAMPAIGN_NOT_FOUND", Status: 404}
	}
	return pub, nil
}
func (tx *memoryTx) Revision(id string) (v2.Revision, error) {
	*tx.calls = append(*tx.calls, "revision")
	if tx.revisionErr != nil {
		return v2.Revision{}, tx.revisionErr
	}
	revision, ok := tx.revisions[id]
	if !ok {
		return revision, &v2.DomainError{Code: "CAMPAIGN_NOT_FOUND", Status: 404}
	}
	return revision, nil
}
func (tx *memoryTx) WasPublished(string) (bool, error) {
	*tx.calls = append(*tx.calls, "published")
	return tx.published, nil
}
func (tx *memoryTx) Interaction(actor, kind string) (*v2.Interaction, error) {
	*tx.calls = append(*tx.calls, "interaction")
	if actor != actorA {
		return nil, errors.New("wrong owner")
	}
	return tx.state, nil
}
func (tx *memoryTx) Command(key CommandKey) (*Record, error) {
	*tx.calls = append(*tx.calls, "command")
	value, ok := tx.ledger[key]
	if !ok {
		return nil, nil
	}
	return &value, nil
}
func (tx *memoryTx) Save(record Record, mutation Mutation) error {
	*tx.calls = append(*tx.calls, "save")
	tx.saved = append(tx.saved, record)
	tx.mutations = append(tx.mutations, mutation)
	tx.ledger[record.Key] = record
	if mutation.Definition != nil {
		tx.pubs[mutation.Definition.ID] = mutation.BasePublication
	}
	if mutation.Revision != nil {
		tx.revisions[mutation.Revision.ID] = *mutation.Revision
	}
	if mutation.Publication != nil {
		tx.pubs[record.Result.CampaignID] = mutation.Publication.After
	}
	if mutation.Interaction != nil && mutation.Interaction.Changed {
		state := mutation.Interaction.After
		tx.state = &state
		pub := mutation.BasePublication
		pub.InteractionVersion = record.Result.InteractionVersion
		tx.pubs[pub.CampaignID] = pub
	}
	return nil
}
