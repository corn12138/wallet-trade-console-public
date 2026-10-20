package v2

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestStoredRevisionRechecksCanonicalContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Revision)
		code string
	}{
		{"missing-id", func(r *Revision) { r.ID = "" }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"missing-campaign", func(r *Revision) { r.CampaignID = "" }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"control-id", func(r *Revision) { r.ID = "revision\n1" }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"changed-terms", func(r *Revision) { r.Input.MaterialTerms.Copy.Zh.Title = "changed promise" }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"changed-display", func(r *Revision) { r.Input.DisplayContent.Layout = "featured" }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"invalid-terms-hash", func(r *Revision) { r.MaterialTermsHash = strings.Repeat("a", 64) }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"invalid-content-hash", func(r *Revision) { r.ContentHash = "wrong" }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"invalid-date", func(r *Revision) { r.Input.MaterialTerms.EndAt = "2026-02-31T00:00:00Z" }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"unknown-canonical", func(r *Revision) { r.Input.TermsCanonicalVersion = 2 }, "CAMPAIGN_UNSUPPORTED"},
		{"unknown-schema", func(r *Revision) { r.Input.SchemaVersion = 2 }, "CAMPAIGN_UNSUPPORTED"},
		{"unknown-rule", func(r *Revision) { r.Input.MaterialTerms.Rules.Join.Version = 2 }, "CAMPAIGN_UNSUPPORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			revision := *publicationFixture(t).Active
			tc.edit(&revision)
			errorCode(t, ValidateStoredRevision(revision, capabilities), tc.code)
		})
	}
	if err := ValidateStoredRevision(*publicationFixture(t).Active, capabilities); err != nil {
		t.Fatal(err)
	}
}

func TestStoredPublicationRejectsInvalidOwnershipAndVersions(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Publication)
		code string
	}{
		{"missing-campaign", func(p *Publication) { p.CampaignID = "" }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"foreign-active", func(p *Publication) { p.Active.CampaignID = "other-campaign" }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"zero-published-version", func(p *Publication) { p.Version = 0 }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"overflow-publication", func(p *Publication) { p.Version = math.MaxInt64 + 1 }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"overflow-interaction", func(p *Publication) { p.InteractionVersion = math.MaxInt64 + 1 }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"unknown-mode", func(p *Publication) { p.Mode = "future-mode" }, "CAMPAIGN_UNSUPPORTED"},
		{"lost-active", func(p *Publication) { p.Active = nil }, "CAMPAIGN_STORED_STATE_INVALID"},
		{"paused-draft", func(p *Publication) { p.Active = nil; p.Version = 0; p.Mode = "paused" }, "CAMPAIGN_STORED_STATE_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub := publicationFixture(t)
			tc.edit(&pub)
			errorCode(t, ValidateStoredPublication(pub, capabilities), tc.code)
		})
	}
	for _, pub := range []Publication{
		{CampaignID: "draft", Mode: "enabled"},
		{CampaignID: "archived-draft", Mode: "archived", Version: 1},
		{CampaignID: "unverified-legacy", Mode: "enabled", HasAcceptedHistory: true},
	} {
		if err := ValidateStoredPublicationIntegrity(pub); err != nil {
			t.Fatalf("valid unpublished/legacy state rejected: %v", err)
		}
	}
}

func TestRemovedCapabilitiesCloseAdmissionButPreserveHistoryAndCancellation(t *testing.T) {
	chain := int64(31337)
	market, media := "ETH-USD", "approved-banner"
	input := revisionFixture(t)
	input.Cta = CTA{ActionKind: "openTrade", ChainID: &chain, MarketID: &market}
	input.DisplayContent.MediaAssetID = &media
	original := Capabilities{Templates: map[string]bool{"campaignCardV1": true}, MediaAssets: map[string]bool{media: true}, TradeMarkets: map[int64]map[string]bool{chain: {market: true}}}
	revision, err := NewRevision("revision-1", "campaign-1", input, original)
	if err != nil {
		t.Fatal(err)
	}
	pub := Publication{CampaignID: "campaign-1", Version: 1, Mode: "enabled", Active: &revision}
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	joined, err := DecideInteraction(pub, nil, "join", true, 0, &pub.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []Capabilities{
		{},
		{Templates: original.Templates, MediaAssets: original.MediaAssets},
		{Templates: original.Templates, TradeMarkets: original.TradeMarkets},
	} {
		errorCode(t, ValidateStoredPublication(pub, current), "CAMPAIGN_UNSUPPORTED")
		if err := ValidateStoredPublicationIntegrity(pub); err != nil {
			t.Fatalf("removed capability erased history: %v", err)
		}
		if err := ValidateStoredPublicationEnvelope(pub); err != nil {
			t.Fatal(err)
		}
		left, err := DecideInteraction(pub, &joined.After, "join", false, joined.After.Version, nil, now)
		if err != nil || !left.Changed || left.After.Status != "left" || left.After.Acceptance.Provenance != "verified" {
			t.Fatalf("removed capability prevented cancellation: %+v, %v", left, err)
		}
	}
}

func TestUnsupportedHistoricalContentDoesNotBecomeAdmissionOrPreventExit(t *testing.T) {
	pub := publicationFixture(t)
	now := time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC)
	joined, err := DecideInteraction(pub, nil, "join", true, 0, &pub.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	pub.Active.Input.TermsCanonicalVersion = 2
	errorCode(t, ValidateStoredPublication(pub, capabilities), "CAMPAIGN_UNSUPPORTED")
	errorCode(t, ValidateStoredPublicationIntegrity(pub), "CAMPAIGN_UNSUPPORTED")
	if err := ValidateStoredPublicationEnvelope(pub); err != nil {
		t.Fatal(err)
	}
	left, err := DecideInteraction(pub, &joined.After, "join", false, joined.After.Version, nil, now)
	if err != nil || left.After.Status != "left" {
		t.Fatalf("cancel required interpreting unknown historic terms: %+v %v", left, err)
	}
}
