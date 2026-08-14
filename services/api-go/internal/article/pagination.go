package article

const (
	defaultArticleListPage  = 1
	maxArticleListPage      = 1_000
	defaultArticleListLimit = 10
	maxArticleListLimit     = 100
)

func boundedArticleListPage(page int) int {
	if page <= 0 {
		return defaultArticleListPage
	}
	if page > maxArticleListPage {
		return maxArticleListPage
	}
	return page
}

// boundedArticleListLimit repeats the HTTP contract at the repository
// boundary so internal callers cannot turn a ListQuery into an oversized
// allocation even when they bypass the handler. Page is bounded separately to
// prevent integer overflow and pathological OFFSET scans.
func boundedArticleListLimit(limit int) int {
	if limit <= 0 {
		return defaultArticleListLimit
	}
	if limit > maxArticleListLimit {
		return maxArticleListLimit
	}
	return limit
}
