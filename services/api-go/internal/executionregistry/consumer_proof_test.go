package executionregistry

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
)

func TestFutureSemanticInterfaceRemainsClosedToOldConsumers(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(manifestJSON, &doc); err != nil {
		t.Fatal(err)
	}
	interfaces := doc["interfaces"].(map[string]any)
	for _, op := range []string{"open", "close"} {
		interfaces["perp.positionManager."+op+".v2"] = interfaces["perp.positionManager."+op+".v1"]
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	future, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	base := mustBuild(t, readFile(t, realDecl), realDeployments)
	for _, op := range []string{OpOpenPosition, OpClosePosition} {
		for _, side := range []string{"web", "server"} {
			t.Run(op+"/"+side, func(t *testing.T) {
				snapshot := copySnapshot(t, base)
				for i := range snapshot.Entries {
					if snapshot.Entries[i].Name == "PositionManager" {
						snapshot.Entries[i].Interfaces = append(snapshot.Entries[i].Interfaces, "perp.positionManager.open.v2", "perp.positionManager.close.v2")
						sort.Strings(snapshot.Entries[i].Interfaces)
					}
				}
				for i := range snapshot.Bindings {
					if snapshot.Bindings[i].Operation == op {
						id := snapshot.Bindings[i].InterfaceID
						snapshot.Bindings[i].InterfaceID = id[:len(id)-1] + "2"
					}
				}
				rehashSnapshot(&snapshot)
				snapshot.ManifestDigest = future.Digest
				request := frozenCheckRequest(t, base, 1, op)
				binding, _ := snapshot.Binding(sepolia, op, "ETH-USD")
				request.BindingDigest = binding.BindingDigest
				server := future
				if side == "server" {
					server = BuiltinManifest()
					request.Consumer.ManifestDigest = future.Digest
					request.Consumer.Interfaces = []string{binding.InterfaceID}
				}
				// A synthetic future publisher isolates consumer negotiation; the current
				// database publication contract intentionally cannot stage this manifest.
				store := &memStore{revisions: map[int64]Snapshot{1: snapshot}, active: 1, version: 1}
				status, body := invokeCheck(t, NewService(store, server), request)
				if status != 422 || body["reason"] != "consumerIncompatible" {
					t.Fatalf("future %s interface accepted by old %s: %d %v", op, side, status, body)
				}
			})
		}
	}
}
func TestUnknownSemanticInterfaceRefusedAtPublicationAndActivation(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	ctx := context.Background()
	invalid := mustBuild(t, readFile(t, realDecl), realDeployments)
	for i := range invalid.Entries {
		if invalid.Entries[i].Name == "PerpOracle" {
			invalid.Entries[i].Interfaces = []string{"perp.positionManager.open.v2"}
		}
	}
	rehashSnapshot(&invalid)
	t.Run("stage", func(t *testing.T) {
		if _, err := repo.Stage(ctx, invalid, "proof", "unknown interface"); !errors.Is(err, ErrBuild) {
			t.Fatalf("unknown interface staged: %v", err)
		}
	})
	t.Run("seal", func(t *testing.T) {
		tx, err := db.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		rev := writeUncheckedSnapshot(t, tx, invalid)
		_, err = tx.Exec(ctx, `UPDATE execution_registry_snapshots SET sealed=true,content_digest=$2 WHERE revision=$1`, rev, invalid.ContentDigest)
		if sqlState(err) != "EXR03" {
			t.Fatalf("unknown interface sealed: %v", err)
		}
	})
	t.Run("activation", func(t *testing.T) {
		proofExec(t, db.owner, `ALTER TABLE execution_registry_snapshots DISABLE TRIGGER execution_registry_snapshots_write`)
		tx, err := db.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		rev := writeUncheckedSnapshot(t, tx, invalid)
		proofExec(t, tx, `UPDATE execution_registry_snapshots SET sealed=true,content_digest=$2 WHERE revision=$1`, rev, invalid.ContentDigest)
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		proofExec(t, db.owner, `ALTER TABLE execution_registry_snapshots ENABLE TRIGGER execution_registry_snapshots_write`)
		if _, err = repo.Activate(ctx, 0, rev, "proof", "unknown interface", nil); !errors.Is(err, ErrNotSealed) {
			t.Fatalf("unknown interface repository activation: %v", err)
		}
		_, err = db.owner.Exec(ctx, `SELECT execution_registry_activate(0,$1,'proof','unknown interface','[]')`, rev)
		if sqlState(err) != "EXR03" {
			t.Fatalf("unknown interface SQL activation: %v", err)
		}
	})
}

func TestUnknownSemanticInterfaceSnapshotRejected(t *testing.T) {
	snapshot := mustBuild(t, readFile(t, realDecl), realDeployments)
	for i := range snapshot.Entries {
		if snapshot.Entries[i].Name == "PerpOracle" {
			snapshot.Entries[i].Interfaces = []string{"perp.positionManager.open.v2"}
		}
	}
	rehashSnapshot(&snapshot)
	if err := ValidateSnapshot(snapshot, BuiltinManifest()); err == nil {
		t.Fatal("unknown semantic interface accepted")
	}
}

func TestDeclarationsRejectUnknownSemanticInterfaceAndDisabledExit(t *testing.T) {
	for _, mode := range []string{"unknown-interface", "disabled-exit"} {
		t.Run(mode, func(t *testing.T) {
			raw := editDecl(t, func(d map[string]any) {
				chain := chainDecl(d, "11155111")
				if mode == "unknown-interface" {
					chain["entries"].(map[string]any)["PerpOracle"] = map[string]any{"interfaces": []any{"perp.positionManager.open.v2"}}
				} else {
					chain["operations"].(map[string]any)[OpClosePosition] = map[string]any{"status": "disabled"}
				}
			})
			if _, _, err := ParseDeclarations(raw, BuiltinManifest()); err == nil {
				t.Fatalf("%s declaration accepted", mode)
			}
		})
	}
}
