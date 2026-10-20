// Command campaign-legacy-inventory captures a read-only inventory of
// unversioned launchpad campaigns, participants and reminders before any
// baseline import. It reads LEGACY_INVENTORY_DATABASE_URL from the environment
// (a DSN can carry a password, so it is never accepted in argv), runs one READ
// ONLY repeatable-read transaction and writes an owner-only JSON report.
//
// Usage:
//
//	LEGACY_INVENTORY_DATABASE_URL=... campaign-legacy-inventory --output report.json
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
)

func main() {
	output := flag.String("output", "", "report path (created owner-only; must not exist)")
	timeout := flag.Duration("timeout", 2*time.Minute, "overall inventory deadline")
	flag.Parse()
	if err := run(*output, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(output string, timeout time.Duration) error {
	url := os.Getenv("LEGACY_INVENTORY_DATABASE_URL")
	if url == "" || output == "" {
		return errors.New("LEGACY_INVENTORY_DATABASE_URL and --output are required")
	}
	// O_EXCL keeps an earlier capture from being silently replaced; each
	// inventory is evidence tied to its own snapshot.
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	pool, err := db.Open(ctx, url)
	if err != nil {
		return errors.New("cannot connect to the legacy database")
	}
	defer pool.Close()
	report, err := legacyimport.Run(ctx, pool)
	if err != nil {
		return fmt.Errorf("inventory failed: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(report); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	t := report.Totals
	fmt.Printf("captured %s: %d campaigns (%d versioned, %d importable, %d review-required, %d blocked), %d participant rows (%d orphan), %d reminder rows (%d orphan); source sha256 %s\n",
		report.CapturedAt, t.Campaigns, t.AlreadyVersioned, t.Importable, t.ReviewRequired, t.Blocked, t.ParticipantRows, t.OrphanParticipants, t.ReminderRows, t.OrphanReminders, report.SourceSHA256)
	return nil
}
