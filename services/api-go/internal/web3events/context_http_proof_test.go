package web3events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/executionregistry"
)

func contextProofPost(t *testing.T, router http.Handler, path, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+signWeb3(t, ownerA))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec.Code
}

func TestOrdinaryClaimFullFieldsPersistAndReorderedRetryResolves(t *testing.T) {
	pool, repo := proofReportDatabase(t)
	ctx := context.Background()
	snapshot := buildSnapshot(t, "../../../../packages/shared/deployments")
	registry := executionregistry.NewRepository(pool)
	rev, err := registry.Stage(ctx, snapshot, "test", "ordinary claim proof")
	if err != nil {
		t.Fatal(err)
	}
	var digest, target, spender string
	for _, binding := range snapshot.Bindings {
		if binding.ChainID == 11155111 && binding.Operation == executionregistry.OpApproveCollateral && binding.Market == "ETH-USD" {
			digest = binding.BindingDigest
		}
	}
	for _, entry := range snapshot.Entries {
		if entry.ChainID == 11155111 && entry.Name == "MockUSDC" {
			target = entry.Address
		}
		if entry.ChainID == 11155111 && entry.Name == "PositionManager" {
			spender = entry.Address
		}
	}
	claim := boundClaim(fmt.Sprint(rev), "ETH-USD", "web-trade")
	claim["bindingDigest"], claim["consumer"] = digest, map[string]any{"id": "web-trade", "manifestDigest": executionregistry.BuiltinManifest().Digest}
	raw := claimJSON(t, claim)
	normalized := NormalizeClaim(raw)
	if normalized.Kind != ClaimNormalized || len(CanonicalClaim(normalized.Claim)) != 293 {
		t.Fatal("the actual posted fixture must be an ordinary 293-byte claim")
	}
	service := func(registry *executionregistry.Repository) http.Handler {
		return Router(NewService(stubReader{}).WithWrites(repo, auth.StrictMiddleware(writeAuth.Verifier), inputVerifier{target, approveInput(t, spender)}).
			WithContextClassifier(executionregistry.NewService(registry, executionregistry.BuiltinManifest())))
	}
	hash := hashOf("a1")
	body := fmt.Sprintf(`{"chainId":11155111,"txHash":%q,"executionContext":%s}`, hash, raw)
	if code := contextProofPost(t, service(executionregistry.NewRepository(nil)), "/transactions", body); code != http.StatusCreated {
		t.Fatalf("outage report HTTP %d", code)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(contextColumnsJSON(t, pool, 11155111, hash)), &fields); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"state": ContextClaimUnverified, "mode": "bound", "claimed_revision": float64(rev), "claim_digest": normalized.Digest,
		"operation": "trade.approveCollateral", "market": "ETH-USD", "binding": digest, "consumer": "web-trade",
		"manifest": executionregistry.BuiltinManifest().Digest, "reason": nil, "diagnostic": nil, "revision": nil,
	} {
		if fields["execution_context_"+key] != want {
			t.Errorf("persisted field %s differs", key)
		}
	}
	if fields["execution_context_classified_at"] == nil {
		t.Fatal("unverified claim lacks classification time")
	}
	reordered := fmt.Sprintf(`{"mode":"bound","registryRevision":%q,"operation":"trade.approveCollateral","market":"ETH-USD","bindingDigest":%q,"consumer":{"manifestDigest":%q,"id":"web-trade"}}`, fmt.Sprint(rev), digest, executionregistry.BuiltinManifest().Digest)
	if string(raw) == reordered || NormalizeClaim([]byte(reordered)).Digest != normalized.Digest {
		t.Fatal("retry must reorder keys while retaining the exact identity")
	}
	if code := contextProofPost(t, service(registry), "/transactions/"+hash+"/receipt", `{"chainId":11155111,"executionContext":`+reordered+`}`); code != http.StatusOK {
		t.Fatalf("retry HTTP %d", code)
	}
	if got := contextOf(t, pool, 11155111, hash); got.state != ContextRecorded || got.revision != rev || got.digest != normalized.Digest {
		t.Fatalf("same-claim retry failed to resolve: %+v", got)
	}
}

