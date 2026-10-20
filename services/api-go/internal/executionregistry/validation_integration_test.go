package executionregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Direct SQL intentionally bypasses Stage's Go validation, using exactly the
// publisher's permitted writes. The database must enforce the same boundary.
func writeUncheckedSnapshot(t *testing.T, tx pgx.Tx, s Snapshot) int64 {
	t.Helper()
	ctx := context.Background()
	var revision int64
	err := tx.QueryRow(ctx, `INSERT INTO execution_registry_snapshots(schema_version,manifest_digest,declarations_digest,source_summary,staged_by,reason) VALUES($1,$2,$3,'{}','test','direct publisher') RETURNING revision`, s.SchemaVersion, s.ManifestDigest, s.DeclarationsDigest).Scan(&revision)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(table string, values map[string]any) {
		t.Helper()
		values["revision"] = revision
		raw, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s SELECT (jsonb_populate_record(NULL::%s,$1)).*`, table, table), raw)
		if err != nil {
			t.Fatalf("%s insert: %v", table, err)
		}
	}
	for _, e := range s.Entries {
		insert("execution_registry_entries", map[string]any{"chain_id": e.ChainID, "name": e.Name, "address": e.Address, "deployment_file": e.DeploymentFile, "deployment_timestamp": e.DeploymentTimestamp, "abi_hash": e.ABIHash, "interfaces": e.Interfaces, "code_hash": e.CodeHash, "code_block_number": e.CodeBlockNumber, "code_block_hash": e.CodeBlockHash, "entry_digest": e.EntryDigest})
	}
	for _, m := range s.Markets {
		insert("execution_registry_markets", map[string]any{"chain_id": m.ChainID, "symbol": m.Symbol, "position_manager": m.PositionManager, "index_token": m.IndexToken, "collateral_token": m.CollateralToken, "index_decimals": m.IndexDecimals, "collateral_decimals": m.CollateralDecimals, "price_precision": m.PricePrecision, "components": m.Components, "market_digest": m.MarketDigest})
	}
	for _, b := range s.Bindings {
		insert("execution_registry_bindings", map[string]any{"chain_id": b.ChainID, "operation": b.Operation, "market": b.Market, "status": b.Status, "interface_id": b.InterfaceID, "target_name": b.Target, "roles": b.Roles, "binding_digest": b.BindingDigest})
	}
	return revision
}

func TestDatabaseRejectsForgedPublisherSnapshots(t *testing.T) {
	db := freshDatabase(t)
	ctx := context.Background()
	valid := mustBuild(t, readFile(t, realDecl), realDeployments)
	for name, s := range invalidSnapshots(t, valid) {
		t.Run(name, func(t *testing.T) {
			tx, err := db.owner.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			revision := writeUncheckedSnapshot(t, tx, s)
			_, err = tx.Exec(ctx, `UPDATE execution_registry_snapshots SET sealed=true,content_digest=$2 WHERE revision=$1`, revision, s.ContentDigest)
			if sqlState(err) != "EXR03" {
				t.Fatalf("forged snapshot seal = %v, want EXR03", err)
			}
		})
	}
	// A fully presealed INSERT must not evade the validation UPDATE trigger.
	_, err := db.owner.Exec(ctx, `INSERT INTO execution_registry_snapshots(schema_version,manifest_digest,declarations_digest,source_summary,staged_by,reason,sealed,content_digest) VALUES(1,$1,$1,'{}','test','presealed',true,$1)`, strings.Repeat("a", 64))
	if sqlState(err) != "EXR03" {
		t.Fatalf("presealed insert: %v", err)
	}
}

func TestActivationRechecksCorruptedSemanticFields(t *testing.T) {
	db := freshDatabase(t)
	ctx := context.Background()
	repo := NewRepository(db.owner)
	valid := mustBuild(t, readFile(t, realDecl), realDeployments)
	rev := stage(t, repo, valid)
	// Only a superuser can plant this impossible state; activation must still
	// detect it instead of trusting an aggregate of stale child digests.
	if _, err := db.owner.Exec(ctx, `ALTER TABLE execution_registry_bindings DISABLE TRIGGER execution_registry_bindings_write;
 UPDATE execution_registry_bindings SET roles='{}' WHERE operation='trade.approveCollateral';
 ALTER TABLE execution_registry_bindings ENABLE TRIGGER execution_registry_bindings_write`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.owner.Exec(ctx, `SELECT execution_registry_activate(0,$1,'test','corrupt','[]')`, rev); sqlState(err) != "EXR03" {
		t.Fatalf("corrupt activation: %v", err)
	}
	var count int
	if err := db.owner.QueryRow(ctx, `SELECT count(*) FROM execution_registry_activations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("activation history changed: %d %v", count, err)
	}
}
