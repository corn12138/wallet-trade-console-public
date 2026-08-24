// Command i18n-bootstrap idempotently seeds the server-owned translation
// system on a migrated database:
//
//  1. creates the baseline locale metadata rows (en = enabled default,
//     zh = enabled) when absent;
//  2. seeds message drafts from the embedded Go baseline — INSERT … ON
//     CONFLICT DO NOTHING, so administrator edits are NEVER overwritten;
//     -resync-baseline additionally refreshes drafts that NO administrator has
//     touched (updated_by is still the bootstrap actor) when the baseline text
//     has since changed. Without it, DO NOTHING means a corrected string in the
//     repository can never reach a database that already has the old one: only
//     brand-new keys propagate, edits never do. That is how /bridge came to
//     tell users "此页面无法提交任何转账" months after the bridge started
//     delivering transfers — the product contradicting itself, in the
//     direction of claiming less than it can do;
//  3. publishes the initial catalog revision for any locale that has no
//     revision yet (validated against the default locale first).
//
// Running it twice is a no-op: no duplicate locales, drafts, revisions or
// audit noise. It is an explicit operator command — normal API startup never
// mutates catalog state.
//
// -republish additionally cuts a NEW revision for locales that already have
// one. This is what makes newly added baseline keys reachable: the served
// catalog is the published revision, NOT a merge of revision-plus-baseline
// (see i18n.Service.Catalog), so a release that adds keys leaves them
// rendering as raw key paths until someone publishes. Step 2 only seeds
// drafts. The flag is opt-in rather than the default because a republish
// snapshots whatever the drafts currently say — including an administrator's
// in-progress edits — and that should be a decision, not a side effect of
// running a seeding command.
//
// Usage:
//
//	DATABASE_URL='postgresql://…' go run ./cmd/i18n-bootstrap
//	DATABASE_URL='postgresql://…' go run ./cmd/i18n-bootstrap -republish
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/i18n"
)

const bootstrapActor = "0x0000000000000000000000000000000000000000"

