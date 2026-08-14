// Package deployments is the Go port of packages/shared/src/web3/node.ts.
// It reads per-deployment JSON files from a directory, merges them by
// chainId (later timestamps win), and exposes contract address lookups
// for the markets/trading code paths.
//
// Format mirrors the TS reader exactly so the same files on disk produce
// identical merged results in both runtimes — that contract is the whole
// point of porting this rather than re-implementing it from scratch.
package deployments

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ContractInfo mirrors DeploymentContractInfo from shared/src/web3/types.ts.
// The TS reader accepts either a raw address string or an object with an
// `address` field plus arbitrary metadata; in Go we model the union with
// a custom UnmarshalJSON so callers always see a normalized struct.
type ContractInfo struct {
	Address string
	// Extra metadata (decimals, creationFee, etc.) is preserved so future
	// callers can opt in without another JSON pass.
	Extra map[string]any
	// scalar records that the source entry was a bare address string
	// ("0x…") rather than an object, so MarshalJSON round-trips the original
	// shape — the deployed NestJS contract-config serves the deployment file
	// as-is (bare strings for most contracts, objects for a few with metadata).
	scalar bool
}

// UnmarshalJSON accepts either "0xABC..." or {"address":"0xABC...", ...}.
func (c *ContractInfo) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		c.Address = s
		c.scalar = true
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if addr, ok := m["address"].(string); ok {
		c.Address = addr
	}
	delete(m, "address")
	c.Extra = m
	return nil
}

// MarshalJSON is the inverse of UnmarshalJSON: it round-trips the source shape so
// /api/contracts/config matches the deployed NestJS byte-for-byte. A bare-string
// entry marshals back to "0x…"; an object entry marshals to {"address":…} with
// the preserved Extra metadata flattened back to the top level. Without this the
// untagged exported fields would serialize as {"Address":…,"Extra":{…}}
// (capitalised + nested) — the divergence the 2026-06-08 live golden run found.
func (c ContractInfo) MarshalJSON() ([]byte, error) {
	if c.scalar && len(c.Extra) == 0 {
		return json.Marshal(c.Address)
	}
	out := make(map[string]any, len(c.Extra)+1)
	for k, v := range c.Extra {
		out[k] = v
	}
	out["address"] = c.Address
	return json.Marshal(out)
}

// ChainConfig is the merged view of one chain's contracts.
type ChainConfig struct {
	Network   string                  `json:"network,omitempty"`
	Deployer  string                  `json:"deployer,omitempty"`
	Timestamp string                  `json:"timestamp,omitempty"`
	Contracts map[string]ContractInfo `json:"contracts"`
	Sources   []string                `json:"sources"`
}

type rawDeploymentFile struct {
	Network    string                  `json:"network"`
	ChainID    *int                    `json:"chainId"`
	Deployer   string                  `json:"deployer"`
	Timestamp  string                  `json:"timestamp"`
	DeployedAt string                  `json:"deployedAt"`
	Contracts  map[string]ContractInfo `json:"contracts"`
}

type deploymentRecord struct {
	fileName    string
	chainID     int
	timestampMs int64
	data        rawDeploymentFile
}

// LoadMerged reads every *.json file in dir, parses, sorts by timestamp
// (ties broken by filename), then merges contracts per chainId. Files
// that fail to parse, files with no chainId, or files with chainId <= 0
// are silently skipped — matching the TS reader's tolerance for partial
// deployments during dev.
func LoadMerged(dir string) (map[int]ChainConfig, error) {
	if dir == "" {
		return map[int]ChainConfig{}, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[int]ChainConfig{}, nil
		}
		return nil, fmt.Errorf("read deployments dir: %w", err)
	}

	records := make([]deploymentRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		var data rawDeploymentFile
		if err := json.Unmarshal(raw, &data); err != nil {
			continue
		}
		if data.ChainID == nil || *data.ChainID <= 0 {
			continue
		}
		records = append(records, deploymentRecord{
			fileName:    entry.Name(),
			chainID:     *data.ChainID,
			timestampMs: resolveTimestampMs(fullPath, data),
			data:        data,
		})
	}

	sort.SliceStable(records, func(i, j int) bool {
		if records[i].timestampMs != records[j].timestampMs {
			return records[i].timestampMs < records[j].timestampMs
		}
		return records[i].fileName < records[j].fileName
	})

	merged := make(map[int]ChainConfig)
	for _, record := range records {
		current, ok := merged[record.chainID]
		if !ok {
			current = ChainConfig{Contracts: map[string]ContractInfo{}}
		}
		if record.data.Network != "" {
			current.Network = record.data.Network
		}
		if record.data.Deployer != "" {
			current.Deployer = record.data.Deployer
		}
		if record.data.Timestamp != "" {
			current.Timestamp = record.data.Timestamp
		} else if record.data.DeployedAt != "" {
			current.Timestamp = record.data.DeployedAt
		}
		for name, contract := range record.data.Contracts {
			current.Contracts[name] = contract
		}
		current.Sources = append(current.Sources, record.fileName)
		merged[record.chainID] = current
	}

	return merged, nil
}

// DiscoverDir resolves the deployments directory for every runtime
// (api / indexer / market-stream). Order:
//  1. CONTRACTS_DEPLOYMENTS_DIR — explicit operator wiring always wins
//     (Docker images set it to the baked-in copy).
//  2. The nearest ancestor of the working directory containing
//     packages/shared/deployments — the repo-checkout layout, so a plain
//     `go run ./cmd/api` (or `pnpm dev:api`) from anywhere inside the
//     repo serves live deployment addresses without manual guessing.
//
// Returns "" when neither resolves; callers keep their documented
// degraded behavior (empty markets, fallback swap quotes).
func DiscoverDir() string {
	if v := os.Getenv("CONTRACTS_DEPLOYMENTS_DIR"); v != "" {
		return v
	}
	return findAncestorDir(filepath.Join("packages", "shared", "deployments"))
}

// findAncestorDir walks from CWD to the filesystem root looking for
// relative as an existing directory.
func findAncestorDir(relative string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, relative)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// LookupAddress returns the first non-empty address found among the
// provided contract names. Mirrors getDeploymentAddress in the TS code:
// callers pass several aliases (e.g. "MockWETH", "weth") because the
// same chain may have either name across deployment files.
func LookupAddress(config ChainConfig, contractNames ...string) string {
	if config.Contracts == nil {
		return ""
	}
	for _, name := range contractNames {
		if entry, ok := config.Contracts[name]; ok && entry.Address != "" {
			return entry.Address
		}
	}
	return ""
}

func resolveTimestampMs(filePath string, data rawDeploymentFile) int64 {
	raw := data.Timestamp
	if raw == "" {
		raw = data.DeployedAt
	}
	if raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return parsed.UnixMilli()
		}
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			return parsed.UnixMilli()
		}
	}
	if info, err := os.Stat(filePath); err == nil {
		return info.ModTime().UnixMilli()
	}
	return 0
}
