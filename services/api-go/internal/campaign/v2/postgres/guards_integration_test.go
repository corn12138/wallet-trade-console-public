//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func guardExec(t *testing.T, tx pgx.Tx, sql string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("guard fixture statement: %v", err)
	}
}

// Matching both SQLSTATE and the specific guard message prevents a fixture's
// unrelated duplicate key, FK, or CHECK failure from masquerading as coverage.
func guardReject(t *testing.T, pool *pgxpool.Pool, message string, attempt func(pgx.Tx) error) {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	err = attempt(tx)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != message {
		t.Fatalf("wanted guard 23514 %q; got %v", message, err)
	}
}

func guardDeferred(tx pgx.Tx, name string) error {
	_, err := tx.Exec(context.Background(), "SET CONSTRAINTS "+pgx.Identifier{name}.Sanitize()+" IMMEDIATE")
	return err
}

type guardCommandRecord struct {
	id string
	at time.Time
}

func guardCommand(t *testing.T, tx pgx.Tx, actor, op, target, resultID string, pub, state *uint64) guardCommandRecord {
	t.Helper()
	c := guardCommandRecord{commandID(t), time.Now().UTC().Truncate(time.Microsecond)}
	result, err := json.Marshal(map[string]any{"campaignId": resultID, "changed": true})
	if err != nil {
		t.Fatal(err)
	}
	integer := func(p *uint64) any {
		if p == nil {
			return nil
		}
		return int64(*p)
	}
	guardExec(t, tx, `INSERT INTO campaign_commands(id,actor,operation,target_id,client_command_id,payload_hash,
		expected_publication_version,expected_state_version,reason,request_id,result,occurred_at)
		VALUES($1,$2,$3,$4,$1,$5,$6,$7,'Guard regression','guard-regression',$8,$9)`,
		c.id, actor, op, target, strings.Repeat("0", 64), integer(pub), integer(state), result, c.at)
	return c
}

const guardPublicationInsert = `INSERT INTO campaign_publication_history
	(campaign_id,publication_version,command_id,actor,before_mode,after_mode,before_revision_id,after_revision_id,occurred_at)
	SELECT campaign_id,$2,$3,$4,$5,$6,$7,active_revision_id,$8 FROM campaign_publications WHERE campaign_id=$1`

const guardInteractionInsert = `INSERT INTO campaign_interaction_history
	(campaign_id,wallet_address,kind,row_version,interaction_version,command_id,before_status,after_status,
	acceptance_provenance,accepted_revision_id,accepted_publication_version,accepted_terms_hash,accepted_at,occurred_at)
	SELECT campaign_id,wallet_address,kind,$2,$3,$4,$5,$6,acceptance_provenance,accepted_revision_id,
	accepted_publication_version,accepted_terms_hash,accepted_at,$7 FROM campaign_interaction_states
	WHERE campaign_id=$1 AND wallet_address=$8 AND kind='join'`

func guardJoined(t *testing.T, pool *pgxpool.Pool) application.Result {
	t.Helper()
	s := service(pool)
	p := publish(t, s, draft(t, s, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)))
	execute(t, s, wallet, interaction(t, "join", p.CampaignID, 1, 0))
	return p
}

