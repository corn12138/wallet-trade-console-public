//go:build livesmoke

package ai

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The only test in this package that touches the network. Behind a build tag so
// it never runs in the normal suite, and driven by scripts/strict/
// ai-explain-live-smoke.sh, which skips when there is no key.
//
// What it proves that nothing offline can: the configured DeepSeek endpoint
// accepts the request we build and returns something that survives validate().
// With DEEPSEEK_BASE_URL unset, that endpoint is api.deepseek.com. Everything
// else — the degradation contract, the wire shape — is covered without a key
// and without a bill.
func TestLiveExplain(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY not set")
	}

	cfg := ConfigFromEnv()
	cfg.Enabled = true
	cfg.APIKey = key
	client, err := NewDeepSeekClient(cfg)
	if err != nil {
		t.Fatalf("NewDeepSeekClient: %v", err)
	}

	review := blockedReview()
	out := NewService(cfg, client, nil).ExplainReview(context.Background(), review, "en")

	if out.Source != SourceAI {
		t.Fatalf("live call degraded to static (reason %q) — the layer would never "+
			"produce an explanation in production", out.Reason)
	}
	if out.Explanation == nil {
		t.Fatal("source=ai with a nil explanation")
	}

	exp := out.Explanation
	t.Logf("model     %s", out.Model)
	t.Logf("headline  %s", exp.Headline)
	for i, s := range exp.WhatHappens {
		t.Logf("  step %d  %s", i+1, s)
	}
	for i, s := range exp.WatchOut {
		t.Logf("  watch %d %s", i+1, s)
	}

	// Grounding and tone cannot be proven with substring checks: for example,
	// "not safe to sign" contains the apparent go-ahead "safe to sign", while a
	// valid synonym for paused may not contain "paus". The deterministic shape,
	// locale, and bounds are hard gates above; reviewers inspect this logged prose
	// when closing provider/model acceptance evidence.
}
