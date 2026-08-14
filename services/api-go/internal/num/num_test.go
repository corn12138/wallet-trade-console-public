package num

import (
	"math/big"
	"strings"
	"testing"
)

func bi(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(s)
	}
	return n
}

func TestWeiToDecimal(t *testing.T) {
	cases := map[string]string{
		"0":                              "0",
		"1":                              "0.000000000000000001", // far below 1 NATIVE
		"1000000000000000000":            "1",
		"1500000000000000000":            "1.5",
		"5000000000000000":               "0.005",
		"123456789012345678901234567890": "123456789012.34567890123456789", // huge supply-scale value
		"-2500000000000000000":           "-2.5",
		"999999999999999999":             "0.999999999999999999",
		"1000000000000000000000000000000000000001": "1000000000000000000000.000000000000000001", // overflow-adjacent, stays exact
	}
	for in, want := range cases {
		if got := WeiToDecimal(bi(in)); got != want {
			t.Errorf("WeiToDecimal(%s) = %s, want %s", in, got, want)
		}
	}
	if WeiToDecimal(nil) != "0" {
		t.Error("nil must be 0")
	}
}

func TestMulWeiInt_MarketCap(t *testing.T) {
	// The bonding-curve market cap = post-trade price (wei) × 1e9 supply.
	// A tiny price must NOT explode into trillion-scale artifacts.
	price := bi("12345678901234") // 0.000012345678901234 NATIVE per token
	got := MulWeiInt(price, 1_000_000_000)
	if got != "12345.678901234" {
		t.Fatalf("market cap = %s, want 12345.678901234", got)
	}
	if MulWeiInt(big.NewInt(0), 1_000_000_000) != "0" {
		t.Fatal("zero price → zero market cap")
	}
}

func TestSumWeiStrings_Volume(t *testing.T) {
	got, err := SumWeiStrings([]string{"5000000000000000", "2500000000000000", "0"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.0075" {
		t.Fatalf("volume = %s, want 0.0075", got)
	}
	if _, err := SumWeiStrings([]string{"12", "notanumber"}); err == nil {
		t.Fatal("invalid input must error, not be skipped")
	}
}

func TestWeiStringToDecimal(t *testing.T) {
	got, err := WeiStringToDecimal("5000000000000000")
	if err != nil || got != "0.005" {
		t.Fatalf("got %s, %v", got, err)
	}
	if _, err := WeiStringToDecimal("0x10"); err == nil {
		t.Fatal("hex must be rejected")
	}
	if _, err := WeiStringToDecimal("1.5"); err == nil {
		t.Fatal("decimals must be rejected (raw amounts are integers)")
	}
}

func TestCompareDecimal(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.5", "1.50", 0},
		{"0.000000000000000001", "0", 1},
		{"-1", "0.5", -1},
		{"12345.678901234", "12345.678901235", -1},
	} {
		got, err := CompareDecimal(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("CompareDecimal(%s,%s) = %d,%v want %d", c.a, c.b, got, err, c.want)
		}
	}
}

func TestNormalizeDecimal(t *testing.T) {
	cases := map[string]string{
		"1.500000000000000000": "1.5",
		"0.000000000000000000": "0",
		"12":                   "12",
		"0.005000":             "0.005",
		"":                     "0",
	}
	for in, want := range cases {
		if got := NormalizeDecimal(in); got != want {
			t.Errorf("NormalizeDecimal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScaleToDecimal_TrimAndPad(t *testing.T) {
	if got := ScaleToDecimal(bi("105"), 2); got != "1.05" {
		t.Fatalf("got %s", got)
	}
	if got := ScaleToDecimal(bi("100"), 2); got != "1" {
		t.Fatalf("trailing zeros must trim: %s", got)
	}
	if !strings.HasPrefix(ScaleToDecimal(bi("1"), 8), "0.0000000") {
		t.Fatal("sub-unit values must zero-pad")
	}
}
