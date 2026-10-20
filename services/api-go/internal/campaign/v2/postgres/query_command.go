package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
)

func commandKind(operation string) string {
	switch operation {
	case "join", "leave":
		return "join"
	case "set-reminder", "remove-reminder":
		return "reminder"
	}
	return ""
}
func (q *querySnapshot) commandCurrent(id, actor, kind string) (application.Result, error) {
	result := application.Result{CampaignID: id}
	var pubVersion, interactionVersion int64
	var active *string
	err := q.tx.QueryRow(q.ctx, `SELECT publication_version,interaction_version,active_revision_id::text FROM campaign_publications WHERE campaign_id=$1`, id).Scan(&pubVersion, &interactionVersion, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, notFound()
	}
	if err != nil {
		return result, err
	}
	if pubVersion < 0 || interactionVersion < 0 {
		return result, unavailable("stored command publication invalid")
	}
	result.PublicationVersion = uint64(pubVersion)
	result.InteractionVersion = uint64(interactionVersion)
	if active != nil {
		result.RevisionID = *active
	}
	if kind != "" {
		row, err := q.personal(id, actor, kind)
		if err != nil {
			return result, err
		}
		if active == nil && row == nil {
			return result, notFound()
		}
		if row != nil {
			view, err := row.view()
			if err != nil {
				return result, err
			}
			if view.State != "known" {
				return result, unavailable("stored command state unsupported")
			}
			result.State = &application.StateView{Kind: kind, Status: row.status, StateVersion: strconv.FormatInt(row.version, 10), AcceptanceProvenance: row.provenance, AcceptedRevisionID: row.revisionID, AcceptedPublicationVersion: view.AcceptedPublicationVersion, AcceptedTermsHash: row.termsHash, AcceptedAt: row.acceptedAt}
		}
	}
	return result, nil
}
func (s *Store) CommandResult(ctx context.Context, key application.CommandKey) (application.CommandResponse, error) {
	var result application.CommandResponse
	err := s.readSnapshot(ctx, func(q *querySnapshot) error {
		kind := commandKind(key.Operation)
		// Resource authorization precedes ledger lookup. Collection creation is an
		// admin-only application operation; its immutable result identifies scope.
		var current application.Result
		var err error
		if key.Operation != "create" {
			current, err = q.commandCurrent(key.TargetID, key.Actor, kind)
			if err != nil {
				return err
			}
		}
		var raw []byte
		var occurredAt time.Time
		var identity application.CommandIdentity
		// Identity columns are read from the stored row (not echoed from the
		// request key) so an administrator response proves which ledger record,
		// including its payload hash, the result belongs to.
		err = q.tx.QueryRow(q.ctx, `SELECT result,occurred_at,client_command_id::text,actor,operation,target_id,payload_hash FROM campaign_commands WHERE actor=$1 AND operation=$2 AND target_id=$3 AND client_command_id=$4`, key.Actor, key.Operation, key.TargetID, key.ClientID).Scan(&raw, &occurredAt, &identity.ClientCommandID, &identity.Actor, &identity.Operation, &identity.TargetID, &identity.PayloadHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound()
		}
		if err != nil {
			return err
		}
		var original application.Result
		if decodeStoredCommandResult(raw, &original) != nil || !readIDPattern.MatchString(original.CampaignID) || original.PublicationVersion > math.MaxInt64 || original.InteractionVersion > math.MaxInt64 {
			return unavailable("stored command result invalid")
		}
		if key.Operation != "create" && original.CampaignID != key.TargetID {
			return unavailable("stored command target mismatch")
		}
		if original.RevisionID != "" && !readUUIDPattern.MatchString(original.RevisionID) {
			return unavailable("stored command revision invalid")
		}
		if key.Operation == "create" {
			if key.TargetID != "campaigns" {
				return v2.InvalidInput("creation target")
			}
			current, err = q.commandCurrent(original.CampaignID, key.Actor, "")
			if err != nil {
				return err
			}
		}
		if kind != "" {
			if original.State == nil || original.State.Kind != kind {
				return unavailable("stored command personal result invalid")
			}
			if err = validateOriginalState(original.State, occurredAt); err != nil {
				return err
			}
		} else if original.State != nil {
			return unavailable("stored administrative result invalid")
		}
		identity.CommittedAt = occurredAt
		result = application.CommandResponse{SchemaVersion: 1, ServerNow: q.timestamp(), AsOf: q.timestamp(), Replayed: true, Original: original, Current: current, Identity: identity}
		return nil
	})
	if err != nil {
		return application.CommandResponse{}, err
	}
	return result, nil
}
func validateOriginalState(state *application.StateView, occurredAt time.Time) error {
	version, err := strconv.ParseInt(state.StateVersion, 10, 64)
	if err != nil || strconv.FormatInt(version, 10) != state.StateVersion {
		return unavailable("stored command state version invalid")
	}
	var acceptedVersion *int64
	if state.AcceptedPublicationVersion != nil {
		value, err := strconv.ParseInt(*state.AcceptedPublicationVersion, 10, 64)
		if err != nil || strconv.FormatInt(value, 10) != *state.AcceptedPublicationVersion {
			return unavailable("stored command accepted version invalid")
		}
		acceptedVersion = &value
	}
	row := personalRow{kind: state.Kind, status: state.Status, version: version, provenance: state.AcceptanceProvenance, revisionID: state.AcceptedRevisionID, publicationVersion: acceptedVersion, termsHash: state.AcceptedTermsHash, acceptedAt: state.AcceptedAt, changedAt: occurredAt}
	view, err := row.view()
	if err != nil {
		return err
	}
	if view.State != "known" {
		return unavailable("stored command state unsupported")
	}
	return nil
}

func decodeStoredCommandResult(raw []byte, result *application.Result) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return unavailable("stored command result schema invalid")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return unavailable("stored command result trailing data")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return unavailable("stored command result invalid")
	}
	for _, name := range []string{"campaignId", "publicationVersion", "interactionVersion", "changed"} {
		if value, ok := fields[name]; !ok || string(value) == "null" {
			return unavailable("stored command result required field missing")
		}
	}
	return nil
}
