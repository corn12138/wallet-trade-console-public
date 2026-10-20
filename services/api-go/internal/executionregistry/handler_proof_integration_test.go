package executionregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func frozenCheckRequest(t *testing.T, s Snapshot, revision int64, op string) CheckRequest {
	t.Helper()
	b, ok := s.Binding(sepolia, op, "ETH-USD")
	if !ok {
		t.Fatal("missing fixture binding")
	}
	e := s.entriesByChain(sepolia)
	iface := BuiltinManifest().Interfaces[b.InterfaceID]
	var data string
	if op == OpApproveCollateral {
		data = calldata(t, iface, e["PositionManager"].Address, "100")
	} else {
		data = calldata(t, iface, e["MockWETH"].Address, e["MockUSDC"].Address, "1", "2", "true", "3", "4")
	}
	return CheckRequest{ChainID: sepolia, RegistryRevision: strconv.FormatInt(revision, 10), Operation: op, Market: "ETH-USD", BindingDigest: b.BindingDigest, To: e[b.Target].Address, Data: data,
		Consumer: ConsumerClaim{ID: "web-trade", ManifestDigest: BuiltinManifest().Digest, Interfaces: []string{"erc20.approve.v1", "perp.positionManager.open.v1", "perp.positionManager.close.v1"}}}
}
func invokeCheck(t *testing.T, svc *Service, request CheckRequest) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/check", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	Router(svc).ServeHTTP(out, req)
	if out.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("check response may be cached")
	}
	var body map[string]any
	if err = json.Unmarshal(out.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return out.Code, body
}

type historicalReadProbe struct {
	Store
	revisions   []int64
	activeReads int
}

func (p *historicalReadProbe) ActiveRevision(ctx context.Context) (Active, error) {
	p.activeReads++
	return p.Store.ActiveRevision(ctx)
}
func (p *historicalReadProbe) Revision(ctx context.Context, revision int64) (Snapshot, error) {
	p.revisions = append(p.revisions, revision)
	return p.Store.Revision(ctx, revision)
}

func TestPostgresHandlerCounterexamplesAndHistoricalClassification(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	ctx := context.Background()
	c := counterexamples(t)
	r1 := stage(t, repo, c.n)
	if _, err := repo.Activate(ctx, 0, r1, "proof", "N", nil); err != nil {
		t.Fatal(err)
	}
	c.c2 = mustBuild(t, editDecl(t, func(d map[string]any) {
		chain := chainDecl(d, "11155111")
		delete(chain["entries"].(map[string]any), "PositionManager")
		// Both markets reference the withdrawn manager, so neither can remain.
		chain["markets"] = map[string]any{}
	}), realDeployments)
	for _, tc := range []struct {
		name         string
		next         Snapshot
		status       int
		code, reason string
	}{
		{"C1", c.c1, 409, "EXECUTION_CONFIG_CHANGED", "bindingChanged"},
		{"C2", c.c2, 422, "CAPABILITY_DISABLED", "operationWithdrawn"},
		{"C3", c.c3, 409, "EXECUTION_CONFIG_CHANGED", "bindingChanged"},
		{"C4", c.c4, 200, "", ""}, {"C5", c.c5, 200, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := stage(t, repo, tc.next)
			active, err := repo.ActiveRevision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			version, err := repo.Activate(ctx, active.ActivationVersion, next, "proof", tc.name, nil)
			if err != nil {
				t.Fatal(err)
			}
			svc := NewService(repo, BuiltinManifest())
			for _, op := range []string{OpApproveCollateral, OpOpenPosition} {
				t.Run(op, func(t *testing.T) {
					request := frozenCheckRequest(t, c.n, r1, op)
					status, body := invokeCheck(t, svc, request)
					if status != tc.status || (tc.code != "" && body["code"] != tc.code) || (tc.reason != "" && body["reason"] != tc.reason) || body["activeRevision"] != strconv.FormatInt(next, 10) || body["activationVersion"] != strconv.FormatInt(version, 10) {
						t.Fatalf("check status=%d body=%v", status, body)
					}
					if status == 200 && body["ok"] != true {
						t.Fatal("passing check did not return ok")
					}
					probe := &historicalReadProbe{Store: repo}
					state, reason, err := NewService(probe, BuiltinManifest()).ClassifyBound(ctx, r1, sepolia, request.BindingDigest, op, request.Market, request.Consumer.ID, request.Consumer.ManifestDigest, request.To, request.Data, true)
					if probe.activeReads != 0 || len(probe.revisions) != 1 || probe.revisions[0] != r1 {
						t.Fatal("classification did not read only claimed N")
					}
					if err != nil || state != StateRecorded || reason != "" {
						t.Fatalf("historical N classification=%s/%s %v", state, reason, err)
					}
					resolved, err := repo.HistoricalBinding(ctx, r1, sepolia, request.BindingDigest)
					if err != nil || resolved.Revision != r1 || resolved.TargetAddress != request.To {
						t.Fatalf("historical identity=%+v %v", resolved, err)
					}
				})
			}
			out := httptest.NewRecorder()
			Router(svc).ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/current?chainId=11155111", nil))
			var current CurrentResponse
			if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &current) != nil || current.Revision != strconv.FormatInt(next, 10) || current.ContentDigest != tc.next.ContentDigest {
				t.Fatal("current does not describe activated snapshot")
			}
		})
	}
}
func TestPostgresHandlerConsumerIncompatibility(t *testing.T) {
	db := freshDatabase(t)
	repo := NewRepository(db.owner)
	ctx := context.Background()
	snapshot := mustBuild(t, readFile(t, realDecl), realDeployments)
	revision := stage(t, repo, snapshot)
	if _, err := repo.Activate(ctx, 0, revision, "proof", "consumer", nil); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{OpApproveCollateral, OpOpenPosition, OpClosePosition} {
		for _, side := range []string{"web", "server", "unknown-manifest"} {
			t.Run(op+"/"+side, func(t *testing.T) {
				request := frozenCheckRequest(t, snapshot, revision, op)
				manifest := BuiltinManifest()
				binding, _ := snapshot.Binding(sepolia, op, "ETH-USD")
				switch side {
				case "web":
					request.Consumer.Interfaces = []string{"unsupported.interface.v2"}
				case "server":
					delete(manifest.Interfaces, binding.InterfaceID)
				case "unknown-manifest":
					request.Consumer.ManifestDigest = string(bytes.Repeat([]byte("f"), 64))
				}
				status, body := invokeCheck(t, NewService(repo, manifest), request)
				if status != 422 || body["code"] != "CAPABILITY_DISABLED" || body["reason"] != "consumerIncompatible" {
					t.Fatalf("unsupported consumer accepted: %d %v", status, body)
				}
			})
		}
	}
}
