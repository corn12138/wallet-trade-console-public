package executionregistry

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
)

const sepolia = 11155111

func TestCurrentIncludesObservationTimeWithoutChangingSnapshotIdentity(t *testing.T) {
	snapshot := mustBuild(t, readFile(t, realDecl), realDeployments)
	svc := NewService(&memStore{revisions: map[int64]Snapshot{1: snapshot}, active: 1, version: 1}, BuiltinManifest())
	before := time.Now().UTC()
	current, err := svc.Current(context.Background(), sepolia)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := time.Parse(time.RFC3339Nano, current.ObservedAt)
	if err != nil || observed.Before(before) || observed.After(time.Now().UTC()) {
		t.Fatalf("observation time %q: %v", current.ObservedAt, err)
	}
	if current.ContentDigest != snapshot.ContentDigest {
		t.Fatal("observation changed snapshot identity")
	}
}

func TestManifestSelectorsAndStrictDecoding(t *testing.T) {
	m := BuiltinManifest()
	want := map[string]string{"erc20.approve.v1": "0x095ea7b3"}
	for id, selector := range want {
		if m.Interfaces[id].Selector != selector {
			t.Errorf("%s selector = %s, want %s", id, m.Interfaces[id].Selector, selector)
		}
	}
	approve := m.Interfaces["erc20.approve.v1"]
	good := calldata(t, approve, "0x"+strings.Repeat("1", 40), "5")
	if call, ok := approve.Decode(good); !ok || call["spender"] != "0x"+strings.Repeat("1", 40) {
		t.Fatalf("decode = %v %v", call, ok)
	}
	for name, data := range map[string]string{
		"extra byte":         good + "00",
		"short":              good[:len(good)-2],
		"dirty address word": good[:10] + "ff" + good[12:],
		"other selector":     "0xdeadbeef" + good[10:],
		"not hex":            good[:20] + "zz" + good[22:],
	} {
		if _, ok := approve.Decode(data); ok {
			t.Errorf("%s decoded", name)
		}
	}
	open := m.Interfaces["perp.positionManager.open.v1"]
	bad := calldata(t, open, "0x"+strings.Repeat("1", 40), "0x"+strings.Repeat("2", 40), "1", "2", "true", "3", "4")
	bad = bad[:10+64*4] + strings.Repeat("0", 62) + "02" + bad[10+64*5:]
	if _, ok := open.Decode(bad); ok {
		t.Error("a bool word of 2 decoded")
	}
}

func TestBuildFromRepositorySources(t *testing.T) {
	s := mustBuild(t, readFile(t, realDecl), realDeployments)
	merged, err := deployments.LoadMerged(realDeployments)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range s.Entries {
		if want := strings.ToLower(merged[e.ChainID].Contracts[e.Name].Address); e.Address != want {
			t.Errorf("chain %d %s address %s, API serves %s", e.ChainID, e.Name, e.Address, want)
		}
		if e.DeploymentFile == "" || e.EntryDigest != EntryDigest(e) {
			t.Errorf("entry %s identity incomplete", e.Name)
		}
	}
	if len(s.Markets) != 4 || len(s.Bindings) != 12 || s.ContentDigest != ContentDigest(s) {
		t.Fatalf("markets %d bindings %d", len(s.Markets), len(s.Bindings))
	}
	for _, b := range s.Bindings {
		if b.Operation == OpApproveCollateral && b.Roles["spender"] != "PositionManager" {
			t.Errorf("approve binding without its spender role: %+v", b)
		}
	}
}

