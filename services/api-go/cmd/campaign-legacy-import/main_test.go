package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func noDatabase(context.Context) (*pgxpool.Pool, error) {
	return nil, errors.New("no database in unit tests")
}

// Argument errors are refused before any connection is attempted.
func TestUsageIsCheckedBeforeConnecting(t *testing.T) {
	for name, args := range map[string][]string{
		"no command":             {},
		"unknown command":        {"import"},
		"capture sans operator":  {"capture"},
		"status sans run":        {"status"},
		"fence sans campaign":    {"fence", "--run", "r", "--operator", "ops"},
		"fence sans operator":    {"fence", "--run", "r", "--campaign", "c"},
		"verify sans run":        {"verify", "--campaign", "c"},
		"abandon sans operator":  {"abandon", "--run", "r"},
		"positional extra":       {"status", "--run", "r", "extra"},
		"retention out of range": {"capture", "--operator", "ops", "--retain-days", "0"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut, noDatabase); code != exitUsage {
			t.Fatalf("%s: exit %d (%s)", name, code, errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"status", "--run", "r"}, &out, &errOut, noDatabase); code != exitError || !strings.Contains(errOut.String(), "no database") {
		t.Fatalf("connection failure: exit %d %q", code, errOut.String())
	}
}
