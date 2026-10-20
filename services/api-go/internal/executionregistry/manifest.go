// Package executionregistry owns the ExecutionRegistrySnapshot authority
// (docs/claude-web3-lg03b-task-2026-10-14.md): reviewed declarations and the
// interface manifest, immutable sealed snapshots, CAS activation, and the
// per-operation bindings that /trade intents, reviews and reports are checked
// against.
package executionregistry

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
)

// interfaces.generated.json is a verbatim copy of
// packages/shared/contracts/execution-interfaces.json, kept in sync by
// scripts/strict/generate-execution-registry-contracts.py --check.
//
//go:embed interfaces.generated.json
var manifestJSON []byte

// Interface is one semantic interface ID of the manifest (§3.1).
type Interface struct {
	ID         string
	Signature  string
	Selector   string // 0x + 8 hex
	Params     []string
	ArgRoles   []string
	roleOffset map[string]int
}

// Manifest is the set of interfaces this build supports.
type Manifest struct {
	Digest     string
	Interfaces map[string]Interface
}

var signaturePattern = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)\(((?:address|bool|uint256)(?:,(?:address|bool|uint256))*)?\)$`)

// ParseManifest validates a manifest document and derives selectors.
func ParseManifest(raw []byte) (Manifest, error) {
	var doc struct {
		SchemaVersion int    `json:"schemaVersion"`
		Description   string `json:"description"`
		Interfaces    map[string]struct {
			Signature string   `json:"signature"`
			ArgRoles  []string `json:"argRoles"`
		} `json:"interfaces"`
	}
	if err := decodeStrict(raw, &doc); err != nil {
		return Manifest{}, fmt.Errorf("executionregistry: manifest: %w", err)
	}
	if doc.SchemaVersion != 1 || len(doc.Interfaces) == 0 {
		return Manifest{}, fmt.Errorf("executionregistry: manifest: unsupported schemaVersion or empty")
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return Manifest{}, err
	}
	digest, err := Digest(generic)
	if err != nil {
		return Manifest{}, err
	}
	out := Manifest{Digest: digest, Interfaces: map[string]Interface{}}
	for id, item := range doc.Interfaces {
		if !interfaceIDPattern.MatchString(id) {
			return Manifest{}, fmt.Errorf("executionregistry: manifest: bad interface id %q", id)
		}
		match := signaturePattern.FindStringSubmatch(item.Signature)
		if match == nil {
			return Manifest{}, fmt.Errorf("executionregistry: manifest: unsupported signature %q", item.Signature)
		}
		var params []string
		if match[2] != "" {
			params = strings.Split(match[2], ",")
		}
		if len(params) != len(item.ArgRoles) {
			return Manifest{}, fmt.Errorf("executionregistry: manifest: %s roles do not name each parameter", id)
		}
		offsets := map[string]int{}
		for i, role := range item.ArgRoles {
			if _, dup := offsets[role]; dup {
				return Manifest{}, fmt.Errorf("executionregistry: manifest: %s repeats role %q", id, role)
			}
			offsets[role] = i
		}
		out.Interfaces[id] = Interface{ID: id, Signature: item.Signature, Selector: "0x" + hex.EncodeToString(abi.Selector(item.Signature)),
			Params: params, ArgRoles: item.ArgRoles, roleOffset: offsets}
	}
	return out, nil
}

// BuiltinManifest is the manifest this binary was built with.
func BuiltinManifest() Manifest {
	m, err := ParseManifest(manifestJSON)
	if err != nil {
		panic(err)
	}
	return m
}

var interfaceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-zA-Z][a-zA-Z0-9]*)+\.v[1-9][0-9]*$`)

// DecodedCall is calldata decoded under one interface: each role maps to its
// canonical word value (addresses lowercase 0x+40 hex, bools "true"/"false",
// uints decimal).
type DecodedCall map[string]string

// Decode accepts only the exact static shape of the interface: selector, one
// canonical 32-byte word per parameter, nothing more.
func (i Interface) Decode(data string) (DecodedCall, bool) {
	data = strings.ToLower(strings.TrimSpace(data))
	if !strings.HasPrefix(data, "0x") || len(data) != 2+8+64*len(i.Params) || data[:10] != i.Selector {
		return nil, false
	}
	raw, err := hex.DecodeString(data[10:])
	if err != nil {
		return nil, false
	}
	out := DecodedCall{}
	for n, param := range i.Params {
		word := raw[n*32 : (n+1)*32]
		switch param {
		case "address":
			for _, b := range word[:12] {
				if b != 0 {
					return nil, false
				}
			}
			out[i.ArgRoles[n]] = "0x" + hex.EncodeToString(word[12:])
		case "bool":
			for _, b := range word[:31] {
				if b != 0 {
					return nil, false
				}
			}
			if word[31] > 1 {
				return nil, false
			}
			out[i.ArgRoles[n]] = map[byte]string{0: "false", 1: "true"}[word[31]]
		default:
			out[i.ArgRoles[n]] = decimalWord(word)
		}
	}
	return out, true
}