func TestMalformedClaimsPersistWithoutRejectingVerifiedReports(t *testing.T) {
	pool, repo := proofReportDatabase(t)
	registry := executionregistry.NewService(executionregistry.NewRepository(pool), executionregistry.BuiltinManifest())
	router := Router(NewService(stubReader{}).WithWrites(repo, auth.StrictMiddleware(writeAuth.Verifier), inputVerifier{}).WithContextClassifier(registry))
	valid := string(claimJSON(t, boundClaim("1", "ETH-USD", "web-trade")))
	cases := []struct{ name, raw, diagnostic string }{
		{"revision length", string(claimJSON(t, boundClaim("10000000000000000000", "ETH-USD", "web-trade"))), "registryRevision:pattern"},
		{"revision overflow", string(claimJSON(t, boundClaim("9223372036854775808", "ETH-USD", "web-trade"))), "registryRevision:range"},
		{"market length", string(claimJSON(t, boundClaim("1", strings.Repeat("A", 17)+"-"+strings.Repeat("B", 16), "web-trade"))), "market:pattern"},
		{"consumer length", string(claimJSON(t, boundClaim("1", "ETH-USD", "a"+strings.Repeat("b", 32)))), "consumer.id:pattern"},
		{"unknown field", strings.TrimSuffix(valid, "}") + `,"privatePayload":"sensitive-test-value"}`, "unknownField:pattern"},
		{"duplicate key", strings.TrimSuffix(valid, "}") + `,"mode":"bound"}`, "body:duplicate"},
		{"wrong type", strings.Replace(valid, `"mode":"bound"`, `"mode":17`, 1), "mode:type"},
		{"claim size", `{"mode":"` + strings.Repeat("x", 3000) + `"}`, "body:size"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hash := fmt.Sprintf("0x%064x", i+1)
			body := fmt.Sprintf(`{"chainId":11155111,"txHash":%q,"executionContext":%s}`, hash, tc.raw)
			if code := contextProofPost(t, router, "/transactions", body); code != http.StatusCreated {
				t.Fatalf("verified report refused: HTTP %d", code)
			}
			got := contextOf(t, pool, 11155111, hash)
			var diagnostic *string
			if err := pool.QueryRow(context.Background(), `SELECT execution_context_diagnostic FROM web3_transactions WHERE chain_id=11155111 AND tx_hash=$1`, hash).Scan(&diagnostic); err != nil {
				t.Fatal(err)
			}
			if got.state != ContextClaimMismatch || got.reason != "malformed" || diagnostic == nil || *diagnostic != tc.diagnostic {
				t.Fatalf("stored state=%s reason=%s diagnostic=%v, want malformed/%s", got.state, got.reason, diagnostic, tc.diagnostic)
			}
		})
	}
	hash := fmt.Sprintf("0x%064x", 100)
	body := fmt.Sprintf(`{"chainId":11155111,"txHash":%q,"executionContext":{"mode":"%s"}}`, hash, strings.Repeat("x", 1<<20))
	if code := contextProofPost(t, router, "/transactions", body); code != http.StatusBadRequest {
		t.Fatalf("oversized report body HTTP %d, want 400", code)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM web3_transactions WHERE tx_hash=$1`, hash).Scan(&count); err != nil || count != 0 {
		t.Fatalf("oversized body persisted an attempt: %d / %v", count, err)
	}
}

func TestUnboundReasonsAndVerifiedNonExitPersistThroughHTTP(t *testing.T) {
	pool, repo := proofReportDatabase(t)
	words := make([][]byte, 7)
	for i := range words {
		words[i] = make([]byte, 32)
	}
	closeInput := abi.EncodeCall("closePosition(address,address,uint256,uint256,bool,uint256,uint256)", words...)
	openInput := abi.EncodeCall("openPosition(address,address,uint256,uint256,bool,uint256,uint256)", words...)
	for _, input := range []string{closeInput, openInput} {
		decoded := false
		for _, iface := range executionregistry.BuiltinManifest().Interfaces {
			if _, ok := iface.Decode(input); ok {
				decoded = true
			}
		}
		if !decoded {
			t.Fatal("proof calldata is not a real supported contract call")
		}
	}
	for i, reason := range []string{"authorityUnavailable", "noActiveRevision", "targetNotActive", "consumerIncompatible", "notAnExit"} {
		t.Run(reason, func(t *testing.T) {
			input, claimedReason := closeInput, reason
			if reason == "notAnExit" {
				input, claimedReason = openInput, "authorityUnavailable"
			}
			router := Router(NewService(stubReader{}).WithWrites(repo, auth.StrictMiddleware(writeAuth.Verifier), inputVerifier{input: input}).
				WithContextClassifier(executionregistry.NewService(executionregistry.NewRepository(pool), executionregistry.BuiltinManifest())))
			raw := claimJSON(t, map[string]any{"mode": "unboundExit", "operation": "trade.closePosition", "market": "ETH-USD", "reason": claimedReason,
				"consumer": map[string]any{"id": "web-trade", "manifestDigest": executionregistry.BuiltinManifest().Digest}})
			hash := fmt.Sprintf("0x%064x", i+1)
			if code := contextProofPost(t, router, "/transactions", fmt.Sprintf(`{"chainId":11155111,"txHash":%q,"executionContext":%s}`, hash, raw)); code != http.StatusCreated {
				t.Fatalf("report HTTP %d", code)
			}
			want := ContextUnboundExit
			if reason == "notAnExit" {
				want = ContextClaimMismatch
			}
			if got := contextOf(t, pool, 11155111, hash); got.state != want || got.reason != reason {
				t.Fatalf("stored %+v, want %s/%s", got, want, reason)
			}
		})
	}
}
