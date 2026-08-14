package web3events

const (
	defaultEventListPage        = 1
	maxEventListPage            = 1_000
	defaultEventListLimit       = 20
	maxEventListLimit           = 100
	defaultUserEventLimit       = 50
	maxUserEventLimit           = 200
	defaultRecentTransactionCap = 20
	maxRecentTransactionCap     = 100
)

func boundedPage(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

// boundedLimit repeats each transport limit at the persistence boundary.
// Repositories are also called by internal services and must not assume every
// caller passed through the public HTTP parser first. Page has the same
// defense so OFFSET arithmetic stays finite and predictably bounded.
func boundedLimit(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}
