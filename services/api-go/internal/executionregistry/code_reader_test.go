package executionregistry

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type alteredObservation struct{ mutate func(*CodeObservation) }

func (c alteredObservation) CodeAt(ctx context.Context, chainID int, address string) (CodeObservation, error) {
	obs, err := (fakeCode{}).CodeAt(ctx, chainID, address)
	if c.mutate != nil {
		c.mutate(&obs)
	}
	return obs, err
}

func TestRevalidationRequiresMatchingCodeAndCompleteObservation(t *testing.T) {
	snapshot := mustBuild(t, readFile(t, realDecl), realDeployments)
	for _, tc := range []struct {
		name   string
		reader CodeReader
		want   error
	}{
		{"missing reader", nil, ErrCodeUnverifiable},
		{"changed code", alteredObservation{func(o *CodeObservation) { o.CodeHash = "0x" + strings.Repeat("e", 64) }}, ErrCodeChanged},
		{"wrong chain", alteredObservation{func(o *CodeObservation) { o.ChainID++ }}, ErrCodeUnverifiable},
		{"wrong target", alteredObservation{func(o *CodeObservation) { o.Address = zeroAddress }}, ErrCodeUnverifiable},
		{"missing block identity", alteredObservation{func(o *CodeObservation) { o.BlockHash = "" }}, ErrCodeUnverifiable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Revalidate(context.Background(), snapshot, tc.reader); !errors.Is(err, tc.want) {
				t.Fatalf("revalidate=%v want=%v", err, tc.want)
			}
		})
	}
	observations, err := Revalidate(context.Background(), snapshot, alteredObservation{})
	if err != nil || len(observations) != len(snapshot.Entries) {
		t.Fatalf("unchanged code=%d observations %v", len(observations), err)
	}
}
