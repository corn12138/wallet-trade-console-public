package deployments

import (
	"encoding/json"
	"testing"
)

// TestContractInfo_MarshalRoundTrips locks the /api/contracts/config byte-shape
// fixed after the 2026-06-08 live golden run: ContractInfo must marshal back to
// the source shape the deployed NestJS serves — bare strings stay bare strings,
// objects stay flattened lowercase {"address":…, <metadata>} — never the
// untagged {"Address":…,"Extra":{…}}.
func TestContractInfo_MarshalRoundTrips(t *testing.T) {
	t.Run("bare string entry stays a bare string", func(t *testing.T) {
		var c ContractInfo
		if err := json.Unmarshal([]byte(`"0xbcDB627E54ABDf3D59bB30e9e49bdB2385A5A170"`), &c); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(got) != `"0xbcDB627E54ABDf3D59bB30e9e49bdB2385A5A170"` {
			t.Errorf("scalar round-trip = %s, want the bare address string", got)
		}
	})

	t.Run("object entry flattens to lowercase address + metadata", func(t *testing.T) {
		var c ContractInfo
		src := `{"address":"0xd69E97092Fc484D5A09c7A6aB3924F093D3753cF","name":"AI Mint","symbol":"AMTI"}`
		if err := json.Unmarshal([]byte(src), &c); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		// Re-parse to compare structurally (map key order is not contractual).
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("re-parse %s: %v", got, err)
		}
		if _, capital := m["Address"]; capital {
			t.Errorf("emitted capitalised \"Address\"; want lowercase: %s", got)
		}
		if _, nested := m["Extra"]; nested {
			t.Errorf("emitted nested \"Extra\"; want flattened: %s", got)
		}
		if m["address"] != "0xd69E97092Fc484D5A09c7A6aB3924F093D3753cF" {
			t.Errorf("address = %v, want the lowercase-keyed address", m["address"])
		}
		if m["name"] != "AI Mint" || m["symbol"] != "AMTI" {
			t.Errorf("metadata not flattened to top level: %s", got)
		}
	})
}
