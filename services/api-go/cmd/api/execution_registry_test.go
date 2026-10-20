package main

import (
	"context"
	"testing"
)

func TestMissingRegistryStillResolvesUnavailableReview(t *testing.T) {
	got := (reviewExecution{}).ResolveReview(context.Background(), 11155111, "0x0000000000000000000000000000000000000001", "0x12345678")
	if got.State != "unavailable" || got.Reason != "authorityUnavailable" {
		t.Fatalf("context=%+v", got)
	}
	if got.Lookup == nil {
		t.Fatal("unavailable review must carry an empty snapshot lookup")
	}
	if _, ok := got.Lookup.ContractAddress(11155111, "PositionManager"); ok {
		t.Fatal("unavailable registry resolved an address")
	}
}
