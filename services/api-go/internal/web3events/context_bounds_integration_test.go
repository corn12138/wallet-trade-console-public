package web3events

import (
	"context"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/executionregistry"
)

func TestMaximumClaimStorageAndDigestIsolation(t *testing.T) {
	pool, repo := contextDB(t)
	chainID := isolatedChain(t, pool)
	ctx := context.Background()
	outage := NewService(nil).WithContextClassifier(executionregistry.NewService(executionregistry.NewRepository(nil), executionregistry.BuiltinManifest()))
	available := NewService(nil).WithContextClassifier(executionregistry.NewService(executionregistry.NewRepository(pool), executionregistry.BuiltinManifest()))
	market, consumer := strings.Repeat("A", 16)+"-"+strings.Repeat("B", 16), "a"+strings.Repeat("b", 31)
	raw := claimJSON(t, boundClaim("9223372036854775807", market, consumer))
	normalized := NormalizeClaim(raw)
	write := outage.classifyContext(ctx, raw, chainID, nil, nil)
	if write == nil || write.State != ContextClaimUnverified || len(CanonicalClaim(normalized.Claim)) != 360 {
		t.Fatal("maximum claim not normalized during outage")
	}
	hash := hashOf("aa")
	if err := submitCtx(t, repo, chainID, hash, 1, write); err != nil {
		t.Fatal(err)
	}
	if got := contextOf(t, pool, chainID, hash); got.state != ContextClaimUnverified {
		t.Fatalf("maximum bound claim was downgraded: %+v", got)
	}
	var revision int64
	var gotMarket, gotConsumer, binding, manifest, digest string
	if err := pool.QueryRow(ctx, `SELECT execution_context_claimed_revision, execution_context_market,
  execution_context_consumer, execution_context_binding, execution_context_manifest, execution_context_claim_digest
  FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`, chainID, hash).
		Scan(&revision, &gotMarket, &gotConsumer, &binding, &manifest, &digest); err != nil {
		t.Fatal(err)
	}
	if revision != normalized.Claim.RegistryRevision || gotMarket != market || gotConsumer != consumer || binding != normalized.Claim.BindingDigest || manifest != normalized.Claim.ManifestDigest || digest != normalized.Digest {
		t.Fatal("normalized fields were truncated or changed")
	}
	otherRaw := claimJSON(t, boundClaim("9223372036854775806", market, consumer))
	other := NormalizeClaim(otherRaw)
	if string(CanonicalClaim(other.Claim)[:290]) != string(CanonicalClaim(normalized.Claim)[:290]) {
		t.Fatal("test claims do not share the required prefix")
	}
	if other.Digest == normalized.Digest {
		t.Fatal("distinct full claims collide when only their common prefix is hashed")
	}
	if err := receiptCtx(t, repo, chainID, hash, 1, available.classifyContext(ctx, otherRaw, chainID, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if got := contextOf(t, pool, chainID, hash); got.state != ContextClaimUnverified || got.digest != normalized.Digest {
		t.Fatalf("different claim upgraded original: %+v", got)
	}
	if err := receiptCtx(t, repo, chainID, hash, 1, available.classifyContext(ctx, raw, chainID, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if got := contextOf(t, pool, chainID, hash); got.state != ContextClaimMismatch || got.reason != "unknownRevision" || got.digest != normalized.Digest {
		t.Fatalf("same claim failed to resolve: %+v", got)
	}

	exit := claimJSON(t, map[string]any{"mode": "unboundExit", "operation": "trade.closePosition", "market": market, "reason": "authorityUnavailable",
		"consumer": map[string]any{"id": consumer, "manifestDigest": strings.Repeat("e", 64)}})
	if len(CanonicalClaim(NormalizeClaim(exit).Claim)) != 270 {
		t.Fatal("exit fixture is not maximum length")
	}
	words := make([][]byte, 7)
	for i := range words {
		words[i] = make([]byte, 32)
	}
	calldata := abi.EncodeCall("closePosition(address,address,uint256,uint256,bool,uint256,uint256)", words...)
	exitWrite := outage.classifyContext(ctx, exit, chainID, nil, &calldata)
	if err := receiptCtx(t, repo, chainID, hashOf("bb"), 2, exitWrite); err != nil {
		t.Fatal(err)
	}
	if got := contextOf(t, pool, chainID, hashOf("bb")); got.state != ContextUnboundExit || got.reason != "authorityUnavailable" {
		t.Fatalf("maximum exit claim: %+v", got)
	}
}
