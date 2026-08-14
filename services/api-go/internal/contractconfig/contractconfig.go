// Package contractconfig is the Go port of legacy NestJS contract-config/.
//
// Returns deployed contract addresses (per chain) + ABIs (per contract
// name). NestJS sourced ABIs from packages/shared/src/web3/abis/*.ts;
// we load them at runtime from foundry artifact JSONs under a
// configurable directory (defaults to contracts/artifacts/), keeping
// the api-go binary independent of the TS package.
package contractconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/go-chi/chi/v5"
)

// DefaultChainID matches the NestJS service constant.
const DefaultChainID = 11155111

// ConfigResponse mirrors GET /contracts/config.
type ConfigResponse struct {
	DefaultChainID int                                `json:"defaultChainId"`
	Chains         map[string]deployments.ChainConfig `json:"chains"`
	ABIs           map[string]json.RawMessage         `json:"abis"`
}

// ChainResponse mirrors GET /contracts/config/:chainId.
type ChainResponse struct {
	ChainID int `json:"chainId"`
	deployments.ChainConfig
}

// ABIResponse mirrors GET /contracts/abi/:contractName.
type ABIResponse struct {
	ContractName string          `json:"contractName"`
	ABI          json.RawMessage `json:"abi"`
}

// DeploymentsLoader is the contract the service needs from the markets
// deployments loader (already exists). Kept as an interface so callers
// can pass a stub for tests.
type DeploymentsLoader interface {
	Load() (map[int]deployments.ChainConfig, error)
}

// Service holds the loaders + the in-memory ABI map.
type Service struct {
	loader DeploymentsLoader
	abis   map[string]json.RawMessage
}

// NewService builds the service. Pass nil loader for the degraded
// shape (empty chains map). abisDir empty → empty ABI map.
func NewService(loader DeploymentsLoader, abisDir string) *Service {
	abis, err := LoadABIs(abisDir)
	if err != nil {
		slog.Warn("contract-config: ABI loader failed", "err", err, "dir", abisDir)
		abis = map[string]json.RawMessage{}
	}
	return &Service{loader: loader, abis: abis}
}

// LoadABIs walks the foundry artifact tree under root and returns a
// map of contract-name → raw ABI JSON. Empty or non-existent dir is
// not an error — returns an empty map.
func LoadABIs(root string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if root == "" {
		return out, nil
	}
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort scan; skip problem entries
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".json") {
			return nil
		}
		// Foundry/hardhat-3 artifacts have a `contractName` + `abi` field.
		// Build-info files (large `output.json`) also live in this tree —
		// skip them to keep startup fast.
		if strings.HasSuffix(path, "output.json") || strings.Contains(path, "build-info") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var artifact struct {
			ContractName string          `json:"contractName"`
			ABI          json.RawMessage `json:"abi"`
		}
		if err := json.Unmarshal(body, &artifact); err != nil {
			return nil
		}
		if artifact.ContractName == "" || len(artifact.ABI) == 0 {
			return nil
		}
		// First write wins — interfaces sometimes share a name with
		// implementations; the artifact path order is deterministic so
		// this stays stable across runs.
		if _, exists := out[artifact.ContractName]; !exists {
			out[artifact.ContractName] = artifact.ABI
		}
		return nil
	})
	if walkErr != nil {
		return out, fmt.Errorf("walk artifacts dir: %w", walkErr)
	}
	return out, nil
}

// GetConfig returns the full payload (chains + ABIs + defaultChainId).
func (s *Service) GetConfig() (ConfigResponse, error) {
	chains := map[string]deployments.ChainConfig{}
	if s.loader != nil {
		raw, err := s.loader.Load()
		if err != nil {
			return ConfigResponse{}, err
		}
		for id, cfg := range raw {
			chains[strconv.Itoa(id)] = cfg
		}
	}
	return ConfigResponse{
		DefaultChainID: DefaultChainID,
		Chains:         chains,
		ABIs:           s.abis,
	}, nil
}

// GetConfigByChain returns {chainId, ...chain-config} or a fallback
// stub with empty contracts when the chain isn't found (matches
// NestJS).
func (s *Service) GetConfigByChain(chainID int) (ChainResponse, error) {
	if s.loader == nil {
		return ChainResponse{ChainID: chainID, ChainConfig: deployments.ChainConfig{Contracts: map[string]deployments.ContractInfo{}}}, nil
	}
	raw, err := s.loader.Load()
	if err != nil {
		return ChainResponse{}, err
	}
	cfg, ok := raw[chainID]
	if !ok {
		return ChainResponse{ChainID: chainID, ChainConfig: deployments.ChainConfig{Contracts: map[string]deployments.ContractInfo{}}}, nil
	}
	return ChainResponse{ChainID: chainID, ChainConfig: cfg}, nil
}

// GetABI returns the ABI for a contract name, or ErrABINotFound.
var ErrABINotFound = errors.New("ABI not found")

func (s *Service) GetABI(name string) (ABIResponse, error) {
	abi, ok := s.abis[name]
	if !ok {
		return ABIResponse{}, ErrABINotFound
	}
	return ABIResponse{ContractName: name, ABI: abi}, nil
}

// Router mounts the 3 GETs under /api/contracts.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/config", svc.handleConfig)
	r.Get("/config/{chainId}", svc.handleConfigByChain)
	r.Get("/abi/{contractName}", svc.handleABI)
	return r
}

func (s *Service) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.GetConfig()
	if err != nil {
		slog.ErrorContext(r.Context(), "contract-config getConfig failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load contract config")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Service) handleConfigByChain(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(chi.URLParam(r, "chainId"))
	chainID, err := strconv.Atoi(raw)
	if err != nil || chainID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid chainId")
		return
	}
	cfg, err := s.GetConfigByChain(chainID)
	if err != nil {
		slog.ErrorContext(r.Context(), "contract-config getConfigByChain failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load contract config")
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Service) handleABI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(chi.URLParam(r, "contractName"))
	if name == "" {
		writeError(w, http.StatusBadRequest, "contractName is required")
		return
	}
	abi, err := s.GetABI(name)
	if errors.Is(err, ErrABINotFound) {
		writeError(w, http.StatusNotFound, "ABI not found: "+name)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load ABI")
		return
	}
	writeJSON(w, http.StatusOK, abi)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("contract-config response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statusCode": status,
		"message":    message,
	})
}
