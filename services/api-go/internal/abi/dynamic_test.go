package abi

import (
	"math/big"
	"strings"
	"testing"
)

func TestParseUnits(t *testing.T) {
	cases := []struct {
		in       string
		decimals int
		want     string
		wantErr  bool
	}{
		{"0", 18, "0", false},
		{"1", 18, "1000000000000000000", false},
		{"1.5", 18, "1500000000000000000", false},
		// Truncation past decimals — matches viem.
		{"1.123456789012345678901", 6, "1123456", false},
		{"  10  ", 0, "10", false},
		{"", 18, "", true},
		{"-1", 18, "", true},
		{"abc", 18, "", true},
		{"1.2.3", 18, "", true},
		{"1", -1, "", true},
		{"1", 79, "", true},
	}
	for _, tc := range cases {
		got, err := ParseUnits(tc.in, tc.decimals)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseUnits(%q, %d) succeeded, want error", tc.in, tc.decimals)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseUnits(%q, %d): err=%v", tc.in, tc.decimals, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("ParseUnits(%q, %d) = %s, want %s", tc.in, tc.decimals, got.String(), tc.want)
		}
	}
}

func TestFormatUnits(t *testing.T) {
	cases := []struct {
		in       string // big.Int as decimal string
		decimals int
		want     string
	}{
		{"0", 18, "0"},
		{"1000000000000000000", 18, "1"},
		{"1500000000000000000", 18, "1.5"},
		{"100", 0, "100"},
		{"123456", 6, "0.123456"},
		// Trailing zeros trimmed.
		{"1100000000000000000", 18, "1.1"},
		{"10000000000000000", 18, "0.01"},
		// Negative input — formatted with leading minus.
		{"-1500000000000000000", 18, "-1.5"},
	}
	for _, tc := range cases {
		v, _ := new(big.Int).SetString(tc.in, 10)
		if got := FormatUnits(v, tc.decimals); got != tc.want {
			t.Errorf("FormatUnits(%s, %d) = %q, want %q", tc.in, tc.decimals, got, tc.want)
		}
	}
}

func TestEncodeCallArgs_SwapExactTokensForTokens(t *testing.T) {
	// Truth-table reference: viem's encodeFunctionData for
	// swapExactTokensForTokens(uint256, uint256, address[], address, uint256)
	// with these specific args produces the expected hex below. The
	// path array forces dynamic-arg encoding (offset 0xa0 = 160 = 5×32).
	amountIn := mustEncodeUint(t, big.NewInt(1000))
	amountOutMin := mustEncodeUint(t, big.NewInt(990))
	to := mustEncodeAddr(t, "0x000000000000000000000000000000000000bEEF")
	deadline := mustEncodeUint(t, big.NewInt(1700000000))
	pathArg, err := EncodeAddressArrayArg([]string{
		"0x000000000000000000000000000000000000aAaA",
		"0x000000000000000000000000000000000000BbBb",
	})
	if err != nil {
		t.Fatalf("path arg: %v", err)
	}

	got, err := EncodeCallArgs(
		"swapExactTokensForTokens(uint256,uint256,address[],address,uint256)",
		Static(amountIn),
		Static(amountOutMin),
		pathArg,
		Static(to),
		Static(deadline),
	)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	// Selector for swapExactTokensForTokens(uint256,uint256,address[],address,uint256)
	// is 0x38ed1739 — universally known across DEX clones (Uniswap V2 router).
	want := "0x38ed1739" +
		"00000000000000000000000000000000000000000000000000000000000003e8" + // amountIn = 1000
		"00000000000000000000000000000000000000000000000000000000000003de" + // amountOutMin = 990
		"00000000000000000000000000000000000000000000000000000000000000a0" + // offset to path = 160
		"000000000000000000000000000000000000000000000000000000000000beef" + // to
		"000000000000000000000000000000000000000000000000000000006553f100" + // deadline = 1700000000
		"0000000000000000000000000000000000000000000000000000000000000002" + // path.length = 2
		"000000000000000000000000000000000000000000000000000000000000aaaa" + // path[0]
		"000000000000000000000000000000000000000000000000000000000000bbbb" //   path[1]
	if !strings.EqualFold(got, want) {
		t.Errorf("EncodeCallArgs mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestEncodeCallArgs_AllStaticMatchesEncodeCall(t *testing.T) {
	// When no dynamic args are present, EncodeCallArgs MUST produce the
	// same bytes as EncodeCall — otherwise the swap port and the
	// security/earn ports would silently disagree on identical payloads.
	addrEnc := mustEncodeAddr(t, "0x000000000000000000000000000000000000DEAD")
	zero := mustEncodeUint(t, big.NewInt(0))

	gotDyn, err := EncodeCallArgs("approve(address,uint256)", Static(addrEnc), Static(zero))
	if err != nil {
		t.Fatalf("encode dyn: %v", err)
	}
	gotStatic := EncodeCall("approve(address,uint256)", addrEnc, zero)
	if !strings.EqualFold(gotDyn, gotStatic) {
		t.Errorf("dyn=%s\nstatic=%s", gotDyn, gotStatic)
	}
}

func TestEncodeAddressArrayArg_RejectsBadAddress(t *testing.T) {
	if _, err := EncodeAddressArrayArg([]string{"0xnotanaddress"}); err == nil {
		t.Errorf("expected error on bad address inside array")
	}
}

func TestEncodeCallArgs_RejectsWrongSizedStatic(t *testing.T) {
	// A 16-byte head value must be rejected — silently truncating
	// or padding would corrupt the call layout for downstream args.
	if _, err := EncodeCallArgs("foo(uint256)", Static(make([]byte, 16))); err == nil {
		t.Errorf("expected error on non-32-byte static arg")
	}
}

func mustEncodeUint(t *testing.T, n *big.Int) []byte {
	t.Helper()
	b, err := EncodeUint256(n)
	if err != nil {
		t.Fatalf("encode uint %v: %v", n, err)
	}
	return b
}

func mustEncodeAddr(t *testing.T, addr string) []byte {
	t.Helper()
	b, err := EncodeAddress(addr)
	if err != nil {
		t.Fatalf("encode addr %q: %v", addr, err)
	}
	return b
}
