package indexer

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// Well-known mainnet topic0 values let us verify EventTopic0() against
// real-chain canonical hashes rather than just consistency-with-itself.
const (
	approvalTopic0    = "0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925"
	transferTopic0    = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	pairCreatedTopic0 = "0x0d3648bd0f6ba80134a33ba9275ac585d9d315f0ad8355cddefde31afa28d0e9"
	swapTopic0        = "0xd78ad95fa46c994b6551d0da85fc275fe613ce37657fb8d5e3d130840159d822"
	mintTopic0        = "0x4c209b5fc8ad50758f13e2e1088ba56a560dff690a1c6fef26394f4c03821c4f"
	burnTopic0        = "0xdccd412f0b1252819cb1fd330b93224ca42612892bb3f4f789976e6d81936496"
	syncTopic0        = "0x1c411e9a96e071241c2f21f7726b17ae89e3cab4c78be50e062b03a9fffbbad1"
)

func TestEventTopic0_MatchesWellKnown(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Approval", approvalTopic0},
		{"Transfer", transferTopic0},
		{"PairCreated", pairCreatedTopic0},
		{"Swap", swapTopic0},
		{"Mint", mintTopic0},
		{"Burn", burnTopic0},
		{"Sync", syncTopic0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := EventSpecByName(tc.name)
			if spec == nil {
				t.Fatalf("EventSpecByName(%q) = nil", tc.name)
			}
			if spec.Topic0Hex != tc.want {
				t.Errorf("%s topic0 = %s, want %s", tc.name, spec.Topic0Hex, tc.want)
			}
		})
	}
}

func TestEventSpecByTopic0_UnknownReturnsNil(t *testing.T) {
	if got := EventSpecByTopic0("0xdeadbeef"); got != nil {
		t.Errorf("expected nil for unknown topic0, got %+v", got)
	}
}

// pad32 produces the ABI 32-byte word for a value: address (left-padded
// 20 bytes), uint (left-padded big-endian), or raw hex.
func padAddr(addr string) string {
	addr = strings.TrimPrefix(strings.ToLower(addr), "0x")
	return "0x" + strings.Repeat("00", 12) + addr
}
func padUint(n uint64) string {
	b := new(big.Int).SetUint64(n).Bytes()
	pad := make([]byte, 32-len(b))
	return hex.EncodeToString(append(pad, b...))
}

func TestParse_Approval(t *testing.T) {
	owner := "0x1111111111111111111111111111111111111111"
	spender := "0x2222222222222222222222222222222222222222"
	log := Log{
		Address: "0xdead",
		Topics: []string{
			approvalTopic0,
			padAddr(owner),
			padAddr(spender),
		},
		Data: "0x" + padUint(1000),
	}
	parsed := ParseIndexedLog(log)
	if parsed.EventName != "Approval" {
		t.Fatalf("EventName = %q", parsed.EventName)
	}
	if got := parsed.RuntimeArgs["owner"]; got != owner {
		t.Errorf("owner = %v, want %s", got, owner)
	}
	if got := parsed.RuntimeArgs["spender"]; got != spender {
		t.Errorf("spender = %v, want %s", got, spender)
	}
	if v, ok := parsed.RuntimeArgs["value"].(*big.Int); !ok || v.Uint64() != 1000 {
		t.Errorf("value = %v, want 1000", parsed.RuntimeArgs["value"])
	}
	if parsed.ActorAddress != owner {
		t.Errorf("actorAddress = %q, want %q", parsed.ActorAddress, owner)
	}
	// JSON-safe persisted form: bigint → decimal string.
	if persisted := parsed.PersistedArgs["value"]; persisted != "1000" {
		t.Errorf("persisted value = %v, want %q", persisted, "1000")
	}
}

func TestParse_Transfer_ZeroFromFallsBackToTo(t *testing.T) {
	zero := "0x0000000000000000000000000000000000000000"
	to := "0x3333333333333333333333333333333333333333"
	log := Log{
		Address: "0xdead",
		Topics: []string{
			transferTopic0,
			padAddr(zero),
			padAddr(to),
		},
		Data: "0x" + padUint(42),
	}
	parsed := ParseIndexedLog(log)
	if parsed.EventName != "Transfer" {
		t.Fatalf("EventName = %q", parsed.EventName)
	}
	if parsed.ActorAddress != to {
		t.Errorf("actorAddress = %q, want %q (mint event should fall to recipient)", parsed.ActorAddress, to)
	}
}

func TestParse_Swap_AllAmounts(t *testing.T) {
	sender := "0x4444444444444444444444444444444444444444"
	to := "0x5555555555555555555555555555555555555555"
	log := Log{
		Topics: []string{
			swapTopic0,
			padAddr(sender),
			padAddr(to),
		},
		Data: "0x" + padUint(100) + padUint(0) + padUint(0) + padUint(99),
	}
	parsed := ParseIndexedLog(log)
	if parsed.EventName != "Swap" {
		t.Fatalf("EventName = %q", parsed.EventName)
	}
	if v, _ := parsed.RuntimeArgs["amount0In"].(*big.Int); v.Uint64() != 100 {
		t.Errorf("amount0In = %v", v)
	}
	if v, _ := parsed.RuntimeArgs["amount1Out"].(*big.Int); v.Uint64() != 99 {
		t.Errorf("amount1Out = %v", v)
	}
	if parsed.RuntimeArgs["sender"] != sender {
		t.Errorf("sender = %v", parsed.RuntimeArgs["sender"])
	}
	if parsed.RuntimeArgs["to"] != to {
		t.Errorf("to = %v", parsed.RuntimeArgs["to"])
	}
	if parsed.ActorAddress != sender {
		t.Errorf("actor = %q, want %q", parsed.ActorAddress, sender)
	}
}

