// Package ai attaches a natural-language explanation to a transaction review
// that has ALREADY been decided (TD 2026-08-09, P2).
//
// The one invariant that shapes every file here: the model's output is never
// load-bearing for fund safety. `txreview` runs deterministic rules against
// observed chain state and produces a ReviewStatus; this package only rephrases
// what those rules already found. It cannot add a risk, clear a risk, or change
// a status — the caller keeps the review verbatim and treats the explanation as
// presentation.
//
// The corollary is that every failure here is a NON-failure for the product:
// no key, disabled flag, timeout, 429, malformed model output — all degrade to
// source="static", and the client renders Check.Title / Check.Summary, which
// are already human-readable English. That degradation path costs nothing to
// build, which is the direct payoff of attaching the model AFTER the rules
// engine instead of in front of it.
package ai

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// Source labels where an explanation came from. The client shows AI prose only
// for SourceAI; SourceStatic means "render the deterministic check text", which
// is a complete, legitimate UI — not an error state to apologize for.
type Source string

const (
	SourceAI     Source = "ai"
	SourceStatic Source = "static"
)

// Reason explains a static result. It is a machine code, not display copy —
// the frontend maps it to i18n, and MUST NOT show "AI failed" wording for it.
type Reason string

const (
	ReasonDisabled    Reason = "AI_DISABLED"
	ReasonUnavailable Reason = "AI_UNAVAILABLE"
)

// Explanation is the model's constrained output. The provider is asked for JSON,
// then the transport rejects missing or unknown fields before validate() applies
// the domain bounds. There is no free-form channel that the product treats as a
// verdict.
type Explanation struct {
	// Headline is one sentence: what this transaction does.
	Headline string `json:"headline"`
	// WhatHappens is the step-by-step from the user's point of view.
	WhatHappens []string `json:"whatHappens"`
	// WatchOut restates concerns that are ALREADY in the review's checks. The
	// prompt forbids adding any not grounded there, and validate() enforces a
	// bound on the count so a runaway list can't bury the real checks.
	WatchOut []string `json:"watchOut"`
	// Locale echoes the requested locale so a client can detect a mismatch.
	Locale string `json:"locale"`
}

// Result is what the service returns: an explanation or an honest absence.
type Result struct {
	Explanation *Explanation `json:"explanation"`
	Source      Source       `json:"source"`
	Reason      Reason       `json:"reason,omitempty"`
	Model       string       `json:"model,omitempty"`
}

// staticResult is the degraded (and entirely valid) outcome.
func staticResult(reason Reason) Result {
	return Result{Explanation: nil, Source: SourceStatic, Reason: reason}
}

// Client is the model transport. One method, so the whole service is testable
// against a fake with no network — the DeepSeek transport lives in deepseek.go.
type Client interface {
	// Explain sends the prompt and returns the parsed explanation. Any error
	// means "no explanation available", never "the transaction is unsafe".
	Explain(ctx context.Context, systemPrompt, userPrompt string) (Explanation, error)
	// Model reports the model id, for the response's audit field.
	Model() string
}

// Config is the deployment knob set. Default is OFF: a deployment that sets
// nothing gets the static path, which is why P2 can ship dark.
type Config struct {
	Enabled bool
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
	// MaxTokens caps the response. The schema is small; this exists so a
	// pathological generation can't run up a bill.
	MaxTokens int64
}

const (
	// The deterministic engine already made the safety decision; this layer only
	// performs a short rewrite, so Flash is the latency/cost-appropriate default.
	// Operators can select deepseek-v4-pro with AI_MODEL without changing code.
	DefaultModel   = "deepseek-v4-flash"
	DefaultBaseURL = "https://api.deepseek.com"
	DefaultTimeout = 12 * time.Second
	// DefaultMaxTokens has headroom over the schema's realistic size (a
	// headline plus two short lists) so a slightly verbose answer completes
	// instead of truncating into an unparseable JSON fragment.
	DefaultMaxTokens = int64(2048)
)

var supportedModels = map[string]struct{}{
	"deepseek-v4-flash": {},
	"deepseek-v4-pro":   {},
}

// ErrNotConfigured means the config asked for AI but can't produce a client.
var ErrNotConfigured = errors.New("ai: enabled but DEEPSEEK_API_KEY is not set")

// ErrUnsupportedModel prevents a stale provider model from creating a client
// that looks configured but silently falls back on every request.
var ErrUnsupportedModel = errors.New("ai: unsupported DeepSeek model")

// ConfigFromEnv reads the deployment knobs.
//
//	AI_ENABLED          "1"/"true"/"yes" turns the layer on (default OFF)
//	DEEPSEEK_API_KEY    required when enabled
//	DEEPSEEK_BASE_URL   optional, default https://api.deepseek.com
//	AI_MODEL            optional model override, default deepseek-v4-flash
//	AI_TIMEOUT_SECONDS  optional, default 12
//	AI_MAX_TOKENS       optional, default 2048
func ConfigFromEnv() Config {
	cfg := Config{
		Enabled:   envBool("AI_ENABLED"),
		APIKey:    strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")),
		BaseURL:   strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL")),
		Model:     strings.TrimSpace(os.Getenv("AI_MODEL")),
		Timeout:   DefaultTimeout,
		MaxTokens: DefaultMaxTokens,
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(os.Getenv("AI_TIMEOUT_SECONDS"))); err == nil && secs > 0 {
		cfg.Timeout = time.Duration(secs) * time.Second
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("AI_MAX_TOKENS")), 10, 64); err == nil && n > 0 {
		cfg.MaxTokens = n
	}
	return cfg
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
