package web3events

import "testing"

func TestBoundedLimit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value, fallback, maximum, want int
	}{{0, 20, 100, 20}, {-1, 20, 100, 20}, {40, 20, 100, 40}, {101, 20, 100, 100}} {
		if got := boundedLimit(tc.value, tc.fallback, tc.maximum); got != tc.want {
			t.Errorf("boundedLimit(%d, %d, %d) = %d, want %d", tc.value, tc.fallback, tc.maximum, got, tc.want)
		}
	}
}

func TestBoundedPage(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		value, fallback, maximum, want int
	}{{0, 1, 1_000, 1}, {-1, 1, 1_000, 1}, {40, 1, 1_000, 40}, {1_001, 1, 1_000, 1_000}} {
		if got := boundedPage(tc.value, tc.fallback, tc.maximum); got != tc.want {
			t.Errorf("boundedPage(%d, %d, %d) = %d, want %d", tc.value, tc.fallback, tc.maximum, got, tc.want)
		}
	}
}
