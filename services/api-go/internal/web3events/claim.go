package web3events

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Execution-context claims on a transaction report
// (docs/claude-web3-lg03b-task-2026-10-14.md §6.1). NormalizeClaim is total:
// any input yields absent, a normalized claim, or malformed with a diagnostic
// drawn from a fixed vocabulary. Nothing here can fail the report.

const maxClaimBytes = 2 << 10

type ClaimKind int

const (
	ClaimAbsent ClaimKind = iota
	ClaimNormalized
	ClaimMalformed
)

// NormalizedClaim holds only validated, bounded values.
type NormalizedClaim struct {
	Mode             string // bound | unboundExit
	RegistryRevision int64  // bound only
	Operation        string
	Market           string
	BindingDigest    string // bound only
	Reason           string // unboundExit only
	ConsumerID       string
	ManifestDigest   string
}

type ClaimResult struct {
	Kind       ClaimKind
	Claim      NormalizedClaim
	Digest     string // sha256 of the canonical normalized claim
	Diagnostic string // malformed only, ≤ 160 chars, fixed vocabulary
}

var (
	claimRevisionRE = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	claimMarketRE   = regexp.MustCompile(`^[A-Z0-9]{1,16}-[A-Z0-9]{1,16}$`)
	claimDigestRE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	claimConsumerRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	claimOperations = map[string]bool{"trade.approveCollateral": true, "trade.openPosition": true, "trade.closePosition": true}
	unboundReasons  = map[string]bool{"authorityUnavailable": true, "noActiveRevision": true, "targetNotActive": true, "consumerIncompatible": true}
)

type diagnostics []string

func (d *diagnostics) add(field, rule string) {
	if len(*d) < 4 {
		*d = append(*d, field+":"+rule)
	}
}

// NormalizeClaim validates the raw executionContext field of a report body.
func NormalizeClaim(raw json.RawMessage) ClaimResult {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ClaimResult{Kind: ClaimAbsent}
	}
	malformed := func(d diagnostics) ClaimResult {
		return ClaimResult{Kind: ClaimMalformed, Diagnostic: strings.Join(d, ";")}
	}
	var compact bytes.Buffer
	if json.Compact(&compact, trimmed) != nil {
		return malformed(diagnostics{"body:type"})
	}
	if compact.Len() > maxClaimBytes {
		return malformed(diagnostics{"body:size"})
	}
	if duplicateKeys(compact.Bytes()) {
		return malformed(diagnostics{"body:duplicate"})
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(compact.Bytes(), &doc) != nil {
		return malformed(diagnostics{"body:type"})
	}
	var d diagnostics
	str := func(key string) (string, bool) {
		value, ok := doc[key]
		if !ok {
			d.add(key, "missing")
			return "", false
		}
		var s string
		if json.Unmarshal(value, &s) != nil {
			d.add(key, "type")
			return "", false
		}
		return s, true
	}
	mode, _ := str("mode")
	var c NormalizedClaim
	allowed := map[string]bool{"mode": true, "operation": true, "market": true, "consumer": true}
	switch mode {
	case "bound":
		allowed["registryRevision"], allowed["bindingDigest"] = true, true
		c.Mode = mode
	case "unboundExit":
		allowed["reason"] = true
		c.Mode = mode
	case "":
	default:
		d.add("mode", "pattern")
	}
	keys := make([]string, 0, len(doc))
	for key := range doc {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !allowed[key] && c.Mode != "" {
			d.add("unknownField", "pattern")
		}
	}
	if op, ok := str("operation"); ok {
		if !claimOperations[op] || (c.Mode == "unboundExit" && op != "trade.closePosition") {
			d.add("operation", "pattern")
		}
		c.Operation = op
	}
	if market, ok := str("market"); ok {
		if !claimMarketRE.MatchString(market) {
			d.add("market", "pattern")
		}
		c.Market = market
	}
	switch c.Mode {
	case "bound":
		if rev, ok := str("registryRevision"); ok {
			if !claimRevisionRE.MatchString(rev) {
				d.add("registryRevision", "pattern")
			} else if v, err := strconv.ParseInt(rev, 10, 64); err != nil {
				d.add("registryRevision", "range")
			} else {
				c.RegistryRevision = v
			}
		}
		if digest, ok := str("bindingDigest"); ok {
			if !claimDigestRE.MatchString(digest) {
				d.add("bindingDigest", "pattern")
			}
			c.BindingDigest = digest
		}
	case "unboundExit":
		if reason, ok := str("reason"); ok {
			if !unboundReasons[reason] {
				d.add("reason", "pattern")
			}
			c.Reason = reason
		}
	}
	if consumerRaw, ok := doc["consumer"]; !ok {
		d.add("consumer", "missing")
	} else {
		var consumer map[string]json.RawMessage
		if json.Unmarshal(consumerRaw, &consumer) != nil {
			d.add("consumer", "type")
		} else {
			for key := range consumer {
				if key != "id" && key != "manifestDigest" {
					d.add("unknownField", "pattern")
				}
			}
			var id, manifest string
			if json.Unmarshal(consumer["id"], &id) != nil || !claimConsumerRE.MatchString(id) {
				d.add("consumer.id", "pattern")
			}
			if json.Unmarshal(consumer["manifestDigest"], &manifest) != nil || !claimDigestRE.MatchString(manifest) {
				d.add("consumer.manifestDigest", "pattern")
			}
			c.ConsumerID, c.ManifestDigest = id, manifest
		}
	}
	if len(d) > 0 || c.Mode == "" {
		if len(d) == 0 {
			d.add("mode", "missing")
		}
		return malformed(d)
	}
	return ClaimResult{Kind: ClaimNormalized, Claim: c, Digest: claimDigest(c)}
}

