package db

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStripPrismaParams(t *testing.T) {
	// runtimeenv.Resolve emits ?schema=public for the compound-env path; pgx
	// parks unknown query params in RuntimeParams to forward as startup params.
	config, err := pgxpool.ParseConfig("postgresql://u:p@localhost:5432/db?schema=public&search_path=custom")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := config.ConnConfig.RuntimeParams["schema"]; !ok {
		t.Skip("this pgx version did not park `schema` in RuntimeParams; fix is a no-op")
	}

	stripPrismaParams(config)

	if _, ok := config.ConnConfig.RuntimeParams["schema"]; ok {
		t.Errorf("schema startup param should be stripped")
	}
	// A real GUC like search_path must be preserved.
	if got := config.ConnConfig.RuntimeParams["search_path"]; got != "custom" {
		t.Errorf("search_path should be preserved, got %q", got)
	}
}

func TestStripPrismaParamsNilSafe(t *testing.T) {
	stripPrismaParams(nil)               // must not panic
	stripPrismaParams(&pgxpool.Config{}) // ConnConfig nil
}
