package web3events

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"time"
)

// The owner-scoped workbench reads (TD-03 §3.1/§3.2). The legacy status stays
// the stored truth; the normalized axes are an explicit, lossy-only-towards-
// unknown conversion of it, so nothing here claims more than was observed.

const (
	defaultWorkbenchLimit = 20
	maxWorkbenchLimit     = 100
	maxWorkbenchCursor    = 1024
	workbenchCursorV1     = 1
)

// Limitations that hold for every row this slice can serve. They are part of
// the response so a client cannot present the axes as stronger evidence.
var workbenchLimitations = []string{
	"canonicalityNotVerified",
	"finalityHeuristicOnly",
	"indexingNotCorrelated",
	"businessResultNotVerified",
	"executionContextNotRecorded",
}

// Action filters accept exactly the derived action statuses.
var actionStatusFilters = map[string]struct{}{
	StatusPending: {}, StatusConfirmed: {}, StatusFailed: {},
	StatusReorged: {}, StatusDropped: {}, StatusReplaced: {},
}

type statusAxes struct {
	execution    string
	canonicality string
	finality     string
}

// legacyAxes is the conversion table of the task document §3.2. A mined row
// is "included" but its canonicality is unknown: nothing has checked the
// block against the canonical chain yet, and a reorg observation is the only
// fact that proves "orphaned".
var legacyAxes = map[string]statusAxes{
	StatusPending:   {"pending", "unknown", "unknown"},
	StatusConfirmed: {"succeeded", "unknown", "included"},
	StatusFailed:    {"reverted", "unknown", "included"},
	StatusReplaced:  {"replaced", "unknown", "unknown"},
	StatusDropped:   {"dropped", "unknown", "unknown"},
	StatusReorged:   {"unknown", "orphaned", "unknown"},
}

func axesOf(legacy string) statusAxes {
	if axes, ok := legacyAxes[legacy]; ok {
		return axes
	}
	return statusAxes{"unknown", "unknown", "unknown"}
}

// workflowOf is the application-flow axis. "completed" means the flow ended
// with an included, successful transaction; it is not chain finality and not
// proof of the business result (businessResultNotVerified).
func workflowOf(actionStatus string) string {
	switch actionStatus {
	case StatusPending:
		return "running"
	case StatusConfirmed:
		return "completed"
	default:
		return "needs_attention"
	}
}

