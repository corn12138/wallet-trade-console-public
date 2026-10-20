package executionregistry

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
)

func copySnapshot(t *testing.T, s Snapshot) Snapshot {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var out Snapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Rehashing adversarial values proves rejection is about dependency semantics,
// not merely an accidental digest mismatch left by a test fixture.
func rehashSnapshot(s *Snapshot) {
	for i := range s.Entries {
		s.Entries[i].EntryDigest = EntryDigest(s.Entries[i])
	}
	markets := map[int]map[string]Market{}
	for i := range s.Markets {
		m := &s.Markets[i]
		m.MarketDigest = MarketDigest(*m, s.entriesByChain(m.ChainID))
		if markets[m.ChainID] == nil {
			markets[m.ChainID] = map[string]Market{}
		}
		markets[m.ChainID][m.Symbol] = *m
	}
	for i := range s.Bindings {
		b := &s.Bindings[i]
		b.BindingDigest = BindingDigest(*b, s.entriesByChain(b.ChainID), markets[b.ChainID][b.Market])
	}
	s.ContentDigest = ContentDigest(*s)
}

func invalidSnapshots(t *testing.T, valid Snapshot) map[string]Snapshot {
	t.Helper()
	mutations := map[string]func(*Snapshot){
		"missing spender role": func(s *Snapshot) {
			for i := range s.Bindings {
				if s.Bindings[i].Operation == OpApproveCollateral {
					s.Bindings[i].Roles = map[string]string{}
				}
			}
		},
		"wrong index role": func(s *Snapshot) {
			for i := range s.Bindings {
				if s.Bindings[i].Operation == OpOpenPosition {
					s.Bindings[i].Roles["indexToken"] = "MockUSDC"
				}
			}
		},
		"missing component":       func(s *Snapshot) { s.Markets[0].Components = append(s.Markets[0].Components, "ZMissing") },
		"duplicate component":     func(s *Snapshot) { s.Markets[0].Components = []string{"PerpMarket", "PerpMarket"} },
		"unknown entry interface": func(s *Snapshot) { s.Entries[0].Interfaces = []string{"perp.positionManager.open.v2"} },
		"undeclared target interface": func(s *Snapshot) {
			for i := range s.Entries {
				if s.Entries[i].Name == "PositionManager" {
					s.Entries[i].Interfaces = []string{"perp.positionManager.close.v1"}
				}
			}
		},
		"wrong operation interface": func(s *Snapshot) {
			for i := range s.Bindings {
				if s.Bindings[i].Operation == OpOpenPosition {
					s.Bindings[i].InterfaceID = "perp.positionManager.close.v1"
				}
			}
		},
		"wrong precision": func(s *Snapshot) { s.Markets[0].PricePrecision = 29 },
		"missing exit": func(s *Snapshot) {
			var bindings []Binding
			for _, b := range s.Bindings {
				if b.Operation != OpClosePosition {
					bindings = append(bindings, b)
				}
			}
			s.Bindings = bindings
		},
		"unknown manifest": func(s *Snapshot) { s.ManifestDigest = strings.Repeat("f", 64) },
	}
	out := map[string]Snapshot{}
	for name, mutate := range mutations {
		s := copySnapshot(t, valid)
		mutate(&s)
		rehashSnapshot(&s)
		out[name] = s
	}
	stale := copySnapshot(t, valid)
	stale.Entries[0].Address = "0x" + strings.Repeat("e", 40)
	out["stale semantic digest"] = stale
	return out
}

func TestSnapshotValidationRejectsResealedSemanticChanges(t *testing.T) {
	valid := mustBuild(t, readFile(t, realDecl), realDeployments)
	if err := ValidateSnapshot(valid, BuiltinManifest()); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for name, s := range invalidSnapshots(t, valid) {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSnapshot(s, BuiltinManifest()); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	compatible := copySnapshot(t, valid)
	compatible.Entries[0].ABIHash = strings.Repeat("f", 64)
	if err := ValidateSnapshot(compatible, BuiltinManifest()); err != nil {
		t.Fatalf("compatible ABI audit change: %v", err)
	}
}

func TestCheckRejectsUnknownConsumerBuild(t *testing.T) {
	valid := mustBuild(t, readFile(t, realDecl), realDeployments)
	service := NewService(&memStore{revisions: map[int64]Snapshot{1: valid}, active: 1, version: 1}, BuiltinManifest())
	for name, mutate := range map[string]func(*CheckRequest){
		"unknown consumer": func(r *CheckRequest) { r.Consumer.ID = "unknown-app" },
		"unknown manifest": func(r *CheckRequest) { r.Consumer.ManifestDigest = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			got := checkFor(t, service, valid, "1", OpApproveCollateral, mutate)
			if got.HTTPStatus != http.StatusUnprocessableEntity || got.Reason != "consumerIncompatible" {
				t.Fatalf("unknown consumer accepted: %+v", got)
			}
		})
	}
}

func TestReviewAuthorizesOnlyTheDeclaredOperation(t *testing.T) {
	valid := mustBuild(t, readFile(t, realDecl), realDeployments)
	for i := range valid.Entries {
		if valid.Entries[i].Name == "PositionManager" {
			valid.Entries[i].Interfaces = []string{"perp.positionManager.close.v1"}
		}
	}
	entries := valid.entriesByChain(sepolia)
	service := NewService(&memStore{revisions: map[int64]Snapshot{1: valid}, active: 1, version: 1}, BuiltinManifest())
	data := calldata(t, BuiltinManifest().Interfaces["perp.positionManager.open.v1"], entries["MockWETH"].Address, entries["MockUSDC"].Address, "1", "2", "true", "3", "4")
	got := service.ResolveReview(context.Background(), sepolia, entries["PositionManager"].Address, data)
	if got.ContractSupports(sepolia, "PositionManager", "perp.positionManager.open.v1") {
		t.Fatal("undeclared open authorized by close interface")
	}
	if !got.ContractSupports(sepolia, "PositionManager", "perp.positionManager.close.v1") {
		t.Fatal("declared close rejected")
	}
	if got.ContractSupports(31337, "PositionManager", "perp.positionManager.close.v1") {
		t.Fatal("other chain authorized")
	}
	var absent *Service
	unavailable := absent.ResolveReview(context.Background(), sepolia, entries["PositionManager"].Address, data)
	if unavailable.State != "unavailable" || unavailable.Reason != ReasonAuthorityUnavailable {
		t.Fatalf("unavailable: %+v", unavailable)
	}
}

func TestDatabaseManifestContractMatchesBuiltin(t *testing.T) {
	schemaPath := repoRoot + "packages/database/prisma/migrations/20261014120000_execution_registry_snapshots/migration.sql"
	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		// Public releases initialize from a standalone schema without private
		// migration history; its publication guard must enforce the same digest.
		schemaPath = repoRoot + "scripts/public-db/schema.sql"
	}
	schema := string(readFile(t, schemaPath))
	if !strings.Contains(schema, "manifest_digest = '"+BuiltinManifest().Digest+"'") {
		t.Fatal("SQL publication manifest is not synchronized with the consumer manifest")
	}
}

func TestSnapshotCodeBlockFitsPostgresBigint(t *testing.T) {
	for _, value := range []uint64{math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64} {
		snapshot := mustBuild(t, readFile(t, realDecl), realDeployments)
		snapshot.Entries[0].CodeBlockNumber = value
		rehashSnapshot(&snapshot)
		err := ValidateSnapshot(snapshot, BuiltinManifest())
		if (err == nil) != (value <= math.MaxInt64) {
			t.Fatalf("code block %d: %v", value, err)
		}
	}
}
