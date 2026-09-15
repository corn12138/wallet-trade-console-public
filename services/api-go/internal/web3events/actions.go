package web3events

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Attempt statuses. The first three predate this model and are what
// portfolio/activity already read; the rest are terminal states the reconciler
// can reach, and every one of them is protected from being overwritten by a
// later client re-report.
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
	StatusFailed    = "failed"
	StatusReplaced  = "replaced"
	StatusDropped   = "dropped"
	StatusReorged   = "reorged"
)

// terminalStatuses may not be downgraded by a client re-report. 'pending' is
// deliberately absent: it is the only non-terminal state.
var terminalStatuses = map[string]struct{}{
	StatusConfirmed: {}, StatusFailed: {},
	StatusReplaced: {}, StatusDropped: {}, StatusReorged: {},
}

func IsTerminalStatus(status string) bool {
	_, ok := terminalStatuses[status]
	return ok
}

// actionStatusRank orders the attempt statuses when an action has several
// attempts in conflicting states. A live attempt outranks a dead one, so an
// action whose first attempt confirmed-then-reorged while a second is still
// pending reads as pending, not reorged — the intent has not finished.
var actionStatusRank = map[string]int{
	StatusConfirmed: 6,
	StatusPending:   5,
	StatusFailed:    4,
	StatusReorged:   3,
	StatusDropped:   2,
	StatusReplaced:  1,
}

// DeriveActionStatus collapses an action's attempts into its own status.
func DeriveActionStatus(attemptStatuses []string) string {
	best, bestRank := StatusPending, -1
	for _, status := range attemptStatuses {
		rank, known := actionStatusRank[status]
		if !known {
			continue
		}
		if rank > bestRank {
			best, bestRank = status, rank
		}
	}
	return best
}

// Client action identity.
//
// The two identity sources live in disjoint namespaces so they cannot collide:
// "cli:" for a key the caller supplied, "srv:" for one the server derived. A
// caller-supplied key matching ^srv: is rejected, which is what makes
// CLIENT_ACTION_ID_REUSED unreachable for a caller that supplies no key —
// a structural guarantee rather than a hope about traffic shape.
const (
	clientActionIDPrefix  = "cli:"
	serverActionIDPrefix  = "srv:"
	maxClientActionIDLen  = 128
	maxClientFingerprint  = 256
	maxStoredActionIDSize = 160
)

var (
	clientActionIDRE = regexp.MustCompile(`^[A-Za-z0-9._:\-]{8,128}$`)

	// ErrClientActionIDInvalid is a 400: the caller sent something that is not a
	// usable key.
	ErrClientActionIDInvalid = errors.New("clientActionId must be 8-128 chars of [A-Za-z0-9._:-] and must not start with \"srv:\"")
	// ErrClientFingerprintTooLong is a 400.
	ErrClientFingerprintTooLong = fmt.Errorf("clientFingerprint must be at most %d bytes", maxClientFingerprint)
	// ErrClientActionIDReused is the stable 409 the contract requires: the same
	// key was reused for a materially different request.
	ErrClientActionIDReused = errors.New("CLIENT_ACTION_ID_REUSED")
	// ErrActionAttemptBound is the 409 for re-parenting: this transaction is
	// already bound to a different action and an attempt belongs to exactly one
	// action, forever.
	ErrActionAttemptBound = errors.New("ACTION_ATTEMPT_BOUND")
)

// NormalizeClientActionID validates a caller-supplied key and moves it into the
// client namespace. An empty key is not an error — it means "derive one".
func NormalizeClientActionID(raw string) (string, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false, nil
	}
	if !clientActionIDRE.MatchString(value) || strings.HasPrefix(value, serverActionIDPrefix) {
		return "", false, ErrClientActionIDInvalid
	}
	return clientActionIDPrefix + value, true, nil
}

