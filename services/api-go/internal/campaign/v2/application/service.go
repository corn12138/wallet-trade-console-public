package application

import (
	"context"
	"encoding/json"
	"strings"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// Capabilities returns an immutable, local deployed-capability snapshot; it must
// not call RPC or a model inside the aggregate transaction.
type Service struct {
	store        Store
	authorizer   Authorizer
	capabilities func() v2.Capabilities
}

func NewService(store Store, authorizer Authorizer, capabilities func() v2.Capabilities) *Service {
	return &Service{store, authorizer, capabilities}
}
func unavailable(detail string) error {
	return &v2.DomainError{Code: "CAMPAIGN_DATA_UNAVAILABLE", Status: 503, Detail: detail}
}

func (s *Service) Execute(ctx context.Context, actor string, command Command) (Outcome, error) {
	var outcome Outcome
	actor = strings.ToLower(actor)
	// Pointer-bearing input is frozen before authorization or lock waits so
	// validation, hashing and mutation all inspect exactly the same intent.
	command = freezeCommand(command)
	admin, err := validateCommand(actor, command)
	if err != nil {
		return outcome, err
	}
	if s == nil || s.store == nil || s.authorizer == nil {
		return outcome, unavailable("campaign command dependencies unavailable")
	}
	if err = s.authorizer.Require(ctx, actor, admin); err != nil {
		return outcome, err
	}
	// RequestID is transport metadata and is excluded from semantic retries
	// under a new HTTP trace.
	payloadHash, err := PayloadHash(command)
	if err != nil {
		return outcome, err
	}
	key := CommandKey{Actor: actor, Operation: command.Operation, TargetID: command.CampaignID, ClientID: command.ClientCommandID}
	scope := LockScope{CampaignID: command.CampaignID}
	if command.Operation == "create" {
		key.TargetID = "campaigns"
		scope.CreationKey = actor + ":create:campaigns:" + command.ClientCommandID
	}
	err = s.store.Transact(ctx, scope, func(tx Transaction) error {
		if err := s.authorizer.Require(ctx, actor, admin); err != nil {
			return err
		}
		var pub v2.Publication
		var state *v2.Interaction
		kind := interactionKind(command.Operation)
		if command.Operation != "create" {
			var err error
			pub, err = loadPublication(tx, command.CampaignID)
			if err != nil {
				return err
			}
			if kind != "" {
				state, err = tx.Interaction(actor, kind)
				if err != nil {
					return err
				}
				if pub.Active == nil && state == nil {
					return &v2.DomainError{Code: "CAMPAIGN_NOT_FOUND", Status: 404, Detail: "campaign not published"}
				}
			}
		}
		stored, err := tx.Command(key)
		if err != nil {
			return err
		}
		if stored != nil {
			if stored.Key != key {
				return unavailable("command ledger identity mismatch")
			}
			if stored.PayloadHash != payloadHash {
				return &v2.DomainError{Code: "IDEMPOTENCY_CONFLICT", Status: 409, Detail: "command key already belongs to another payload"}
			}
			if command.Operation == "create" {
				if !identityPattern.MatchString(stored.Result.CampaignID) {
					return unavailable("command ledger target invalid")
				}
				pub, err = loadPublication(tx, stored.Result.CampaignID)
				if err != nil {
					return err
				}
			} else if stored.Result.CampaignID != pub.CampaignID {
				return unavailable("command ledger target mismatch")
			}
			outcome = Outcome{Original: stored.Result, Current: snapshot(pub, state), Replayed: true}
			return nil
		}
		// UUID shape was checked before any PostgreSQL cast. Age is checked only
		// after authorized lookup so an expired key can still replay its result.
		now, err := tx.Now()
		if err != nil {
			return err
		}
		if err = v2.ValidateNewCommandID(command.ClientCommandID, now); err != nil {
			return err
		}
		recordID, err := NewID()
		if err != nil {
			return err
		}
		record := Record{ID: recordID, Key: key, PayloadHash: payloadHash, Reason: command.Reason, RequestID: command.RequestID, ExpectedPublication: command.ExpectedPublication, ExpectedState: command.ExpectedState, At: now}
		mutation, result, err := s.prepareMutation(tx, command, pub, state, now)
		if err != nil {
			return err
		}
		record.Result = result
		if _, err = json.Marshal(record.Result); err != nil {
			return err
		}
		if err = tx.Save(record, mutation); err != nil {
			return err
		}
		outcome = Outcome{Original: record.Result, Current: record.Result}
		return nil
	})
	if err != nil {
		return Outcome{}, err
	}
	return outcome, nil
}

func freezeCommand(command Command) Command {
	frozen := command
	frozen.ExpectedPublication = copyValue(command.ExpectedPublication)
	frozen.ExpectedState = copyValue(command.ExpectedState)
	if command.Revision != nil {
		revision := *command.Revision
		revision.DisplayContent.MediaAssetID = copyValue(revision.DisplayContent.MediaAssetID)
		revision.Cta.ChainID = copyValue(revision.Cta.ChainID)
		revision.Cta.MarketID = copyValue(revision.Cta.MarketID)
		frozen.Revision = &revision
	}
	return frozen
}

func copyValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func loadPublication(tx Transaction, campaignID string) (v2.Publication, error) {
	pub, err := tx.Publication(campaignID)
	if err != nil {
		return pub, err
	}
	if pub.CampaignID != campaignID {
		return pub, unavailable("publication identity mismatch")
	}
	return pub, v2.ValidateStoredPublicationEnvelope(pub)
}
