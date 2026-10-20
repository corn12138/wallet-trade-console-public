package campaign

import (
	"context"
	"time"
)

// LegacyAnswer reads campaign id the way the legacy API answers it for wallet
// ("" for an anonymous request) while the legacy tables own the ID: through q
// at the fixed instant now. The legacy activation calls it inside its
// transaction before writing (docs/campaign-legacy-activation-protocol-2026-10-09.md §1.5).
func LegacyAnswer(ctx context.Context, q Querier, now time.Time, id, wallet string) (Campaign, error) {
	return (&Service{repo: NewRepositoryAt(q, now)}).byID(ctx, id, wallet)
}

// CompatibleAnswer reads it the way the legacy API answers once the versioned
// model may own the ID: reader first, the legacy tables (through q at now)
// only when the versioned model does not have it. The activation calls it
// after writing, with a reader on the same transaction and instant.
func CompatibleAnswer(ctx context.Context, q Querier, reader PublishedReader, now time.Time, id, wallet string) (Campaign, error) {
	return (&Service{repo: NewRepositoryAt(q, now), compat: &publishedCompatibility{reader: reader}}).byID(ctx, id, wallet)
}