func TestBuildRefusals(t *testing.T) {
	pm := strings.ToLower(mustBuild(t, readFile(t, realDecl), realDeployments).entriesByChain(sepolia)["PositionManager"].Address)
	cases := map[string]func() error{
		"zero address": func() error {
			_, err := buildWith(t, readFile(t, realDecl), copyDeployments(t, map[string]map[string]string{"all-sepolia.json": {"PositionManager": zeroAddress}}), fakeCode{})
			return err
		},
		"missing deployment": func() error {
			_, err := buildWith(t, readFile(t, realDecl), copyDeployments(t, map[string]map[string]string{"all-sepolia.json": {"PerpVault": ""}}), fakeCode{})
			return err
		},
		"empty code": func() error {
			_, err := buildWith(t, readFile(t, realDecl), realDeployments, fakeCode{empty: map[string]bool{pm: true}})
			return err
		},
		"code unreadable": func() error {
			_, err := buildWith(t, readFile(t, realDecl), realDeployments, fakeCode{fail: errDown})
			return err
		},
		"interface not in this build's manifest (unknown schema / v2)": func() error {
			decl := editDecl(t, func(d map[string]any) {
				chainDecl(d, "11155111")["entries"].(map[string]any)["PositionManager"] = map[string]any{"interfaces": []any{"perp.positionManager.close.v1", "perp.positionManager.open.v2"}}
			})
			_, err := buildWith(t, decl, realDeployments, fakeCode{})
			return err
		},
		"artifact lacks the declared signature": func() error {
			decl := editDecl(t, func(d map[string]any) {
				chainDecl(d, "11155111")["entries"].(map[string]any)["MockWETH"] = map[string]any{"interfaces": []any{"perp.positionManager.open.v1"}}
			})
			_, err := buildWith(t, decl, realDeployments, fakeCode{})
			return err
		},
		"disabled exit": func() error {
			decl := editDecl(t, func(d map[string]any) {
				chainDecl(d, "31337")["operations"].(map[string]any)["trade.closePosition"] = map[string]any{"status": "disabled"}
			})
			_, err := buildWith(t, decl, realDeployments, fakeCode{})
			return err
		},
		"unknown declaration field": func() error {
			decl := editDecl(t, func(d map[string]any) { d["extra"] = true })
			_, err := buildWith(t, decl, realDeployments, fakeCode{})
			return err
		},
		"unknown schemaVersion": func() error {
			decl := editDecl(t, func(d map[string]any) { d["schemaVersion"] = 2 })
			_, err := buildWith(t, decl, realDeployments, fakeCode{})
			return err
		},
	}
	for name, run := range cases {
		if err := run(); !errors.Is(err, ErrBuild) {
			t.Errorf("%s: err = %v, want ErrBuild", name, err)
		}
	}
	if _, err := ParseManifest([]byte(`{"schemaVersion":2,"interfaces":{}}`)); err == nil {
		t.Error("an unknown manifest schemaVersion parsed")
	}
}

// The §10 cases at digest level (R1).
type cases struct{ n, c1, c2, c3, c4, c5 Snapshot }

func counterexamples(t *testing.T) cases {
	t.Helper()
	decl := readFile(t, realDecl)
	return cases{
		n:  mustBuild(t, decl, realDeployments),
		c1: mustBuild(t, decl, copyDeployments(t, map[string]map[string]string{"all-sepolia.json": {"PositionManager": "0x" + strings.Repeat("f", 40)}})),
		c2: mustBuild(t, editDecl(t, func(d map[string]any) {
			chain := chainDecl(d, "11155111")
			delete(chain["entries"].(map[string]any), "PositionManager")
			chain["markets"] = map[string]any{}
		}), realDeployments),
		c3: mustBuild(t, editDecl(t, func(d map[string]any) {
			chainDecl(d, "11155111")["markets"].(map[string]any)["ETH-USD"].(map[string]any)["collateralDecimals"] = 6
		}), realDeployments),
		c4: mustBuild(t, decl, copyDeployments(t, map[string]map[string]string{"all-sepolia.json": {"MockWBTC": "0x" + strings.Repeat("c", 40)},
			"sepolia.json": {"MockWBTC": "0x" + strings.Repeat("c", 40)}})),
		c5: mustBuild(t, decl, realDeployments),
	}
}

