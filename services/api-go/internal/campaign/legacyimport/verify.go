package legacyimport

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Verify re-reads the legacy source and refuses an approved inventory unless it
// was made with the current DigestAlgorithm and every legacy row still has the
// digest it had when reviewed. It is the precondition for any import step: a
// plan approved against different data, or hashed under an older algorithm,
// must be re-inventoried and reviewed again. Verify never writes or repairs.
func Verify(ctx context.Context, pool *pgxpool.Pool, approved Report) (Report, error) {
	if approved.DigestAlgorithm != DigestAlgorithm {
		return Report{}, fmt.Errorf("inventory used digest algorithm %q, not %q; capture and review a new inventory", approved.DigestAlgorithm, DigestAlgorithm)
	}
	current, err := Run(ctx, pool)
	if err != nil {
		return Report{}, err
	}
	if current.SourceSHA256 == approved.SourceSHA256 {
		return current, nil
	}
	before, after := map[string]string{}, map[string]string{}
	for _, c := range approved.Campaigns {
		before[c.ID] = c.LegacyRowSHA256
	}
	for _, c := range current.Campaigns {
		after[c.ID] = c.LegacyRowSHA256
	}
	var changed []string
	for id, digest := range before {
		if next, ok := after[id]; !ok {
			changed = append(changed, id+" (removed)")
		} else if next != digest {
			changed = append(changed, id+" (changed)")
		}
	}
	for id := range after {
		if _, ok := before[id]; !ok {
			changed = append(changed, id+" (added)")
		}
	}
	sort.Strings(changed)
	if len(changed) == 0 {
		changed = []string{"participant or reminder rows"}
	}
	return current, fmt.Errorf("legacy source changed since the approved inventory: %s", strings.Join(changed, ", "))
}
