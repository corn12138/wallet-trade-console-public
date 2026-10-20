package application

import (
	"math"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// The administrator wire bodies map to exactly one ledger command each. The HTTP
// adapter and the operator client both use these functions, so a client can
// recompute the payload hash the ledger stores and prove that a returned
// command record belongs to its own frozen request rather than trusting shape.

func CreateCommand(body v2.CreateCommandInput) Command {
	command := Command{Operation: "create", ClientCommandID: body.ClientCommandID, Revision: &body.Revision, Reason: body.Reason}
	if body.Slug != nil {
		command.Slug = *body.Slug
	}
	return command
}

func RevisionCommand(campaignID string, body v2.RevisionCommandInput) (Command, error) {
	expected, err := ParseVersion(body.ExpectedPublicationVersion)
	if err != nil {
		return Command{}, err
	}
	return Command{Operation: "revision", CampaignID: campaignID, ClientCommandID: body.ClientCommandID, ExpectedPublication: expected, Revision: &body.Revision, Reason: body.Reason}, nil
}

func PublicationCommand(campaignID string, body v2.PublicationCommandInput) (Command, error) {
	expected, err := ParseVersion(body.ExpectedPublicationVersion)
	if err != nil {
		return Command{}, err
	}
	command := Command{Operation: body.Operation, CampaignID: campaignID, ClientCommandID: body.ClientCommandID, ExpectedPublication: expected, Reason: body.Reason}
	if body.CandidateRevisionID != nil {
		command.CandidateRevisionID = *body.CandidateRevisionID
	}
	return command, nil
}

// ParseVersion accepts only canonical decimal versions, so "01" or "+1" cannot
// alias a different command payload under the same idempotency key.
func ParseVersion(raw string) (*uint64, error) {
	n, err := strconv.ParseUint(raw, 10, 63)
	if err != nil || n > math.MaxInt64 || strconv.FormatUint(n, 10) != raw {
		return nil, v2.InvalidInput("invalid version")
	}
	return &n, nil
}

// PayloadHash is the ledger's idempotency payload hash. Execute stores it and
// compares replays against it; RequestID is transport metadata and excluded.
func PayloadHash(command Command) (string, error) {
	return v2.CanonicalHash(freezeCommand(command))
}

// CommandIdentity is the ledger row a command response was read from. Admin
// responses expose it so the caller can bind a result to its own key, actor,
// operation, target and payload; participant responses keep their old shape.
type CommandIdentity struct {
	ClientCommandID string
	Actor           string
	Operation       string
	TargetID        string
	PayloadHash     string
	CommittedAt     time.Time
}
