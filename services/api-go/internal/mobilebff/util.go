package mobilebff

import (
	"net/url"
	"sort"
	"strconv"
	"time"
)

const (
	defaultSearchResultLimit = 3
	maxSearchResultLimit     = 20
	maxConcurrentSearches    = 2
	searchQueryTimeout       = 2 * time.Second
)

var searchQuerySlots = make(chan struct{}, maxConcurrentSearches)

// normalizeSearchResultLimit bounds all search fan-out before it reaches the
// three database queries or any response-slice allocation.
func normalizeSearchResultLimit(limit int) int {
	if limit <= 0 {
		return defaultSearchResultLimit
	}
	if limit > maxSearchResultLimit {
		return maxSearchResultLimit
	}
	return limit
}

func parseSearchResultLimit(raw string) int {
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return defaultSearchResultLimit
	}
	return normalizeSearchResultLimit(limit)
}

// mapArticleCards maps article rows to cards.
func mapArticleCards(s *Service, arts []ArticleRow) []Card {
	out := make([]Card, 0, len(arts))
	for _, a := range arts {
		out = append(out, s.toArticleCard(a))
	}
	return out
}

// sliceCards returns cards[start:end] clamped to bounds (never panics).
func sliceCards(cards []Card, start, end int) []Card {
	if start < 0 {
		start = 0
	}
	if end > len(cards) {
		end = len(cards)
	}
	if start >= end {
		return []Card{}
	}
	out := make([]Card, end-start)
	copy(out, cards[start:end])
	return out
}

// truncateCards returns the first n cards.
func truncateCards(cards []Card, n int) []Card {
	if n < 0 {
		n = 0
	}
	if n > len(cards) {
		n = len(cards)
	}
	out := make([]Card, n)
	copy(out, cards[:n])
	return out
}

// sortTagsByCount returns a copy of tags sorted by ArticleCount desc (stable).
func sortTagsByCount(tags []TagRow) []TagRow {
	out := make([]TagRow, len(tags))
	copy(out, tags)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ArticleCount > out[j].ArticleCount })
	return out
}

// firstTags returns the first n tags.
func firstTags(tags []TagRow, n int) []TagRow {
	if n > len(tags) {
		n = len(tags)
	}
	if n < 0 {
		n = 0
	}
	return tags[:n]
}

// dedupeNonEmpty returns the input with empties dropped and duplicates removed,
// preserving first-seen order.
func dedupeNonEmpty(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
