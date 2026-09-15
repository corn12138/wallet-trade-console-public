package main

import (
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/bridge"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

func TestBuildWeb3TransactionRPCsPreservesTestnetBoundary(t *testing.T) {
	clients := map[int]bridge.EthCaller{
		1:        rpc.NewClient("http://mainnet.invalid", 0),
		8453:     rpc.NewClient("http://base.invalid", 0),
		31337:    rpc.NewClient("http://anvil.invalid", 0),
		84532:    rpc.NewClient("http://base-sepolia.invalid", 0),
		421614:   rpc.NewClient("http://arbitrum-sepolia.invalid", 0),
		11155111: rpc.NewClient("http://sepolia.invalid", 0),
	}

	got := buildWeb3TransactionRPCs(clients)
	for _, chainID := range []int{31337, 84532, 421614, 11155111} {
		if got[chainID] == nil {
			t.Errorf("testnet chain %d was excluded", chainID)
		}
	}
	for _, chainID := range []int{1, 8453} {
		if got[chainID] != nil {
			t.Errorf("mainnet chain %d entered the transaction verifier", chainID)
		}
	}
}