func TestBindingDigestsFollowRelevantDependenciesOnly(t *testing.T) {
	c := counterexamples(t)
	digest := func(s Snapshot, op string) string {
		b, _ := s.Binding(sepolia, op, "ETH-USD")
		return b.BindingDigest
	}
	usdc := func(s Snapshot) string { return s.entriesByChain(sepolia)["MockUSDC"].EntryDigest }
	if usdc(c.c1) != usdc(c.n) {
		t.Fatal("C1 premise: the token entry must be unchanged")
	}
	for name, s := range map[string]Snapshot{"C1 P1->P2": c.c1, "C3 collateral decimals": c.c3} {
		for _, op := range []string{OpApproveCollateral, OpOpenPosition, OpClosePosition} {
			if digest(s, op) == digest(c.n, op) {
				t.Errorf("%s: %s binding unchanged", name, op)
			}
		}
	}
	if _, ok := c.c2.Binding(sepolia, OpApproveCollateral, "ETH-USD"); ok {
		t.Error("C2: the withdrawn market still has bindings")
	}
	for name, s := range map[string]Snapshot{"C4 unrelated market": c.c4, "C5 identical": c.c5} {
		for _, op := range []string{OpApproveCollateral, OpOpenPosition, OpClosePosition} {
			if digest(s, op) != digest(c.n, op) {
				t.Errorf("%s: %s binding changed", name, op)
			}
		}
	}
	// deploymentIdentity is bound; abiHash is not.
	e := c.n.entriesByChain(sepolia)["PositionManager"]
	moved := e
	moved.DeploymentTimestamp = "2030-01-01T00:00:00.000Z"
	if EntryDigest(moved) == e.EntryDigest {
		t.Error("deployment identity is not bound")
	}
	rehashed := e
	rehashed.ABIHash = strings.Repeat("0", 64)
	if EntryDigest(rehashed) != e.EntryDigest {
		t.Error("an ABI hash change that keeps the declared signatures must be compatible")
	}
}

func checkFor(t *testing.T, svc *Service, frozen Snapshot, revision string, op string, mutate func(*CheckRequest)) CheckOutcome {
	t.Helper()
	b, ok := frozen.Binding(sepolia, op, "ETH-USD")
	if !ok {
		t.Fatalf("no %s binding", op)
	}
	entries := frozen.entriesByChain(sepolia)
	iface := BuiltinManifest().Interfaces[b.InterfaceID]
	var data string
	if op == OpApproveCollateral {
		data = calldata(t, iface, entries["PositionManager"].Address, "100")
	} else {
		data = calldata(t, iface, entries["MockWETH"].Address, entries["MockUSDC"].Address, "1", "2", "true", "3", "4")
	}
	req := CheckRequest{ChainID: sepolia, RegistryRevision: revision, Operation: op, Market: "ETH-USD", BindingDigest: b.BindingDigest,
		To: entries[b.Target].Address, Data: data, Consumer: ConsumerClaim{ID: "web-trade", ManifestDigest: BuiltinManifest().Digest,
			Interfaces: []string{"erc20.approve.v1", "perp.positionManager.close.v1", "perp.positionManager.open.v1"}}}
	if mutate != nil {
		mutate(&req)
	}
	return svc.Check(context.Background(), req)
}

