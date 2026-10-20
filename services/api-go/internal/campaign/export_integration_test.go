//go:build integration

package campaign

// Fixtures for the compatibility integration test, which lives in the
// external campaign_test package: it wires the real v2 HTTP authorizer, and
// that package reaches this one through legacyactivation, so importing it
// from an in-package test is an import cycle.
const FixtureWallet = testWallet

var (
	CampaignAuth      = campaignAuth
	StrictResolver    = strictResolver
	CompatibilityView = compatibilityView
)
