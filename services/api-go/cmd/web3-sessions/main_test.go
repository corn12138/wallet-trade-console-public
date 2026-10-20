package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
)

type fakeStore struct {
	wallet, by string
	revoked    int64
	fail       bool
}

func (f *fakeStore) RevokeWallet(_ context.Context, wallet, by string) (int64, error) {
	if f.fail {
		return 0, errors.New("connection reset by peer at 192.0.2.1:5432")
	}
	f.wallet, f.by = wallet, by
	return f.revoked, nil
}

func (f *fakeStore) Summary(context.Context) (web3session.Summary, error) {
	return web3session.Summary{MaxEpoch: 4, LiveSessions: 2}, nil
}

func runWith(store *fakeStore, args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut, func(context.Context) (revoker, func(), error) { return store, func() {}, nil })
	return code, out.String(), errOut.String()
}

func TestRevokeWalletValidatesAndRecordsTheOperator(t *testing.T) {
	store := &fakeStore{revoked: 3}
	code, out, _ := runWith(store, "revoke-wallet", "--wallet", "0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "--operator", "ops:alice")
	if code != 0 || store.wallet != "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || store.by != "ops:alice" || !strings.Contains(out, `"revokedSessions": 3`) {
		t.Fatalf("exit %d store %+v out %s", code, store, out)
	}
	for name, args := range map[string][]string{
		"no command":       {},
		"unknown command":  {"revoke-all"},
		"missing wallet":   {"revoke-wallet", "--operator", "ops"},
		"zero wallet":      {"revoke-wallet", "--wallet", "0x0000000000000000000000000000000000000000", "--operator", "ops"},
		"short wallet":     {"revoke-wallet", "--wallet", "0xabc", "--operator", "ops"},
		"missing operator": {"revoke-wallet", "--wallet", "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"operator spaces":  {"revoke-wallet", "--wallet", "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--operator", "a b"},
		"extra argument":   {"status", "now"},
	} {
		store := &fakeStore{}
		if code, _, _ := runWith(store, args...); code != 2 || store.wallet != "" {
			t.Fatalf("%s: exit %d store %+v", name, code, store)
		}
	}
}

func TestFailuresDoNotEchoConnectionDetails(t *testing.T) {
	code, _, errOut := runWith(&fakeStore{fail: true}, "revoke-wallet", "--wallet", "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--operator", "ops")
	if code != 1 || strings.Contains(errOut, "192.0.2.1") || !strings.Contains(errOut, "not confirmed") {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	code, out, _ := runWith(&fakeStore{}, "status")
	if code != 0 || !strings.Contains(out, `"maxEpoch": 4`) || !strings.Contains(out, `"liveSessions": 2`) {
		t.Fatalf("status: exit %d out %s", code, out)
	}
}
