package web3events

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

func claimJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func boundClaim(revision, market, consumer string) map[string]any {
	return map[string]any{"mode": "bound", "registryRevision": revision, "operation": "trade.approveCollateral", "market": market,
		"bindingDigest": strings.Repeat("a", 64), "consumer": map[string]any{"id": consumer, "manifestDigest": strings.Repeat("b", 64)}}
}

// N2: bounds, identity and the fixed diagnostic vocabulary.
func TestClaimNormalizationBounds(t *testing.T) {
	ordinary := NormalizeClaim(claimJSON(t, boundClaim("1", "ETH-USD", "web-trade")))
	if ordinary.Kind != ClaimNormalized || len(CanonicalClaim(ordinary.Claim)) != 293 {
		t.Fatalf("ordinary claim: kind %d, %d chars", ordinary.Kind, len(CanonicalClaim(ordinary.Claim)))
	}
	maxBound := NormalizeClaim(claimJSON(t, boundClaim("9223372036854775807", strings.Repeat("A", 16)+"-"+strings.Repeat("B", 16), "a"+strings.Repeat("b", 31))))
	maxExit := NormalizeClaim(claimJSON(t, map[string]any{"mode": "unboundExit", "operation": "trade.closePosition", "market": strings.Repeat("A", 16) + "-" + strings.Repeat("B", 16),
		"reason": "authorityUnavailable", "consumer": map[string]any{"id": "a" + strings.Repeat("b", 31), "manifestDigest": strings.Repeat("e", 64)}}))
	if maxBound.Kind != ClaimNormalized || len(CanonicalClaim(maxBound.Claim)) != 360 || maxExit.Kind != ClaimNormalized || len(CanonicalClaim(maxExit.Claim)) != 270 {
		t.Fatalf("maximum claims: %d/%d chars", len(CanonicalClaim(maxBound.Claim)), len(CanonicalClaim(maxExit.Claim)))
	}
	retry := NormalizeClaim(json.RawMessage(`{"consumer":{"manifestDigest":"` + strings.Repeat("b", 64) + `","id":"web-trade"},"bindingDigest":"` + strings.Repeat("a", 64) +
		`","market":"ETH-USD","operation":"trade.approveCollateral","registryRevision":"1","mode":"bound"}`))
	if retry.Digest != ordinary.Digest {
		t.Error("the same claim with reordered keys must have the same identity")
	}
	other := NormalizeClaim(claimJSON(t, boundClaim("2", "ETH-USD", "web-trade")))
	a, b := string(CanonicalClaim(ordinary.Claim)), string(CanonicalClaim(other.Claim))
	if a[:256] != b[:256] || ordinary.Digest == other.Digest {
		t.Error("claims sharing their first 256 characters must still differ in identity")
	}
	diagnosticRE := regexp.MustCompile(`^[A-Za-z.]+:[a-z]+(;[A-Za-z.]+:[a-z]+){0,3}$`)
	for name, raw := range map[string]json.RawMessage{
		"20-digit revision":     claimJSON(t, boundClaim("10000000000000000000", "ETH-USD", "web-trade")),
		"above BIGINT":          claimJSON(t, boundClaim("9223372036854775808", "ETH-USD", "web-trade")),
		"34-character market":   claimJSON(t, boundClaim("1", strings.Repeat("A", 17)+"-"+strings.Repeat("B", 16), "web-trade")),
		"33-character consumer": claimJSON(t, boundClaim("1", "ETH-USD", "a"+strings.Repeat("b", 32))),
		"unknown field":         json.RawMessage(`{"mode":"bound","registryRevision":"1","operation":"trade.approveCollateral","market":"ETH-USD","bindingDigest":"` + strings.Repeat("a", 64) + `","consumer":{"id":"web-trade","manifestDigest":"` + strings.Repeat("b", 64) + `"},"token":"Bearer secret"}`),
		"duplicate key":         json.RawMessage(`{"mode":"bound","mode":"unboundExit"}`),
		"wrong type":            json.RawMessage(`{"mode":1}`),
		"array":                 json.RawMessage(`[1,2]`),
		"over 2 KiB":            claimJSON(t, map[string]any{"mode": strings.Repeat("x", 3000)}),
		"exit for an open":      claimJSON(t, map[string]any{"mode": "unboundExit", "operation": "trade.openPosition", "market": "ETH-USD", "reason": "noActiveRevision", "consumer": map[string]any{"id": "web-trade", "manifestDigest": strings.Repeat("b", 64)}}),
		"not JSON":              json.RawMessage(`{"mode":`),
	} {
		got := NormalizeClaim(raw)
		if got.Kind != ClaimMalformed || !diagnosticRE.MatchString(got.Diagnostic) || len(got.Diagnostic) > 160 || strings.Contains(got.Diagnostic, "secret") {
			t.Errorf("%s: kind %d diagnostic %q", name, got.Kind, got.Diagnostic)
		}
	}
	if NormalizeClaim(nil).Kind != ClaimAbsent || NormalizeClaim(json.RawMessage(`null`)).Kind != ClaimAbsent {
		t.Error("absent claims")
	}
}