func TestParse_Sync_NoTopics(t *testing.T) {
	log := Log{
		Topics: []string{syncTopic0},
		Data:   "0x" + padUint(1_000_000) + padUint(2_000_000),
	}
	parsed := ParseIndexedLog(log)
	if parsed.EventName != "Sync" {
		t.Fatalf("EventName = %q", parsed.EventName)
	}
	if v, _ := parsed.RuntimeArgs["reserve0"].(*big.Int); v.Uint64() != 1_000_000 {
		t.Errorf("reserve0 = %v", v)
	}
	if v, _ := parsed.RuntimeArgs["reserve1"].(*big.Int); v.Uint64() != 2_000_000 {
		t.Errorf("reserve1 = %v", v)
	}
}

func TestParse_TokenCreated_WithDynamicStrings(t *testing.T) {
	token := "0x6666666666666666666666666666666666666666"
	bonding := "0x7777777777777777777777777777777777777777"
	creator := "0x8888888888888888888888888888888888888888"
	spec := EventSpecByName("TokenCreated")
	if spec == nil {
		t.Fatal("missing TokenCreated spec")
	}

	// Data layout for (string, string): two 32-byte head offsets, then
	// two tail records (len + utf8 padded to 32). Both string offsets
	// are absolute byte positions from the start of data.
	headLen := 64
	symbolPadded := abiPadBytes([]byte("SYM"))
	symbolHead := padUintHex(uint64(headLen)) // 0x40
	symbolTail := padUintHex(uint64(len("SYM"))) + hex.EncodeToString(symbolPadded)
	nameOffset := uint64(headLen + 32 + len(symbolPadded)) // 0x40 + 0x20 + len
	namePadded := abiPadBytes([]byte("Sym Token"))
	nameHead := padUintHex(nameOffset)
	nameTail := padUintHex(uint64(len("Sym Token"))) + hex.EncodeToString(namePadded)
	data := "0x" + symbolHead + nameHead + symbolTail + nameTail

	log := Log{
		Topics: []string{
			spec.Topic0Hex,
			padAddr(token),
			padAddr(bonding),
			padAddr(creator),
		},
		Data: data,
	}
	parsed := ParseIndexedLog(log)
	if parsed.EventName != "TokenCreated" {
		t.Fatalf("EventName = %q", parsed.EventName)
	}
	if got := parsed.RuntimeArgs["symbol"]; got != "SYM" {
		t.Errorf("symbol = %v, want SYM", got)
	}
	if got := parsed.RuntimeArgs["name"]; got != "Sym Token" {
		t.Errorf("name = %v, want %q", got, "Sym Token")
	}
	if parsed.ActorAddress != creator {
		t.Errorf("actor = %q, want %q", parsed.ActorAddress, creator)
	}
}

func TestParse_UnknownTopic0_FallsBackToRawTopicsData(t *testing.T) {
	log := Log{
		Topics: []string{
			"0x000000000000000000000000000000000000000000000000000000000000beef",
			padAddr("0xabababababababababababababababababababab"),
		},
		Data: "0xdeadbeef",
	}
	parsed := ParseIndexedLog(log)
	if parsed.EventName != "Unknown" {
		t.Fatalf("EventName = %q", parsed.EventName)
	}
	if got := parsed.PersistedArgs["topics"]; got == nil {
		t.Errorf("expected topics in persisted args, got nil")
	}
	if got := parsed.PersistedArgs["data"]; got != "0xdeadbeef" {
		t.Errorf("data = %v", got)
	}
	if parsed.ActorAddress != "0xabababababababababababababababababababab" {
		t.Errorf("actor = %q, want extracted from topic[1]", parsed.ActorAddress)
	}
}

func TestParse_NoTopics_IsUnknown(t *testing.T) {
	parsed := ParseIndexedLog(Log{})
	if parsed.EventName != "Unknown" {
		t.Errorf("empty log should be Unknown, got %q", parsed.EventName)
	}
}

func TestParse_TruncatedTopicsForKnownEvent_IsUnknown(t *testing.T) {
	// Approval expects 3 topics (topic0 + owner + spender). Only topic0
	// here → decode fails → fallback to Unknown.
	log := Log{Topics: []string{approvalTopic0}, Data: "0x" + padUint(1)}
	parsed := ParseIndexedLog(log)
	if parsed.EventName != "Unknown" {
		t.Errorf("expected Unknown for truncated topics, got %q", parsed.EventName)
	}
}

// abiPadBytes left-pads a byte string to the next 32-byte boundary.
func abiPadBytes(b []byte) []byte {
	if len(b)%32 == 0 {
		return b
	}
	out := make([]byte, len(b))
	copy(out, b)
	tail := make([]byte, 32-(len(b)%32))
	return append(out, tail...)
}

func padUintHex(n uint64) string {
	return padUint(n)
}
