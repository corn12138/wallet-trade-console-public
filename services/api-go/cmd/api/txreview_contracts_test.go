package main

import (
	"errors"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
)

type reviewDeploymentLoader struct {
	registry map[int]deployments.ChainConfig
	err      error
}

func (l reviewDeploymentLoader) Load() (map[int]deployments.ChainConfig, error) {
	return l.registry, l.err
}

func TestReviewContractAddressRequiresNamedChainDeployment(t *testing.T) {
	const address = "0x000000000000000000000000000000000000beef"
	lookup := knownSpendersLookup{loader: reviewDeploymentLoader{registry: map[int]deployments.ChainConfig{
		11155111: {Contracts: map[string]deployments.ContractInfo{"PositionManager": {Address: address}}},
	}}}
	if got, ok := lookup.ContractAddress(11155111, "PositionManager"); !ok || got != address {
		t.Fatalf("named deployment not returned: %q %v", got, ok)
	}
	for _, query := range []struct {
		chain int
		name  string
	}{{84532, "PositionManager"}, {11155111, "PerpMarket"}} {
		if _, ok := lookup.ContractAddress(query.chain, query.name); ok {
			t.Fatalf("unregistered deployment resolved: %+v", query)
		}
	}
	for _, unavailable := range []knownSpendersLookup{{}, {loader: reviewDeploymentLoader{err: errors.New("unreadable")}}} {
		if _, ok := unavailable.ContractAddress(11155111, "PositionManager"); ok {
			t.Fatal("unavailable registry resolved")
		}
	}
}