// Property: whatever arrives, normalizer output satisfies every named context
// constraint of the migration (mirrored here), so storage cannot fail on it.
func TestClaimNormalizerOutputFitsTheColumns(t *testing.T) {
	mirror := map[string]*regexp.Regexp{
		"market": regexp.MustCompile(`^[A-Z0-9]{1,16}-[A-Z0-9]{1,16}$`), "digest": regexp.MustCompile(`^[0-9a-f]{64}$`),
		"consumer": regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`), "diagnostic": regexp.MustCompile(`^[A-Za-z.]+:[a-z]+(;[A-Za-z.]+:[a-z]+){0,3}$`),
	}
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"mode", "bound", "unboundExit", "registryRevision", "market", "ETH-USD", "operation", "trade.closePosition",
		"consumer", "id", "manifestDigest", "reason", "x", "", "1", "-1", "99999999999999999999", strings.Repeat("a", 64), "{", "}", "[", "]", ":", ",", "\"", "null", "true"}
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for n := rng.Intn(30); n > 0; n-- {
			b.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		raw := []byte(b.String())
		if i%3 == 0 {
			c := boundClaim(fmt.Sprint(rng.Int63()), "ETH-USD", "web-trade")
			raw = claimJSON(t, c)
		}
		got := NormalizeClaim(raw)
		switch got.Kind {
		case ClaimMalformed:
			if !mirror["diagnostic"].MatchString(got.Diagnostic) || len(got.Diagnostic) > 160 {
				t.Fatalf("diagnostic %q outside its column", got.Diagnostic)
			}
		case ClaimNormalized:
			c := got.Claim
			if !mirror["market"].MatchString(c.Market) || !mirror["digest"].MatchString(got.Digest) || !mirror["consumer"].MatchString(c.ConsumerID) ||
				!mirror["digest"].MatchString(c.ManifestDigest) || !claimOperations[c.Operation] || (c.Mode == "bound" && (c.RegistryRevision <= 0 || !mirror["digest"].MatchString(c.BindingDigest))) {
				t.Fatalf("normalized claim outside its columns: %+v", c)
			}
		}
	}
}

func TestWorkbenchContextNegotiation(t *testing.T) {
	rev := int64(12)
	state := func(s string) *string { return &s }
	for name, tc := range map[string]struct {
		row      AttemptRow
		base, v2 string
	}{
		"historical null":  {AttemptRow{}, `{"state":"notRecorded","registryRevision":null}`, `{"state":"notRecorded","registryRevision":null,"reason":null}`},
		"recorded":         {AttemptRow{ContextState: state("recorded"), ContextRevision: &rev}, `{"state":"recorded","registryRevision":"12"}`, `{"state":"recorded","registryRevision":"12","reason":null}`},
		"claim mismatch":   {AttemptRow{ContextState: state("claimMismatch"), ContextReason: state("bytesMismatch")}, `{"state":"notRecorded","registryRevision":null}`, `{"state":"claimMismatch","registryRevision":null,"reason":"bytesMismatch"}`},
		"unbound exit":     {AttemptRow{ContextState: state("unboundExit"), ContextReason: state("authorityUnavailable")}, `{"state":"notRecorded","registryRevision":null}`, `{"state":"unboundExit","registryRevision":null,"reason":"authorityUnavailable"}`},
		"claim unverified": {AttemptRow{ContextState: state("claimUnverified")}, `{"state":"notRecorded","registryRevision":null}`, `{"state":"claimUnverified","registryRevision":null,"reason":null}`},
	} {
		for v2, want := range map[bool]string{false: tc.base, true: tc.v2} {
			got, _ := json.Marshal(NewAttemptViewFor(tc.row, v2).ExecutionContext)
			if string(got) != want {
				t.Errorf("%s v2=%v: %s, want %s", name, v2, got, want)
			}
		}
	}
}
