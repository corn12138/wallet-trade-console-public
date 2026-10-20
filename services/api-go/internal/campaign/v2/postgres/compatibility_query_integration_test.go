//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Seed a later reader/writer contract without bypassing immutable-row or
// attribution guards. This is compatibility data, not proof that this writer
// implements or admits the future template.
func futurePublication(t *testing.T, pool *pgxpool.Pool, corrupt bool, variant ...string) application.Result {
	t.Helper()
	ctx := context.Background()
	r := draft(t, service(pool), time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	value := input(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	raw, _ := json.Marshal(value)
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["schemaVersion"] = 2
	document["templateId"] = "campaignCardV2"
	document["displayContent"].(map[string]any)["sortOrder"] = "future-sort-format"
	schema := int64(2)
	if len(variant) > 0 {
		schema = 1
		document["schemaVersion"] = 1
		document["templateId"] = "campaignCardV1"
		document["displayContent"].(map[string]any)["sortOrder"] = 0
		switch variant[0] {
		case "layout":
			document["displayContent"].(map[string]any)["layout"] = "future-layout"
		case "cta":
			document["cta"].(map[string]any)["actionKind"] = "future-action"
		case "reward":
			document["materialTerms"].(map[string]any)["rewardDelivery"] = "future-reward"
		}
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	termsHash, _ := v2.CanonicalHash(document["materialTerms"])
	contentHash, _ := v2.CanonicalHash(json.RawMessage(raw))
	if corrupt {
		contentHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	revisionID, ledgerID, clientID := commandID(t), commandID(t), commandID(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var at time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	guardExec(t, tx, `INSERT INTO campaign_revisions(id,campaign_id,schema_version,terms_canonical_version,input,material_terms_hash,content_hash,created_by,created_at) VALUES ($1,$2,$8,1,$3,$4,$5,$6,$7)`, revisionID, r.CampaignID, raw, termsHash, contentHash, admin, at, schema)
	if corrupt {
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return application.Result{CampaignID: r.CampaignID, RevisionID: revisionID}
	}
	result := application.Result{CampaignID: r.CampaignID, RevisionID: revisionID, PublicationVersion: 1, Changed: true}
	encoded, _ := json.Marshal(result)
	guardExec(t, tx, `INSERT INTO campaign_commands(id,actor,operation,target_id,client_command_id,payload_hash,expected_publication_version,reason,request_id,result,occurred_at) VALUES ($1,$2,'publish',$3,$4,$5,0,'future compatibility fixture','query-test',$6,$7)`, ledgerID, admin, r.CampaignID, clientID, contentHash, encoded, at)
	guardExec(t, tx, `UPDATE campaign_publications SET publication_version=1,active_revision_id=$2,updated_by=$3,updated_at=$4 WHERE campaign_id=$1`, r.CampaignID, revisionID, admin, at)
	guardExec(t, tx, `INSERT INTO campaign_publication_history(campaign_id,publication_version,command_id,actor,before_mode,after_mode,after_revision_id,occurred_at) VALUES ($1,1,$2,$3,'enabled','enabled',$4,$5)`, r.CampaignID, ledgerID, admin, revisionID, at)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestQueryUnknownVersionRemainsPublicMetadataWithSafeSorting(t *testing.T) {
	pool := poolFor(t)
	r := futurePublication(t, pool, false)
	read := reader(pool)
	ctx := context.Background()
	detail, err := read.Detail(ctx, r.CampaignID, v2.Variants{})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Item.ContentState != "unsupported" || detail.Item.Content != nil || detail.Item.EffectivePhase != "unsupported" || detail.Item.NextTransitionAt != nil || detail.Item.RevisionID != r.RevisionID || detail.Item.MaterialTermsHash == "" {
		t.Fatal("future public metadata lost or made executable")
	}
	response, err := read.List(ctx, application.ListQuery{Phase: "unsupported", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range response.Items {
		if item.ID == r.CampaignID {
			found = true
		}
	}
	if !found {
		t.Fatal("future publication disappeared from list")
	}
	preview, err := read.Preview(ctx, admin, r.CampaignID, r.RevisionID)
	if err != nil || preview.Item.ContentState != "unsupported" || !preview.Item.IsPublished {
		t.Fatalf("future preview inaccessible: %v", err)
	}
}
func TestQueryCorruptFutureCandidateHashReturns503(t *testing.T) {
	pool := poolFor(t)
	r := futurePublication(t, pool, true)
	read := reader(pool)
	ctx := context.Background()
	_, err := read.Detail(ctx, r.CampaignID, v2.Variants{})
	expectCode(t, err, "CAMPAIGN_NOT_FOUND")
	_, err = read.Preview(ctx, admin, r.CampaignID, r.RevisionID)
	expectCode(t, err, "CAMPAIGN_DATA_UNAVAILABLE")
}

func TestQueryFutureSemanticEnumsUseUnsupportedPhaseFilter(t *testing.T) {
	pool := poolFor(t)
	read := reader(pool)
	ctx := context.Background()
	ids := map[string]bool{}
	for _, variant := range []string{"layout", "cta", "reward"} {
		r := futurePublication(t, pool, false, variant)
		ids[r.CampaignID] = true
		detail, err := read.Detail(ctx, r.CampaignID, v2.Variants{})
		if err != nil || detail.Item.EffectivePhase != "unsupported" {
			t.Fatalf("future enum phase: %s %v", variant, err)
		}
	}
	response, err := read.List(ctx, application.ListQuery{Phase: "unsupported", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range response.Items {
		delete(ids, item.ID)
	}
	if len(ids) != 0 {
		t.Fatal("phase filter omitted unsupported semantic enum")
	}
}