func TestCheckOutcomes(t *testing.T) {
	c := counterexamples(t)
	for name, tc := range map[string]struct {
		next   Snapshot
		status int
		code   string
	}{
		"C1 P1->P2":            {c.c1, http.StatusConflict, "EXECUTION_CONFIG_CHANGED"},
		"C2 spender withdrawn": {c.c2, http.StatusUnprocessableEntity, "CAPABILITY_DISABLED"},
		"C3 market relation":   {c.c3, http.StatusConflict, "EXECUTION_CONFIG_CHANGED"},
		"C4 unrelated market":  {c.c4, http.StatusOK, ""},
		"C5 identical":         {c.c5, http.StatusOK, ""},
	} {
		store := &memStore{revisions: map[int64]Snapshot{1: c.n, 2: tc.next}, active: 2, version: 2}
		svc := NewService(store, BuiltinManifest())
		for _, op := range []string{OpApproveCollateral, OpOpenPosition} {
			if out := checkFor(t, svc, c.n, "1", op, nil); out.HTTPStatus != tc.status || out.Code != tc.code {
				t.Errorf("%s %s: %d %s/%s, want %d %s", name, op, out.HTTPStatus, out.Code, out.Reason, tc.status, tc.code)
			}
		}
	}
	store := &memStore{revisions: map[int64]Snapshot{1: c.n}, active: 1, version: 1}
	svc := NewService(store, BuiltinManifest())
	for name, tc := range map[string]struct {
		mutate func(*CheckRequest)
		status int
		reason string
	}{
		"bytes approve another spender": {func(r *CheckRequest) {
			r.Data = calldata(t, BuiltinManifest().Interfaces["erc20.approve.v1"], "0x"+strings.Repeat("9", 40), "100")
		}, http.StatusConflict, "bytesMismatch"},
		"to another target":       {func(r *CheckRequest) { r.To = "0x" + strings.Repeat("9", 40) }, http.StatusConflict, "bytesMismatch"},
		"web lacks the interface": {func(r *CheckRequest) { r.Consumer.Interfaces = []string{"perp.positionManager.open.v1"} }, http.StatusUnprocessableEntity, "consumerIncompatible"},
		"unknown revision":        {func(r *CheckRequest) { r.RegistryRevision = "99" }, http.StatusConflict, "unknownRevision"},
		"other chain":             {func(r *CheckRequest) { r.ChainID = 31337 }, http.StatusConflict, ""},
		"malformed revision":      {func(r *CheckRequest) { r.RegistryRevision = "01" }, http.StatusBadRequest, ""},
		"bigint overflow":         {func(r *CheckRequest) { r.RegistryRevision = "9223372036854775808" }, http.StatusBadRequest, ""},
		"unknown operation":       {func(r *CheckRequest) { r.Operation = "trade.swap" }, http.StatusBadRequest, ""},
	} {
		out := checkFor(t, svc, c.n, "1", OpApproveCollateral, tc.mutate)
		if out.HTTPStatus != tc.status || (tc.reason != "" && out.Reason != tc.reason) {
			t.Errorf("%s: %d %s/%s", name, out.HTTPStatus, out.Code, out.Reason)
		}
	}
	// An older Web build: the server knows an interface the binding needs, the Web does not.
	serverOnly := Manifest{Digest: BuiltinManifest().Digest, Interfaces: map[string]Interface{}}
	for id, i := range BuiltinManifest().Interfaces {
		if id != "erc20.approve.v1" {
			serverOnly.Interfaces[id] = i
		}
	}
	if out := checkFor(t, NewService(store, serverOnly), c.n, "1", OpApproveCollateral, nil); out.Reason != "consumerIncompatible" {
		t.Errorf("server without the interface: %+v", out)
	}
	disabled := c.n
	disabled.Bindings = append([]Binding{}, c.n.Bindings...)
	for i := range disabled.Bindings {
		if disabled.Bindings[i].Operation == OpOpenPosition {
			disabled.Bindings[i].Status = "disabled"
		}
	}
	if out := checkFor(t, NewService(&memStore{revisions: map[int64]Snapshot{1: disabled}, active: 1, version: 1}, BuiltinManifest()), disabled, "1", OpOpenPosition, nil); out.Reason != "operationDisabled" {
		t.Errorf("disabled open: %+v", out)
	}
	for name, st := range map[string]*memStore{"no active": {revisions: map[int64]Snapshot{}}, "down": {err: errDown}} {
		if out := checkFor(t, NewService(st, BuiltinManifest()), c.n, "1", OpApproveCollateral, nil); out.HTTPStatus != http.StatusServiceUnavailable {
			t.Errorf("%s: %+v", name, out)
		}
	}
}

