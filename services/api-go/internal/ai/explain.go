package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/txreview"
)

// This file owns the model-facing contract. `ai` imports `txreview` and never
// the reverse — the rules engine has no idea a model exists, which is what
// makes "the model cannot change a verdict" a structural property rather than
// a policy someone has to remember.

// SystemPrompt is fixed because a prompt that varies per request is a prompt
// nobody can audit. Every constraint here exists to keep the model on the
// presentation side of the line.
const SystemPrompt = `You explain Ethereum transaction reviews to wallet users.

You are given a risk determination that has ALREADY been made by a deterministic
rules engine reading live on-chain state. Your job is to make that determination
readable. It is not to re-do it.

Hard rules:
1. Never overturn, soften, or escalate the determination. Never tell the user
   whether to sign. The reviewStatus field is the verdict; you are the caption.
2. Only describe risks that appear in the checks you were given. If a concern is
   not in the checks, it does not go in your answer. Do not speculate about
   what might also be wrong.
3. "Unknown" is not "fine" and it is not "bad". When a check says a value could
   not be read, say it could not be read. Never restate unknown as zero, as
   absent, or as safe.
4. No investment advice. No price predictions. No opinions about whether a token
   or protocol is a good idea.
5. When a check concerns a trusted-relayer bridge, state plainly who holds the
   power the user is trusting: the relayer operator and the contract admin.
6. Write for someone who is about to sign, in plain language, without hype and
   without scare wording. Describe mechanics, not feelings.

Output contract:
Return valid json and nothing else. Include exactly these four keys, using the
requested locale for every user-facing string. The arrays may be empty only
where the review supports that.

Example JSON output:
{"headline":"This transaction approves a token allowance.","whatHappens":["The spender receives permission to move the stated amount."],"watchOut":[],"locale":"en"}

Answer in the requested locale.`

// Reviewer is the deterministic engine this layer explains. Narrow on purpose:
// the explain endpoint re-runs the review server-side rather than trusting a
// client-supplied one (see RegisterGuarded).
type Reviewer interface {
	ReviewTransaction(ctx context.Context, in txreview.ReviewInput) (txreview.Result, error)
}

// Service turns a completed review into an explanation, or into an honest
// absence of one.
type Service struct {
	cfg      Config
	client   Client
	reviewer Reviewer
}

// NewService wires the layer. A nil client is the normal disabled state, not an
// error — every call then returns source="static" with AI_DISABLED.
func NewService(cfg Config, client Client, reviewer Reviewer) *Service {
	return &Service{cfg: cfg, client: client, reviewer: reviewer}
}

// Enabled reports whether an explanation could be produced at all. Used by
// /api/status/product so an operator can see the layer is dark on purpose.
func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled && s.client != nil
}

// ExplainReview is the whole feature. It never returns an error: an explanation
// that cannot be produced is a static result, because the caller already holds
// a complete, renderable review.
func (s *Service) ExplainReview(ctx context.Context, review txreview.Result, locale string) Result {
	if !s.Enabled() {
		return staticResult(ReasonDisabled)
	}
	locale = normalizeLocale(locale)

	exp, err := s.client.Explain(ctx, SystemPrompt, buildUserPrompt(review, locale))
	if err != nil {
		// Warn, not error: the user is unaffected and the product still works.
		slog.WarnContext(ctx, "ai: explanation unavailable, falling back to static check text", "err", err)
		return staticResult(ReasonUnavailable)
	}
	if err := validate(&exp, review, locale); err != nil {
		slog.WarnContext(ctx, "ai: model output rejected, falling back to static check text", "err", err)
		return staticResult(ReasonUnavailable)
	}
	return Result{Explanation: &exp, Source: SourceAI, Model: s.client.Model()}
}

