package pricefeed

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// stubCaller returns canned eth_call responses keyed by selector.
type stubCaller struct {
	responses map[string][]byte
	err       error
	calls     int
}

func (s *stubCaller) EthCall(_ context.Context, _, dataHex, _ string) ([]byte, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if out, ok := s.responses[dataHex]; ok {
		return out, nil
	}
	return nil, errors.New("stub: unexpected selector " + dataHex)
}

// mustHex decodes a 0x-prefixed hex blob.
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	return b
}

// liveETHRound is the verbatim latestRoundData() response captured from the
// Sepolia ETH/USD aggregator, and decimals() = 8. Using the real bytes means
// this test would catch a word-offset mistake that a synthetic fixture built
// with the same (wrong) assumption would not.
const (
	liveETHRound = "0x" +
		"00000000000000000000000000000000000000000000000100000000000088d4" + // roundId
		"0000000000000000000000000000000000000000000000000000002c1a220c40" + // answer
		"000000000000000000000000000000000000000000000000000000006a748afc" + // startedAt
		"000000000000000000000000000000000000000000000000000000006a748afc" + // updatedAt
		"00000000000000000000000000000000000000000000000100000000000088d4" // answeredInRound
	liveDecimals8 = "0x0000000000000000000000000000000000000000000000000000000000000008"
)

func newLiveReader(t *testing.T) (*ChainlinkReader, *stubCaller) {
	t.Helper()
	stub := &stubCaller{responses: map[string][]byte{
		selLatestRoundData: mustHex(t, liveETHRound),
		selDecimals:        mustHex(t, liveDecimals8),
	}}
	return NewChainlinkReader(stub), stub
}

func TestChainlinkLatestRoundDecodesLiveResponse(t *testing.T) {
	reader, _ := newLiveReader(t)
	feed := FeedRef{Symbol: "ETH-USD", ChainID: 11155111, Address: "0x694AA1769357215DE4FAC081bf1f309aDC325306"}

	obs, err := reader.LatestRound(context.Background(), feed)
	if err != nil {
		t.Fatalf("LatestRound: %v", err)
	}

	// 0x2c1a220c40 = 189417000000, /1e8 = 1894.17 USD.
	if obs.Price != "1894.17" {
		t.Errorf("price: want 1894.17, got %s", obs.Price)
	}
	if obs.Decimals != 8 {
		t.Errorf("decimals: want 8, got %d", obs.Decimals)
	}
	// 0x6a748afc = 1786022652.
	if want := time.Unix(1786022652, 0).UTC(); !obs.ObservedAt.Equal(want) {
		t.Errorf("observedAt: want %s, got %s", want, obs.ObservedAt)
	}
	// roundId 0x100000000000088d4 packs phase 1 in the high 64 bits:
	// 2^64 + 0x88d4 = 18446744073709551616 + 35028.
	if obs.RoundID != "18446744073709586644" {
		t.Errorf("roundId: got %s", obs.RoundID)
	}
	// The address is lower-cased so the unique index cannot be defeated by
	// checksum casing.
	if obs.FeedAddress != strings.ToLower(feed.Address) {
		t.Errorf("feedAddress not normalized: %s", obs.FeedAddress)
	}
}

func TestChainlinkDecimalsCachedPerAddress(t *testing.T) {
	reader, stub := newLiveReader(t)
	feed := FeedRef{Symbol: "ETH-USD", ChainID: 11155111, Address: "0x694AA1769357215DE4FAC081bf1f309aDC325306"}

	for i := range 3 {
		if _, err := reader.LatestRound(context.Background(), feed); err != nil {
			t.Fatalf("LatestRound #%d: %v", i, err)
		}
	}
	// 3 latestRoundData calls + exactly 1 decimals call.
	if stub.calls != 4 {
		t.Errorf("want 4 eth_calls (3 rounds + 1 cached decimals), got %d", stub.calls)
	}
}