func main() {
	republish := flag.Bool("republish", false,
		"also cut a new revision for locales that already have one (picks up newly added baseline keys)")
	resync := flag.Bool("resync-baseline", false,
		"refresh drafts no administrator has edited when the baseline text has changed")
	flag.Parse()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("i18n-bootstrap: DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("i18n-bootstrap: connect: %v", err)
	}
	defer pool.Close()

	store := i18n.NewPgStore(pool)

	localesCreated, draftsSeeded, revisionsPublished := 0, 0, 0
	draftsResynced, draftsHeldByAdmin := 0, 0

	for _, b := range i18n.BaselineLocales() {
		// 1) locale metadata (direct insert keeps is_default from the baseline;
		//    conflicts mean an operator/admin already owns the row).
		tag, err := pool.Exec(ctx, `
			INSERT INTO i18n_locales (code, english_name, native_name, enabled, is_default, created_by, updated_by, created_at, updated_at)
			VALUES ($1,$2,$3,true,$4,$5,$5,NOW(),NOW())
			ON CONFLICT (code) DO NOTHING`,
			b.Code, b.EnglishName, b.NativeName, b.IsDefault, bootstrapActor)
		if err != nil {
			log.Fatalf("i18n-bootstrap: locale %s: %v", b.Code, err)
		}
		if tag.RowsAffected() > 0 {
			localesCreated++
		}

		// 2) seed drafts from the embedded baseline without overwriting edits.
		nested, err := i18n.BaselineCatalog(b.Code)
		if err != nil {
			log.Fatalf("i18n-bootstrap: baseline %s: %v", b.Code, err)
		}
		flat, err := i18n.Flatten(nested)
		if err != nil {
			log.Fatalf("i18n-bootstrap: flatten %s: %v", b.Code, err)
		}
		batch := &pgx.Batch{}
		for _, m := range flat {
			batch.Queue(`
				INSERT INTO i18n_message_drafts (id, locale_code, namespace, message_key, value, version, updated_by, created_at, updated_at)
				VALUES (gen_random_uuid()::text, $1, $2, $3, $4, 1, $5, NOW(), NOW())
				ON CONFLICT (locale_code, namespace, message_key) DO NOTHING`,
				b.Code, m.Namespace, m.Key, m.Value, bootstrapActor)
		}
		res := pool.SendBatch(ctx, batch)
		for range flat {
			tag, err := res.Exec()
			if err != nil {
				_ = res.Close()
				log.Fatalf("i18n-bootstrap: seed drafts %s: %v", b.Code, err)
			}
			if tag.RowsAffected() > 0 {
				draftsSeeded++
			}
		}
		if err := res.Close(); err != nil {
			log.Fatalf("i18n-bootstrap: seed drafts %s: %v", b.Code, err)
		}

		// 2b) refresh stale drafts the baseline has since corrected. Scoped to
		// rows still owned by the bootstrap actor: a row with any other
		// updated_by is a human's edit and outranks the repository. Skipped
		// rows are counted and reported rather than passed over silently —
		// invisible drift is what made this necessary in the first place.
		if *resync {
			for _, m := range flat {
				tag, err := pool.Exec(ctx, `
					UPDATE i18n_message_drafts
					   SET value = $4, version = version + 1, updated_at = NOW()
					 WHERE locale_code = $1 AND namespace = $2 AND message_key = $3
					   AND updated_by = $5 AND value IS DISTINCT FROM $4`,
					b.Code, m.Namespace, m.Key, m.Value, bootstrapActor)
				if err != nil {
					log.Fatalf("i18n-bootstrap: resync %s %s.%s: %v", b.Code, m.Namespace, m.Key, err)
				}
				if tag.RowsAffected() > 0 {
					draftsResynced++
					fmt.Printf("resynced %s %s.%s\n", b.Code, m.Namespace, m.Key)
					continue
				}
				var owner, current string
				if err := pool.QueryRow(ctx, `
					SELECT updated_by, value FROM i18n_message_drafts
					 WHERE locale_code = $1 AND namespace = $2 AND message_key = $3`,
					b.Code, m.Namespace, m.Key).Scan(&owner, &current); err == nil &&
					owner != bootstrapActor && current != m.Value {
					draftsHeldByAdmin++
					fmt.Printf("kept admin edit %s %s.%s (baseline differs)\n", b.Code, m.Namespace, m.Key)
				}
			}
		}
	}

	// 3) initial published revision per locale lacking one — validated, from
	//    the CURRENT drafts (which may already contain admin edits).
	svc := i18n.NewService(store)
	for _, b := range i18n.BaselineLocales() {
		existing, err := store.LatestRevision(ctx, b.Code)
		if err != nil {
			log.Fatalf("i18n-bootstrap: latest revision %s: %v", b.Code, err)
		}
		if existing != nil && !*republish {
			continue // idempotent: never create a duplicate initial revision
		}
		issues, err := svc.Validate(ctx, b.Code)
		if err != nil {
			log.Fatalf("i18n-bootstrap: validate %s: %v", b.Code, err)
		}
		if len(issues) > 0 {
			for _, is := range issues[:min(10, len(issues))] {
				log.Printf("  issue %s %s.%s: %s", is.Code, is.Namespace, is.Key, is.Detail)
			}
			log.Fatalf("i18n-bootstrap: %s baseline failed validation with %d issue(s)", b.Code, len(issues))
		}
		drafts, err := store.ListDrafts(ctx, b.Code)
		if err != nil {
			log.Fatalf("i18n-bootstrap: drafts %s: %v", b.Code, err)
		}
		nested, err := i18n.Nest(drafts)
		if err != nil {
			log.Fatalf("i18n-bootstrap: nest %s: %v", b.Code, err)
		}
		body, err := i18n.CanonicalJSON(nested)
		if err != nil {
			log.Fatalf("i18n-bootstrap: marshal %s: %v", b.Code, err)
		}
		action := "bootstrap.publish"
		if existing != nil {
			action = "bootstrap.republish"
		}
		rev, err := store.CreateRevision(ctx, b.Code, body, i18n.Checksum(body), bootstrapActor, nil, nil, i18n.AuditEntry{
			Actor: bootstrapActor, Action: action, LocaleCode: b.Code,
			Metadata: map[string]any{"keyCount": len(drafts), "tool": "i18n-bootstrap"},
		})
		if err != nil {
			log.Fatalf("i18n-bootstrap: publish %s: %v", b.Code, err)
		}
		revisionsPublished++
		fmt.Printf("published %s v%d checksum=%s keys=%d\n", b.Code, rev.Version, rev.Checksum[:12], len(drafts))
	}

	fmt.Printf("i18n-bootstrap: locales created=%d drafts seeded=%d resynced=%d admin-held=%d revisions published=%d (re-runs are no-ops)\n",
		localesCreated, draftsSeeded, draftsResynced, draftsHeldByAdmin, revisionsPublished)
}