// ActionRow is one stored action, already constrained to its owner.
type ActionRow struct {
	ID             string
	ChainID        int
	ClientActionID string
	ActionType     *string
	Status         string
	AttemptCount   int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// AttemptRow is one stored attempt of an owned action.
type AttemptRow struct {
	ID              int64
	AttemptNumber   int
	ChainID         int
	TxHash          string
	FromAddress     string
	ToAddress       *string
	ContractAddress *string
	Value           *string
	SenderNonce     *int64
	BlockNumber     int64
	BlockHash       *string
	Confirmations   int
	GasUsed         *int64
	GasPrice        *int64
	Status          string
	ReplacedByHash  *string
	FailureCode     *string
	SubmittedAt     *time.Time
	ConfirmedAt     *time.Time
	FinalizedAt     *time.Time
	LastCheckedAt   *time.Time
	CreatedAt       time.Time
	// Stored execution context (LG-03b); all nil = notRecorded.
	ContextState    *string
	ContextRevision *int64
	ContextReason   *string
}

type ActionView struct {
	ID              string  `json:"id"`
	ChainID         int     `json:"chainId"`
	ClientActionID  string  `json:"clientActionId"`
	ActionType      *string `json:"actionType"`
	LegacyStatus    string  `json:"legacyStatus"`
	ExecutionStatus string  `json:"executionStatus"`
	WorkflowStatus  string  `json:"workflowStatus"`
	AttemptCount    int     `json:"attemptCount"`
	CreatedAt       string  `json:"createdAt"`
	UpdatedAt       string  `json:"updatedAt"`
}

type AttemptObservation struct {
	BlockNumber   *string `json:"blockNumber"`
	BlockHash     *string `json:"blockHash"`
	Confirmations int     `json:"confirmations"`
	GasUsed       *string `json:"gasUsed"`
	GasPrice      *string `json:"gasPrice"`
	LastCheckedAt *string `json:"lastCheckedAt"`
}

// ExecutionContextView reads stored report evidence only: a later registry
// change must not rewrite what an already broadcast attempt was bound to.
type ExecutionContextView struct {
	State            string  `json:"state"`
	RegistryRevision *string `json:"registryRevision"`
	// Reason is serialized only in the executionContextV2 representation.
	Reason *string `json:"-"`
	v2     bool
}

// WorkbenchVariantContextV2 is the X-Workbench-Variants token a client sends
// when it decodes every LG-03b context state (§9).
const WorkbenchVariantContextV2 = "executionContextV2"

func (v ExecutionContextView) MarshalJSON() ([]byte, error) {
	if v.v2 {
		return json.Marshal(struct {
			State            string  `json:"state"`
			RegistryRevision *string `json:"registryRevision"`
			Reason           *string `json:"reason"`
		}{v.State, v.RegistryRevision, v.Reason})
	}
	return json.Marshal(struct {
		State            string  `json:"state"`
		RegistryRevision *string `json:"registryRevision"`
	}{v.State, v.RegistryRevision})
}

// contextView maps the stored state. The base representation (clients built
// before LG-03b) only knows notRecorded and recorded: every unresolved,
// mismatched or unbound state reads as notRecorded there — "no validated
// configuration recorded" — and never as a recorded revision.
func contextView(row AttemptRow, v2 bool) ExecutionContextView {
	out := ExecutionContextView{State: "notRecorded", v2: v2}
	if row.ContextState == nil {
		return out
	}
	if *row.ContextState == "recorded" && row.ContextRevision != nil {
		out.State, out.RegistryRevision = "recorded", int64String(row.ContextRevision)
		return out
	}
	if v2 {
		out.State, out.Reason = *row.ContextState, row.ContextReason
	}
	return out
}

type AttemptView struct {
	AttemptNumber     int                  `json:"attemptNumber"`
	ChainID           int                  `json:"chainId"`
	TxHash            string               `json:"txHash"`
	FromAddress       string               `json:"fromAddress"`
	ToAddress         *string              `json:"toAddress"`
	ContractAddress   *string              `json:"contractAddress"`
	Value             *string              `json:"value"`
	SenderNonce       *string              `json:"senderNonce"`
	LegacyStatus      string               `json:"legacyStatus"`
	ExecutionStatus   string               `json:"executionStatus"`
	Canonicality      string               `json:"canonicality"`
	Finality          string               `json:"finality"`
	IndexingStatus    *string              `json:"indexingStatus"`
	ReplacedByHash    *string              `json:"replacedByHash"`
	FailureCode       *string              `json:"failureCode"`
	Observation       AttemptObservation   `json:"observation"`
	SubmittedAt       *string              `json:"submittedAt"`
	ConfirmedAt       *string              `json:"confirmedAt"`
	LegacyFinalizedAt *string              `json:"legacyFinalizedAt"`
	CreatedAt         string               `json:"createdAt"`
	ExecutionContext  ExecutionContextView `json:"executionContext"`
}

type ActionListResponse struct {
	Items       []ActionView `json:"items"`
	NextCursor  *string      `json:"nextCursor"`
	HasMore     bool         `json:"hasMore"`
	HasUpdates  bool         `json:"hasUpdates"`
	AsOf        string       `json:"asOf"`
	Limitations []string     `json:"limitations"`
}

type ActionDetailResponse struct {
	Item        ActionView `json:"item"`
	AsOf        string     `json:"asOf"`
	Limitations []string   `json:"limitations"`
}

type AttemptListResponse struct {
	Items       []AttemptView `json:"items"`
	NextCursor  *string       `json:"nextCursor"`
	HasMore     bool          `json:"hasMore"`
	AsOf        string        `json:"asOf"`
	Limitations []string      `json:"limitations"`
}

func wireTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func wireTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := wireTime(*t)
	return &s
}

var decimalRE = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// decimalOrNull keeps only canonical decimal integers; anything else is an
// unknown value, never a coerced zero.
func decimalOrNull(v *string) *string {
	if v == nil || !decimalRE.MatchString(*v) {
		return nil
	}
	out := *v
	return &out
}

func int64String(v *int64) *string {
	if v == nil || *v < 0 {
		return nil
	}
	s := strconv.FormatInt(*v, 10)
	return &s
}

func NewActionView(row ActionRow) ActionView {
	return ActionView{
		ID: row.ID, ChainID: row.ChainID, ClientActionID: row.ClientActionID, ActionType: row.ActionType,
		LegacyStatus: row.Status, ExecutionStatus: axesOf(row.Status).execution, WorkflowStatus: workflowOf(row.Status),
		AttemptCount: row.AttemptCount, CreatedAt: wireTime(row.CreatedAt), UpdatedAt: wireTime(row.UpdatedAt),
	}
}

func NewAttemptView(row AttemptRow) AttemptView { return NewAttemptViewFor(row, false) }

// NewAttemptViewFor renders an attempt in the negotiated representation.
func NewAttemptViewFor(row AttemptRow, v2 bool) AttemptView {
	axes := axesOf(row.Status)
	observation := AttemptObservation{
		BlockHash: row.BlockHash, Confirmations: row.Confirmations,
		GasUsed: int64String(row.GasUsed), GasPrice: int64String(row.GasPrice), LastCheckedAt: wireTimePtr(row.LastCheckedAt),
	}
	// A pending submit stores block_number = 0 as a sentinel; it is not a block.
	if row.BlockNumber > 0 {
		observation.BlockNumber = int64String(&row.BlockNumber)
	}
	return AttemptView{
		AttemptNumber: row.AttemptNumber, ChainID: row.ChainID, TxHash: row.TxHash, FromAddress: row.FromAddress,
		ToAddress: row.ToAddress, ContractAddress: row.ContractAddress, Value: decimalOrNull(row.Value),
		SenderNonce: int64String(row.SenderNonce), LegacyStatus: row.Status, ExecutionStatus: axes.execution,
		Canonicality: axes.canonicality, Finality: axes.finality, ReplacedByHash: row.ReplacedByHash,
		FailureCode: row.FailureCode, Observation: observation, SubmittedAt: wireTimePtr(row.SubmittedAt),
		ConfirmedAt: wireTimePtr(row.ConfirmedAt), LegacyFinalizedAt: wireTimePtr(row.FinalizedAt),
		CreatedAt: wireTime(row.CreatedAt), ExecutionContext: contextView(row, v2),
	}
}

