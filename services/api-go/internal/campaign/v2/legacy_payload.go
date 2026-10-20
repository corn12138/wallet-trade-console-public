package v2

// LegacyActivationPayloadHash is the ledger payload hash of one legacy import
// activation request (docs/campaign-legacy-activation-protocol-2026-10-09.md
// §5). The server records it with the activation; a client recomputes it from
// the request it froze to recognize its own activation after an uncertain send.
func LegacyActivationPayloadHash(runID, campaignID string, body LegacyActivationCommandInput) (string, error) {
	return CanonicalHash(map[string]any{"runId": runID, "campaignId": campaignID, "clientCommandId": body.ClientCommandID, "reason": body.Reason,
		"legacyRowSha256": body.LegacyRowSha256, "interactionsSha256": body.InteractionsSha256})
}