func TestCampaignGuardCommandAttribution(t *testing.T) {
	pool := poolFor(t)
	p := guardJoined(t, pool)
	t.Run("publication actor", func(t *testing.T) {
		guardReject(t, pool, "publication command attribution mismatch", func(tx pgx.Tx) error {
			c := guardCommand(t, tx, admin, "publish", p.CampaignID, p.CampaignID, pointer(1), nil)
			_, err := tx.Exec(context.Background(), guardPublicationInsert, p.CampaignID, 2, c.id, wallet, "enabled", "enabled", p.RevisionID, c.at)
			return err
		})
	})
	for _, tc := range []struct{ name, actor, op, target string }{
		{"personal actor", admin, "leave", p.CampaignID},
		{"personal target", wallet, "leave", "wrong-campaign"},
		{"personal operation", wallet, "join", p.CampaignID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guardReject(t, pool, "interaction command attribution mismatch", func(tx pgx.Tx) error {
				c := guardCommand(t, tx, tc.actor, tc.op, tc.target, p.CampaignID, nil, pointer(1))
				_, err := tx.Exec(context.Background(), guardInteractionInsert, p.CampaignID, 2, 2, c.id, "joined", "left", c.at, wallet)
				return err
			})
		})
	}
	for _, tc := range []struct{ name, actor, op, target, result, message string }{
		{"audit actor", wallet, "revision", p.CampaignID, p.CampaignID, "campaign audit attribution mismatch"},
		{"audit operation", admin, "pause", p.CampaignID, p.CampaignID, "campaign audit attribution mismatch"},
		{"audit target", admin, "revision", "wrong-campaign", p.CampaignID, "campaign audit target mismatch"},
		{"create result", admin, "create", "campaigns", "wrong-campaign", "campaign creation attribution mismatch"},
		{"create collection", admin, "create", p.CampaignID, p.CampaignID, "campaign creation attribution mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guardReject(t, pool, tc.message, func(tx pgx.Tx) error {
				op := "revision"
				if tc.op == "create" {
					op = "create"
				}
				c := guardCommand(t, tx, admin, op, tc.target, tc.result, nil, nil)
				_, err := tx.Exec(context.Background(), `INSERT INTO campaign_audit_events(id,campaign_id,command_id,actor,operation,changes,occurred_at)
					VALUES($1,$2,$3,$4,$5,'{}',$6)`, commandID(t), p.CampaignID, c.id, tc.actor, tc.op, c.at)
				return err
			})
		})
	}
	t.Run("fact time", func(t *testing.T) {
		guardReject(t, pool, "campaign command time mismatch", func(tx pgx.Tx) error {
			c := guardCommand(t, tx, wallet, "leave", p.CampaignID, p.CampaignID, nil, pointer(1))
			_, err := tx.Exec(context.Background(), guardInteractionInsert, p.CampaignID, 2, 2, c.id, "joined", "left", c.at.Add(time.Second), wallet)
			return err
		})
	})
	// The real create path is also the positive collection-key case: its audit
	// refers to the created ID although its command target remains "campaigns".
	if count(t, pool, `SELECT count(*) FROM campaign_audit_events a JOIN campaign_commands c ON c.id=a.command_id
		WHERE a.campaign_id=$1 AND c.operation='create' AND c.target_id='campaigns' AND c.result->>'campaignId'=a.campaign_id`, p.CampaignID) != 1 {
		t.Fatal("valid collection creation was not attributed")
	}
}

func TestCampaignGuardRejectsFutureFacts(t *testing.T) {
	pool := poolFor(t)
	p := guardJoined(t, pool)
	t.Run("outbox ahead without counter update", func(t *testing.T) {
		before := count(t, pool, `SELECT count(*) FROM durable_outbox WHERE payload->>'campaignId'=$1`, p.CampaignID)
		guardReject(t, pool, "outbox event ahead of allocated stream version", func(tx pgx.Tx) error {
			guardExec(t, tx, `INSERT INTO durable_outbox(id,source,aggregate_id,aggregate_version,topic,payload_version,payload,occurred_at,available_at)
				SELECT $1,o.source,o.aggregate_id,s.last_stream_version+1,o.topic,1,o.payload,o.occurred_at,o.available_at
				FROM durable_outbox o JOIN outbox_streams s ON s.stream_key=o.aggregate_id
				WHERE o.payload->>'campaignId'=$2 AND o.topic='campaign.publication.changed' LIMIT 1`, commandID(t), p.CampaignID)
			return guardDeferred(tx, "outbox_event_stream_version")
		})
		if count(t, pool, `SELECT count(*) FROM durable_outbox WHERE payload->>'campaignId'=$1`, p.CampaignID) != before {
			t.Fatal("rejected future event escaped transaction rollback")
		}
	})
	t.Run("publication history ahead", func(t *testing.T) {
		guardReject(t, pool, "publication history ahead of current version", func(tx pgx.Tx) error {
			c := guardCommand(t, tx, admin, "publish", p.CampaignID, p.CampaignID, pointer(1), nil)
			guardExec(t, tx, guardPublicationInsert, p.CampaignID, 2, c.id, admin, "enabled", "enabled", p.RevisionID, c.at)
			return guardDeferred(tx, "campaign_publication_history_current")
		})
	})
	for _, side := range []string{"personal", "global"} {
		t.Run("interaction history ahead of "+side, func(t *testing.T) {
			guardReject(t, pool, "interaction history ahead of current version", func(tx pgx.Tx) error {
				c := guardCommand(t, tx, wallet, "leave", p.CampaignID, p.CampaignID, nil, pointer(1))
				if side == "personal" {
					guardExec(t, tx, `UPDATE campaign_publications SET interaction_version=2 WHERE campaign_id=$1`, p.CampaignID)
				} else {
					guardExec(t, tx, `UPDATE campaign_interaction_states SET row_version=2,status='left' WHERE campaign_id=$1 AND wallet_address=$2 AND kind='join'`, p.CampaignID, wallet)
				}
				guardExec(t, tx, guardInteractionInsert, p.CampaignID, 2, 2, c.id, "joined", "left", c.at, wallet)
				return guardDeferred(tx, "campaign_interaction_history_current")
			})
		})
	}
}