// buildUserPrompt serializes the review as plain text. Everything in here was
// produced by this server: check titles and summaries are hardcoded English
// literals in the rules engine, and the numbers come from chain reads. No
// client-supplied string reaches the model.
func buildUserPrompt(review txreview.Result, locale string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Locale: %s\n", locale)
	fmt.Fprintf(&b, "Operation: %s\n", review.OperationType)
	fmt.Fprintf(&b, "Chain ID: %d\n", review.ChainID)
	fmt.Fprintf(&b, "Verdict (do not change): %s\n", review.ReviewStatus)
	fmt.Fprintf(&b, "Risk score: %d/100\n", review.RiskScore)

	b.WriteString("\nTransaction the wallet would sign:\n")
	fmt.Fprintf(&b, "  to: %s\n", orDash(review.GeneratedTx.To))
	fmt.Fprintf(&b, "  value (wei): %s\n", orDash(review.GeneratedTx.Value))
	fmt.Fprintf(&b, "  calldata selector: %s\n", selectorOf(review.GeneratedTx.Data))

	b.WriteString("\nChecks (the complete set — do not add to it):\n")
	if len(review.Checks) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, c := range review.Checks {
		fmt.Fprintf(&b, "  - [%s/%s] %s: %s\n", c.Status, c.Severity, c.Title, c.Summary)
	}

	if review.AllowanceChange != nil {
		b.WriteString("\nAllowance change:\n")
		fmt.Fprintf(&b, "  spender: %s\n", review.AllowanceChange.Spender)
		fmt.Fprintf(&b, "  requested: %s\n", derefOr(review.AllowanceChange.RequestedAllowance, "—"))
		fmt.Fprintf(&b, "  current: %s\n", derefOr(review.AllowanceChange.CurrentAllowance, "—"))
	}

	if review.Simulation.Mode != "" {
		fmt.Fprintf(&b, "\nSimulation mode: %s\n", review.Simulation.Mode)
		if review.Simulation.ErrorMessage != nil && *review.Simulation.ErrorMessage != "" {
			fmt.Fprintf(&b, "Simulation error: %s\n", *review.Simulation.ErrorMessage)
		}
	}

	b.WriteString("\nExplain this review for the user in the requested locale. Return only valid json.")
	return b.String()
}

// validate is the deterministic gate on model output. It cannot check that the
// prose is grounded — nothing can, mechanically — so it enforces what it can:
// shape, non-emptiness, and a bound tied to the review's own check count, so a
// runaway list cannot bury the checks the user actually needs to read.
func validate(exp *Explanation, review txreview.Result, locale string) error {
	exp.Headline = strings.TrimSpace(exp.Headline)
	if exp.Headline == "" {
		return fmt.Errorf("empty headline")
	}
	exp.WhatHappens = compact(exp.WhatHappens)
	exp.WatchOut = compact(exp.WatchOut)
	if len(exp.WhatHappens) == 0 {
		return fmt.Errorf("empty whatHappens")
	}
	if len(exp.WhatHappens) > maxSteps {
		return fmt.Errorf("whatHappens has %d items, max %d", len(exp.WhatHappens), maxSteps)
	}
	// One caution per check is the ceiling: the model restates concerns, it
	// does not discover them.
	if limit := len(review.Checks); len(exp.WatchOut) > limit {
		return fmt.Errorf("watchOut has %d items but the review has %d checks", len(exp.WatchOut), limit)
	}
	// Echo the locale we asked for; a mismatch means the model drifted and the
	// prose may not be in the language the user is reading.
	if normalizeLocale(exp.Locale) != locale {
		return fmt.Errorf("locale mismatch: asked %q, got %q", locale, exp.Locale)
	}
	exp.Locale = locale
	return nil
}

const maxSteps = 8

func compact(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// normalizeLocale keeps the layer to the locales the product actually ships.
// An unknown locale falls back to English rather than being passed through to
// the model, which would produce prose no catalog can pair with.
func normalizeLocale(locale string) string {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "zh", "zh-cn", "zh-hans":
		return "zh"
	default:
		return "en"
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func derefOr(p *string, fallback string) string {
	if p == nil || *p == "" {
		return fallback
	}
	return *p
}

// selectorOf passes only the 4-byte selector, not the full calldata. The
// arguments are already described by the checks and the allowance change; the
// raw bytes would add tokens without adding meaning.
func selectorOf(data string) string {
	d := strings.TrimSpace(data)
	if !strings.HasPrefix(d, "0x") || len(d) < 10 {
		return "—"
	}
	return d[:10]
}
