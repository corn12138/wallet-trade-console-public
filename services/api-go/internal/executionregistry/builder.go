package executionregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
)

// Declarations is the reviewed declarations document (§4.1).
type Declarations struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Description   string                      `json:"description"`
	Chains        map[string]ChainDeclaration `json:"chains"`
}

type ChainDeclaration struct {
	Entries map[string]struct {
		Interfaces []string `json:"interfaces"`
	} `json:"entries"`
	Markets map[string]struct {
		PositionManager    string   `json:"positionManager"`
		IndexToken         string   `json:"indexToken"`
		CollateralToken    string   `json:"collateralToken"`
		IndexDecimals      int      `json:"indexDecimals"`
		CollateralDecimals int      `json:"collateralDecimals"`
		PricePrecision     int      `json:"pricePrecision"`
		Components         []string `json:"components"`
	} `json:"markets"`
	Operations map[string]struct {
		Status string `json:"status"`
	} `json:"operations"`
}

// CodeObservation is eth_getCode at a pinned block.
type CodeObservation struct {
	ChainID     int    `json:"chainId"`
	Address     string `json:"address"`
	CodeHash    string `json:"codeHash"`
	BlockNumber uint64 `json:"blockNumber"`
	BlockHash   string `json:"blockHash"`
}

// CodeReader reads deployed code through the chain's RPC (D3).
type CodeReader interface {
	CodeAt(ctx context.Context, chainID int, address string) (CodeObservation, error)
}

// BuildInput names the reviewed sources of one revision.
type BuildInput struct {
	Declarations   []byte
	DeploymentsDir string
	ArtifactsDir   string
	Code           CodeReader
	Manifest       Manifest
}

var (
	addressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	namePattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)
	marketPattern  = regexp.MustCompile(`^[A-Z0-9]{1,16}-[A-Z0-9]{1,16}$`)
	zeroAddress    = "0x0000000000000000000000000000000000000000"
	emptyCodeHash  = "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
)

// ErrBuild wraps every refusal of the builder.
var ErrBuild = errors.New("executionregistry: build refused")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBuild, fmt.Sprintf(format, args...))
}

// ParseDeclarations decodes and validates the declarations against a manifest.
func ParseDeclarations(raw []byte, manifest Manifest) (Declarations, string, error) {
	var d Declarations
	if err := decodeStrict(raw, &d); err != nil {
		return Declarations{}, "", refuse("declarations: %v", err)
	}
	if d.SchemaVersion != 1 || len(d.Chains) == 0 {
		return Declarations{}, "", refuse("declarations: unsupported schemaVersion or no chains")
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return Declarations{}, "", refuse("declarations: %v", err)
	}
	digest, err := Digest(generic)
	if err != nil {
		return Declarations{}, "", err
	}
	for key, chain := range d.Chains {
		chainID, err := strconv.Atoi(key)
		if err != nil || chainID <= 0 || strconv.Itoa(chainID) != key {
			return Declarations{}, "", refuse("declarations: bad chain id %q", key)
		}
		for name, entry := range chain.Entries {
			if !namePattern.MatchString(name) {
				return Declarations{}, "", refuse("declarations: bad entry name %q", name)
			}
			if !sort.StringsAreSorted(entry.Interfaces) {
				return Declarations{}, "", refuse("%s.%s: interfaces must be sorted", key, name)
			}
			for i, id := range entry.Interfaces {
				if _, ok := manifest.Interfaces[id]; !ok {
					return Declarations{}, "", refuse("%s.%s: interface %q is not in this build's manifest", key, name, id)
				}
				if i > 0 && entry.Interfaces[i-1] == id {
					return Declarations{}, "", refuse("%s.%s: duplicate interface", key, name)
				}
			}
		}
		for op, item := range chain.Operations {
			if !IsOperation(op) || (item.Status != "enabled" && item.Status != "disabled") {
				return Declarations{}, "", refuse("%s: bad operation %q", key, op)
			}
			if op == OpClosePosition && item.Status != "enabled" && len(chain.Markets) > 0 {
				return Declarations{}, "", refuse("%s: an exit cannot be disabled while markets exist", key)
			}
		}
		if len(chain.Markets) > 0 {
			if _, ok := chain.Operations[OpClosePosition]; !ok {
				return Declarations{}, "", refuse("%s: trade.closePosition must be declared while markets exist", key)
			}
		}
		for symbol, m := range chain.Markets {
			if !marketPattern.MatchString(symbol) {
				return Declarations{}, "", refuse("%s: bad market %q", key, symbol)
			}
			for _, ref := range append([]string{m.PositionManager, m.IndexToken, m.CollateralToken}, m.Components...) {
				if _, ok := chain.Entries[ref]; !ok {
					return Declarations{}, "", refuse("%s.%s: %q is not a declared entry", key, symbol, ref)
				}
			}
			if !sortedUnique(m.Components) || m.PricePrecision != 30 || m.CollateralDecimals < 0 || m.CollateralDecimals > 30 || m.IndexDecimals < 0 || m.IndexDecimals > 36 {
				return Declarations{}, "", refuse("%s.%s: bad market parameters", key, symbol)
			}
		}
	}
	return d, digest, nil
}