func TestCampaignGuardRequiresHistoryPredecessors(t *testing.T) {
	pool := poolFor(t)
	p := guardJoined(t, pool)
	t.Run("publication predecessor", func(t *testing.T) {
		guardReject(t, pool, "publication history predecessor mismatch", func(tx pgx.Tx) error {
			c := guardCommand(t, tx, admin, "publish", p.CampaignID, p.CampaignID, pointer(1), nil)
			guardExec(t, tx, `UPDATE campaign_publications SET publication_version=2,mode='paused' WHERE campaign_id=$1`, p.CampaignID)
			guardExec(t, tx, guardPublicationInsert, p.CampaignID, 2, c.id, admin, "paused", "paused", p.RevisionID, c.at)
			return guardDeferred(tx, "campaign_publication_history_current")
		})
	})
	t.Run("personal predecessor", func(t *testing.T) {
		guardReject(t, pool, "interaction history predecessor mismatch", func(tx pgx.Tx) error {
			c := guardCommand(t, tx, wallet, "leave", p.CampaignID, p.CampaignID, nil, pointer(1))
			guardExec(t, tx, `UPDATE campaign_interaction_states SET row_version=2,status='left' WHERE campaign_id=$1 AND wallet_address=$2 AND kind='join'`, p.CampaignID, wallet)
			guardExec(t, tx, `UPDATE campaign_publications SET interaction_version=2 WHERE campaign_id=$1`, p.CampaignID)
			guardExec(t, tx, guardInteractionInsert, p.CampaignID, 2, 2, c.id, "left", "left", c.at, wallet)
			return guardDeferred(tx, "campaign_interaction_history_current")
		})
	})
	t.Run("first personal predecessor must be absent", func(t *testing.T) {
		fresh := publish(t, service(pool), draft(t, service(pool), time.Now().Add(-time.Minute), time.Now().Add(time.Hour)))
		guardReject(t, pool, "first interaction predecessor must be absent", func(tx pgx.Tx) error {
			c := guardCommand(t, tx, wallet, "leave", fresh.CampaignID, fresh.CampaignID, nil, pointer(0))
			guardExec(t, tx, `INSERT INTO campaign_interaction_states(campaign_id,wallet_address,kind,status,row_version,acceptance_provenance,changed_at)
				VALUES($1,$2,'join','left',1,'neverAccepted',$3)`, fresh.CampaignID, wallet, c.at)
			guardExec(t, tx, `UPDATE campaign_publications SET interaction_version=1 WHERE campaign_id=$1`, fresh.CampaignID)
			guardExec(t, tx, guardInteractionInsert, fresh.CampaignID, 1, 1, c.id, "joined", "left", c.at, wallet)
			return guardDeferred(tx, "campaign_interaction_history_current")
		})
	})
}

