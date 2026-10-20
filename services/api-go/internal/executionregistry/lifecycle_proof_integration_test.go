package executionregistry

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func proofExec(t *testing.T, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func TestSealedInsertAndImmutableRowSurfaces(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	ctx := context.Background()
	snapshot := mustBuild(t, readFile(t, realDecl), realDeployments)
	var spare Market
	for _, m := range snapshot.Markets {
		if m.ChainID == sepolia && m.Symbol == "ETH-USD" {
			spare = m
		}
	}
	spare.Symbol = "LATE-PROOF"
	snapshot.Markets = append(snapshot.Markets, spare)
	exit, _ := snapshot.Binding(sepolia, OpClosePosition, "ETH-USD")
	exit.Market = spare.Symbol
	snapshot.Bindings = append(snapshot.Bindings, exit)
	rehashSnapshot(&snapshot)
	rev := stage(t, repo, snapshot)
	if _, err := repo.Activate(ctx, 0, rev, "proof", "immutable", nil); err != nil {
		t.Fatal(err)
	}
	empty := Snapshot{SchemaVersion: 1, ManifestDigest: BuiltinManifest().Digest, DeclarationsDigest: snapshot.DeclarationsDigest, SourceSummary: map[string]any{}}
	empty.ContentDigest = ContentDigest(empty)
	emptyRevision := stage(t, repo, empty)
	for _, table := range []string{"entries", "markets", "bindings"} {
		t.Run("insert-"+table, func(t *testing.T) {
			tx, err := db.owner.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			open := writeUncheckedSnapshot(t, tx, snapshot)
			proofExec(t, tx, `UPDATE execution_registry_snapshots SET sealed=true,content_digest=$2 WHERE revision=$1`, open, snapshot.ContentDigest)
			changes, filter := "'name','LateProof','entry_digest',repeat('f',64)", ""
			if table == "markets" {
				changes = "'symbol','NEW-PROOF','market_digest',repeat('f',64)"
			}
			if table == "bindings" {
				changes = "'market','LATE-PROOF','binding_digest',repeat('f',64)"
				filter = " AND chain_id=11155111 AND operation='trade.approveCollateral'"
			}
			_, err = tx.Exec(ctx, "INSERT INTO execution_registry_"+table+" SELECT (jsonb_populate_record(NULL::execution_registry_"+table+",to_jsonb(row)||jsonb_build_object("+changes+"))).* FROM execution_registry_"+table+" row WHERE revision=$1"+filter+" LIMIT 1", open)
			if sqlState(err) != "EXR01" {
				t.Fatalf("sealed %s insert=%v", table, err)
			}
		})
	}
	for _, tc := range []struct{ table, column string }{{"snapshots", "reason"}, {"entries", "address"}, {"markets", "components"}, {"bindings", "roles"}, {"activations", "reason"}} {
		for _, op := range []string{"UPDATE", "DELETE"} {
			t.Run(strings.ToLower(op)+"-"+tc.table, func(t *testing.T) {
				tx, err := db.owner.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				query := "DELETE FROM execution_registry_" + tc.table
				if op == "DELETE" {
					switch tc.table {
					case "snapshots":
						query += fmt.Sprintf(" WHERE revision=%d", emptyRevision)
					case "entries":
						query += " WHERE name='PerpOracle'"
					case "markets":
						// Remove only referencing fixture bindings so a disabled market
						// guard permits deletion instead of failing at a foreign key.
						proofExec(t, tx, `ALTER TABLE execution_registry_bindings DISABLE TRIGGER execution_registry_bindings_write`)
						proofExec(t, tx, `DELETE FROM execution_registry_bindings WHERE market='LATE-PROOF'`)
						proofExec(t, tx, `ALTER TABLE execution_registry_bindings ENABLE TRIGGER execution_registry_bindings_write`)
						query += " WHERE symbol='LATE-PROOF'"
					}
				}
				if op == "UPDATE" {
					query = "UPDATE execution_registry_" + tc.table + " SET " + tc.column + "=" + tc.column
				}
				_, err = tx.Exec(ctx, query)
				if sqlState(err) != "EXR01" {
					t.Fatalf("immutable %s %s=%v", op, tc.table, err)
				}
			})
		}
	}
}
func TestForeignBuilderCannotAppendOrSealVisibleUnsealedRevision(t *testing.T) {
	db := freshDatabase(t)
	ctx := context.Background()
	valid := mustBuild(t, readFile(t, realDecl), realDeployments)
	// A privileged fixture makes a committed unsealed header visible. The
	// restored guards must reject its foreign builder, independently of MVCC.
	proofExec(t, db.owner, `ALTER TABLE execution_registry_snapshots DISABLE TRIGGER execution_registry_snapshots_sealed`)
	tx, err := db.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rev := writeUncheckedSnapshot(t, tx, valid)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	proofExec(t, db.owner, `ALTER TABLE execution_registry_snapshots ENABLE TRIGGER execution_registry_snapshots_sealed`)
	var foreign bool
	if err = db.owner.QueryRow(ctx, `SELECT builder_xid<>pg_current_xact_id() AND NOT sealed FROM execution_registry_snapshots WHERE revision=$1`, rev).Scan(&foreign); err != nil || !foreign {
		t.Fatal("fixture is not a visible foreign-builder header")
	}
	e := valid.Entries[0]
	_, err = db.owner.Exec(ctx, `INSERT INTO execution_registry_entries VALUES($1,$2,'ForeignMember',$3,$4,$5,$6,$7,$8,$9,$10,$11)`, rev, e.ChainID, e.Address, e.DeploymentFile, e.DeploymentTimestamp, e.ABIHash, e.Interfaces, e.CodeHash, e.CodeBlockNumber, e.CodeBlockHash, strings.Repeat("f", 64))
	if sqlState(err) != "EXR01" {
		t.Fatalf("foreign builder appended visible member: %v", err)
	}
	_, err = db.owner.Exec(ctx, `UPDATE execution_registry_snapshots SET sealed=true,content_digest=$2 WHERE revision=$1`, rev, valid.ContentDigest)
	if sqlState(err) != "EXR01" {
		t.Fatalf("foreign builder sealed visible revision: %v", err)
	}
}
func TestCompleteSnapshotWrongDigestAndUnsealedCommitAreIndependent(t *testing.T) {
	db := freshDatabase(t)
	ctx := context.Background()
	valid := mustBuild(t, readFile(t, realDecl), realDeployments)
	for _, mode := range []string{"wrong-digest", "unsealed-commit"} {
		t.Run(mode, func(t *testing.T) {
			tx, err := db.owner.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			rev := writeUncheckedSnapshot(t, tx, valid)
			if mode == "wrong-digest" {
				proofExec(t, tx, `SELECT execution_registry_validate($1)`, rev)
				_, err = tx.Exec(ctx, `UPDATE execution_registry_snapshots SET sealed=true,content_digest=$2 WHERE revision=$1`, rev, strings.Repeat("0", 64))
			} else {
				err = tx.Commit(ctx)
			}
			if sqlState(err) != "EXR03" {
				t.Fatalf("%s accepted otherwise valid snapshot: %v", mode, err)
			}
		})
	}
}
func TestPointerRequiresSameTransactionHistoryAndAuditFindsGaps(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	ctx := context.Background()
	rev := stage(t, repo, mustBuild(t, readFile(t, realDecl), realDeployments))
	if _, err := repo.Activate(ctx, 0, rev, "proof", "first", nil); err != nil {
		t.Fatal(err)
	}
	proofExec(t, db.owner, `INSERT INTO execution_registry_activations(activation_version,revision,previous_revision,activated_by,reason,code_observations)VALUES(2,$1,$1,'proof','prior transaction','[]')`, rev)
	_, err := db.owner.Exec(ctx, `UPDATE execution_registry_pointer SET activation_version=2 WHERE id=1`)
	if sqlState(err) != "EXR04" {
		t.Fatalf("historical record authorized pointer update: %v", err)
	}
	st, err := repo.Audit(ctx)
	if err != nil || st.Consistent {
		t.Fatalf("orphan history escaped audit: %+v %v", st, err)
	}
	proofExec(t, db.owner, `INSERT INTO execution_registry_activations(activation_version,revision,previous_revision,activated_by,reason,code_observations)VALUES(4,$1,$1,'proof','gap','[]')`, rev)
	proofExec(t, db.owner, `ALTER TABLE execution_registry_pointer DISABLE TRIGGER execution_registry_pointer_write`)
	proofExec(t, db.owner, `UPDATE execution_registry_pointer SET activation_version=4 WHERE id=1`)
	proofExec(t, db.owner, `ALTER TABLE execution_registry_pointer ENABLE TRIGGER execution_registry_pointer_write`)
	st, err = repo.Audit(ctx)
	if err != nil || st.Consistent || fmt.Sprint(st.HistoryVersions) != "[1 2 4]" {
		t.Fatalf("history gap escaped audit: %+v %v", st, err)
	}
}
func TestRepositoryReadTransactionIsRepeatableAndReadOnly(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	err := repo.readOnly(context.Background(), func(tx pgx.Tx) error {
		var isolation, readOnly string
		if err := tx.QueryRow(context.Background(), `SELECT current_setting('transaction_isolation'),current_setting('transaction_read_only')`).Scan(&isolation, &readOnly); err != nil {
			return err
		}
		if isolation != "repeatable read" || readOnly != "on" {
			t.Errorf("read transaction=%s/%s", isolation, readOnly)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentActivationHasSingleCASWinner(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	ctx := context.Background()
	snapshot := mustBuild(t, readFile(t, realDecl), realDeployments)
	r1, r2 := stage(t, repo, snapshot), stage(t, repo, snapshot)
	if _, err := repo.Activate(ctx, 0, r1, "proof", "first", nil); err != nil {
		t.Fatal(err)
	}
	var wins, conflicts atomic.Int32
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := repo.Activate(ctx, 1, r2, "proof", "concurrent", nil)
			if err == nil {
				wins.Add(1)
			} else if err == ErrActivationConflict {
				conflicts.Add(1)
			} else {
				t.Errorf("unexpected activation: %v", err)
			}
		}()
	}
	close(start)
	workers.Wait()
	if wins.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("CAS race winners=%d conflicts=%d", wins.Load(), conflicts.Load())
	}
}
