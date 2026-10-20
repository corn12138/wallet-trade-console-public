package executionregistry

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQL acceptance for the authority (§4.3). EXECUTION_REGISTRY_TEST_ADMIN_URL
// is a loopback superuser URL ending in /postgres; EXECUTION_REGISTRY_TEST_TEMPLATE
// names a database migrated by the real migration chain. Unset means UNPROVEN.
var dbCounter atomic.Int32

type testDB struct {
	owner *pgxpool.Pool
	url   string // owner URL of the database
	name  string
}

func freshDatabase(t *testing.T) testDB {
	t.Helper()
	admin, template := os.Getenv("EXECUTION_REGISTRY_TEST_ADMIN_URL"), os.Getenv("EXECUTION_REGISTRY_TEST_TEMPLATE")
	if admin == "" || template == "" {
		t.Skip("EXECUTION_REGISTRY_TEST_ADMIN_URL / _TEMPLATE unset: the registry authority is UNPROVEN, not passing")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("admin database configured but unusable: %v", err)
	}
	defer conn.Close(ctx)
	name := fmt.Sprintf("%s_t%d_%d", template, os.Getpid(), dbCounter.Add(1))
	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, pgx.Identifier{name}.Sanitize(), pgx.Identifier{template}.Sanitize())); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		c, err := pgx.Connect(context.Background(), admin)
		if err == nil {
			_, _ = c.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{name}.Sanitize()))
			c.Close(context.Background())
		}
	})
	return testDB{owner: pool, url: u.String(), name: name}
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func stage(t *testing.T, repo *Repository, s Snapshot) int64 {
	t.Helper()
	rev, err := repo.Stage(context.Background(), s, "test", "integration")
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	return rev
}

func TestStageSealActivateRoundTrip(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	ctx := context.Background()
	if _, err := repo.ActiveRevision(ctx); !errors.Is(err, ErrNoActiveRevision) {
		t.Fatalf("empty registry = %v, want ErrNoActiveRevision", err)
	}
	built := mustBuild(t, readFile(t, realDecl), realDeployments)
	rev := stage(t, repo, built)
	var sqlDigest string
	if err := db.owner.QueryRow(ctx, `SELECT execution_registry_content_digest($1)`, rev).Scan(&sqlDigest); err != nil || sqlDigest != built.ContentDigest {
		t.Fatalf("SQL content digest %s, Go %s (%v)", sqlDigest, built.ContentDigest, err)
	}
	loaded, err := repo.Revision(ctx, rev)
	if err != nil || ContentDigest(loaded) != built.ContentDigest || len(loaded.Bindings) != len(built.Bindings) {
		t.Fatalf("round trip differs: %v", err)
	}
	for i, b := range loaded.Bindings {
		entries := loaded.entriesByChain(b.ChainID)
		var market Market
		for _, m := range loaded.Markets {
			if m.ChainID == b.ChainID && m.Symbol == b.Market {
				market = m
			}
		}
		if BindingDigest(b, entries, market) != b.BindingDigest {
			t.Errorf("binding %d does not recompute after the round trip", i)
		}
	}
	version, err := repo.Activate(ctx, 0, rev, "test", "first", nil)
	if err != nil || version != 1 {
		t.Fatalf("activate = %d, %v", version, err)
	}
	active, err := repo.ActiveRevision(ctx)
	if err != nil || active.Revision != rev || active.ActivationVersion != 1 {
		t.Fatalf("active = %+v %v", active, err)
	}
	st, err := repo.Audit(ctx)
	if err != nil || !st.Consistent {
		t.Fatalf("audit = %+v %v", st, err)
	}
}

