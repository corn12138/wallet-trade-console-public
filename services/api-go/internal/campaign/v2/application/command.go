package application

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

type Command struct {
	Operation           string            `json:"operation"`
	CampaignID          string            `json:"campaignId"`
	ClientCommandID     string            `json:"clientCommandId"`
	ExpectedPublication *uint64           `json:"expectedPublicationVersion,string"`
	ExpectedState       *uint64           `json:"expectedStateVersion,string"`
	CandidateRevisionID string            `json:"candidateRevisionId"`
	Revision            *v2.RevisionInput `json:"revision"`
	Slug                string            `json:"slug"`
	Reason              string            `json:"reason"`
	RequestID           string            `json:"-"`
}

var walletPattern = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
var identityPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var clientIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var revisionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

func validateCommand(actor string, command Command) (bool, error) {
	if !walletPattern.MatchString(actor) {
		return false, &v2.DomainError{Code: "UNAUTHORIZED", Status: 401, Detail: "verified wallet required"}
	}
	if !clientIDPattern.MatchString(command.ClientCommandID) {
		return false, v2.InvalidInput("clientCommandId must be canonical UUIDv7")
	}
	if command.CandidateRevisionID != "" && !revisionIDPattern.MatchString(command.CandidateRevisionID) {
		return false, v2.InvalidInput("candidateRevisionId must be canonical UUID")
	}
	if strings.TrimSpace(command.RequestID) == "" || len(command.RequestID) > 96 || utf8.RuneCountInString(command.Reason) > 1000 || !utf8.ValidString(command.RequestID) || !utf8.ValidString(command.Reason) || strings.ContainsRune(command.RequestID, 0) || strings.ContainsRune(command.Reason, 0) {
		return false, v2.InvalidInput("request metadata outside bounds")
	}
	if command.ExpectedPublication != nil && *command.ExpectedPublication > math.MaxInt64 || command.ExpectedState != nil && *command.ExpectedState > math.MaxInt64 {
		return false, v2.InvalidInput("version outside range")
	}
	if command.Revision != nil && !revisionEncodingValid(*command.Revision) {
		return false, v2.InvalidInput("revision text encoding")
	}
	admin := true
	switch command.Operation {
	case "create":
		if command.CampaignID != "" || command.Revision == nil || command.CandidateRevisionID != "" || command.ExpectedPublication != nil || command.ExpectedState != nil || (command.Slug != "" && !slugPattern.MatchString(command.Slug)) {
			return false, v2.InvalidInput("create command fields")
		}
	case "revision":
		if command.Revision == nil || command.ExpectedPublication == nil || command.CandidateRevisionID != "" || command.ExpectedState != nil || command.Slug != "" {
			return false, v2.InvalidInput("revision command fields")
		}
	case "publish", "rollback", "pause", "resume", "archive":
		candidateRequired := command.Operation == "publish" || command.Operation == "rollback"
		if command.ExpectedPublication == nil || command.ExpectedState != nil || command.Revision != nil || command.Slug != "" || candidateRequired != (command.CandidateRevisionID != "") {
			return false, v2.InvalidInput("publication command fields")
		}
	case "join", "leave", "set-reminder", "remove-reminder":
		admin = false
		positive := command.Operation == "join" || command.Operation == "set-reminder"
		if command.ExpectedState == nil || positive != (command.ExpectedPublication != nil) || command.Revision != nil || command.CandidateRevisionID != "" || command.Slug != "" {
			return false, v2.InvalidInput("interaction command fields")
		}
	default:
		return false, v2.InvalidInput("unknown operation")
	}
	if admin && strings.TrimSpace(command.Reason) == "" {
		return false, v2.InvalidInput("admin reason required")
	}
	if command.Operation != "create" && !identityPattern.MatchString(command.CampaignID) {
		return false, v2.InvalidInput("campaign identity")
	}
	return admin, nil
}

func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := hex.EncodeToString(raw[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], nil
}

// PostgreSQL JSONB rejects U+0000; JSON marshaling replaces invalid UTF-8. Reject
// both before hashing so malformed text cannot alias a previously valid intent.
func revisionEncodingValid(input v2.RevisionInput) bool {
	terms := input.MaterialTerms
	values := []string{input.TemplateID, input.DisplayContent.Layout, terms.StartAt, terms.EndAt, terms.RewardDelivery, terms.Rules.Join.Kind, terms.Rules.Reminder.Kind, input.Cta.ActionKind}
	for _, copy := range []v2.LocalizedCopy{terms.Copy.En, terms.Copy.Zh} {
		values = append(values, copy.Title, copy.Description, copy.Rules, copy.RewardDescription)
	}
	for _, value := range []*string{input.DisplayContent.MediaAssetID, input.Cta.MarketID} {
		if value != nil {
			values = append(values, *value)
		}
	}
	for _, value := range values {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return false
		}
	}
	return true
}