// DeriveServerActionID builds the key for a caller that supplied none.
//
// It prefers the sender's NONCE, because a nonce reuse IS a replacement: a
// speed-up or cancel arrives under a new hash with the same nonce, and keying on
// the nonce is what lands it as a second attempt of the same action instead of a
// second action. Only when the node did not report a nonce does it fall back to
// the hash, and such an action is single-attempt by construction — replacement
// detection is unreachable for it, which is recorded rather than guessed.
func DeriveServerActionID(chainID int, senderNonce *uint64, txHash string) string {
	if senderNonce != nil {
		return serverActionIDPrefix + "nonce:" + strconv.Itoa(chainID) + ":" + strconv.FormatUint(*senderNonce, 10)
	}
	return serverActionIDPrefix + "tx:" + strconv.Itoa(chainID) + ":" + strings.ToLower(strings.TrimSpace(txHash))
}

// fingerprintInput is marshalled in field-declaration order, which encoding/json
// guarantees, so the same request always hashes the same way. Addresses are
// lowercased and absent values are explicit nulls so "" and absent cannot
// collide.
type fingerprintInput struct {
	ChainID           int     `json:"chainId"`
	Owner             string  `json:"owner"`
	ActionType        *string `json:"actionType"`
	ToAddress         *string `json:"toAddress"`
	ContractAddress   *string `json:"contractAddress"`
	Value             *string `json:"value"`
	ClientFingerprint *string `json:"clientFingerprint"`
}

// ComputeRequestFingerprint hashes the canonical reviewed request. It is
// computed server-side from normalized fields; a client-supplied fingerprint is
// only one more input, never the identity itself.
func ComputeRequestFingerprint(
	chainID int,
	owner string,
	actionType, toAddress, contractAddress, value, clientFingerprint *string,
) (string, error) {
	payload, err := json.Marshal(fingerprintInput{
		ChainID:           chainID,
		Owner:             strings.ToLower(strings.TrimSpace(owner)),
		ActionType:        normalizeFingerprintField(actionType, false),
		ToAddress:         normalizeFingerprintField(toAddress, true),
		ContractAddress:   normalizeFingerprintField(contractAddress, true),
		Value:             normalizeFingerprintField(value, false),
		ClientFingerprint: normalizeFingerprintField(clientFingerprint, false),
	})
	if err != nil {
		return "", fmt.Errorf("web3events: canonical fingerprint: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// normalizeFingerprintField collapses an empty string to absent so a caller
// sending "" and a caller sending nothing produce the same identity.
func normalizeFingerprintField(value *string, lower bool) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	if lower {
		trimmed = strings.ToLower(trimmed)
	}
	return &trimmed
}

// ValidateClientFingerprint bounds the one free-form field a caller controls
// before it reaches the hash or the database.
func ValidateClientFingerprint(value *string) error {
	if value == nil {
		return nil
	}
	if len(*value) > maxClientFingerprint {
		return ErrClientFingerprintTooLong
	}
	return nil
}

// Failure codes are a closed server-side enum. A provider's error text is never
// stored or returned; only one of these plus a truncated summary.
const (
	FailureRPCUnavailable   = "rpc_unavailable"
	FailureUnsupportedChain = "unsupported_chain"
	FailureSenderMismatch   = "sender_mismatch"
	FailureEvidenceMismatch = "evidence_mismatch"
	FailureNotFound         = "not_found"
	FailureReceiptPending   = "receipt_pending"
)

// maxFailureDetail keeps the stored summary short enough that no provider body
// can be smuggled into it wholesale.
const maxFailureDetail = 200

// SanitizeFailureDetail reduces an internal error to a code-tagged summary. It
// never returns err.Error() verbatim and the result is never serialized to a
// public route.
func SanitizeFailureDetail(code string, err error) *string {
	if code == "" {
		return nil
	}
	detail := code
	if err != nil {
		detail = code + ": " + firstLine(err.Error())
	}
	if len(detail) > maxFailureDetail {
		detail = detail[:maxFailureDetail]
	}
	return &detail
}

func firstLine(text string) string {
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		return text[:idx]
	}
	return text
}