func TestChainlinkRejectsNonPositiveAnswer(t *testing.T) {
	zeroAnswer := "0x" +
		strings.Repeat("0", 64) + // roundId
		strings.Repeat("0", 64) + // answer = 0
		strings.Repeat("0", 64) +
		"000000000000000000000000000000000000000000000000000000006a748afc" +
		strings.Repeat("0", 64)

	stub := &stubCaller{responses: map[string][]byte{
		selLatestRoundData: mustHex(t, zeroAnswer),
		selDecimals:        mustHex(t, liveDecimals8),
	}}
	reader := NewChainlinkReader(stub)

	_, err := reader.LatestRound(context.Background(), FeedRef{Symbol: "ETH-USD", Address: "0xfeed"})
	if err == nil {
		t.Fatal("a zero answer must be an error — storing it would poison every candle built on it")
	}
}

func TestChainlinkRejectsZeroTimestamp(t *testing.T) {
	noTimestamp := "0x" +
		strings.Repeat("0", 64) +
		"0000000000000000000000000000000000000000000000000000002c1a220c40" +
		strings.Repeat("0", 64) +
		strings.Repeat("0", 64) + // updatedAt = 0
		strings.Repeat("0", 64)

	stub := &stubCaller{responses: map[string][]byte{
		selLatestRoundData: mustHex(t, noTimestamp),
		selDecimals:        mustHex(t, liveDecimals8),
	}}
	reader := NewChainlinkReader(stub)

	if _, err := reader.LatestRound(context.Background(), FeedRef{Symbol: "ETH-USD", Address: "0xfeed"}); err == nil {
		t.Fatal("a zero updatedAt means the feed has no valid round and must error")
	}
}

func TestChainlinkShortResponseIsAnError(t *testing.T) {
	stub := &stubCaller{responses: map[string][]byte{
		selLatestRoundData: make([]byte, 64), // only 2 of 5 words
		selDecimals:        mustHex(t, liveDecimals8),
	}}
	reader := NewChainlinkReader(stub)

	if _, err := reader.LatestRound(context.Background(), FeedRef{Symbol: "ETH-USD", Address: "0xfeed"}); err == nil {
		t.Fatal("a truncated response must error rather than decode garbage")
	}
}

func TestDecodeInt256HandlesNegative(t *testing.T) {
	// -1 in two's complement is 2^256 - 1.
	raw := new(big.Int).Sub(two256, big.NewInt(1))
	if got := decodeInt256(raw); got.Cmp(big.NewInt(-1)) != 0 {
		t.Errorf("want -1, got %s", got)
	}
	if got := decodeInt256(big.NewInt(42)); got.Cmp(big.NewInt(42)) != 0 {
		t.Errorf("want 42, got %s", got)
	}
}

func TestScaleDecimalIsExact(t *testing.T) {
	cases := []struct {
		in       string
		decimals int
		want     string
	}{
		{"189417000000", 8, "1894.17"},
		{"6434777302461", 8, "64347.77302461"},
		{"100000000", 8, "1"},
		{"1", 8, "0.00000001"},
		{"0", 8, "0"},
		{"-189417000000", 8, "-1894.17"},
		{"12345", 0, "12345"},
	}
	for _, tc := range cases {
		v, ok := new(big.Int).SetString(tc.in, 10)
		if !ok {
			t.Fatalf("bad fixture %q", tc.in)
		}
		if got := scaleDecimal(v, tc.decimals); got != tc.want {
			t.Errorf("scaleDecimal(%s, %d): want %s, got %s", tc.in, tc.decimals, tc.want, got)
		}
	}
}

func TestSepoliaFeedsAreWellFormed(t *testing.T) {
	feeds := SepoliaFeeds()
	if len(feeds) == 0 {
		t.Fatal("no Sepolia feeds configured")
	}
	seen := map[string]bool{}
	for _, f := range feeds {
		if f.ChainID != 11155111 {
			t.Errorf("%s: want Sepolia chain id, got %d", f.Symbol, f.ChainID)
		}
		if len(f.Address) != 42 || !strings.HasPrefix(f.Address, "0x") {
			t.Errorf("%s: malformed address %q", f.Symbol, f.Address)
		}
		if seen[strings.ToLower(f.Address)] {
			t.Errorf("duplicate feed address %s", f.Address)
		}
		seen[strings.ToLower(f.Address)] = true
	}
}
