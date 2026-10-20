package executionregistry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
)

const repoRoot = "../../../../"

var (
	realDeployments  = repoRoot + "packages/shared/deployments"
	fixtureArtifacts = "testdata/artifacts"
	realDecl         = repoRoot + "packages/shared/execution-registry/declarations.json"
)

// fakeCode returns a deterministic non-empty code hash per address, unless
// the address is listed as empty or overridden.
type fakeCode struct {
	empty    map[string]bool
	override map[string]string
	fail     error
}

func (f fakeCode) CodeAt(_ context.Context, chainID int, address string) (CodeObservation, error) {
	if f.fail != nil {
		return CodeObservation{}, f.fail
	}
	hash := KeccakHex([]byte("code:" + address))
	if f.empty[address] {
		hash = emptyCodeHash
	}
	if h, ok := f.override[address]; ok {
		hash = h
	}
	return CodeObservation{ChainID: chainID, Address: address, CodeHash: hash, BlockNumber: 7, BlockHash: KeccakHex([]byte("block"))}, nil
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func buildWith(t *testing.T, decl []byte, deploymentsDir string, code CodeReader) (Snapshot, error) {
	t.Helper()
	return Build(context.Background(), BuildInput{Declarations: decl, DeploymentsDir: deploymentsDir, ArtifactsDir: fixtureArtifacts,
		Code: code, Manifest: BuiltinManifest()})
}

func mustBuild(t *testing.T, decl []byte, deploymentsDir string) Snapshot {
	t.Helper()
	s, err := buildWith(t, decl, deploymentsDir, fakeCode{})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return s
}

// copyDeployments copies the real deployment records and lets a test edit one
// contract address in one file.
func copyDeployments(t *testing.T, edits map[string]map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files, _ := filepath.Glob(filepath.Join(realDeployments, "*.json"))
	for _, file := range files {
		raw := readFile(t, file)
		name := filepath.Base(file)
		if change, ok := edits[name]; ok {
			var doc map[string]any
			_ = json.Unmarshal(raw, &doc)
			contracts := doc["contracts"].(map[string]any)
			for contract, address := range change {
				if address == "" {
					delete(contracts, contract)
					continue
				}
				if obj, ok := contracts[contract].(map[string]any); ok {
					obj["address"] = address
				} else {
					contracts[contract] = address
				}
			}
			raw, _ = json.Marshal(doc)
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func editDecl(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(readFile(t, realDecl), &doc); err != nil {
		t.Fatal(err)
	}
	edit(doc)
	raw, _ := json.Marshal(doc)
	return raw
}

func chainDecl(doc map[string]any, chain string) map[string]any {
	return doc["chains"].(map[string]any)[chain].(map[string]any)
}

// memStore is an in-memory Store with an active pointer over sealed revisions.
type memStore struct {
	revisions map[int64]Snapshot
	active    int64
	version   int64
	err       error
}

func (m *memStore) ActiveRevision(context.Context) (Active, error) {
	if m.err != nil {
		return Active{}, m.err
	}
	if m.active == 0 {
		return Active{}, ErrNoActiveRevision
	}
	return Active{Revision: m.active, ActivationVersion: m.version, Snapshot: m.revisions[m.active]}, nil
}

func (m *memStore) Revision(_ context.Context, revision int64) (Snapshot, error) {
	if m.err != nil {
		return Snapshot{}, m.err
	}
	s, ok := m.revisions[revision]
	if !ok {
		return Snapshot{}, ErrUnknownRevision
	}
	return s, nil
}

var errDown = errors.New("connection refused")

// calldata encodes an interface call with addresses/uints/bools as words.
func calldata(t *testing.T, iface Interface, args ...string) string {
	t.Helper()
	parts := make([][]byte, len(args))
	for i, a := range args {
		switch iface.Params[i] {
		case "address":
			b, err := abi.EncodeAddress(a)
			if err != nil {
				t.Fatal(err)
			}
			parts[i] = b
		case "bool":
			w := make([]byte, 32)
			if a == "true" {
				w[31] = 1
			}
			parts[i] = w
		default:
			w := make([]byte, 32)
			w[31] = byte(len(a))
			parts[i] = w
		}
	}
	return strings.ToLower(abi.EncodeCall(iface.Signature, parts...))
}
