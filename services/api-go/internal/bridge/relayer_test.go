package bridge

import (
	"bytes"
	"context"
	"log/slog"
	"math/big"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// Both cases here came out of bringing a third chain online against real
// testnets. Neither is exotic; both are what a multi-chain relayer meets in
// normal operation.

// A fulfil bids the last observed gas price. On Arbitrum Sepolia the base fee
// rose 0.3% between the read and the broadcast and the transfer was rejected
// with "max fee per gas less than block base fee", leaving a user's deposit
// escrowed until the next cycle. The gas LIMIT was already padded; the price
// was not.
func TestPadGasPrice(t *testing.T) {
	cases := []struct {
		name string
		in   *big.Int
		pad  uint64
		want *big.Int
	}{
		{"default 25% clears the observed rise", big.NewInt(20_106_000), 25, big.NewInt(25_132_500)},
		{"zero pad bids exactly the observed price", big.NewInt(1_000), 0, big.NewInt(1_000)},
		{"rounds down, never up past the multiplier", big.NewInt(3), 25, big.NewInt(3)},
		{"handles a zero price", big.NewInt(0), 25, big.NewInt(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := padGasPrice(tc.in, tc.pad); got.Cmp(tc.want) != 0 {
				t.Errorf("padGasPrice(%s, %d) = %s, want %s", tc.in, tc.pad, got, tc.want)
			}
		})
	}

	if padGasPrice(nil, 25) != nil {
		t.Error("a nil price must stay nil rather than becoming zero")
	}

	// The whole point: the padded bid must clear the base fee that rejected us.
	observed, baseFeeAtBroadcast := big.NewInt(20_106_000), big.NewInt(20_174_000)
	if padGasPrice(observed, DefaultRelayerConfig().GasPricePadPercent).Cmp(baseFeeAtBroadcast) <= 0 {
		t.Error("the default pad does not clear the base fee move that caused the failure")
	}
}

func TestDefaultConfigPadsGasPrice(t *testing.T) {
	if DefaultRelayerConfig().GasPricePadPercent == 0 {
		t.Error("default must pad the gas price; bidding the observed price loses the race")
	}
}

// A chain in the registry with no RPC is a deployment fact, not a fault. It
// must not fail the cycle: doing so turns "one chain is unconfigured" into "the
// relayer is broken", which in a one-shot/cron deployment hides every real
// failure behind a permanent one.
func TestRunOnceToleratesAnUnconfiguredChain(t *testing.T) {
	const (
		configured   = 11155111
		unconfigured = 84532
	)
	src, _ := healthyCallers()
	chains := map[int]deployments.ChainConfig{
		configured:   {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: srcGW}}},
		unconfigured: {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: dstGW}}},
	}
	registry := NewRegistry(chains, map[int]EthCaller{configured: src})

	var logBuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// No ChainClient for either chain: the configured one fails on its own
	// terms (a transport error), the unconfigured one on ErrNoRPC. Only the
	// former may set the cycle error.
	r := NewRelayer(registry, NewStore(nil), nil, map[int]ChainClient{}, DefaultRelayerConfig(), log)
	err := r.RunOnce(context.Background())

	logs := logBuf.String()
	if !strings.Contains(logs, "no RPC configured") {
		t.Errorf("an unscanned chain must still be reported every cycle: %s", logs)
	}
	if !strings.Contains(logs, "BRIDGE_RPC_URL_") {
		t.Errorf("the warning must name the env var that fixes it: %s", logs)
	}
	// Both chains lack a client here, so the cycle is still an error — but it
	// must not be the ErrNoRPC one.
	if err != nil && strings.Contains(err.Error(), "84532") {
		t.Errorf("an unconfigured chain must not be the cycle error: %v", err)
	}
}

