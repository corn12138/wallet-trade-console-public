package executionregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// Operations of /trade (§4.2) and how each binds.
const (
	OpApproveCollateral = "trade.approveCollateral"
	OpOpenPosition      = "trade.openPosition"
	OpClosePosition     = "trade.closePosition"
)

type operationShape struct {
	interfaceID string
	target      string            // market field naming the target entry
	roles       map[string]string // interface arg role -> market field naming the bound entry
}

var operationShapes = map[string]operationShape{
	OpApproveCollateral: {"erc20.approve.v1", "collateralToken", map[string]string{"spender": "positionManager"}},
	OpOpenPosition:      {"perp.positionManager.open.v1", "positionManager", map[string]string{"indexToken": "indexToken", "collateralToken": "collateralToken"}},
	OpClosePosition:     {"perp.positionManager.close.v1", "positionManager", map[string]string{"indexToken": "indexToken", "collateralToken": "collateralToken"}},
}

// IsOperation reports whether op is one of the three /trade operations.
func IsOperation(op string) bool { _, ok := operationShapes[op]; return ok }

// Entry is one registered contract of a revision (§4.2).
type Entry struct {
	ChainID             int      `json:"chainId"`
	Name                string   `json:"name"`
	Address             string   `json:"address"`
	DeploymentFile      string   `json:"deploymentFile"`
	DeploymentTimestamp string   `json:"deploymentTimestamp"`
	ABIHash             string   `json:"abiHash"`
	Interfaces          []string `json:"interfaces"`
	CodeHash            string   `json:"codeHash"`
	CodeBlockNumber     uint64   `json:"codeBlockNumber"`
	CodeBlockHash       string   `json:"codeBlockHash"`
	EntryDigest         string   `json:"entryDigest"`
}

// Market is a bound market relation.
type Market struct {
	ChainID            int      `json:"chainId"`
	Symbol             string   `json:"symbol"`
	PositionManager    string   `json:"positionManager"`
	IndexToken         string   `json:"indexToken"`
	CollateralToken    string   `json:"collateralToken"`
	IndexDecimals      int      `json:"indexDecimals"`
	CollateralDecimals int      `json:"collateralDecimals"`
	PricePrecision     int      `json:"pricePrecision"`
	Components         []string `json:"components"`
	MarketDigest       string   `json:"marketDigest"`
}

// Binding is one operation × market (§4.2). Roles maps an interface arg role
// to the name of the entry it must equal.
type Binding struct {
	ChainID       int               `json:"chainId"`
	Operation     string            `json:"operation"`
	Market        string            `json:"market"`
	Status        string            `json:"status"`
	InterfaceID   string            `json:"interfaceId"`
	Target        string            `json:"target"`
	Roles         map[string]string `json:"roles"`
	BindingDigest string            `json:"bindingDigest"`
}

// Snapshot is the full content of one revision.
type Snapshot struct {
	SchemaVersion      int
	ManifestDigest     string
	DeclarationsDigest string
	Entries            []Entry
	Markets            []Market
	Bindings           []Binding
	ContentDigest      string
	SourceSummary      map[string]any
}

func chainKey(chainID int) string { return strconv.Itoa(chainID) }

// EntryDigest binds what makes an entry semantically the same contract
// (§4.2): abiHash and code observation coordinates are deliberately excluded.
func EntryDigest(e Entry) string {
	interfaces := append([]string{}, e.Interfaces...)
	sort.Strings(interfaces)
	list := make([]any, len(interfaces))
	for i, id := range interfaces {
		list[i] = id
	}
	return mustDigest(map[string]any{
		"chainId": chainKey(e.ChainID), "name": e.Name, "address": e.Address,
		"deploymentIdentity": map[string]any{"file": e.DeploymentFile, "timestamp": e.DeploymentTimestamp},
		"codeHash":           e.CodeHash, "interfaces": list,
	})
}

// MarketDigest covers every input buildTradingSteps consumes for this market.
func MarketDigest(m Market, entries map[string]Entry) string {
	components := append([]string{}, m.Components...)
	sort.Strings(components)
	list := make([]any, len(components))
	for i, name := range components {
		list[i] = entries[name].EntryDigest
	}
	return mustDigest(map[string]any{
		"chainId": chainKey(m.ChainID), "symbol": m.Symbol,
		"positionManager": entries[m.PositionManager].EntryDigest, "indexToken": entries[m.IndexToken].EntryDigest,
		"collateralToken": entries[m.CollateralToken].EntryDigest,
		"indexDecimals":   strconv.Itoa(m.IndexDecimals), "collateralDecimals": strconv.Itoa(m.CollateralDecimals),
		"pricePrecision": strconv.Itoa(m.PricePrecision), "components": list,
	})
}

// BindingDigest is the unit of review-to-wallet binding.
func BindingDigest(b Binding, entries map[string]Entry, market Market) string {
	roles := map[string]any{}
	for role, name := range b.Roles {
		roles[role] = entries[name].EntryDigest
	}
	return mustDigest(map[string]any{
		"schema": "execution-binding/v1", "chainId": chainKey(b.ChainID), "operation": b.Operation, "market": b.Market,
		"interfaceId": b.InterfaceID, "target": entries[b.Target].EntryDigest, "roles": roles, "marketDigest": market.MarketDigest,
	})
}

// ContentDigest is the line form the seal trigger recomputes in SQL.
func ContentDigest(s Snapshot) string {
	lines := make([]string, 0, len(s.Entries)+len(s.Markets)+len(s.Bindings))
	for _, e := range s.Entries {
		lines = append(lines, "e:"+e.EntryDigest)
	}
	for _, m := range s.Markets {
		lines = append(lines, "m:"+m.MarketDigest)
	}
	for _, b := range s.Bindings {
		lines = append(lines, "b:"+b.BindingDigest)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte("execution-registry-content/v1\n" + strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// entriesByChain indexes entries of one chain by name.
func (s Snapshot) entriesByChain(chainID int) map[string]Entry {
	out := map[string]Entry{}
	for _, e := range s.Entries {
		if e.ChainID == chainID {
			out[e.Name] = e
		}
	}
	return out
}

// Binding finds the binding for (chain, operation, market).
func (s Snapshot) Binding(chainID int, operation, market string) (Binding, bool) {
	for _, b := range s.Bindings {
		if b.ChainID == chainID && b.Operation == operation && b.Market == market {
			return b, true
		}
	}
	return Binding{}, false
}

// ChainIDs lists the chains present in the snapshot.
func (s Snapshot) ChainIDs() []int {
	seen := map[int]bool{}
	var out []int
	for _, e := range s.Entries {
		if !seen[e.ChainID] {
			seen[e.ChainID] = true
			out = append(out, e.ChainID)
		}
	}
	sort.Ints(out)
	return out
}
