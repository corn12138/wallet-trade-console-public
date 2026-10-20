package web3events

import (
	"encoding/json"
	"os"
	"testing"
)

func TestClaimIdentityMatchesTheSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/shared/contracts/fixtures/execution-registry-identities.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		OrdinaryClaim struct {
			Claim           json.RawMessage `json:"claim"`
			Canonical       string          `json:"canonical"`
			CanonicalLength int             `json:"canonicalLength"`
			ClaimDigest     string          `json:"claimDigest"`
		} `json:"ordinaryClaim"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	got := NormalizeClaim(fixture.OrdinaryClaim.Claim)
	if got.Kind != ClaimNormalized || string(CanonicalClaim(got.Claim)) != fixture.OrdinaryClaim.Canonical ||
		len(fixture.OrdinaryClaim.Canonical) != fixture.OrdinaryClaim.CanonicalLength || got.Digest != fixture.OrdinaryClaim.ClaimDigest {
		t.Fatalf("claim identity differs from the shared fixture: %+v", got)
	}
}