// Build joins declarations, deployment files, artifact ABIs and on-chain code
// into a snapshot. Any doubt refuses the whole revision.
func Build(ctx context.Context, in BuildInput) (Snapshot, error) {
	decl, declDigest, err := ParseDeclarations(in.Declarations, in.Manifest)
	if err != nil {
		return Snapshot{}, err
	}
	merged, sources, err := deployments.LoadMergedWithSources(in.DeploymentsDir)
	if err != nil {
		return Snapshot{}, refuse("deployments: %v", err)
	}
	if in.Code == nil {
		return Snapshot{}, refuse("no code reader: every entry needs an observed code hash")
	}
	snapshot := Snapshot{SchemaVersion: 1, ManifestDigest: in.Manifest.Digest, DeclarationsDigest: declDigest,
		SourceSummary: map[string]any{"deploymentsDir": filepath.Base(in.DeploymentsDir)}}
	chainKeys := make([]string, 0, len(decl.Chains))
	for key := range decl.Chains {
		chainKeys = append(chainKeys, key)
	}
	sort.Strings(chainKeys)
	files := map[string]any{}
	for _, key := range chainKeys {
		chainID, _ := strconv.Atoi(key)
		chain := decl.Chains[key]
		cfg, ok := merged[chainID]
		if !ok {
			return Snapshot{}, refuse("chain %d has no deployment record", chainID)
		}
		entries := map[string]Entry{}
		names := make([]string, 0, len(chain.Entries))
		for name := range chain.Entries {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			info, ok := cfg.Contracts[name]
			if !ok || !addressPattern.MatchString(info.Address) || strings.EqualFold(info.Address, zeroAddress) {
				return Snapshot{}, refuse("chain %d: %s has no usable deployed address", chainID, name)
			}
			source := sources[chainID][name]
			abiHash, err := artifactCheck(in.ArtifactsDir, name, chain.Entries[name].Interfaces, in.Manifest)
			if err != nil {
				return Snapshot{}, err
			}
			address := strings.ToLower(info.Address)
			code, err := in.Code.CodeAt(ctx, chainID, address)
			if err != nil {
				return Snapshot{}, refuse("chain %d: %s code unreadable: %v", chainID, name, err)
			}
			if code.ChainID != chainID || !strings.EqualFold(code.Address, address) || !codeHashPattern.MatchString(code.CodeHash) || code.CodeHash == emptyCodeHash || !codeHashPattern.MatchString(code.BlockHash) {
				return Snapshot{}, refuse("chain %d: %s has no deployed code or an unusable observation", chainID, name)
			}
			e := Entry{ChainID: chainID, Name: name, Address: address, DeploymentFile: source.File, DeploymentTimestamp: source.Timestamp,
				ABIHash: abiHash, Interfaces: append([]string{}, chain.Entries[name].Interfaces...), CodeHash: code.CodeHash,
				CodeBlockNumber: code.BlockNumber, CodeBlockHash: code.BlockHash}
			if e.DeploymentFile == "" || e.DeploymentTimestamp == "" {
				return Snapshot{}, refuse("chain %d: %s has no deployment identity", chainID, name)
			}
			files[e.DeploymentFile] = e.DeploymentTimestamp
			e.EntryDigest = EntryDigest(e)
			entries[name] = e
			snapshot.Entries = append(snapshot.Entries, e)
		}
		symbols := make([]string, 0, len(chain.Markets))
		for symbol := range chain.Markets {
			symbols = append(symbols, symbol)
		}
		sort.Strings(symbols)
		for _, symbol := range symbols {
			d := chain.Markets[symbol]
			m := Market{ChainID: chainID, Symbol: symbol, PositionManager: d.PositionManager, IndexToken: d.IndexToken, CollateralToken: d.CollateralToken,
				IndexDecimals: d.IndexDecimals, CollateralDecimals: d.CollateralDecimals, PricePrecision: d.PricePrecision, Components: append([]string{}, d.Components...)}
			m.MarketDigest = MarketDigest(m, entries)
			snapshot.Markets = append(snapshot.Markets, m)
			fields := map[string]string{"positionManager": m.PositionManager, "indexToken": m.IndexToken, "collateralToken": m.CollateralToken}
			ops := make([]string, 0, len(chain.Operations))
			for op := range chain.Operations {
				ops = append(ops, op)
			}
			sort.Strings(ops)
			for _, op := range ops {
				shape := operationShapes[op]
				target := fields[shape.target]
				if !contains(entries[target].Interfaces, shape.interfaceID) {
					return Snapshot{}, refuse("chain %d %s: %s needs %s on %s", chainID, symbol, op, shape.interfaceID, target)
				}
				roles := map[string]string{}
				for role, field := range shape.roles {
					roles[role] = fields[field]
				}
				b := Binding{ChainID: chainID, Operation: op, Market: symbol, Status: chain.Operations[op].Status, InterfaceID: shape.interfaceID, Target: target, Roles: roles}
				b.BindingDigest = BindingDigest(b, entries, m)
				snapshot.Bindings = append(snapshot.Bindings, b)
			}
		}
	}
	snapshot.SourceSummary["files"] = files
	snapshot.ContentDigest = ContentDigest(snapshot)
	if err := ValidateSnapshot(snapshot, in.Manifest); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

var codeHashPattern = regexp.MustCompile(`^0x[0-9a-f]{64}$`)

func contains(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}

// artifactCheck finds the unique artifact for a contract name, requires every
// declared interface's exact function signature in its ABI, and returns the
// canonical ABI hash (recorded for audit, not used for selection).
func artifactCheck(root, name string, interfaces []string, manifest Manifest) (string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == name+".json" {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return "", refuse("artifacts: %v", err)
	}
	if len(found) != 1 {
		return "", refuse("artifact for %s: found %d candidates, need exactly one", name, len(found))
	}
	raw, err := os.ReadFile(found[0])
	if err != nil {
		return "", refuse("artifact for %s: %v", name, err)
	}
	var artifact struct {
		ABI json.RawMessage `json:"abi"`
	}
	if err := json.Unmarshal(raw, &artifact); err != nil || len(artifact.ABI) == 0 {
		return "", refuse("artifact for %s has no ABI", name)
	}
	var items []struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		Inputs []struct {
			Type string `json:"type"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(artifact.ABI, &items); err != nil {
		return "", refuse("artifact for %s has a malformed ABI", name)
	}
	signatures := map[string]bool{}
	for _, item := range items {
		if item.Type != "function" {
			continue
		}
		types := make([]string, len(item.Inputs))
		for i, input := range item.Inputs {
			types[i] = input.Type
		}
		signatures[item.Name+"("+strings.Join(types, ",")+")"] = true
	}
	for _, id := range interfaces {
		if !signatures[manifest.Interfaces[id].Signature] {
			return "", refuse("artifact for %s lacks %s (%s)", name, manifest.Interfaces[id].Signature, id)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(string(artifact.ABI)))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return "", refuse("artifact for %s has a malformed ABI", name)
	}
	return Digest(generic)
}
