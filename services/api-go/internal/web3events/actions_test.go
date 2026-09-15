package web3events

import (
	"errors"
	"strings"
	"testing"
)

// The single decision that makes replacement work: a speed-up arrives under a
// NEW hash with the SAME nonce, so keying on the nonce lands it as a second
// attempt of one action. Keying on the hash would put it in a second action,
// where the replacement rule could never fire.
func TestDeriveServerActionIDKeysOnTheNonceSoAReplacementJoinsTheSameAction(t *testing.T) {
	nonce := uint64(7)
	original := DeriveServerActionID(11155111, &nonce, "0x"+strings.Repeat("a", 64))
	speedUp := DeriveServerActionID(11155111, &nonce, "0x"+strings.Repeat("b", 64))

	if original != speedUp {
		t.Fatalf("a speed-up must resolve to the same action:\n  original=%q\n  speed-up=%q", original, speedUp)
	}
	if !strings.HasPrefix(original, "srv:") {
		t.Errorf("derived ids must live in the server namespace, got %q", original)
	}
}

func TestDeriveServerActionIDSeparatesDifferentNoncesAndChains(t *testing.T) {
	seven, eight := uint64(7), uint64(8)
	hash := "0x" + strings.Repeat("a", 64)
	if DeriveServerActionID(11155111, &seven, hash) == DeriveServerActionID(11155111, &eight, hash) {
		t.Error("different nonces must be different actions")
	}
	if DeriveServerActionID(11155111, &seven, hash) == DeriveServerActionID(84532, &seven, hash) {
		t.Error("the same nonce on a different chain must be a different action")
	}
}

// Without a nonce the server cannot know two hashes are one intent, so it falls
// back to the hash and the action is single-attempt by construction.
func TestDeriveServerActionIDFallsBackToTheHashWhenTheNonceIsUnknown(t *testing.T) {
	hashA, hashB := "0x"+strings.Repeat("a", 64), "0x"+strings.Repeat("b", 64)
	if DeriveServerActionID(11155111, nil, hashA) == DeriveServerActionID(11155111, nil, hashB) {
		t.Error("with no nonce, two hashes must not be merged into one action")
	}
	if !strings.Contains(DeriveServerActionID(11155111, nil, hashA), "tx:") {
		t.Error("the fallback must be visibly hash-derived")
	}
}

// The 409 must be unreachable for a caller that supplies no key. That is
// guaranteed by namespace disjointness, not by assumptions about traffic.
func TestClientSuppliedActionIDsCannotEnterTheServerNamespace(t *testing.T) {
	nonce := uint64(7)
	derived := DeriveServerActionID(11155111, &nonce, "0x"+strings.Repeat("a", 64))

	if _, _, err := NormalizeClientActionID(derived); !errors.Is(err, ErrClientActionIDInvalid) {
		t.Fatalf("a caller must not be able to squat a derived key: err=%v", err)
	}
	if _, _, err := NormalizeClientActionID("srv:anything-at-all"); !errors.Is(err, ErrClientActionIDInvalid) {
		t.Fatal("^srv: must be rejected outright")
	}

	normalized, supplied, err := NormalizeClientActionID("checkout-9f2a11")
	if err != nil || !supplied {
		t.Fatalf("a valid key must be accepted: %v", err)
	}
	if !strings.HasPrefix(normalized, "cli:") {
		t.Errorf("client keys must be namespaced, got %q", normalized)
	}
	if strings.HasPrefix(normalized, "srv:") {
		t.Error("a client key reached the server namespace")
	}
}

