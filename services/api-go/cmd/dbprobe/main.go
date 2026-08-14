// Command dbprobe resolves the DATABASE_* environment exactly as cmd/api does
// (runtimeenv.Resolve + the same pgx connect/ping, incl. stripPrismaParams) and
// classifies the result for the strict remote-smoke preflight. It is the
// dependency-free replacement for the psql auth check in
// scripts/strict/verify-remote-smoke.sh, so the preflight is never skipped when
// psql is absent. It prints NO secret values.
//
// Exit codes (consumed by the smoke script):
//
//	0  auth OK (connected + ping succeeded)
//	2  env invalid / no database configured
//	3  AUTH rejected (28P01 / password / authentication)
//	4  CONNECTIVITY failure (refused / timeout / dial / reset)
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/runtimeenv"
)

func main() {
	url, err := runtimeenv.Resolve()
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbprobe: database env invalid:", err)
		os.Exit(2)
	}
	if url == "" {
		fmt.Fprintln(os.Stderr, "dbprobe: no database configured (set DATABASE_URL or DATABASE_HOST/PORT/NAME/USER/PASSWORD)")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, url)
	if err != nil {
		// Classify without echoing the raw error (host/user are not secret, but
		// keep output minimal). pgx never includes the password in its errors.
		msg := strings.ToLower(err.Error())
		switch {
		case strings.Contains(msg, "28p01") || strings.Contains(msg, "password") || strings.Contains(msg, "authentication"):
			fmt.Fprintln(os.Stderr, "dbprobe: AUTH rejected (credential wrong/rotated for the tunnel target)")
			os.Exit(3)
		case strings.Contains(msg, "refused") || strings.Contains(msg, "timeout") ||
			strings.Contains(msg, "dial") || strings.Contains(msg, "no route") ||
			strings.Contains(msg, "reset") || strings.Contains(msg, "closed") ||
			strings.Contains(msg, "i/o"):
			fmt.Fprintln(os.Stderr, "dbprobe: CONNECTIVITY failure (tunnel down / host unreachable)")
			os.Exit(4)
		default:
			fmt.Fprintln(os.Stderr, "dbprobe: DB preflight failed (treating as auth/preflight)")
			os.Exit(3)
		}
	}
	pool.Close()
	fmt.Println("dbprobe: remote DB auth OK")
}
