package adminclient

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

// Intent statuses. "sending" is persisted before the request leaves; finding it
// on load means the process stopped mid-flight, which is treated as unknown.
// The full transition table lives with the classifiers in outcome.go.
const (
	StatusPlanned   = "planned"
	StatusSending   = "sending"
	StatusCommitted = "committed"
	StatusRejected  = "rejected"
	StatusUnknown   = "unknown"
	StatusAbandoned = "abandoned"
)

// Intent is one administrator command frozen before confirmation. Its
// clientCommandId is the server idempotency key, and Body is the exact request
// bytes; every retry reuses both, so a retry can only replay or perform this
// same reviewed command and never silently becomes a different one.
type Intent struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"clientCommandId"`
	CreatedAt     string `json:"createdAt"`
	API           string `json:"api"`
	Actor         string `json:"actor"`
	Operation     string `json:"operation"`
	CampaignID    string `json:"campaignId,omitempty"`
	Method        string `json:"method"`
	Path          string `json:"path"`
	Body          string `json:"body"`
	BodySHA256    string `json:"bodySha256"`
	Review        Review `json:"review"`
	Status        string `json:"status"`
	Retryable     bool   `json:"retryable,omitempty"`
	// Uncertain is sticky: once any send could have committed, later refusals
	// or failed reads cannot clear it. Only a verified ledger record, or a
	// refusal that proves the key absent and permanently inadmissible, does.
	Uncertain bool `json:"uncertain,omitempty"`
	// Settlement records how a terminal status was established.
	Settlement string `json:"settlement,omitempty"`
	// LedgerPayloadHash is the payload hash the server ledger must report for
	// this exact body; it is recomputed from Body whenever a result is checked.
	LedgerPayloadHash string                   `json:"ledgerPayloadHash,omitempty"`
	Attempts          []Attempt                `json:"attempts"`
	Result            *v2.AdminCommandResponse `json:"result,omitempty"`
}

// Review is what the operator confirmed. It is a snapshot of server reads plus
// local predictions; none of it is authoritative once the command is sent.
type Review struct {
	Reason                     string        `json:"reason"`
	ExpectedPublicationVersion *string       `json:"expectedPublicationVersion,omitempty"`
	Current                    *CurrentState `json:"current,omitempty"`
	Candidate                  *Candidate    `json:"candidate,omitempty"`
	Impact                     []string      `json:"impact"`
	Warnings                   []string      `json:"warnings"`
}

type CurrentState struct {
	AsOf               string  `json:"asOf"`
	PublicationVersion string  `json:"publicationVersion"`
	Mode               string  `json:"mode"`
	EffectivePhase     string  `json:"effectivePhase"`
	ActiveRevisionID   *string `json:"activeRevisionId"`
	ActiveTermsHash    *string `json:"activeMaterialTermsHash"`
	MaterialTermsLock  string  `json:"materialTermsLock"`
}

// Candidate hashes come from the server preview for existing revisions. For a
// new revision they are computed locally and marked so, because only the
// server's recomputation after validation is authoritative.
type Candidate struct {
	RevisionID        string `json:"revisionId,omitempty"`
	MaterialTermsHash string `json:"materialTermsHash"`
	ContentHash       string `json:"contentHash"`
	HashSource        string `json:"hashSource"`
	ContentState      string `json:"contentState,omitempty"`
	PreviouslyActive  *bool  `json:"previouslyPublished,omitempty"`
	StartAt           string `json:"startAt,omitempty"`
	EndAt             string `json:"endAt,omitempty"`
	TitleEn           string `json:"titleEn,omitempty"`
}

type Attempt struct {
	At         string `json:"at"`
	Kind       string `json:"kind"`
	RequestID  string `json:"requestId,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Code       string `json:"code,omitempty"`
	Outcome    string `json:"outcome"`
	Note       string `json:"note,omitempty"`
}

var intentIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// NewCommandID returns a canonical UUIDv7. The server accepts an absent key
// only within its first-submission window measured from the embedded time.
func NewCommandID(now time.Time) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	millis := now.UnixMilli()
	for i := 5; i >= 0; i-- {
		raw[i] = byte(millis)
		millis >>= 8
	}
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := hex.EncodeToString(raw[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], nil
}

func digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func (h Home) intentPath(id string) string { return filepath.Join(h.Dir, "intents", id+".json") }

func (h Home) SaveIntent(intent *Intent) error {
	if !intentIDPattern.MatchString(intent.ID) {
		return errors.New("intent ID must be a canonical UUIDv7")
	}
	return writePrivate(h.intentPath(intent.ID), intent)
}

// LoadIntent refuses a record whose body no longer matches its digest, so a
// hand-edited payload can never be sent under the original reviewed key.
func (h Home) LoadIntent(id string) (*Intent, error) {
	if !intentIDPattern.MatchString(id) {
		return nil, errors.New("intent ID must be a canonical UUIDv7")
	}
	var intent Intent
	if err := readPrivate(h.intentPath(id), &intent); err != nil {
		return nil, err
	}
	if intent.ID != id || intent.SchemaVersion != 1 || digest(intent.Body) != intent.BodySHA256 {
		return nil, fmt.Errorf("intent %s failed integrity check; do not send it", id)
	}
	return &intent, nil
}

func (h Home) ListIntents() ([]*Intent, error) {
	entries, err := os.ReadDir(filepath.Join(h.Dir, "intents"))
	if err != nil {
		return nil, err
	}
	var result []*Intent
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !intentIDPattern.MatchString(id) {
			continue
		}
		intent, err := h.LoadIntent(id)
		if err != nil {
			return nil, err
		}
		result = append(result, intent)
	}
	// UUIDv7 keys sort by creation time.
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// LockIntent takes a non-blocking exclusive flock so two terminals cannot send
// or reconcile the same intent concurrently. The kernel releases it on exit.
func (h Home) LockIntent(id string) (func(), error) {
	if !intentIDPattern.MatchString(id) {
		return nil, errors.New("intent ID must be a canonical UUIDv7")
	}
	file, err := os.OpenFile(filepath.Join(h.Dir, "intents", id+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("intent %s is in use by another campaign-admin process", id)
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }, nil
}

// frozenCommand rebuilds, from the stored request bytes, the exact command the
// server ledger hashes. The mapping is the server's own (application package),
// so the client verifies results against the canonical definition, not a copy.
func (i *Intent) frozenCommand() (application.Command, error) {
	raw := []byte(i.Body)
	switch i.Operation {
	case "create":
		var body v2.CreateCommandInput
		if err := v2.DecodeContract("CreateCommandInput", raw, &body); err != nil {
			return application.Command{}, err
		}
		return application.CreateCommand(body), nil
	case "revision":
		var body v2.RevisionCommandInput
		if err := v2.DecodeContract("RevisionCommandInput", raw, &body); err != nil {
			return application.Command{}, err
		}
		return application.RevisionCommand(i.CampaignID, body)
	default:
		var body v2.PublicationCommandInput
		if err := v2.DecodeContract("PublicationCommandInput", raw, &body); err != nil {
			return application.Command{}, err
		}
		if body.Operation != i.Operation {
			return application.Command{}, errors.New("frozen body operation differs from the intent")
		}
		return application.PublicationCommand(i.CampaignID, body)
	}
}

func (i *Intent) ledgerPayloadHash() (string, error) {
	command, err := i.frozenCommand()
	if err != nil {
		return "", err
	}
	return application.PayloadHash(command)
}

// sentBefore reports whether any earlier attempt transmitted the body. A first
// transmission can never legitimately be answered with a replayed result.
func (i *Intent) sentBefore() bool {
	for _, attempt := range i.Attempts {
		if attempt.Kind == "send" {
			return true
		}
	}
	return false
}

// normalize maps on-disk states to the in-memory model: a persisted "sending"
// means the process stopped mid-flight, and older records marked unknown
// predate the explicit uncertainty flag.
func (i *Intent) normalize() {
	if i.Status == StatusSending || i.Status == StatusUnknown {
		i.Status, i.Uncertain = StatusUnknown, true
	}
}