func TestSealedMembershipCannotChange(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	ctx := context.Background()
	built := mustBuild(t, readFile(t, realDecl), realDeployments)
	rev := stage(t, repo, built)
	e := built.Entries[0]
	insertEntry := func(q interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	}, revision int64, name string) error {
		_, err := q.Exec(ctx, `INSERT INTO execution_registry_entries VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			revision, e.ChainID, name, e.Address, e.DeploymentFile, e.DeploymentTimestamp, e.ABIHash, e.Interfaces, e.CodeHash, 1, e.CodeBlockHash, strings.Repeat("d", 64))
		return err
	}
	if err := insertEntry(db.owner, rev, "Late"); sqlState(err) != "EXR01" {
		t.Errorf("INSERT into a sealed revision = %v, want EXR01", err)
	}
	for name, stmt := range map[string]string{
		"update entry":      `UPDATE execution_registry_entries SET address = address`,
		"delete entry":      `DELETE FROM execution_registry_entries`,
		"update market":     `UPDATE execution_registry_markets SET components = components`,
		"delete binding":    `DELETE FROM execution_registry_bindings`,
		"re-seal":           `UPDATE execution_registry_snapshots SET reason = 'changed'`,
		"delete snapshot":   `DELETE FROM execution_registry_snapshots`,
		"truncate entries":  `TRUNCATE execution_registry_entries CASCADE`,
		"truncate snapshot": `TRUNCATE execution_registry_snapshots CASCADE`,
	} {
		if _, err := db.owner.Exec(ctx, stmt); sqlState(err) != "EXR01" {
			t.Errorf("%s = %v, want EXR01", name, err)
		}
	}
	// A second transaction cannot add members to a revision being staged.
	builder, err := db.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var open int64
	if err := builder.QueryRow(ctx, `INSERT INTO execution_registry_snapshots (schema_version, manifest_digest, declarations_digest, source_summary, staged_by, reason)
		VALUES (1, $1, $1, '{}', 'test', 'open') RETURNING revision`, strings.Repeat("a", 64)).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if err := insertEntry(db.owner, open, "Intruder"); sqlState(err) != "EXR01" {
		t.Errorf("concurrent INSERT during staging = %v, want EXR01", err)
	}
	if _, err := builder.Exec(ctx, `UPDATE execution_registry_snapshots SET sealed = true, content_digest = $2 WHERE revision = $1`, open, strings.Repeat("0", 64)); sqlState(err) != "EXR03" {
		t.Errorf("seal with a wrong digest = %v, want EXR03", err)
	}
	_ = builder.Rollback(ctx)
	unsealed, _ := db.owner.Begin(ctx)
	_, _ = unsealed.Exec(ctx, `INSERT INTO execution_registry_snapshots (schema_version, manifest_digest, declarations_digest, source_summary, staged_by, reason)
		VALUES (1, $1, $1, '{}', 'test', 'unsealed')`, strings.Repeat("a", 64))
	if err := unsealed.Commit(ctx); sqlState(err) != "EXR03" {
		t.Errorf("unsealed commit = %v, want EXR03", err)
	}
	// Normal staging of a new complete revision still works.
	if rev2 := stage(t, repo, built); rev2 <= rev {
		t.Errorf("second stage = %d", rev2)
	}
}

func TestActivationCASRollbackAndConcurrency(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	ctx := context.Background()
	built := mustBuild(t, readFile(t, realDecl), realDeployments)
	r1, r2 := stage(t, repo, built), stage(t, repo, built)
	if _, err := repo.Activate(ctx, 0, r1, "test", "first", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Activate(ctx, 0, r2, "test", "stale", nil); !errors.Is(err, ErrActivationConflict) {
		t.Fatalf("stale CAS = %v", err)
	}
	var wins, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Activate(ctx, 1, r2, "test", "race", nil)
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ErrActivationConflict):
				conflicts.Add(1)
			default:
				t.Errorf("race: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("concurrent activations: %d wins, %d conflicts", wins.Load(), conflicts.Load())
	}
	version, err := repo.Activate(ctx, 2, r1, "test", "rollback", nil)
	if err != nil || version != 3 {
		t.Fatalf("rollback = %d %v, want a higher version 3", version, err)
	}
	st, _ := repo.Audit(ctx)
	if !st.Consistent || fmt.Sprint(st.HistoryVersions) != "[1 2 3]" || st.Active.Revision != r1 {
		t.Fatalf("audit after rollback = %+v", st)
	}
	if _, err := repo.Activate(ctx, 3, 999, "test", "missing", nil); !errors.Is(err, ErrNotSealed) {
		t.Errorf("activate unknown revision = %v", err)
	}
	// Direct pointer changes without their activation record are refused even
	// for the owner; the triggers do not depend on grants.
	for name, stmt := range map[string]string{
		"pointer update": `UPDATE execution_registry_pointer SET active_revision = ` + fmt.Sprint(r2) + `, activation_version = 4`,
		"pointer delete": `DELETE FROM execution_registry_pointer`,
		"history update": `UPDATE execution_registry_activations SET reason = 'x'`,
	} {
		if _, err := db.owner.Exec(ctx, stmt); sqlState(err) != "EXR04" && sqlState(err) != "EXR01" {
			t.Errorf("%s = %v", name, err)
		}
	}
	// A superuser can still bypass triggers; the audit must notice.
	if _, err := db.owner.Exec(ctx, `ALTER TABLE execution_registry_pointer DISABLE TRIGGER USER;
		UPDATE execution_registry_pointer SET activation_version = 9; ALTER TABLE execution_registry_pointer ENABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if st, _ := repo.Audit(ctx); st.Consistent {
		t.Error("the audit did not detect a planted pointer/history inconsistency")
	}
}

func TestReadConsistencyAcrossActivation(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	ctx := context.Background()
	c := counterexamples(t)
	r1, r2 := stage(t, repo, c.n), stage(t, repo, c.c1)
	if _, err := repo.Activate(ctx, 0, r1, "test", "first", nil); err != nil {
		t.Fatal(err)
	}
	other := NewRepository(db.owner)
	got, err := repo.activeWith(ctx, func() {
		if _, err := other.Activate(ctx, 1, r2, "test", "between", nil); err != nil {
			t.Errorf("activation between reads: %v", err)
		}
	})
	if err != nil || got.Revision != r1 || ContentDigest(got.Snapshot) != c.n.ContentDigest {
		t.Fatalf("a read straddling an activation mixed revisions: rev %d err %v", got.Revision, err)
	}
	if now, _ := repo.ActiveRevision(ctx); now.Revision != r2 || ContentDigest(now.Snapshot) != c.c1.ContentDigest {
		t.Fatalf("next read = %d", now.Revision)
	}
}

// Real least-privilege roles through scripts/ops/execution-registry-grants.sql.
func TestLeastPrivilegeRoles(t *testing.T) {
	db := freshDatabase(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d_%d", os.Getpid(), dbCounter.Add(1))
	api, publisher, nobody := "lg03b_api_"+suffix, "lg03b_pub_"+suffix, "lg03b_none_"+suffix
	owner := "lg03b_owner_" + suffix
	// Disposable roles must authenticate on SCRAM-protected CI databases as
	// well as local trust-authenticated clusters; grants alone do not prove it.
	var passwordBytes [32]byte
	if _, err := rand.Read(passwordBytes[:]); err != nil {
		t.Fatal(err)
	}
	password := fmt.Sprintf("%x", passwordBytes)
	for _, role := range []string{api, publisher, nobody} {
		if _, err := db.owner.Exec(ctx, fmt.Sprintf(`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN CREATE ROLE %s LOGIN NOSUPERUSER PASSWORD '%s'; END IF; END $$`, role, role, password)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.owner.Exec(ctx, fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s; GRANT USAGE ON SCHEMA public TO %s`, pgx.Identifier{db.name}.Sanitize(), role, role)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, role := range []string{api, publisher, nobody, owner} {
			_, _ = db.owner.Exec(context.Background(), fmt.Sprintf(`DROP OWNED BY %s; DROP ROLE IF EXISTS %s`, role, role))
		}
	})
	grants := strings.NewReplacer("__API_ROLE__", api, "__PUBLISHER_ROLE__", publisher, "__OWNER_ROLE__", owner).Replace(string(readFile(t, repoRoot+"scripts/ops/execution-registry-grants.sql")))
	if _, err := db.owner.Exec(ctx, grants); err != nil {
		t.Fatalf("grants: %v", err)
	}
	poolAs := func(role string) *pgxpool.Pool {
		u, _ := url.Parse(db.url)
		u.User = url.UserPassword(role, password)
		p, err := pgxpool.New(ctx, u.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		if err := p.Ping(ctx); err != nil {
			t.Fatalf("test role %s could not authenticate", role)
		}
		return p
	}
	apiPool, pubPool, nobodyPool := poolAs(api), poolAs(publisher), poolAs(nobody)
	built := mustBuild(t, readFile(t, realDecl), realDeployments)

	pub := NewRepository(pubPool)
	rev := stage(t, pub, built)
	if _, err := pub.Activate(ctx, 0, rev, "publisher", "first", nil); err != nil {
		t.Fatalf("publisher activation: %v", err)
	}
	if _, err := NewRepository(apiPool).ActiveRevision(ctx); err != nil {
		t.Fatalf("API read: %v", err)
	}
	for name, run := range map[string]func() error{
		"API stages": func() error {
			_, err := NewRepository(apiPool).Stage(ctx, built, "api", "x")
			return errors.Unwrap(err)
		},
		"API activates": func() error {
			_, err := apiPool.Exec(ctx, `SELECT execution_registry_activate(1, $1, 'api', 'x', '[]')`, rev)
			return err
		},
		"API moves pointer": func() error {
			_, err := apiPool.Exec(ctx, `UPDATE execution_registry_pointer SET activation_version = 2`)
			return err
		},
		"API writes history": func() error {
			_, err := apiPool.Exec(ctx, `INSERT INTO execution_registry_activations (activation_version, revision, activated_by, reason, code_observations) VALUES (2, $1, 'api', 'x', '[]')`, rev)
			return err
		},
		"PUBLIC activates": func() error {
			_, err := nobodyPool.Exec(ctx, `SELECT execution_registry_activate(1, $1, 'n', 'x', '[]')`, rev)
			return err
		},
		"PUBLIC reads":      func() error { _, err := nobodyPool.Exec(ctx, `SELECT * FROM execution_registry_pointer`); return err },
		"publisher deletes": func() error { _, err := pubPool.Exec(ctx, `DELETE FROM execution_registry_entries`); return err },
		"publisher moves pointer": func() error {
			_, err := pubPool.Exec(ctx, `UPDATE execution_registry_pointer SET activation_version = 2`)
			return err
		},
	} {
		err := run()
		if sqlState(err) != "42501" && !strings.Contains(fmt.Sprint(err), "permission denied") {
			t.Errorf("%s = %v, want permission denied", name, err)
		}
	}
	// Distinct role names are insufficient when PostgreSQL role membership
	// reintroduces writes after a direct REVOKE.
	for _, tc := range []struct{ name, grant, revoke, normalizableRole string }{
		{"publisher inherits history writer", fmt.Sprintf("GRANT INSERT ON execution_registry_activations TO %s; GRANT %s TO %s", nobody, nobody, publisher), fmt.Sprintf("REVOKE %s FROM %s; REVOKE INSERT ON execution_registry_activations FROM %s", nobody, publisher, nobody), ""},
		{"publisher retains column writer", fmt.Sprintf("GRANT UPDATE(address) ON execution_registry_entries TO %s", publisher), fmt.Sprintf("REVOKE UPDATE(address) ON execution_registry_entries FROM %s", publisher), publisher},
		{"owner retains column writer", fmt.Sprintf("GRANT UPDATE(address) ON execution_registry_entries TO %s", owner), fmt.Sprintf("REVOKE UPDATE(address) ON execution_registry_entries FROM %s", owner), owner},
		{"API inherits publisher", fmt.Sprintf("GRANT %s TO %s", publisher, api), fmt.Sprintf("REVOKE %s FROM %s", publisher, api), ""},
		{"owner inherits another role", fmt.Sprintf("GRANT %s TO %s", nobody, owner), fmt.Sprintf("REVOKE %s FROM %s", nobody, owner), ""},
		{"API inherits an unrelated writer", fmt.Sprintf("GRANT INSERT ON execution_registry_entries TO %s; GRANT %s TO %s", nobody, nobody, api), fmt.Sprintf("REVOKE %s FROM %s; REVOKE INSERT ON execution_registry_entries FROM %s", nobody, api, nobody), ""},
		{"publisher inherits column writer", fmt.Sprintf("GRANT UPDATE(address) ON execution_registry_entries TO %s; GRANT %s TO %s", nobody, nobody, publisher), fmt.Sprintf("REVOKE %s FROM %s; REVOKE UPDATE(address) ON execution_registry_entries FROM %s", nobody, publisher, nobody), ""},
		{"API inherits column writer", fmt.Sprintf("GRANT UPDATE(address) ON execution_registry_entries TO %s; GRANT %s TO %s", nobody, nobody, api), fmt.Sprintf("REVOKE %s FROM %s; REVOKE UPDATE(address) ON execution_registry_entries FROM %s", nobody, api, nobody), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.owner.Exec(ctx, tc.grant); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := db.owner.Exec(ctx, tc.revoke); err != nil {
					t.Error(err)
				}
			}()
			_, err := db.owner.Exec(ctx, grants)
			if tc.normalizableRole == "" {
				if err == nil {
					t.Fatal("unsafe inherited role topology accepted")
				}
				if sqlState(err) != "P0001" {
					t.Fatalf("unexpected isolation error: %v", err)
				}
				return
			}
			// PostgreSQL can remove directly granted column rights during table-level
			// REVOKE ALL. Successful normalization is safe only if effective access is
			// gone; implementations that reject instead must use the isolation error.
			if err != nil {
				if sqlState(err) != "P0001" {
					t.Fatalf("unexpected normalization error: %v", err)
				}
				return
			}
			var canUpdate bool
			if err := db.owner.QueryRow(ctx, `SELECT has_column_privilege($1,'execution_registry_entries','address','UPDATE')`, tc.normalizableRole).Scan(&canUpdate); err != nil || canUpdate {
				t.Fatalf("normalization retained forbidden column privilege: allowed=%v err=%v", canUpdate, err)
			}
		})
	}
	// The function owner is not the migration superuser and cannot log in.
	var functionOwner string
	var canLogin, superuser bool
	if err := db.owner.QueryRow(ctx, `SELECT r.rolname, r.rolcanlogin, r.rolsuper FROM pg_proc p JOIN pg_roles r ON r.oid=p.proowner WHERE p.proname='execution_registry_activate'`).Scan(&functionOwner, &canLogin, &superuser); err != nil || functionOwner != owner || canLogin || superuser {
		t.Fatalf("unsafe function owner: %s login=%v super=%v (%v)", functionOwner, canLogin, superuser, err)
	}
	// The SECURITY DEFINER function resolves names under a fixed path.
	var secdef bool
	var config []string
	if err := db.owner.QueryRow(ctx, `SELECT prosecdef, proconfig FROM pg_proc WHERE proname = 'execution_registry_activate'`).Scan(&secdef, &config); err != nil ||
		!secdef || fmt.Sprint(config) != "[search_path=pg_catalog, pg_temp]" {
		t.Errorf("activate function: secdef %v config %v (%v)", secdef, config, err)
	}
	_ = time.Second
}
