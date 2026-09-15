package indexer

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// projectionStableID keeps rebuilt read-model identities stable across repair
// runs; API clients may retain these IDs even though the rows are derived data.
func projectionStableID(domain string, parts ...string) string {
	canonical := domain + "\x00" + strings.Join(parts, "\x00")
	digest := sha256.Sum256([]byte(canonical))
	return "idx_" + hex.EncodeToString(digest[:16])
}
