package v2

import (
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

var commandIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Only call this for an authenticated, authorized command absent from the
// ledger. An existing key returns its original result even after this window.
func ValidateNewCommandID(id string, now time.Time) error {
	if !commandIDPattern.MatchString(id) {
		return InvalidInput("clientCommandId must be canonical UUIDv7")
	}
	raw, _ := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	var milliseconds int64
	for _, value := range raw[:6] {
		milliseconds = milliseconds<<8 | int64(value)
	}
	issued := time.UnixMilli(milliseconds)
	if issued.Before(now.Add(-7 * 24 * time.Hour)) {
		return &DomainError{"CAMPAIGN_COMMAND_EXPIRED", 410, "command absent and outside acceptance window"}
	}
	if issued.After(now.Add(5 * time.Minute)) {
		return InvalidInput("clientCommandId is too far in the future")
	}
	return nil
}
