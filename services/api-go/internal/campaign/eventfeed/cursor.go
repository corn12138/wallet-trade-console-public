package eventfeed

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"strconv"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

var campaignPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)
var opaquePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type cursorValue struct {
	Schema  int    `json:"schema"`
	Scope   string `json:"scope"`
	Version string `json:"version"`
}

func digest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func sessionScope(claims auth.Claims) string { return digest(claims.Session()) }
func feedScope(id, family, identity string) string {
	return digest([]string{"campaign-events-v1", id, family, identity})
}
func encodeCursor(scope string, version int64) string {
	raw, _ := json.Marshal(cursorValue{Schema: 1, Scope: scope, Version: strconv.FormatInt(version, 10)})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// The cursor is an opaque position, not an authorization credential. SQL still
// resolves the stream from the authenticated owner on every request.
func decodeCursor(raw, scope string) (*int64, error) {
	if raw == "" {
		return nil, nil
	}
	invalid := func() (*int64, error) { return nil, v2.InvalidInput("invalid event feed cursor") }
	if len(raw) > 1024 || !opaquePattern.MatchString(raw) {
		return invalid()
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return invalid()
	}
	var cursor cursorValue
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Schema != 1 || cursor.Scope != scope || !decimalPattern.MatchString(cursor.Version) {
		return invalid()
	}
	version, err := strconv.ParseInt(cursor.Version, 10, 64)
	if err != nil || encodeCursor(scope, version) != raw {
		return invalid()
	}
	return &version, nil
}
