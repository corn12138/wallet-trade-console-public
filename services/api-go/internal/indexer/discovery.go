package indexer

import (
	"context"
	"encoding/hex"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
)

// EthCaller is the minimal RPC surface pair discovery needs.
// *rpc.Client satisfies it; tests pass a fake.
type EthCaller interface {
	EthCall(ctx context.Context, to, dataHex, block string) ([]byte, error)
}

// DiscoverDexPairs asks the AMM pair factory for the pair contract of every
// unordered combination of the known tokens and returns the non-zero
// addresses. The registry only lists ONE pair statically (tokenA/tokenB), but
// the DEX has a pair per traded combination (e.g. mWBTC/mWETH backing the
// swap page's quote pair) — without discovery their Swap/Sync events would
// never reach web3_events, leaving the activity feed blind to real swaps.
//
// Best-effort: any eth_call failure just skips that combination (the watch
// set falls back to the statically-known contracts). Bounded: n tokens → at
// most n·(n-1)/2 calls, once at startup.
func DiscoverDexPairs(ctx context.Context, caller EthCaller, dexFactory string, tokens []string) []string {
	if caller == nil || dexFactory == "" || len(tokens) < 2 {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for i := range tokens {
		for j := i + 1; j < len(tokens); j++ {
			pair, err := callGetPair(ctx, caller, dexFactory, tokens[i], tokens[j])
			if err != nil || pair == "" || strings.EqualFold(pair, ZeroAddress) {
				continue
			}
			key := strings.ToLower(pair)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, pair)
		}
	}
	return out
}

func callGetPair(ctx context.Context, caller EthCaller, factory, a, b string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	argA, err := abi.EncodeAddress(a)
	if err != nil {
		return "", err
	}
	argB, err := abi.EncodeAddress(b)
	if err != nil {
		return "", err
	}
	data := abi.EncodeCall("getPair(address,address)", argA, argB)
	raw, err := caller.EthCall(callCtx, factory, data, "")
	if err != nil {
		return "", err
	}
	if len(raw) < 32 {
		return "", nil
	}
	return "0x" + hex.EncodeToString(raw[12:32]), nil
}
