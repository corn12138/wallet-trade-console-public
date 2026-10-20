package application

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// Scope binds private pagination to its principal without placing a wallet in
// the opaque cursor. Every SQL page must still constrain the verified actor.
type QueryCursor struct {
	Kind       string `json:"kind"`
	Scope      string `json:"scope"`
	Phase      string `json:"phase"`
	ID         string `json:"id"`
	SortOrder  int64  `json:"sortOrder"`
	At         string `json:"at"`
	StateKind  string `json:"stateKind"`
	RowVersion int64  `json:"rowVersion"`
}

var cursorPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func QueryScope(actor string) string {
	sum := sha256.Sum256([]byte(actor))
	return hex.EncodeToString(sum[:])
}

// HistoryScope additionally binds a history cursor to its representation: the
// imported-entry variant pages a different sequence, so a cursor continues
// only the representation that issued it. The base scope is unchanged, so
// existing cursors keep working.
func HistoryScope(actor string, variants v2.Variants) string {
	if !variants.LegacyImportHistory {
		return QueryScope(actor)
	}
	sum := sha256.Sum256([]byte(v2.VariantLegacyImportHistory + "\x00" + actor))
	return hex.EncodeToString(sum[:])
}
func EncodeQueryCursor(cursor QueryCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func DecodeQueryCursor(raw, kind, scope, phase string) (QueryCursor, error) {
	var cursor QueryCursor
	if raw == "" {
		return cursor, nil
	}
	invalid := func() (QueryCursor, error) { return QueryCursor{}, v2.InvalidInput("invalid query cursor") }
	if len(raw) > 2048 || !cursorPattern.MatchString(raw) {
		return invalid()
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Kind != kind || cursor.Scope != scope || cursor.Phase != phase || !identityPattern.MatchString(cursor.ID) {
		return invalid()
	}
	switch kind {
	case "list":
		if cursor.SortOrder < 0 || cursor.SortOrder > 10000 || cursor.At != "" || cursor.StateKind != "" || cursor.RowVersion != 0 {
			return invalid()
		}
	case "history":
		at, err := time.Parse(time.RFC3339Nano, cursor.At)
		if err != nil || at.Location() != time.UTC || cursor.SortOrder != 0 || cursor.RowVersion < 1 || (cursor.StateKind != "join" && cursor.StateKind != "reminder") {
			return invalid()
		}
	default:
		return invalid()
	}
	return cursor, nil
}
