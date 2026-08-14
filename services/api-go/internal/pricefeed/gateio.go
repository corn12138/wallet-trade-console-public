package pricefeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// gateioBaseURL is the public, key-less spot market-data endpoint. It was
// chosen over Binance/Coinbase/CoinGecko because it is the one dense OHLCV
// source reachable from BOTH a developer machine and the production VM (the
// others resolve but are blocked there), so dev and prod ingest identical data.
const gateioBaseURL = "https://api.gateio.ws/api/v4"

// gateioMaxLimit is the venue's per-request candle cap.
const gateioMaxLimit = 1000

// MarketProvider fetches real OHLCV for a canonical symbol.
type MarketProvider interface {
	// ID is the provider id persisted on every row it produces.
	ID() string
	// FetchCandles returns up to `limit` candles ending at "now", newest last.
	FetchCandles(ctx context.Context, symbol string, res Resolution, limit int) ([]Candle, error)
	// Supports reports whether the provider can serve this symbol at all.
	Supports(symbol string) bool
}

// GateioProvider reads spot candlesticks from Gate.io.
type GateioProvider struct {
	baseURL string
	http    *http.Client
	// pairs maps canonical product symbols to venue trading pairs. USD markets
	// are served by the venue's USDT pair — the closest real, liquid proxy.
	pairs map[string]string
}

// DefaultGateioPairs maps the product's canonical symbols onto Gate.io pairs.
// Only markets the product actually lists belong here; an unmapped symbol
// yields "unsupported" rather than a guessed pair.
func DefaultGateioPairs() map[string]string {
	return map[string]string{
		"ETH-USD": "ETH_USDT",
		"BTC-USD": "BTC_USDT",
	}
}

// NewGateioProvider builds a provider. baseURL == "" uses the public endpoint;
// tests pass an httptest server. pairs == nil uses DefaultGateioPairs.
func NewGateioProvider(baseURL string, pairs map[string]string, timeout time.Duration) *GateioProvider {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = gateioBaseURL
	}
	if pairs == nil {
		pairs = DefaultGateioPairs()
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &GateioProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
		pairs:   pairs,
	}
}

// ID implements MarketProvider.
func (p *GateioProvider) ID() string { return "gateio" }

// Supports implements MarketProvider.
func (p *GateioProvider) Supports(symbol string) bool {
	_, ok := p.pairs[NormalizeSymbol(symbol)]
	return ok
}

// ErrUnsupportedSymbol is returned for a symbol the provider has no mapping for.
var ErrUnsupportedSymbol = errors.New("pricefeed: unsupported symbol")

// FetchCandles implements MarketProvider. Gate.io returns candles oldest-first
// as arrays of strings:
//
//	[0] bucket start (unix seconds)
//	[1] quote-asset volume
//	[2] close   [3] high   [4] low   [5] open
//	[6] base-asset volume
//	[7] "true" once the bucket has closed
//
// The field order is deliberately not OHLC — decoding it positionally by name
// here is what keeps the rest of the package honest about which value is which.
func (p *GateioProvider) FetchCandles(ctx context.Context, symbol string, res Resolution, limit int) ([]Candle, error) {
	pair, ok := p.pairs[NormalizeSymbol(symbol)]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSymbol, symbol)
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > gateioMaxLimit {
		limit = gateioMaxLimit
	}

	q := url.Values{}
	q.Set("currency_pair", pair)
	q.Set("interval", string(res))
	q.Set("limit", strconv.Itoa(limit))
	endpoint := p.baseURL + "/spot/candlesticks?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("pricefeed/gateio: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pricefeed/gateio: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("pricefeed/gateio: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pricefeed/gateio: http %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var rows [][]string
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("pricefeed/gateio: decode: %w", err)
	}

	out := make([]Candle, 0, len(rows))
	for _, row := range rows {
		if len(row) < 7 {
			// A short row means the venue changed its response shape. Skipping
			// silently would fabricate a gap, so fail loudly instead.
			return nil, fmt.Errorf("pricefeed/gateio: unexpected row width %d", len(row))
		}
		sec, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("pricefeed/gateio: bad timestamp %q: %w", row[0], err)
		}
		closed := len(row) > 7 && row[7] == "true"
		out = append(out, Candle{
			Timestamp:   sec * 1000,
			Open:        row[5],
			High:        row[3],
			Low:         row[4],
			Close:       row[2],
			Volume:      row[6],
			QuoteVolume: row[1],
			Closed:      closed,
		})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
