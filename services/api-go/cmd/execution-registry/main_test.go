package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/executionregistry"
)

type cliRegistry struct {
	snapshot    executionregistry.Snapshot
	activations int
	staged      int
	expected    int64
}

func (r *cliRegistry) Stage(_ context.Context, s executionregistry.Snapshot, _, _ string) (int64, error) {
	r.snapshot = s
	r.staged++
	return 1, nil
}
func (r *cliRegistry) Activate(_ context.Context, expected, revision int64, _, _ string, obs []executionregistry.CodeObservation) (int64, error) {
	r.activations++
	r.expected = expected
	return expected + 1, nil
}
func (r *cliRegistry) ActiveRevision(context.Context) (executionregistry.Active, error) {
	return executionregistry.Active{}, executionregistry.ErrNoActiveRevision
}
func (r *cliRegistry) Revision(context.Context, int64) (executionregistry.Snapshot, error) {
	return r.snapshot, nil
}
func (r *cliRegistry) Audit(context.Context) (executionregistry.Status, error) {
	return executionregistry.Status{Consistent: true}, nil
}

type cliCode struct{ changed bool }

func (c cliCode) CodeAt(_ context.Context, chainID int, address string) (executionregistry.CodeObservation, error) {
	hash := "0x" + strings.Repeat("1", 64)
	if c.changed {
		hash = "0x" + strings.Repeat("2", 64)
	}
	return executionregistry.CodeObservation{ChainID: chainID, Address: address, CodeHash: hash, BlockNumber: 1, BlockHash: "0x" + strings.Repeat("3", 64)}, nil
}
func cliSnapshot(t *testing.T) executionregistry.Snapshot {
	t.Helper()
	path, err := reviewedDeclarations()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(path), "../../.."))
	snapshot, err := executionregistry.Build(context.Background(), executionregistry.BuildInput{Declarations: raw, DeploymentsDir: filepath.Join(root, "packages/shared/deployments"), ArtifactsDir: filepath.Join(root, "services/api-go/internal/executionregistry/testdata/artifacts"), Code: cliCode{}, Manifest: executionregistry.BuiltinManifest()})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestCLIRefusesAdHocDeclarationsBeforeOpeningDatabase(t *testing.T) {
	var stdout, stderr bytes.Buffer
	opened := false
	code := run([]string{"stage", "--declarations", "/tmp/unreviewed.json"}, &stdout, &stderr, env{open: func(context.Context) (registry, func(), error) { opened = true; return nil, func() {}, nil }})
	if code != 2 || opened {
		t.Fatalf("code=%d opened=%v", code, opened)
	}
}

func TestCLIActivationRevalidatesSemanticsAndCode(t *testing.T) {
	valid := cliSnapshot(t)
	for _, tc := range []struct {
		name                     string
		badManifest, changedCode bool
		want                     int
	}{
		{"valid activation", false, false, 0},
		{"unknown staged manifest", true, false, 1},
		{"code changed", false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &cliRegistry{snapshot: valid}
			if tc.badManifest {
				reg.snapshot.ManifestDigest = strings.Repeat("f", 64)
			}
			var stdout, stderr bytes.Buffer
			code := run([]string{"activate", "--revision", "1", "--expected-activation-version", "4", "--actor", "operator", "--reason", "test"}, &stdout, &stderr, env{
				open: func(context.Context) (registry, func(), error) { return reg, func() {}, nil },
				code: func([]int) (executionregistry.CodeReader, error) { return cliCode{changed: tc.changedCode}, nil },
			})
			if code != tc.want {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			if (reg.activations == 1) != (tc.want == 0) {
				t.Fatalf("activations=%d stderr=%s", reg.activations, stderr.String())
			}
			if tc.want == 0 && reg.expected != 4 {
				t.Fatal("CAS expected version lost")
			}
		})
	}
}
