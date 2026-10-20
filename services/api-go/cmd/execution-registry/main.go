// Command execution-registry is the operator's publication path for the
// ExecutionRegistrySnapshot authority (docs/claude-web3-lg03b-task-2026-10-14.md
// §4). It is the only writer besides the API's read-only use; there is no HTTP
// admin route (D2).
//
// The database is resolved from DATABASE_URL or the compound DATABASE_*
// variables, and each chain's RPC from EXECUTION_REGISTRY_RPC_<chainId>: both
// can carry credentials, so neither is accepted in argv or ever printed.
//
// Usage:
//
//	execution-registry stage --deployments D --artifacts D --actor NAME --reason TEXT
//	execution-registry activate --revision N --expected-activation-version V --actor NAME --reason TEXT
//	execution-registry rollback --revision N --expected-activation-version V --actor NAME --reason TEXT
//	execution-registry status
//	execution-registry diff --from N --to M
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/executionregistry"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/runtimeenv"
)

const usage = `usage: execution-registry <command>

  stage     --deployments DIR --artifacts DIR --actor NAME --reason TEXT
  activate  --revision N --expected-activation-version V --actor NAME --reason TEXT
  rollback  --revision N --expected-activation-version V --actor NAME --reason TEXT
  status
  diff      --from N --to M
RPC per chain: EXECUTION_REGISTRY_RPC_<chainId>. Database: DATABASE_URL (UTF8 encoding required).
`