func TestScanChainReportsMissingRPCAsErrNoRPC(t *testing.T) {
	chains := map[int]deployments.ChainConfig{
		84532: {Contracts: map[string]deployments.ContractInfo{"bridgeGateway": {Address: dstGW}}},
	}
	r := NewRelayer(NewRegistry(chains, nil), NewStore(nil), nil, map[int]ChainClient{},
		DefaultRelayerConfig(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	err := r.ScanChain(context.Background(), 84532)
	if err == nil {
		t.Fatal("want an error for a chain with no client")
	}
	// Typed, so RunOnce can tell a config gap from a transport failure.
	if !strings.Contains(err.Error(), ErrNoRPC.Error()) {
		t.Errorf("err = %v, want it to wrap ErrNoRPC", err)
	}
}

// fakeChainClient answers only fulfilled(); everything else is unused by these
// tests and fails loudly if reached.
type fakeChainClient struct {
	fulfilled bool
	err       error
	gotData   string
	balance   *big.Int
}

func (f *fakeChainClient) EthCall(_ context.Context, _, data, _ string) ([]byte, error) {
	f.gotData = data
	if f.err != nil {
		return nil, f.err
	}
	out := make([]byte, 32)
	if f.fulfilled {
		out[31] = 1
	}
	return out, nil
}
func (f *fakeChainClient) BlockNumber(context.Context) (*big.Int, error) { return big.NewInt(1), nil }
func (f *fakeChainClient) GetLogs(context.Context, rpc.LogFilter) ([]rpc.Log, error) {
	return nil, nil
}
func (f *fakeChainClient) GetTransactionCount(context.Context, string, string) (*big.Int, error) {
	return big.NewInt(0), nil
}
func (f *fakeChainClient) GetGasPrice(context.Context) (*big.Int, error) { return big.NewInt(1), nil }
func (f *fakeChainClient) EstimateGas(context.Context, string, string, string, string) (*big.Int, error) {
	return big.NewInt(21000), nil
}
func (f *fakeChainClient) SendRawTransaction(context.Context, string) (string, error) {
	return "0x", nil
}
func (f *fakeChainClient) ChainID(context.Context) (*big.Int, error) { return big.NewInt(1), nil }
func (f *fakeChainClient) GetBalance(context.Context, string, string) (*big.Int, error) {
	return f.balance, nil
}

// A BridgeFulfilled event can be missed — a restart past it, a scan gap — and
// the projection then reads INITIATED forever while the recipient already holds
// the funds. That is the worst lie this product can tell: a user's money shown
// in limbo after it arrived. Observed live on Arbitrum Sepolia, where ~0.25s
// blocks put the event thousands of blocks behind a fixed-count lookback.
func TestAlreadyFulfilledReadsTheDestinationGateway(t *testing.T) {
	r := NewRelayer(NewRegistry(nil, nil), NewStore(nil), nil, nil,
		DefaultRelayerConfig(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	tid := "0x" + strings.Repeat("ab", 32)

	for name, tc := range map[string]struct {
		client *fakeChainClient
		want   bool
		wantOK bool
	}{
		"delivered":     {&fakeChainClient{fulfilled: true}, true, true},
		"not delivered": {&fakeChainClient{fulfilled: false}, false, true},
		"read failed":   {&fakeChainClient{err: context.DeadlineExceeded}, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := r.alreadyFulfilled(context.Background(), tc.client, dstGW, tid)
			if (err == nil) != tc.wantOK {
				t.Fatalf("err = %v, wantOK %v", err, tc.wantOK)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			if tc.wantOK && !strings.HasPrefix(tc.client.gotData, selFulfilled) {
				t.Errorf("wrong selector: %s", tc.client.gotData[:10])
			}
		})
	}

	// A malformed id must not be padded into a lookup of some other transfer.
	if _, err := r.alreadyFulfilled(context.Background(), &fakeChainClient{}, dstGW, "0xdead"); err == nil {
		t.Error("a short transferId must be rejected, not silently padded")
	}
}

// A chain absent from the registry is not the relayer's business at all.
func TestScanChainIgnoresUnknownChain(t *testing.T) {
	r := NewRelayer(NewRegistry(nil, nil), NewStore(nil), nil, map[int]ChainClient{},
		DefaultRelayerConfig(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err := r.ScanChain(context.Background(), 999); err != nil {
		t.Errorf("unknown chain should be a no-op, got %v", err)
	}
}

// A deposit's destination token is not in the BridgeInitiated event, so the
// projection stored nothing and /api/bridge/status served a blank `dstToken`
// for the entire in-flight window — precisely when a user is watching the
// transfer. It is resolved from the source gateway's route table instead, the
// same table `deliver` consults to decide what to pay out.
//
// The cases that must NOT produce a value matter as much as the happy one: a
// guessed destination token on a closed route or an unreadable RPC would be
// this projection asserting a chain fact it never read.
func TestResolveDstTokenReadsTheRouteAndNeverGuesses(t *testing.T) {
	newRelayer := func(src *fakeCaller) *Relayer {
		dst := &fakeCaller{liquidity: word(big.NewInt(1))}
		return NewRelayer(twoChainRegistry(src, dst), NewStore(nil), nil, nil,
			DefaultRelayerConfig(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	}

	for name, tc := range map[string]struct {
		src  *fakeCaller
		want string
	}{
		"supported route": {
			&fakeCaller{route: routeResponse(true, dstToken, big.NewInt(1)), paused: boolWord(false)},
			dstToken,
		},
		"route closed since deposit": {
			&fakeCaller{route: routeResponse(false, dstToken, big.NewInt(1)), paused: boolWord(false)},
			"",
		},
		"rpc unreadable": {
			&fakeCaller{err: context.DeadlineExceeded},
			"",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := newRelayer(tc.src).resolveDstToken(context.Background(), sepolia, srcToken, baseSep)
			if got != tc.want {
				t.Errorf("dstToken = %q, want %q", got, tc.want)
			}
		})
	}

	// A chain with no gateway at all is unresolvable, not an empty-address claim.
	r := NewRelayer(NewRegistry(nil, nil), NewStore(nil), nil, nil,
		DefaultRelayerConfig(), slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if got := r.resolveDstToken(context.Background(), sepolia, srcToken, baseSep); got != "" {
		t.Errorf("unknown chain returned %q, want empty", got)
	}
}

// The delivery log is authoritative over the route table: an operator can
// re-point a route while a transfer is in flight, and what the recipient
// actually received is what BridgeFulfilled paid out. Pins the word offsets —
// dstToken is the FIRST data word, ahead of amount and srcChainId, and reading
// the wrong one would record the amount as an address.
func TestDecodeFulfilledReadsTheDeliveredToken(t *testing.T) {
	data := append([]byte{}, addrWord(dstToken)...)     // dstToken
	data = append(data, word(big.NewInt(5_000_000))...) // amount
	data = append(data, word(big.NewInt(sepolia))...)   // srcChainId

	gotToken, gotChain, err := decodeFulfilled(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotToken != dstToken {
		t.Errorf("delivered token = %q, want %q", gotToken, dstToken)
	}
	// Returned raw: narrowing belongs to bridgeChainID, which rejects a value
	// that would wrap into some other chain's identifier.
	if gotChain == nil || gotChain.Cmp(big.NewInt(sepolia)) != 0 {
		t.Errorf("srcChainId = %v, want %d", gotChain, sepolia)
	}

	// A truncated log is rejected rather than decoded into zero values, which
	// would blank a known destination token on the way through MarkFulfilled.
	if _, _, err := decodeFulfilled(data[:63]); err == nil {
		t.Error("short BridgeFulfilled data must be rejected")
	}

	// An out-of-range chain id must not survive the handler path: the decoder
	// hands it over intact and the bound is what refuses it.
	huge := append([]byte{}, addrWord(dstToken)...)
	huge = append(huge, word(big.NewInt(1))...)
	huge = append(huge, new(big.Int).Lsh(big.NewInt(1), 200).FillBytes(make([]byte, 32))...)
	_, oversized, err := decodeFulfilled(huge)
	if err != nil {
		t.Fatalf("decode oversized: %v", err)
	}
	if _, err := bridgeChainID("source chain ID", oversized); err == nil {
		t.Error("a chain id beyond the database integer range must be rejected")
	}
}
