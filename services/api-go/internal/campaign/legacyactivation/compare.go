package legacyactivation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// Difference classes a review may accept (protocol §1.5). Any other
// difference is a mapping defect and always rolls the activation back.
const (
	ClassVisibility       = "visibility"
	ClassParticipantCount = "participantCount"
	ClassWalletState      = "walletState"
	ClassField            = "field"
)

var acceptable = map[string]bool{ClassVisibility: true, ClassParticipantCount: true, ClassWalletState: true}

// Difference is one field whose legacy answer changed. Values appear only as
// digest prefixes; wallets only as references.
type Difference struct {
	Class     string `json:"class"`
	Field     string `json:"field"`
	WalletRef string `json:"walletRef,omitempty"`
	Before    string `json:"before"`
	After     string `json:"after"`
}

// Comparison is the record of one activation's before/after comparison.
type Comparison struct {
	Instant     string       `json:"instant"`
	Wallets     int          `json:"wallets"`
	Differences []Difference `json:"differences"`
	Classes     []string     `json:"classes"`
	Accepted    []string     `json:"accepted"`
}

// answer is one legacy API answer: absent, a refusal code, or the DTO fields.
type answer struct {
	found   bool
	failure string
	fields  map[string]json.RawMessage
}

func answerOf(value campaign.Campaign, err error) (answer, error) {
	if errors.Is(err, campaign.ErrNotFound) {
		return answer{}, nil
	}
	var domain *v2.DomainError
	if errors.As(err, &domain) && domain.Status < 500 {
		return answer{failure: domain.Code}, nil
	}
	if err != nil {
		return answer{}, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return answer{}, err
	}
	result := answer{found: true}
	if err = json.Unmarshal(raw, &result.fields); err != nil {
		return answer{}, err
	}
	return result, nil
}

func valueRef(raw json.RawMessage, present bool) string {
	if !present {
		return "absent"
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// walletFields differ by caller; every other field must not.
var walletFields = map[string]bool{"isParticipating": true, "hasReminder": true}

type comparator struct {
	differences []Difference
	seen        map[string]bool
}

func (c *comparator) add(d Difference) {
	key := d.Class + "\x00" + d.Field + "\x00" + d.WalletRef + "\x00" + d.Before + "\x00" + d.After
	if !c.seen[key] {
		c.seen[key] = true
		c.differences = append(c.differences, d)
	}
}

// compare records how the answer for wallet ("" for anonymous) changed.
func (c *comparator) compare(before, after answer, wallet string) {
	ref := ""
	if wallet != "" {
		ref = legacyimport.WalletRef(wallet)
	}
	switch {
	case before.failure != "" || after.failure != "":
		c.add(Difference{Class: ClassField, Field: "answer", WalletRef: ref, Before: orNone(before.failure), After: orNone(after.failure)})
		return
	case before.found != after.found:
		c.add(Difference{Class: ClassVisibility, Field: "visible", Before: boolRef(before.found), After: boolRef(after.found)})
		return
	case !before.found:
		return
	}
	keys := map[string]bool{}
	for key := range before.fields {
		keys[key] = true
	}
	for key := range after.fields {
		keys[key] = true
	}
	for key := range keys {
		b, inBefore := before.fields[key]
		a, inAfter := after.fields[key]
		if inBefore == inAfter && bytes.Equal(b, a) {
			continue
		}
		d := Difference{Class: ClassField, Field: key, Before: valueRef(b, inBefore), After: valueRef(a, inAfter)}
		switch {
		case key == "participantCount":
			d.Class = ClassParticipantCount
		case walletFields[key] && wallet != "":
			d.Class, d.WalletRef = ClassWalletState, ref
		}
		c.add(d)
	}
}

func orNone(code string) string {
	if code == "" {
		return "none"
	}
	return code
}

func boolRef(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// result orders the differences and lists their classes.
func (c *comparator) result(instant string, wallets int, accepted []string) Comparison {
	sort.Slice(c.differences, func(i, j int) bool {
		a, b := c.differences[i], c.differences[j]
		return a.Class+a.Field+a.WalletRef+a.Before+a.After < b.Class+b.Field+b.WalletRef+b.Before+b.After
	})
	classes := map[string]bool{}
	for _, d := range c.differences {
		classes[d.Class] = true
	}
	out := Comparison{Instant: instant, Wallets: wallets, Differences: c.differences, Classes: []string{}, Accepted: accepted}
	if out.Differences == nil {
		out.Differences = []Difference{}
	}
	if out.Accepted == nil {
		out.Accepted = []string{}
	}
	for class := range classes {
		out.Classes = append(out.Classes, class)
	}
	sort.Strings(out.Classes)
	return out
}

// passes reports whether every difference class was accepted by the review.
func (c Comparison) passes() bool {
	accepted := map[string]bool{}
	for _, class := range c.Accepted {
		accepted[class] = acceptable[class]
	}
	for _, class := range c.Classes {
		if !accepted[class] {
			return false
		}
	}
	return true
}
