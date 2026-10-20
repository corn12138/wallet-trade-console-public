// Command campaign-legacy-import runs the staging half of the controlled
// legacy campaign import (protocol: docs/campaign-legacy-import-protocol-2026-10-09.md)
// on the deployment control plane. It reads LEGACY_IMPORT_DATABASE_URL from the
// environment (a DSN can carry a password, so it is never accepted in argv) and
// prints shareable JSON: digests, counts and wallet hashes, never raw values.
//
// Usage:
//
//	campaign-legacy-import capture --operator NAME [--max-rows N] [--max-bytes N] [--max-duration D] [--retain-days N] [--report PATH]
//	campaign-legacy-import status --run ID
//	campaign-legacy-import fence --run ID --operator NAME --campaign ID [--campaign ID ...]
//	campaign-legacy-import lift --run ID --operator NAME --campaign ID [--campaign ID ...]
//	campaign-legacy-import verify --run ID --campaign ID [--campaign ID ...]
//	campaign-legacy-import restore-check --run ID
//	campaign-legacy-import abandon --run ID --operator NAME
//
// Exit codes: 0 done, 1 error, 2 usage, 3 the check did not pass (capture
// incomplete, verification drift, archive not restorable).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitNotClean = 3
)

const usage = `usage: campaign-legacy-import <command>

  capture --operator NAME [--max-rows N] [--max-bytes N] [--max-duration D] [--retain-days N] [--report PATH]
  status --run ID
  fence --run ID --operator NAME --campaign ID [--campaign ID ...]
  lift --run ID --operator NAME --campaign ID [--campaign ID ...]
  verify --run ID --campaign ID [--campaign ID ...]
  restore-check --run ID
  abandon --run ID --operator NAME

The database URL is read from LEGACY_IMPORT_DATABASE_URL, never from argv.
`

type campaigns []string

func (c *campaigns) String() string      { return strings.Join(*c, ",") }
func (c *campaigns) Set(id string) error { *c = append(*c, id); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, openPool))
}

func openPool(ctx context.Context) (*pgxpool.Pool, error) {
	url := os.Getenv("LEGACY_IMPORT_DATABASE_URL")
	if url == "" {
		return nil, errors.New("LEGACY_IMPORT_DATABASE_URL is required")
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		return nil, errors.New("cannot connect to the legacy database")
	}
	return pool, nil
}

func run(args []string, stdout, stderr io.Writer, open func(context.Context) (*pgxpool.Pool, error)) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var ids campaigns
	runID := fs.String("run", "", "run ID")
	operator := fs.String("operator", "", "who performs the step")
	maxRows := fs.Int64("max-rows", legacyimport.DefaultLimits.MaxRows, "row budget")
	maxBytes := fs.Int64("max-bytes", legacyimport.DefaultLimits.MaxBytes, "byte budget")
	maxDuration := fs.Duration("max-duration", legacyimport.DefaultLimits.MaxDuration, "time budget")
	retainDays := fs.Int("retain-days", int(legacyimport.DefaultRetention/(24*time.Hour)), "archive retention")
	reportPath := fs.String("report", "", "write the shareable inventory report here (created owner-only; must not exist)")
	fs.Var(&ids, "campaign", "campaign ID (repeatable)")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	need := map[string][]string{
		"capture": {"operator"}, "status": {"run"}, "fence": {"run", "operator", "campaign"}, "lift": {"run", "operator", "campaign"},
		"verify": {"run", "campaign"}, "restore-check": {"run"}, "abandon": {"run", "operator"},
	}
	required, known := need[command]
	if !known {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	for _, name := range required {
		if (name == "run" && *runID == "") || (name == "operator" && *operator == "") || (name == "campaign" && len(ids) == 0) {
			fmt.Fprintf(stderr, "error: --%s is required for %s\n", name, command)
			return exitUsage
		}
	}
	if *retainDays < 1 || *retainDays > 3650 {
		fmt.Fprintln(stderr, "error: --retain-days must be between 1 and 3650")
		return exitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), *maxDuration+time.Minute)
	defer cancel()
	pool, err := open(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	defer pool.Close()
	emit := func(value any) {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(value)
	}
	switch command {
	case "capture":
		var report *os.File
		if *reportPath != "" {
			// O_EXCL keeps an earlier report from being silently replaced.
			if report, err = os.OpenFile(*reportPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
				fmt.Fprintln(stderr, "error: report file:", err)
				return exitError
			}
			defer report.Close()
		}
		result, err := legacyimport.Capture(ctx, pool, legacyimport.CaptureOptions{Operator: *operator,
			Limits:    legacyimport.Limits{MaxRows: *maxRows, MaxBytes: *maxBytes, MaxDuration: *maxDuration},
			Retention: time.Duration(*retainDays) * 24 * time.Hour})
		if report != nil && result.Report != nil {
			encoder := json.NewEncoder(report)
			encoder.SetIndent("", "  ")
			if writeErr := encoder.Encode(result.Report); writeErr != nil {
				fmt.Fprintln(stderr, "error: report file:", writeErr)
				return exitError
			}
		}
		result.Report = nil
		emit(result)
		if errors.Is(err, legacyimport.ErrIncomplete) {
			fmt.Fprintln(stderr, "capture incomplete:", result.LimitHit, "— nothing was archived; raise the limit and capture again")
			return exitNotClean
		}
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitError
		}
	case "status":
		status, err := legacyimport.Status(ctx, pool, *runID)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitError
		}
		emit(status)
	case "fence":
		if err = legacyimport.Fence(ctx, pool, *runID, ids, *operator); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitError
		}
		emit(map[string]any{"runId": *runID, "fenced": []string(ids)})
	case "lift":
		missing, err := legacyimport.Lift(ctx, pool, *runID, ids, *operator)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitError
		}
		if missing == nil {
			missing = []string{}
		}
		emit(map[string]any{"runId": *runID, "notFenced": missing})
	case "verify":
		results := make([]legacyimport.VerifyResult, 0, len(ids))
		clean := true
		for _, id := range ids {
			result, err := legacyimport.VerifyFenced(ctx, pool, *runID, id)
			if err != nil {
				fmt.Fprintf(stderr, "error: %s: %v\n", id, err)
				return exitError
			}
			clean = clean && result.Verified
			results = append(results, result)
		}
		emit(results)
		if !clean {
			return exitNotClean
		}
	case "restore-check":
		report, err := legacyimport.RestoreCheck(ctx, pool, *runID)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitError
		}
		emit(map[string]any{"report": report, "restorable": report.Restorable()})
		if !report.Restorable() {
			return exitNotClean
		}
	case "abandon":
		if err = legacyimport.Abandon(ctx, pool, *runID, *operator); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitError
		}
		emit(map[string]any{"runId": *runID, "status": "abandoned"})
	}
	return exitOK
}
