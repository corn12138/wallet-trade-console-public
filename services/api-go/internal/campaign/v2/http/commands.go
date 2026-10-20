package campaignhttp

import (
	"net/http"
	"strconv"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/go-chi/chi/v5"
)

func (h handler) create(w http.ResponseWriter, r *http.Request) {
	var body v2.CreateCommandInput
	if err := decodeBody(w, r, "CreateCommandInput", &body); err != nil {
		writeFailure(w, r, err)
		return
	}
	h.execute(w, r, application.CreateCommand(body))
}
func (h handler) revision(w http.ResponseWriter, r *http.Request) {
	var body v2.RevisionCommandInput
	if err := decodeBody(w, r, "RevisionCommandInput", &body); err != nil {
		writeFailure(w, r, err)
		return
	}
	c, err := application.RevisionCommand(chi.URLParam(r, "id"), body)
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	h.execute(w, r, c)
}
func (h handler) publication(w http.ResponseWriter, r *http.Request) {
	var body v2.PublicationCommandInput
	if err := decodeBody(w, r, "PublicationCommandInput", &body); err != nil {
		writeFailure(w, r, err)
		return
	}
	c, err := application.PublicationCommand(chi.URLParam(r, "id"), body)
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	h.execute(w, r, c)
}
func (h handler) interact(operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := application.Command{Operation: operation, CampaignID: chi.URLParam(r, "id")}
		var state string
		if operation == "join" || operation == "set-reminder" {
			var body v2.PositiveInteractionInput
			if err := decodeBody(w, r, "PositiveInteractionInput", &body); err != nil {
				writeFailure(w, r, err)
				return
			}
			pub, err := version(body.ExpectedPublicationVersion)
			if err != nil {
				writeFailure(w, r, err)
				return
			}
			c.ClientCommandID, c.ExpectedPublication, state = body.ClientCommandID, pub, body.ExpectedStateVersion
		} else {
			var body v2.CancelInteractionInput
			if err := decodeBody(w, r, "CancelInteractionInput", &body); err != nil {
				writeFailure(w, r, err)
				return
			}
			c.ClientCommandID, state = body.ClientCommandID, body.ExpectedStateVersion
		}
		expected, err := version(state)
		if err != nil {
			writeFailure(w, r, err)
			return
		}
		c.ExpectedState = expected
		h.execute(w, r, c)
	}
}
func (h handler) execute(w http.ResponseWriter, r *http.Request, command application.Command) {
	if h.Commands == nil || h.Queries == nil {
		writeFailure(w, r, unavailable("CAMPAIGN_UNAVAILABLE"))
		return
	}
	command.RequestID = requestID(r)
	actor := auth.AddressFromContext(r.Context())
	outcome, err := h.Commands.Execute(r.Context(), actor, command)
	if err != nil {
		writeFailure(w, r, err)
		return
	}
	target := command.CampaignID
	if command.Operation == "create" {
		target = "campaigns"
	}
	// The committed ledger is the recovery authority. If this read fails, the
	// client must reconcile the same command key before starting another intent.
	value, err := h.Queries.Command(r.Context(), actor, target, command.Operation, command.ClientCommandID)
	if err != nil {
		writeFailure(w, r, unavailable("CAMPAIGN_RESULT_UNAVAILABLE"))
		return
	}
	value.Replayed = outcome.Replayed
	status := http.StatusOK
	if command.Operation == "create" && !outcome.Replayed {
		status = http.StatusCreated
	}
	writeCommand(w, r, command.Operation, value, status)
}

// writeCommand keeps participant routes on CommandResponse (deployed Web
// clients decode it strictly) and gives administrator routes the versioned
// AdminCommandResponse, whose ledger identity lets an operator client prove the
// result belongs to its own key, actor, operation, target and payload.
func writeCommand(w http.ResponseWriter, r *http.Request, operation string, value application.CommandResponse, status int) {
	if interactionOperation(operation) {
		writeContract(w, r, "CommandResponse", commandResponse(value), status)
		return
	}
	id := value.Identity
	identity := v2.AdminCommandIdentity{ClientCommandID: id.ClientCommandID, Actor: id.Actor, Operation: id.Operation, TargetID: id.TargetID, PayloadHash: id.PayloadHash, CommittedAt: id.CommittedAt.UTC().Format(time.RFC3339Nano)}
	writeContract(w, r, "AdminCommandResponse", v2.AdminCommandResponse{SchemaVersion: int64(value.SchemaVersion), ServerNow: value.ServerNow, AsOf: value.AsOf, Command: identity, Original: commandResult(value.Original), Current: commandResult(value.Current), Replayed: value.Replayed}, status)
}

func interactionOperation(operation string) bool {
	switch operation {
	case "join", "leave", "set-reminder", "remove-reminder":
		return true
	}
	return false
}
func commandResponse(value application.CommandResponse) v2.CommandResponse {
	return v2.CommandResponse{SchemaVersion: int64(value.SchemaVersion), ServerNow: value.ServerNow, AsOf: value.AsOf, Original: commandResult(value.Original), Current: commandResult(value.Current), Replayed: value.Replayed}
}
func commandResult(value application.Result) v2.CommandResult {
	result := v2.CommandResult{CampaignID: value.CampaignID, PublicationVersion: strconv.FormatUint(value.PublicationVersion, 10), InteractionVersion: strconv.FormatUint(value.InteractionVersion, 10), Changed: value.Changed}
	if value.RevisionID != "" {
		result.RevisionID = &value.RevisionID
	}
	if state := value.State; state != nil {
		result.State = &v2.CommandState{Kind: state.Kind, Status: state.Status, StateVersion: state.StateVersion, AcceptanceProvenance: state.AcceptanceProvenance, AcceptedRevisionID: state.AcceptedRevisionID, AcceptedPublicationVersion: state.AcceptedPublicationVersion, AcceptedTermsHash: state.AcceptedTermsHash}
		if state.AcceptedAt != nil {
			at := state.AcceptedAt.UTC().Format(time.RFC3339Nano)
			result.State.AcceptedAt = &at
		}
	}
	return result
}
