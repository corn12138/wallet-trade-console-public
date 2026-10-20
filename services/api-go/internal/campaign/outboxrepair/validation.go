package outboxrepair

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)
var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func validAuthority(a Authority) bool {
	return identifier.MatchString(a.Principal) && identifier.MatchString(a.Role) && a.Version > 0
}

func validTime(t time.Time) bool {
	return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 9999 && t.Nanosecond()%1000 == 0
}

func validRequest(r Request) bool {
	if !uuid.MatchString(r.CommandID) || !uuid.MatchString(r.EventID) || !identifier.MatchString(r.RequestID) ||
		len(r.Reason) < 1 || len(r.Reason) > 500 || !utf8.ValidString(r.Reason) || strings.TrimSpace(r.Reason) != r.Reason ||
		r.Expected.Generation < 0 || r.Expected.Generation == math.MaxInt64 || r.Expected.Attempts < 1 || r.Expected.Attempts > math.MaxInt32 || !validTime(r.Expected.DeadLetterAt) {
		return false
	}
	for _, c := range r.Reason {
		if unicode.IsControl(c) {
			return false
		}
	}
	switch r.Expected.ErrorCode {
	case "publisherUnavailable", "unsupportedSchema", "leaseExpired":
		return true
	}
	return false
}

func digest(value any) string {
	raw, _ := json.Marshal(value)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func prepare(r Request, a Authority) Prepared {
	r.Expected.DeadLetterAt = r.Expected.DeadLetterAt.UTC()
	fingerprint := digest(struct {
		Schema               int
		Operation, Source    string
		Principal, CommandID string
		EventID, Reason      string
		Expected             Expected
	}{1, ReplayOperation, "campaign", a.Principal, r.CommandID, r.EventID, r.Reason, r.Expected})
	return Prepared{Request: r, Authority: a, Fingerprint: fingerprint}
}
