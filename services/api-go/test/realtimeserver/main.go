// Command realtimeserver is a minimal harness that mounts ONLY the strict
// zero-Nest realtime tier (internal/realtime) plus a few control/REST endpoints,
// so the real socket.io-client@4.8.3 e2e suite
// (services/api-go/test/realtime-e2e/client.e2e.js) can drive it without a DB,
// Redis, or the full cmd/api wiring. It is test-only — not shipped.
//
// Endpoints:
//
//	/socket.io/                     -> Socket.IO transport (markets + token-events)
//	POST /control/broadcast         -> fan a sample market snapshot to the rooms
//	POST /control/broadcast-trade   -> fan a sample token trade to token-events
//	GET  /api/markets/snapshot/{sym}-> REST fallback snapshot (FE polling parity)
//	GET  /healthz                   -> readiness probe for the runner
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/eventbus"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/realtime"
)

const (
	sampleSymbol  = "ETH-USD"
	sampleChainID = 11155111
	sampleToken   = "0xaabbccddeeff00112233445566778899aabbccdd"
)

func sampleSnapshot() *realtime.Snapshot {
	return &realtime.Snapshot{
		Symbol:  sampleSymbol,
		ChainID: sampleChainID,
		Ticker: map[string]any{
			"symbol":    sampleSymbol,
			"chainId":   sampleChainID,
			"price":     "1234.56",
			"updatedAt": "2026-06-09T00:00:00.000Z",
		},
		Bids:      [][]string{{"1234.00", "10"}},
		Asks:      [][]string{{"1235.00", "8"}},
		Trades:    []map[string]any{{"id": "t1", "price": "1234.56", "isLong": true}},
		UpdatedAt: "2026-06-09T00:00:00.000Z",
	}
}

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = "127.0.0.1:8099"
	}

	rt := realtime.NewServer(realtime.Options{
		// snapshot-on-subscribe: a fresh subscriber to ETH-USD gets an immediate
		// frame (mirrors emitCachedSnapshotIfAvailable).
		MarketSnapshot: func(symbol string, _ int) *realtime.Snapshot {
			if strings.EqualFold(symbol, sampleSymbol) {
				return sampleSnapshot()
			}
			return nil
		},
	})

	mux := http.NewServeMux()
	mux.Handle("/socket.io/", rt.Handler())

	mux.HandleFunc("/control/broadcast", func(w http.ResponseWriter, _ *http.Request) {
		rt.BroadcastMarket(sampleSnapshot())
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/control/broadcast-trade", func(w http.ResponseWriter, _ *http.Request) {
		rt.BroadcastTrade(sampleToken, map[string]any{
			"tokenAddress": sampleToken, "type": "buy", "price": "1.23", "amount": "100",
		})
		w.WriteHeader(http.StatusNoContent)
	})

	// Producer-path leg: the body is {kind, tokenAddress, payload}. It is
	// wrapped with eventbus.NewTokenEvent + EncodeTokenEvent (exactly what the
	// indexer's DB sink publishes over pg_notify) and then fed through
	// eventbus.DispatchEncoded (exactly what the API's LISTEN loop runs per
	// notification). Green here proves the PRODUCTION encode→decode→broadcast
	// pipeline delivers to Socket.IO subscribers — only the Postgres transport
	// hop is substituted.
	mux.HandleFunc("/control/publish-token-event", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var in struct {
			Kind         string          `json:"kind"`
			TokenAddress string          `json:"tokenAddress"`
			Payload      json.RawMessage `json:"payload"`
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil || json.Unmarshal(body, &in) != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		ev, err := eventbus.NewTokenEvent(in.Kind, in.TokenAddress, json.RawMessage(in.Payload))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		encoded, err := eventbus.EncodeTokenEvent(ev)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := eventbus.DispatchEncoded(rt, encoded); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// REST fallback parity: GET /api/markets/snapshot/{symbol} returns the same
	// {symbol, ticker, orderbook, trades, updatedAt} contract the FE polls.
	mux.HandleFunc("/api/markets/snapshot/", func(w http.ResponseWriter, r *http.Request) {
		sym := strings.ToUpper(strings.TrimPrefix(r.URL.Path, "/api/markets/snapshot/"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"symbol":  sym,
			"chainId": sampleChainID,
			"ticker": map[string]any{
				"symbol": sym, "chainId": sampleChainID, "price": "1234.56",
				"updatedAt": "2026-06-09T00:00:00.000Z",
			},
			"orderbook": map[string]any{
				"bids": [][]string{{"1234.00", "10"}},
				"asks": [][]string{{"1235.00", "8"}},
			},
			"trades":    []map[string]any{{"id": "t1", "price": "1234.56", "isLong": true}},
			"updatedAt": "2026-06-09T00:00:00.000Z",
		})
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("realtime e2e server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