var actorPattern = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,64}$`)

// registry is the part of the repository the commands use.
type registry interface {
	Stage(ctx context.Context, s executionregistry.Snapshot, actor, reason string) (int64, error)
	Activate(ctx context.Context, expected, revision int64, actor, reason string, obs []executionregistry.CodeObservation) (int64, error)
	ActiveRevision(ctx context.Context) (executionregistry.Active, error)
	Revision(ctx context.Context, revision int64) (executionregistry.Snapshot, error)
	Audit(ctx context.Context) (executionregistry.Status, error)
}

type env struct {
	open func(context.Context) (registry, func(), error)
	code func(chains []int) (executionregistry.CodeReader, error)
	read func(string) ([]byte, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, env{open: openRegistry, code: rpcCode, read: os.ReadFile}))
}

func openRegistry(ctx context.Context) (registry, func(), error) {
	url, err := runtimeenv.Resolve()
	if err != nil || url == "" {
		return nil, nil, errors.New("DATABASE_URL (or the compound DATABASE_* variables) is required")
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		return nil, nil, errors.New("cannot connect to the database")
	}
	repo := executionregistry.NewRepository(pool)
	if err := repo.Probe(ctx); err != nil {
		pool.Close()
		return nil, nil, errors.New("execution registry requires a migrated UTF8 database")
	}
	return repo, pool.Close, nil
}

func rpcCode(chains []int) (executionregistry.CodeReader, error) {
	clients := map[int]*rpc.Client{}
	for _, chain := range chains {
		url := strings.TrimSpace(os.Getenv("EXECUTION_REGISTRY_RPC_" + strconv.Itoa(chain)))
		if url == "" {
			return nil, fmt.Errorf("EXECUTION_REGISTRY_RPC_%d is required to observe code on chain %d", chain, chain)
		}
		clients[chain] = rpc.NewClient(url, 0)
	}
	return executionregistry.RPCCodeReader{Clients: clients}, nil
}

func run(args []string, stdout, stderr io.Writer, e env) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)

	deploymentsDir := flags.String("deployments", "", "deployment records directory")
	artifactsDir := flags.String("artifacts", "", "contract artifacts directory")
	actor := flags.String("actor", "", "operator name recorded with the change")
	reason := flags.String("reason", "", "reason recorded with the change")
	revision := flags.Int64("revision", 0, "revision to activate")
	expected := flags.Int64("expected-activation-version", -1, "current activation version (CAS)")
	from := flags.Int64("from", 0, "diff from revision")
	to := flags.Int64("to", 0, "diff to revision")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	needsActor := args[0] == "stage" || args[0] == "activate" || args[0] == "rollback"
	if needsActor && (!actorPattern.MatchString(*actor) || len(strings.TrimSpace(*reason)) == 0 || len(*reason) > 500) {
		fmt.Fprintln(stderr, "error: --actor (1-64 of [A-Za-z0-9._@:-]) and --reason (1-500 chars) are required")
		return 2
	}
	switch args[0] {
	case "stage", "activate", "rollback", "status", "diff":
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	reg, closeFn, err := e.open(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	defer closeFn()
	manifest := executionregistry.BuiltinManifest()
	switch args[0] {
	case "stage":
		declarations, err := reviewedDeclarations()
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		raw, err := e.read(declarations)
		if err != nil || *deploymentsDir == "" || *artifactsDir == "" {
			fmt.Fprintln(stderr, "error: reviewed declarations, --deployments and --artifacts are required")
			return 2
		}
		decl, _, err := executionregistry.ParseDeclarations(raw, manifest)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		var chains []int
		for key := range decl.Chains {
			n, _ := strconv.Atoi(key)
			chains = append(chains, n)
		}
		code, err := e.code(chains)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		snapshot, err := executionregistry.Build(ctx, executionregistry.BuildInput{Declarations: raw, DeploymentsDir: *deploymentsDir,
			ArtifactsDir: *artifactsDir, Code: code, Manifest: manifest})
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		rev, err := reg.Stage(ctx, snapshot, *actor, *reason)
		if err != nil {
			fmt.Fprintln(stderr, "error: stage failed:", err)
			return 1
		}
		fmt.Fprintf(stdout, "staged revision %d content %s (%d entries, %d markets, %d bindings)\n", rev, snapshot.ContentDigest,
			len(snapshot.Entries), len(snapshot.Markets), len(snapshot.Bindings))
		if active, err := reg.ActiveRevision(ctx); err == nil {
			for _, line := range executionregistry.Diff(active.Snapshot, snapshot) {
				fmt.Fprintln(stdout, line)
			}
		}
		return 0
	case "activate", "rollback":
		if *revision <= 0 || *expected < 0 {
			fmt.Fprintln(stderr, "error: --revision and --expected-activation-version are required")
			return 2
		}
		snapshot, err := reg.Revision(ctx, *revision)
		if err != nil {
			fmt.Fprintln(stderr, "error: revision is not a sealed snapshot")
			return 1
		}
		if err := executionregistry.ValidateSnapshot(snapshot, manifest); err != nil {
			fmt.Fprintln(stderr, "error: revision has unsupported or inconsistent execution semantics")
			return 1
		}
		code, err := e.code(snapshot.ChainIDs())
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		obs, err := executionregistry.Revalidate(ctx, snapshot, code)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		version, err := reg.Activate(ctx, *expected, *revision, *actor, *reason, obs)
		switch {
		case errors.Is(err, executionregistry.ErrActivationConflict):
			fmt.Fprintln(stderr, "error: ACTIVATION_CONFLICT: the activation version changed; re-read status")
			return 1
		case err != nil:
			fmt.Fprintln(stderr, "error: activation failed:", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s revision %d as activation version %d\n", map[string]string{"activate": "activated", "rollback": "rolled back to"}[args[0]], *revision, version)
		return 0
	case "status":
		st, err := reg.Audit(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
		if st.Active == nil {
			fmt.Fprintln(stdout, "no active revision")
		} else {
			fmt.Fprintf(stdout, "active revision %d, activation version %d\n", st.Active.Revision, st.Active.ActivationVersion)
		}
		fmt.Fprintf(stdout, "history versions %d, consistent %t %s\n", len(st.HistoryVersions), st.Consistent, st.ConsistencyDetail)
		if !st.Consistent {
			return 1
		}
		return 0
	case "diff":
		a, errA := reg.Revision(ctx, *from)
		b, errB := reg.Revision(ctx, *to)
		if errA != nil || errB != nil {
			fmt.Fprintln(stderr, "error: --from and --to must be sealed revisions")
			return 1
		}
		for _, line := range executionregistry.Diff(a, b) {
			fmt.Fprintln(stdout, line)
		}
		return 0
	}
	return 2
}

// Publication always uses the repository's reviewed declaration file. Arbitrary
// declaration paths would let an operator bypass that source-of-truth boundary.
func reviewedDeclarations() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, "packages", "shared", "execution-registry", "declarations.json")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run stage from the repository containing reviewed execution-registry declarations")
		}
		dir = parent
	}
}
