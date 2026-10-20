// Command web3-sessions is the operator's control over persistent web3 (SIWE)
// sessions (FQ-01). It runs on the deployment control plane against the same
// database as the API, resolved from DATABASE_URL or the compound DATABASE_*
// variables (a DSN can carry a password, so it is never accepted in argv).
//
// Usage:
//
//	web3-sessions revoke-wallet --wallet 0x.. --operator NAME
//	    end every live session of one wallet; its tokens stop working on the
//	    next request on every API instance
//	web3-sessions status
//	    highest stored auth epoch and live session count (no identities)
//
// Revoking sessions does not remove an administrator role: take the wallet out
// of CAMPAIGN_ADMIN_WALLETS / I18N_ADMIN_WALLETS separately. After a database
// restore, raise WEB3_AUTH_EPOCH instead: rows restored from a backup cannot
// carry revocations made after the backup was taken.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/runtimeenv"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
)

const usage = `usage: web3-sessions <command>

  revoke-wallet --wallet 0x.. --operator NAME   end every live session of one wallet
  status                                        highest stored auth epoch and live session count
`

var (
	walletPattern   = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	operatorPattern = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,64}$`)
)

// revoker is the part of web3session.Store the commands use.
type revoker interface {
	RevokeWallet(ctx context.Context, wallet, by string) (int64, error)
	Summary(ctx context.Context) (web3session.Summary, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, openStore))
}

func openStore(ctx context.Context) (revoker, func(), error) {
	url, err := runtimeenv.Resolve()
	if err != nil || url == "" {
		return nil, nil, errors.New("DATABASE_URL (or the compound DATABASE_* variables) is required")
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		return nil, nil, errors.New("cannot connect to the database")
	}
	return web3session.New(pool), pool.Close, nil
}

func run(args []string, stdout, stderr io.Writer, open func(context.Context) (revoker, func(), error)) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var wallet, operator string
	switch args[0] {
	case "revoke-wallet":
		flags := flag.NewFlagSet("revoke-wallet", flag.ContinueOnError)
		flags.SetOutput(stderr)
		flags.StringVar(&wallet, "wallet", "", "wallet whose sessions end")
		flags.StringVar(&operator, "operator", "", "who revokes, recorded with each session")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		wallet = strings.ToLower(strings.TrimSpace(wallet))
		if !walletPattern.MatchString(wallet) || wallet == "0x0000000000000000000000000000000000000000" {
			fmt.Fprintln(stderr, "error: --wallet must be a non-zero 0x address")
			return 2
		}
		if !operatorPattern.MatchString(operator) {
			fmt.Fprintln(stderr, "error: --operator must be 1-64 characters of letters, digits and ._@:-")
			return 2
		}
	case "status":
		if len(args) != 1 {
			fmt.Fprint(stderr, usage)
			return 2
		}
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, closeStore, err := open(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	defer closeStore()
	var result any
	if args[0] == "revoke-wallet" {
		revoked, err := store.RevokeWallet(ctx, wallet, operator)
		if err != nil {
			// A dropped connection leaves the outcome unknown; revoking again is
			// safe because only live sessions are touched.
			fmt.Fprintln(stderr, "error: revocation not confirmed; run revoke-wallet again")
			return 1
		}
		result = map[string]any{"wallet": wallet, "revokedSessions": revoked, "operator": operator}
	} else {
		summary, err := store.Summary(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "error: cannot read the session store")
			return 1
		}
		result = summary
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return 1
	}
	return 0
}