// Cursors carry no wallet: the scope is a digest of the verified owner, and
// every page query still constrains the owner itself. A cursor continues only
// the owner, chain, filter (and action) that issued it.
type actionCursor struct {
	V    int    `json:"v"`
	K    string `json:"k"`
	S    string `json:"s"`
	C    int    `json:"c"`
	F    string `json:"f"`
	HwAt string `json:"hwAt"`
	HwID string `json:"hwId"`
	AsOf string `json:"asOf"`
	At   string `json:"at"`
	ID   string `json:"id"`
}

type attemptCursor struct {
	V  int    `json:"v"`
	K  string `json:"k"`
	S  string `json:"s"`
	C  int    `json:"c"`
	A  string `json:"a"`
	N  int    `json:"n"`
	ID int64  `json:"id"`
}

// ActionKey is a keyset position in (createdAt DESC, id DESC).
type ActionKey struct {
	At time.Time
	ID string
}

// ActionCursorState is a decoded action cursor.
type ActionCursorState struct {
	After     ActionKey
	HighWater ActionKey
	AsOf      time.Time
}

// AttemptKey is a keyset position in (attemptNumber ASC, id ASC).
type AttemptKey struct {
	Number int
	ID     int64
}

var (
	ErrInvalidCursor = errors.New("INVALID_CURSOR")
	cursorRE         = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	actionIDRE       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	cursorTimeLayout = "2006-01-02T15:04:05.000Z"
)

func workbenchScope(owner string) string {
	sum := sha256.Sum256([]byte("web3-actions\x00" + owner))
	return hex.EncodeToString(sum[:])
}

func encodeCursor(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeStrict(raw string, into any) error {
	if len(raw) > maxWorkbenchCursor || !cursorRE.MatchString(raw) {
		return ErrInvalidCursor
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return ErrInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(into) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrInvalidCursor
	}
	return nil
}

func parseCursorTime(raw string) (time.Time, bool) {
	t, err := time.Parse(cursorTimeLayout, raw)
	return t, err == nil
}

func EncodeActionCursor(owner string, chainID int, filter string, state ActionCursorState) string {
	return encodeCursor(actionCursor{
		V: workbenchCursorV1, K: "actions", S: workbenchScope(owner), C: chainID, F: filter,
		HwAt: wireTime(state.HighWater.At), HwID: state.HighWater.ID, AsOf: wireTime(state.AsOf),
		At: wireTime(state.After.At), ID: state.After.ID,
	})
}

func DecodeActionCursor(raw, owner string, chainID int, filter string) (ActionCursorState, error) {
	var c actionCursor
	if err := decodeStrict(raw, &c); err != nil {
		return ActionCursorState{}, err
	}
	if c.V != workbenchCursorV1 || c.K != "actions" || c.S != workbenchScope(owner) || c.C != chainID || c.F != filter ||
		!actionIDRE.MatchString(c.ID) || !actionIDRE.MatchString(c.HwID) {
		return ActionCursorState{}, ErrInvalidCursor
	}
	at, okAt := parseCursorTime(c.At)
	hw, okHw := parseCursorTime(c.HwAt)
	asOf, okAsOf := parseCursorTime(c.AsOf)
	// The position can never be above the first page's high-water mark.
	if !okAt || !okHw || !okAsOf || at.After(hw) || (at.Equal(hw) && c.ID > c.HwID) {
		return ActionCursorState{}, ErrInvalidCursor
	}
	return ActionCursorState{After: ActionKey{at, c.ID}, HighWater: ActionKey{hw, c.HwID}, AsOf: asOf}, nil
}

func EncodeAttemptCursor(owner string, chainID int, actionID string, after AttemptKey) string {
	return encodeCursor(attemptCursor{V: workbenchCursorV1, K: "attempts", S: workbenchScope(owner), C: chainID, A: actionID, N: after.Number, ID: after.ID})
}

func DecodeAttemptCursor(raw, owner string, chainID int, actionID string) (AttemptKey, error) {
	var c attemptCursor
	if err := decodeStrict(raw, &c); err != nil {
		return AttemptKey{}, err
	}
	if c.V != workbenchCursorV1 || c.K != "attempts" || c.S != workbenchScope(owner) || c.C != chainID || c.A != actionID || c.N < 1 || c.ID < 1 {
		return AttemptKey{}, ErrInvalidCursor
	}
	return AttemptKey{Number: c.N, ID: c.ID}, nil
}
