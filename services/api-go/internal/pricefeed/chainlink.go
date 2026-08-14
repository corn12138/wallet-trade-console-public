package pricefeed

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// Chainlink AggregatorV3Interface selectors.
//
//	latestRoundData() → (uint80 roundId, int256 answer, uint256 startedAt,
//	                     uint256 updatedAt, uint80 answeredInRound)
//	decimals()        → uint8
const (
	selLatestRoundData = "0xfeaf968c"
	selDecimals        = "0x313ce567"
)

// FeedRef points at one aggregator. Addresses are public on-chain constants —
// nothing here is a secret.
type FeedRef struct {
	Symbol  string // canonical product symbol, e.g. "ETH-USD"
	ChainID int
	Address string
}

// SepoliaFeeds are Chainlink's published Sepolia aggregators for the markets
// this product lists. They are read over the same RPC the indexer already
// uses, which is why the oracle path works from the production VM even where
// off-chain market APIs are blocked.
func SepoliaFeeds() []FeedRef {
	return []FeedRef{
		{Symbol: "ETH-USD", ChainID: 11155111, Address: "0x694AA1769357215DE4FAC081bf1f309aDC325306"},
		{Symbol: "BTC-USD", ChainID: 11155111, Address: "0x1b44F3514812d835EB1BDB0acB33d3fA3351Ee43"},
	}
}

// EthCaller is the slice of rpc.Client the oracle reader needs, so tests can
// drive decode paths without a node.
type EthCaller interface {
	EthCall(ctx context.Context, to, dataHex, block string) ([]byte, error)
}

var _ EthCaller = (*rpc.Client)(nil)

// ChainlinkReader reads aggregator rounds and caches each feed's decimals
// (an immutable property of the contract).
type ChainlinkReader struct {
	client   EthCaller
	decimals map[string]int
}

// NewChainlinkReader builds a reader over any EthCaller.
func NewChainlinkReader(client EthCaller) *ChainlinkReader {
	return &ChainlinkReader{client: client, decimals: map[string]int{}}
}

// Decimals returns the aggregator's decimals, reading once per address.
func (r *ChainlinkReader) Decimals(ctx context.Context, address string) (int, error) {
	key := strings.ToLower(address)
	if d, ok := r.decimals[key]; ok {
		return d, nil
	}
	raw, err := r.client.EthCall(ctx, address, selDecimals, "latest")
	if err != nil {
		return 0, fmt.Errorf("pricefeed/chainlink: decimals(%s): %w", address, err)
	}
	n, err := rpc.DecodeUint256At(raw, 0)
	if err != nil {
		return 0, fmt.Errorf("pricefeed/chainlink: decode decimals(%s): %w", address, err)
	}
	if !n.IsInt64() || n.Int64() < 0 || n.Int64() > 36 {
		return 0, fmt.Errorf("pricefeed/chainlink: implausible decimals %s for %s", n, address)
	}
	d := int(n.Int64())
	r.decimals[key] = d
	return d, nil
}

// LatestRound reads one aggregator round and normalizes the answer into whole
// quote units (e.g. USD). A non-positive answer or a zero updatedAt means the
// feed has no valid round yet — that is reported as an error rather than
// stored, because a fabricated 0 price would corrupt every candle built on it.
func (r *ChainlinkReader) LatestRound(ctx context.Context, feed FeedRef) (Observation, error) {
	raw, err := r.client.EthCall(ctx, feed.Address, selLatestRoundData, "latest")
	if err != nil {
		return Observation{}, fmt.Errorf("pricefeed/chainlink: latestRoundData(%s): %w", feed.Address, err)
	}
	if len(raw) < 160 {
		return Observation{}, fmt.Errorf("pricefeed/chainlink: short latestRoundData response (%d bytes)", len(raw))
	}

	roundID, err := rpc.DecodeUint256At(raw, 0)
	if err != nil {
		return Observation{}, err
	}
	answerRaw, err := rpc.DecodeUint256At(raw, 32)
	if err != nil {
		return Observation{}, err
	}
	answer := decodeInt256(answerRaw)
	updatedAt, err := rpc.DecodeUint256At(raw, 96)
	if err != nil {
		return Observation{}, err
	}

	if answer.Sign() <= 0 {
		return Observation{}, fmt.Errorf("pricefeed/chainlink: %s reported non-positive answer %s", feed.Symbol, answer)
	}
	if !updatedAt.IsInt64() || updatedAt.Int64() <= 0 {
		return Observation{}, fmt.Errorf("pricefeed/chainlink: %s has no valid round timestamp", feed.Symbol)
	}

	decimals, err := r.Decimals(ctx, feed.Address)
	if err != nil {
		return Observation{}, err
	}

	return Observation{
		Symbol:      NormalizeSymbol(feed.Symbol),
		ChainID:     feed.ChainID,
		FeedAddress: strings.ToLower(feed.Address),
		RoundID:     roundID.String(),
		Price:       scaleDecimal(answer, decimals),
		Decimals:    decimals,
		ObservedAt:  time.Unix(updatedAt.Int64(), 0).UTC(),
	}, nil
}

// two256 is 2^256, used to reinterpret a raw word as a signed int256.
var two256 = new(big.Int).Lsh(big.NewInt(1), 256)

// decodeInt256 reinterprets an unsigned 256-bit word as two's-complement
// signed. Chainlink's `answer` is int256; treating it as unsigned would turn a
// negative answer into an astronomically large price.
func decodeInt256(u *big.Int) *big.Int {
	// Values with the high bit set are negative in two's complement.
	if u.BitLen() < 256 {
		return new(big.Int).Set(u)
	}
	return new(big.Int).Sub(u, two256)
}

// scaleDecimal renders `v / 10^decimals` as an exact decimal string, without
// going through float64. Trailing fractional zeros are trimmed so the stored
// NUMERIC and the JSON string agree.
func scaleDecimal(v *big.Int, decimals int) string {
	if decimals <= 0 {
		return v.String()
	}
	neg := v.Sign() < 0
	abs := new(big.Int).Abs(v)

	digits := abs.String()
	if len(digits) <= decimals {
		digits = strings.Repeat("0", decimals-len(digits)+1) + digits
	}
	intPart := digits[:len(digits)-decimals]
	fracPart := strings.TrimRight(digits[len(digits)-decimals:], "0")

	out := intPart
	if fracPart != "" {
		out += "." + fracPart
	}
	if neg {
		out = "-" + out
	}
	return out
}
