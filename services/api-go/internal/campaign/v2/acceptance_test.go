package v2

import (
	"testing"
	"time"
)

func TestStoredAcceptanceCannotBeInventedOrReset(t *testing.T) {
	pub := publicationFixture(t)
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	for _, previous := range []Interaction{
		{Kind: "join", Status: "joined", Version: 1, Acceptance: Acceptance{Provenance: "neverAccepted"}},
		{Kind: "join", Status: "joined", Version: 1, Acceptance: Acceptance{Provenance: "verified"}},
		{Kind: "join", Status: "left", Version: 1, Acceptance: Acceptance{Provenance: "futureV2"}},
		{Kind: "join", Status: "left", Version: 0, Acceptance: Acceptance{Provenance: "neverAccepted"}},
	} {
		_, err := DecideInteraction(pub, &previous, "join", false, previous.Version, nil, now)
		errorCode(t, err, "CAMPAIGN_UNSUPPORTED")
	}
	// Legacy acceptance remains explicitly unknown through cancellation; an
	// empty accepted revision is not patched to the current publication.
	legacy := Interaction{Kind: "join", Status: "joined", Version: 1, Acceptance: Acceptance{Provenance: "legacyUnknown"}}
	pub.Mode = "archived"
	change, err := DecideInteraction(pub, &legacy, "join", false, 1, nil, now)
	if err != nil || !change.Changed || change.After.Acceptance.Provenance != "legacyUnknown" || change.After.Acceptance.RevisionID != nil {
		t.Fatalf("legacy cancellation fabricated acceptance: %+v, %v", change, err)
	}
}

func TestUnsupportedConfigurationCannotBecomeActiveAdmission(t *testing.T) {
	pub := publicationFixture(t)
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	pub.Active.Input.MaterialTerms.Rules.Join.Version = 2
	if Phase(pub, now) != "unsupported" || NextTransition(pub, now) != nil {
		t.Fatal("future rule presented as a supported phase")
	}
	_, err := DecideInteraction(pub, nil, "join", true, 0, &pub.Version, now)
	errorCode(t, err, "CAMPAIGN_UNSUPPORTED")
}