// The revision-N broadcast reported after N+1 invariant, at the service level.
func TestClassificationUsesTheClaimedHistoricalRevision(t *testing.T) {
	c := counterexamples(t)
	store := &memStore{revisions: map[int64]Snapshot{1: c.n, 2: c.c1}, active: 2, version: 2}
	svc := NewService(store, BuiltinManifest())
	b, _ := c.n.Binding(sepolia, OpApproveCollateral, "ETH-USD")
	entries := c.n.entriesByChain(sepolia)
	input := calldata(t, BuiltinManifest().Interfaces["erc20.approve.v1"], entries["PositionManager"].Address, "100")
	usdc := entries["MockUSDC"].Address
	for name, tc := range map[string]struct {
		revision int64
		chain    int
		digest   string
		to       string
		input    string
		known    bool
		state    string
		reason   string
	}{
		"broadcast under N, reported after N+1": {1, sepolia, b.BindingDigest, usdc, input, true, StateRecorded, ""},
		"unknown revision":                      {9, sepolia, b.BindingDigest, usdc, input, true, StateClaimMismatch, "unknownRevision"},
		"digest not in that revision":           {2, sepolia, b.BindingDigest, usdc, input, true, StateClaimMismatch, "unknownBinding"},
		"other chain":                           {1, 31337, b.BindingDigest, usdc, input, true, StateClaimMismatch, "chainMismatch"},
		"target mismatch":                       {1, sepolia, b.BindingDigest, "0x" + strings.Repeat("9", 40), input, true, StateClaimMismatch, "targetMismatch"},
		"spender word differs":                  {1, sepolia, b.BindingDigest, usdc, input[:73] + "9" + input[74:], true, StateClaimMismatch, "bytesMismatch"},
		"amount differs (not a bound role)":     {1, sepolia, b.BindingDigest, usdc, input[:len(input)-1] + "1", true, StateRecorded, ""},
		"input missing":                         {1, sepolia, b.BindingDigest, usdc, "", false, StateClaimMismatch, "evidenceIncomplete"},
	} {
		state, reason, err := svc.ClassifyBound(context.Background(), tc.revision, tc.chain, tc.digest, OpApproveCollateral, "ETH-USD", "web-trade", BuiltinManifest().Digest, tc.to, tc.input, tc.known)
		if err != nil || state != tc.state || reason != tc.reason {
			t.Errorf("%s: %s/%s %v", name, state, reason, err)
		}
	}
	if _, _, err := NewService(&memStore{err: errDown}, BuiltinManifest()).ClassifyBound(context.Background(), 1, sepolia, b.BindingDigest, OpApproveCollateral, "ETH-USD", "web-trade", BuiltinManifest().Digest, usdc, input, true); err == nil {
		t.Error("a registry outage must surface as an error (claimUnverified)")
	}
	closeData := calldata(t, BuiltinManifest().Interfaces["perp.positionManager.close.v1"], entries["MockWETH"].Address, usdc, "1", "2", "true", "3", "4")
	if s, _ := svc.ClassifyUnboundExit(closeData, true); s != StateUnboundExit {
		t.Errorf("close classified %s", s)
	}
	if s, r := svc.ClassifyUnboundExit(input, true); s != StateClaimMismatch || r != "notAnExit" {
		t.Errorf("approve as exit classified %s/%s", s, r)
	}
}

func TestReviewContextListsEveryMatchingBinding(t *testing.T) {
	c := counterexamples(t)
	svc := NewService(&memStore{revisions: map[int64]Snapshot{1: c.n}, active: 1, version: 1}, BuiltinManifest())
	entries := c.n.entriesByChain(sepolia)
	input := calldata(t, BuiltinManifest().Interfaces["erc20.approve.v1"], entries["PositionManager"].Address, "100")
	got := svc.ResolveReview(context.Background(), sepolia, entries["MockUSDC"].Address, input)
	if got.State != "bound" || len(got.Bindings) != 2 {
		t.Fatalf("approve review = %+v, want both markets sharing the collateral", got)
	}
	if addr, ok := got.ContractAddress(sepolia, "PositionManager"); !ok || addr != entries["PositionManager"].Address {
		t.Error("the decoder lookup must come from the same snapshot")
	}
	if got := svc.ResolveReview(context.Background(), sepolia, "0x"+strings.Repeat("9", 40), input); got.State != "unbound" || got.Reason != "notRegistered" {
		t.Errorf("unregistered = %+v", got)
	}
	if got := NewService(&memStore{err: errDown}, BuiltinManifest()).ResolveReview(context.Background(), sepolia, entries["MockUSDC"].Address, input); got.State != "unavailable" {
		t.Errorf("down = %+v", got)
	}
}

// Real artifact validation is a separate evidence boundary from the hermetic
// fixture suite; CI without compiled contracts must report it as unproven.
func TestBuildWithCompiledContractArtifacts(t *testing.T) {
	artifacts := os.Getenv("EXECUTION_REGISTRY_ARTIFACTS_DIR")
	if artifacts == "" {
		t.Skip("EXECUTION_REGISTRY_ARTIFACTS_DIR unset: compiled artifact validation is UNPROVEN")
	}
	_, err := Build(context.Background(), BuildInput{Declarations: readFile(t, realDecl), DeploymentsDir: realDeployments, ArtifactsDir: artifacts, Code: fakeCode{}, Manifest: BuiltinManifest()})
	if err != nil {
		t.Fatal(err)
	}
}
