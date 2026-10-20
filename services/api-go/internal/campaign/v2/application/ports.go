package application

import (
	"context"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"time"
)

// Authorization is required again after waiting for the aggregate lock. The
// HTTP adapter supplies the verified principal; a body wallet is never one.
type Authorizer interface {
	Require(context.Context, string, bool) error
}
type Store interface {
	Transact(context.Context, LockScope, func(Transaction) error) error
}
type LockScope struct{ CampaignID, CreationKey string }
type Transaction interface {
	Now() (time.Time, error)
	Publication(string) (v2.Publication, error)
	Revision(string) (v2.Revision, error)
	WasPublished(string) (bool, error)
	Interaction(string, string) (*v2.Interaction, error)
	Command(CommandKey) (*Record, error)
	Save(Record, Mutation) error
}

type CommandKey struct{ Actor, Operation, TargetID, ClientID string }
type Record struct {
	ID                                 string
	Key                                CommandKey
	PayloadHash, Reason, RequestID     string
	ExpectedPublication, ExpectedState *uint64
	Result                             Result
	At                                 time.Time
}
type Definition struct{ ID, Slug string }
type PublicationChange struct{ Before, After v2.Publication }
type Mutation struct {
	Definition  *Definition
	Revision    *v2.Revision
	Publication *PublicationChange
	Interaction *v2.InteractionChange
	// BasePublication supplies the already locked aggregate for interaction and
	// revision-only commands, so adapters never infer cross-aggregate ownership.
	BasePublication v2.Publication
}
type Result struct {
	CampaignID         string     `json:"campaignId"`
	RevisionID         string     `json:"revisionId,omitempty"`
	PublicationVersion uint64     `json:"publicationVersion,string"`
	InteractionVersion uint64     `json:"interactionVersion,string"`
	State              *StateView `json:"state,omitempty"`
	Changed            bool       `json:"changed"`
}
type Outcome struct {
	Original, Current Result
	Replayed          bool
}
