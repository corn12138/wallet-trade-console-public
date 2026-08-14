package bigsum

import "testing"

func TestSum_EmptySliceIsZero(t *testing.T) {
	got, err := Sum(nil)
	if err != nil || got != "0" {
		t.Fatalf("Sum(nil) = (%q, %v); want (\"0\", nil)", got, err)
	}
}

func TestSum_EmptyStringsTreatedAsZero(t *testing.T) {
	got, err := Sum([]string{"", "", ""})
	if err != nil || got != "0" {
		t.Fatalf("Sum(empties) = (%q, %v); want (\"0\", nil)", got, err)
	}
}

func TestSum_BasicAddition(t *testing.T) {
	got, err := Sum([]string{"1", "2", "3", "4"})
	if err != nil || got != "10" {
		t.Fatalf("Sum = (%q, %v); want (\"10\", nil)", got, err)
	}
}

func TestSum_VeryLargeValues(t *testing.T) {
	// 30-decimal precision per the perp schema. 10^30 fits in math/big.Int.
	got, err := Sum([]string{
		"1000000000000000000000000000000",
		"2000000000000000000000000000000",
		"3000000000000000000000000000000",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "6000000000000000000000000000000" {
		t.Errorf("Sum = %q", got)
	}
}

func TestSum_NegativeValues(t *testing.T) {
	got, err := Sum([]string{"-5", "10", "-3"})
	if err != nil || got != "2" {
		t.Fatalf("Sum = (%q, %v); want (\"2\", nil)", got, err)
	}
}

func TestSum_MalformedReturnsError(t *testing.T) {
	if _, err := Sum([]string{"1", "abc", "3"}); err == nil {
		t.Error("expected error on malformed value")
	}
}

func TestSum_DecimalPointNotAllowed(t *testing.T) {
	// Prisma stores as raw integers — decimals indicate corrupt data.
	if _, err := Sum([]string{"1.5"}); err == nil {
		t.Error("expected error on decimal value")
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"", "", 0},
		{"", "0", 0},
		{"0", "", 0},
		{"1", "2", -1},
		{"2", "1", 1},
		{"100", "100", 0},
		// 30-decimal precision: ensure big.Int comparison beats string compare.
		{"9", "10", -1},
		{"999999999999999999999999999999", "1000000000000000000000000000000", -1},
	}
	for _, tc := range cases {
		got, err := Compare(tc.left, tc.right)
		if err != nil {
			t.Errorf("Compare(%q,%q) err = %v", tc.left, tc.right, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Compare(%q,%q) = %d, want %d", tc.left, tc.right, got, tc.want)
		}
	}
}

func TestCompare_Malformed(t *testing.T) {
	if _, err := Compare("abc", "0"); err == nil {
		t.Error("expected error on malformed left")
	}
	if _, err := Compare("0", "xyz"); err == nil {
		t.Error("expected error on malformed right")
	}
}
