package abi

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

func TestSelector_KnownSignatures(t *testing.T) {
	// These selectors are stable across the entire Ethereum ecosystem;
	// they're the truth-table against which a correct keccak256 + the
	// canonical signature MUST agree.
	cases := map[string]string{
		"approve(address,uint256)":  "095ea7b3",
		"transfer(address,uint256)": "a9059cbb",
		"balanceOf(address)":        "70a08231",
		"stake(uint256)":            "a694fc3a",
		"unstake(uint256)":          "2e17de78",
	}
	for sig, want := range cases {
		got := hex.EncodeToString(Selector(sig))
		if got != want {
			t.Errorf("Selector(%q) = %s, want %s", sig, got, want)
		}
	}
}

func TestEncodeUint256(t *testing.T) {
	cases := []struct {
		in   *big.Int
		want string
	}{
		{nil, "0000000000000000000000000000000000000000000000000000000000000000"},
		{big.NewInt(0), "0000000000000000000000000000000000000000000000000000000000000000"},
		{big.NewInt(1), "0000000000000000000000000000000000000000000000000000000000000001"},
		// 2^160 fits in 32 bytes: 11 zero bytes + 0x01 + 20 zero bytes.
		{new(big.Int).Lsh(big.NewInt(1), 160), "0000000000000000000000010000000000000000000000000000000000000000"},
	}
	for _, tc := range cases {
		got, err := EncodeUint256(tc.in)
		if err != nil {
			t.Fatalf("EncodeUint256(%v): %v", tc.in, err)
		}
		if hex.EncodeToString(got) != tc.want {
			t.Errorf("EncodeUint256(%v) = %s, want %s", tc.in, hex.EncodeToString(got), tc.want)
		}
	}
}

func TestEncodeUint256_RejectsNegative(t *testing.T) {
	if _, err := EncodeUint256(big.NewInt(-1)); err == nil {
		t.Errorf("expected error on negative input")
	}
}

func TestEncodeUint256_RejectsOverflow(t *testing.T) {
	// 2^256 needs 33 bytes — must be rejected.
	overflow := new(big.Int).Lsh(big.NewInt(1), 256)
	if _, err := EncodeUint256(overflow); err == nil {
		t.Errorf("expected overflow error")
	}
}

func TestEncodeAddress(t *testing.T) {
	got, err := EncodeAddress("0x000000000000000000000000000000000000DEAD")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := "000000000000000000000000000000000000000000000000000000000000dead"
	if hex.EncodeToString(got) != want {
		t.Errorf("got %s, want %s", hex.EncodeToString(got), want)
	}
}

func TestEncodeAddress_BadInputs(t *testing.T) {
	for _, bad := range []string{"", "0x", "not-an-address", "0x123", "0xZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"} {
		if _, err := EncodeAddress(bad); err == nil {
			t.Errorf("EncodeAddress(%q) succeeded, want error", bad)
		}
	}
}

func TestEncodeCall_ApproveZero(t *testing.T) {
	// The classic ERC-20 "revoke" call: approve(spender, 0).
	encoded, err := EncodeAddress("0x000000000000000000000000000000000000DEAD")
	if err != nil {
		t.Fatalf("encode addr: %v", err)
	}
	zero, err := EncodeUint256(big.NewInt(0))
	if err != nil {
		t.Fatalf("encode 0: %v", err)
	}
	data := EncodeCall("approve(address,uint256)", encoded, zero)
	want := "0x095ea7b3" +
		"000000000000000000000000000000000000000000000000000000000000dead" +
		"0000000000000000000000000000000000000000000000000000000000000000"
	if !strings.EqualFold(data, want) {
		t.Errorf("EncodeCall mismatch:\n got %s\nwant %s", data, want)
	}
}
