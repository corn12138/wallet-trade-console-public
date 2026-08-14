package article

import "testing"

func TestBoundedArticleListLimit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		input int
		want  int
	}{{0, 10}, {-1, 10}, {20, 20}, {101, 100}} {
		if got := boundedArticleListLimit(tc.input); got != tc.want {
			t.Errorf("boundedArticleListLimit(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestBoundedArticleListPage(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		input int
		want  int
	}{{0, 1}, {-1, 1}, {20, 20}, {1_001, 1_000}} {
		if got := boundedArticleListPage(tc.input); got != tc.want {
			t.Errorf("boundedArticleListPage(%d) = %d, want %d", tc.input, got, tc.want)
		}
	}
}