func TestClientActionIDValidationBoundsLengthAndCharset(t *testing.T) {
	for _, bad := range []string{"short", strings.Repeat("a", 129), "has space", "emoji-🙂-here", "semi;colon"} {
		if _, _, err := NormalizeClientActionID(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	// Absent is not an error — it means "derive one".
	if _, supplied, err := NormalizeClientActionID("   "); err != nil || supplied {
		t.Errorf("an absent key must be allowed and not marked supplied: supplied=%v err=%v", supplied, err)
	}
}

func TestRequestFingerprintIsStableAndDiscriminating(t *testing.T) {
	to, other := "0xCAFE", "0xbeef"
	value := "100"
	base := func(toAddr *string, val *string) string {
		fp, err := ComputeRequestFingerprint(11155111, "0xDEAD", nil, toAddr, nil, val, nil)
		if err != nil {
			t.Fatalf("fingerprint: %v", err)
		}
		return fp
	}
	if base(&to, &value) != base(&to, &value) {
		t.Error("the same request must hash the same way twice")
	}
	// Address casing must not change identity; the owner is lowercased too.
	lower := "0xcafe"
	if base(&to, &value) != base(&lower, &value) {
		t.Error("address casing must not change the fingerprint")
	}
	if base(&to, &value) == base(&other, &value) {
		t.Error("a different destination must change the fingerprint")
	}
	bigger := "101"
	if base(&to, &value) == base(&to, &bigger) {
		t.Error("a different value must change the fingerprint")
	}
}

// "" and absent are the same request. Without this an optional field that the
// client sometimes sends as an empty string would look like a reuse.
func TestRequestFingerprintTreatsEmptyStringAsAbsent(t *testing.T) {
	empty := ""
	withEmpty, err := ComputeRequestFingerprint(1, "0xdead", &empty, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	withAbsent, err := ComputeRequestFingerprint(1, "0xdead", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if withEmpty != withAbsent {
		t.Error(`"" and absent must produce the same fingerprint`)
	}
}

// The named conflicting case from the design: one attempt confirmed then
// reorged, another still pending. The action is pending — the intent has not
// finished, and a live attempt outranks a dead one.
func TestActionStatusPrefersALiveAttemptOverADeadOne(t *testing.T) {
	if got := DeriveActionStatus([]string{StatusReorged, StatusPending}); got != StatusPending {
		t.Errorf("reorged + pending = %q, want pending", got)
	}
	if got := DeriveActionStatus([]string{StatusReplaced, StatusConfirmed}); got != StatusConfirmed {
		t.Errorf("replaced + confirmed = %q, want confirmed", got)
	}
	if got := DeriveActionStatus([]string{StatusReplaced, StatusPending}); got != StatusPending {
		t.Errorf("replaced + pending = %q, want pending", got)
	}
	if got := DeriveActionStatus([]string{StatusReplaced, StatusDropped}); got != StatusDropped {
		t.Errorf("replaced + dropped = %q, want dropped", got)
	}
	if got := DeriveActionStatus([]string{StatusConfirmed, StatusPending}); got != StatusConfirmed {
		t.Errorf("confirmed + pending = %q, want confirmed", got)
	}
	if got := DeriveActionStatus(nil); got != StatusPending {
		t.Errorf("no attempts = %q, want pending", got)
	}
	if got := DeriveActionStatus([]string{"nonsense"}); got != StatusPending {
		t.Errorf("an unknown status must not win: %q", got)
	}
}

func TestEveryStatusBeyondPendingIsTerminal(t *testing.T) {
	for _, terminal := range []string{StatusConfirmed, StatusFailed, StatusReplaced, StatusDropped, StatusReorged} {
		if !IsTerminalStatus(terminal) {
			t.Errorf("%q must be terminal — a client re-report must not downgrade it", terminal)
		}
	}
	if IsTerminalStatus(StatusPending) {
		t.Error("pending must not be terminal")
	}
}

// failure_detail must never carry a provider body. It is a code-tagged,
// truncated summary and nothing else.
func TestSanitizeFailureDetailTruncatesAndTagsWithoutLeakingTheProviderBody(t *testing.T) {
	leaky := errors.New("dial tcp 127.0.0.1:8545: connect: refused\nhttps://provider.example/v3/SECRETKEY returned 500")
	detail := SanitizeFailureDetail(FailureRPCUnavailable, leaky)
	if detail == nil {
		t.Fatal("a coded failure must produce a detail")
	}
	if len(*detail) > maxFailureDetail {
		t.Errorf("detail is %d bytes, want <= %d", len(*detail), maxFailureDetail)
	}
	if strings.Contains(*detail, "SECRETKEY") || strings.Contains(*detail, "\n") {
		t.Errorf("detail leaked past the first line: %q", *detail)
	}
	if !strings.HasPrefix(*detail, FailureRPCUnavailable) {
		t.Errorf("detail must be code-tagged, got %q", *detail)
	}
	if SanitizeFailureDetail("", leaky) != nil {
		t.Error("no code means no detail")
	}
}

func TestValidateClientFingerprintBoundsSize(t *testing.T) {
	ok := strings.Repeat("a", maxClientFingerprint)
	if err := ValidateClientFingerprint(&ok); err != nil {
		t.Errorf("a fingerprint at the cap must be accepted: %v", err)
	}
	tooBig := strings.Repeat("a", maxClientFingerprint+1)
	if err := ValidateClientFingerprint(&tooBig); err == nil {
		t.Error("an oversize fingerprint must be rejected before it reaches SQL")
	}
	if err := ValidateClientFingerprint(nil); err != nil {
		t.Errorf("absent must be fine: %v", err)
	}
}