// claimDigest hashes the canonical normalized claim: exactly the §6.1 fields,
// keys sorted, compact, integers as decimal strings.
func claimDigest(c NormalizedClaim) string {
	sum := sha256.Sum256(CanonicalClaim(c))
	return hex.EncodeToString(sum[:])
}

// CanonicalClaim is the exact byte string claimDigest hashes (for the
// documented size bounds and cross-language tests).
func CanonicalClaim(c NormalizedClaim) []byte { return canonicalJSON(claimMap(c)) }

func claimMap(c NormalizedClaim) map[string]any {
	value := map[string]any{"mode": c.Mode, "operation": c.Operation, "market": c.Market,
		"consumer": map[string]any{"id": c.ConsumerID, "manifestDigest": c.ManifestDigest}}
	if c.Mode == "bound" {
		value["registryRevision"] = strconv.FormatInt(c.RegistryRevision, 10)
		value["bindingDigest"] = c.BindingDigest
	} else {
		value["reason"] = c.Reason
	}
	return value
}

func canonicalJSON(value any) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// duplicateKeys walks the token stream; encoding/json would silently keep the
// last duplicate.
func duplicateKeys(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	type frame struct {
		object bool
		keys   map[string]bool
		expect bool // next string token is a key
	}
	var stack []*frame
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true
		}
		var top *frame
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch t := token.(type) {
		case json.Delim:
			switch t {
			case '{':
				if top != nil && top.object {
					top.expect = true
				}
				stack = append(stack, &frame{object: true, keys: map[string]bool{}, expect: true})
			case '[':
				if top != nil && top.object {
					top.expect = true
				}
				stack = append(stack, &frame{})
			case '}', ']':
				stack = stack[:len(stack)-1]
				if len(stack) > 0 && stack[len(stack)-1].object {
					stack[len(stack)-1].expect = true
				}
			}
		case string:
			if top != nil && top.object && top.expect {
				if top.keys[t] {
					return true
				}
				top.keys[t] = true
				top.expect = false
				continue
			}
			if top != nil && top.object {
				top.expect = true
			}
		default:
			if top != nil && top.object {
				top.expect = true
			}
		}
	}
}
