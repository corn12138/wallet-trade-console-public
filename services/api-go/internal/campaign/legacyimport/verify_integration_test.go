//go:build integration

package legacyimport_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
)

// renderedDigest asks PostgreSQL to store the metadata as jsonb and render the
// row, then hashes it like the inventory does, so the declared equivalence
// policy is checked against real jsonb normalization, not assumed.
func renderedDigest(t *testing.T, metadata string) string {
	t.Helper()
	pool := legacyPool(t)
	var raw string
	if err := pool.QueryRow(context.Background(), `SELECT row_to_json(x)::text FROM (SELECT 'same'::text AS id, $1::jsonb AS metadata) x`, metadata).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	digest, err := legacyimport.RowDigestForTest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestDigestPolicyMatchesJSONBStorage(t *testing.T) {
	for _, pair := range [][2]string{
		{`{"n":9007199254740992}`, `{"n":9007199254740993}`},
		{`{"n":0.1000000000000000055511151231257827}`, `{"n":0.1}`},
		{`{"n":1.0}`, `{"n":1}`},
		{`{"n":-7}`, `{"n":7}`},
		{`{"a":[1,{"b":2}]}`, `{"a":[1,{"b":3}]}`},
	} {
		if renderedDigest(t, pair[0]) == renderedDigest(t, pair[1]) {
			t.Fatalf("jsonb keeps %s and %s apart; the digest must too", pair[0], pair[1])
		}
	}
	// jsonb stores these identically, so no archive could distinguish them.
	for _, pair := range [][2]string{{`{"n":1e2}`, `{"n":100}`}, {`{"b":1,"a":2}`, `{"a":2,"b":1}`}} {
		if renderedDigest(t, pair[0]) != renderedDigest(t, pair[1]) {
			t.Fatalf("jsonb stores %s and %s identically", pair[0], pair[1])
		}
	}
}

// An inventory approved before a metadata change must be refused, even when
// the change is one unit in a large integer that float parsing would hide.
func TestVerifyRefusesApprovalAfterMetadataChange(t *testing.T) {
	pool := legacyPool(t)
	random := make([]byte, 6)
	rand.Read(random)
	id := "verify-" + hex.EncodeToString(random)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM launchpad_campaigns WHERE id=$1`, id)
	})
	if _, err := pool.Exec(context.Background(), `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,status,participants,metadata,updated_at) VALUES ($1,'Verify','2020-01-01','2020-02-01','ended',0,'{"limit":9007199254740992}',now())`, id); err != nil {
		t.Fatal(err)
	}
	approved, err := legacyimport.Run(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacyimport.Verify(context.Background(), pool, approved); err != nil {
		t.Fatalf("unchanged source must verify: %v", err)
	}
	if _, err = pool.Exec(context.Background(), `UPDATE launchpad_campaigns SET metadata='{"limit":9007199254740993}' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = legacyimport.Verify(context.Background(), pool, approved); err == nil || !strings.Contains(err.Error(), id+" (changed)") {
		t.Fatalf("metadata change must invalidate the approved inventory: %v", err)
	}
	old := approved
	old.DigestAlgorithm = "legacy-row-json/v1"
	if _, err = legacyimport.Verify(context.Background(), pool, old); err == nil || !strings.Contains(err.Error(), "digest algorithm") {
		t.Fatal("an inventory hashed with another algorithm must be refused")
	}
}