func TestCampaignGuardCountersRequireDurableFacts(t *testing.T) {
	pool := poolFor(t)
	p := guardJoined(t, pool)
	for _, tc := range []struct{ name, sql, constraint, message string }{
		{"publication", `UPDATE campaign_publications SET publication_version=2 WHERE campaign_id=$1`, "campaign_publication_evidence", "publication history missing or mismatched"},
		{"global interaction", `UPDATE campaign_publications SET interaction_version=2 WHERE campaign_id=$1`, "campaign_publication_evidence", "interaction global version has no history"},
		{"personal state", `UPDATE campaign_interaction_states SET row_version=2,status='left' WHERE campaign_id=$1 AND wallet_address='` + wallet + `' AND kind='join'`, "campaign_interaction_evidence", "personal state history missing or mismatched"},
		{"stream", `UPDATE outbox_streams SET last_stream_version=last_stream_version+1 WHERE stream_key IN (SELECT aggregate_id FROM durable_outbox WHERE payload->>'campaignId'=$1 AND topic='campaign.publication.changed')`, "outbox_stream_evidence", "stream version has no durable event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guardReject(t, pool, tc.message, func(tx pgx.Tx) error {
				guardExec(t, tx, tc.sql, p.CampaignID)
				return guardDeferred(tx, tc.constraint)
			})
		})
	}
}

// The adapter and its aggregate lock are borrowed, not recreated: both calls
// below mutate the exact same pgx transaction and commit only after the batch.
type guardBorrowedStore struct {
	transaction application.Transaction
	campaignID  string
}

func (s guardBorrowedStore) Transact(_ context.Context, scope application.LockScope, apply func(application.Transaction) error) error {
	if scope.CampaignID != s.campaignID || scope.CreationKey != "" {
		return fmt.Errorf("batch transaction campaign scope mismatch")
	}
	return apply(s.transaction)
}

func guardBatch(t *testing.T, pool *pgxpool.Pool, id, actor string, commands ...application.Command) {
	t.Helper()
	err := postgres.New(pool).Transact(context.Background(), application.LockScope{CampaignID: id}, func(tx application.Transaction) error {
		s := application.NewService(guardBorrowedStore{tx, id}, allowed{}, caps)
		for _, c := range commands {
			if _, err := s.Execute(context.Background(), actor, c); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("same-transaction batch: %v", err)
	}
}

func TestCampaignGuardAllowsMultipleVersionsInOneTransaction(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	d := draft(t, s, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	first := command(t, "publish", d.CampaignID)
	first.ExpectedPublication, first.CandidateRevisionID = pointer(0), d.RevisionID
	pause := command(t, "pause", d.CampaignID)
	pause.ExpectedPublication = pointer(1)
	guardBatch(t, pool, d.CampaignID, admin, first, pause)
	if count(t, pool, `SELECT count(*) FROM campaign_publication_history WHERE campaign_id=$1`, d.CampaignID) != 2 {
		t.Fatal("batch did not retain both publication facts")
	}
	resume := command(t, "resume", d.CampaignID)
	resume.ExpectedPublication = pointer(2)
	execute(t, s, admin, resume)
	guardBatch(t, pool, d.CampaignID, wallet,
		interaction(t, "join", d.CampaignID, 3, 0), interaction(t, "leave", d.CampaignID, 3, 1))
	if count(t, pool, `SELECT count(*) FROM campaign_interaction_states WHERE campaign_id=$1 AND row_version=2 AND status='left'`, d.CampaignID) != 1 ||
		count(t, pool, `SELECT count(*) FROM campaign_interaction_history WHERE campaign_id=$1`, d.CampaignID) != 2 {
		t.Fatal("batch lost an intermediate personal state")
	}
	if count(t, pool, `SELECT count(*) FROM (SELECT aggregate_id FROM durable_outbox WHERE payload->>'campaignId'=$1
		GROUP BY aggregate_id HAVING min(aggregate_version)=1 AND max(aggregate_version)=count(*)) streams`, d.CampaignID) != 3 {
		t.Fatal("public and private streams did not preserve consecutive batch versions")
	}
	if count(t, pool, `SELECT count(*) FROM outbox_streams s WHERE s.stream_key IN (SELECT aggregate_id FROM durable_outbox WHERE payload->>'campaignId'=$1)
		AND s.last_stream_version=(SELECT max(o.aggregate_version) FROM durable_outbox o WHERE o.aggregate_id=s.stream_key)`, d.CampaignID) != 3 {
		t.Fatal("stream counters differ from committed events")
	}
}
