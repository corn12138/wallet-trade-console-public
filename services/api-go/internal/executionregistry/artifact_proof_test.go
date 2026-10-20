package executionregistry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func changedArtifacts(t *testing.T, change func([]map[string]any) []map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	files, err := filepath.Glob(filepath.Join(fixtureArtifacts, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw := readFile(t, file)
		if filepath.Base(file) == "PositionManager.json" {
			var artifact struct {
				ABI []map[string]any `json:"abi"`
			}
			if err = json.Unmarshal(raw, &artifact); err != nil {
				t.Fatal(err)
			}
			artifact.ABI = change(artifact.ABI)
			raw, err = json.Marshal(artifact)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err = os.WriteFile(filepath.Join(dir, filepath.Base(file)), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func TestArtifactCompatibilityUsesDeclaredSignatureInsteadOfWholeABIHash(t *testing.T) {
	original := mustBuild(t, readFile(t, realDecl), realDeployments)
	for _, mode := range []string{"compatible-addition", "missing-declared-signature"} {
		t.Run(mode, func(t *testing.T) {
			dir := changedArtifacts(t, func(items []map[string]any) []map[string]any {
				if mode == "compatible-addition" {
					return append(items, map[string]any{"type": "function", "name": "compatibleAuditView", "inputs": []any{}, "outputs": []any{}, "stateMutability": "view"})
				}
				var kept []map[string]any
				for _, item := range items {
					if item["name"] != "openPosition" {
						kept = append(kept, item)
					}
				}
				return kept
			})
			got, err := Build(context.Background(), BuildInput{Declarations: readFile(t, realDecl), DeploymentsDir: realDeployments, ArtifactsDir: dir, Code: fakeCode{}, Manifest: BuiltinManifest()})
			if mode == "missing-declared-signature" {
				if !errors.Is(err, ErrBuild) {
					t.Fatalf("missing declared signature accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			before, after := original.entriesByChain(sepolia)["PositionManager"], got.entriesByChain(sepolia)["PositionManager"]
			if before.ABIHash == after.ABIHash || before.Address != after.Address || before.EntryDigest != after.EntryDigest {
				t.Fatal("compatible artifact did not preserve semantic identity")
			}
			for _, op := range []string{OpApproveCollateral, OpOpenPosition, OpClosePosition} {
				a, _ := original.Binding(sepolia, op, "ETH-USD")
				b, _ := got.Binding(sepolia, op, "ETH-USD")
				if a.BindingDigest != b.BindingDigest {
					t.Fatal("compatible artifact changed binding")
				}
			}
		})
	}
}
func TestDeclarationAndManifestSchemaVersionsAreClosed(t *testing.T) {
	declarations := editDecl(t, func(d map[string]any) { d["schemaVersion"] = 2 })
	if _, _, err := ParseDeclarations(declarations, BuiltinManifest()); err == nil {
		t.Fatal("unknown declaration schema accepted")
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["schemaVersion"] = 2
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseManifest(raw); err == nil {
		t.Fatal("unknown manifest schema accepted")
	}
}

func TestRestagingNewObservationCoordinatesPreservesBindingIdentity(t *testing.T) {
	original := mustBuild(t, readFile(t, realDecl), realDeployments)
	restaged, err := buildWith(t, readFile(t, realDecl), realDeployments, alteredObservation{func(o *CodeObservation) {
		o.BlockNumber++
		o.BlockHash = KeccakHex([]byte("later-observation"))
	}})
	if err != nil {
		t.Fatal(err)
	}
	if original.Entries[0].CodeBlockNumber == restaged.Entries[0].CodeBlockNumber {
		t.Fatal("fixture did not advance observations")
	}
	for _, op := range []string{OpApproveCollateral, OpOpenPosition, OpClosePosition} {
		before, _ := original.Binding(sepolia, op, "ETH-USD")
		after, _ := restaged.Binding(sepolia, op, "ETH-USD")
		if before.BindingDigest != after.BindingDigest {
			t.Fatal("C5 observation-only restage changed binding")
		}
	}
}

func TestDeploymentIdentityAtSameAddressChangesOperationBinding(t *testing.T) {
	before := mustBuild(t, readFile(t, realDecl), realDeployments)
	dir := copyDeployments(t, nil)
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		var record map[string]any
		if err = json.Unmarshal(readFile(t, file), &record); err != nil {
			t.Fatal(err)
		}
		record["timestamp"] = "2030-01-01T00:00:00.000Z"
		record["deployedAt"] = "2030-01-01T00:00:00.000Z"
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	after := mustBuild(t, readFile(t, realDecl), dir)
	for name, entry := range before.entriesByChain(sepolia) {
		next := after.entriesByChain(sepolia)[name]
		if entry.Address != next.Address {
			t.Fatal("fixture changed deployed address")
		}
	}
	for _, op := range []string{OpApproveCollateral, OpOpenPosition, OpClosePosition} {
		a, _ := before.Binding(sepolia, op, "ETH-USD")
		b, _ := after.Binding(sepolia, op, "ETH-USD")
		if a.BindingDigest == b.BindingDigest {
			t.Fatalf("deployment identity did not change %s binding", op)
		}
	}
}
