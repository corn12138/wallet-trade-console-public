package legacyimport

import "testing"

// Literal-level policy: canonicalRow sees PostgreSQL's rendering, so any two
// different numeric literals, strings or structures must hash differently,
// while formatting that carries no value (key order, whitespace, escapes of
// the same character) must not change the digest.
func TestCanonicalRowKeepsExactValues(t *testing.T) {
	distinct := [][2]string{
		{`{"m":{"n":9007199254740992}}`, `{"m":{"n":9007199254740993}}`},
		{`{"m":{"n":0.1000000000000000055511151231257827}}`, `{"m":{"n":0.1}}`},
		{`{"m":{"n":1.0}}`, `{"m":{"n":1}}`},
		{`{"m":{"n":-5}}`, `{"m":{"n":5}}`},
		{`{"m":{"n":1e2}}`, `{"m":{"n":100}}`},
		{`{"m":{"a":[1,2]}}`, `{"m":{"a":[2,1]}}`},
		{`{"m":{"a":{"b":{"c":1}}}}`, `{"m":{"a":{"b":{"c":"1"}}}}`},
		{`{"m":"x"}`, `{"m":"X"}`},
	}
	for _, pair := range distinct {
		a, errA := canonicalRow([]byte(pair[0]))
		b, errB := canonicalRow([]byte(pair[1]))
		if errA != nil || errB != nil || a == b {
			t.Fatalf("%s and %s must differ (%v %v)", pair[0], pair[1], errA, errB)
		}
	}
	same := [][2]string{
		{`{"a":1,"b":2}`, `{"b":2, "a":1}`},
		{`{"s":"<"}`, `{"s":"<"}`},
	}
	for _, pair := range same {
		a, _ := canonicalRow([]byte(pair[0]))
		b, _ := canonicalRow([]byte(pair[1]))
		if a != b {
			t.Fatalf("%s and %s carry the same value", pair[0], pair[1])
		}
	}
	for _, bad := range []string{"{\"s\":\"\xff\"}", `{"a":1} {"b":2}`, `{"a":`} {
		if _, err := canonicalRow([]byte(bad)); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}
