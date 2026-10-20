package main

import (
	"errors"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
)

func TestCampaignTradeMarketsComeFromDeployedMarkets(t *testing.T) {
	got := campaignTradeMarkets(func(*int) ([]markets.SymbolView, error) {
		return []markets.SymbolView{{Symbol: "ETH-USD", ChainID: 11155111}, {Symbol: "BTC-USD", ChainID: 11155111}, {Symbol: "ETH-USD", ChainID: 31337}}, nil
	})
	if !got[11155111]["ETH-USD"] || !got[11155111]["BTC-USD"] || !got[31337]["ETH-USD"] || got[31337]["BTC-USD"] || len(got) != 2 {
		t.Fatalf("markets: %v", got)
	}
	for name, source := range map[string]func(*int) ([]markets.SymbolView, error){
		"no markets service": nil,
		"deployments fail":   func(*int) ([]markets.SymbolView, error) { return nil, errors.New("down") },
	} {
		if got := campaignTradeMarkets(source); len(got) != 0 {
			t.Fatalf("%s must leave trade entries inadmissible: %v", name, got)
		}
	}
}
