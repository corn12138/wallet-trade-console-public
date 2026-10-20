package postgres

import (
	"context"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
)

// Baselines returns the verified legacy baselines among refs. A reference to a
// missing revision fails; references to other revisions are left out.
func (s *Store) Baselines(ctx context.Context, refs []application.RevisionRef) (map[application.RevisionRef]v2.LegacyBaseline, error) {
	var result map[application.RevisionRef]v2.LegacyBaseline
	err := s.readSnapshot(ctx, func(q *querySnapshot) error {
		var err error
		result, err = q.baselines(refs)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (q *querySnapshot) baselines(refs []application.RevisionRef) (map[application.RevisionRef]v2.LegacyBaseline, error) {
	out := map[application.RevisionRef]v2.LegacyBaseline{}
	for _, ref := range refs {
		read, err := q.revision(ref.CampaignID, ref.RevisionID)
		if err != nil {
			return nil, err
		}
		if read.baseline != nil {
			out[ref] = *read.baseline
		}
	}
	return out, nil
}

// TxReader answers the public and personal reads inside the caller's
// transaction at one fixed instant. The legacy activation uses it to read the
// campaign back through the same queries the API serves, before it commits.
// It performs no authorization and is never reachable from HTTP.
type TxReader struct {
	tx           pgx.Tx
	now          time.Time
	capabilities v2.Capabilities
}

func NewTxReader(tx pgx.Tx, now time.Time, capabilities v2.Capabilities) *TxReader {
	return &TxReader{tx: tx, now: now.UTC(), capabilities: capabilities}
}

func (r *TxReader) snapshot(ctx context.Context) *querySnapshot {
	return &querySnapshot{ctx: ctx, tx: r.tx, now: r.now}
}

func (r *TxReader) List(ctx context.Context, query application.ListQuery) (application.ListResponse, error) {
	var result application.ListResponse
	cursor, err := application.DecodeQueryCursor(query.Cursor, "list", application.ListScope(query), query.Phase)
	if err != nil {
		return result, err
	}
	if query.Limit < 1 || query.Limit > 50 {
		return result, v2.InvalidInput("query limit")
	}
	if err = r.snapshot(ctx).list(query, cursor, r.capabilities, &result); err != nil {
		return application.ListResponse{}, classify(err)
	}
	return result, nil
}

func (r *TxReader) Detail(ctx context.Context, id string, variants v2.Variants) (application.DetailResponse, error) {
	var result application.DetailResponse
	if err := r.snapshot(ctx).detail(id, variants, r.capabilities, &result); err != nil {
		return application.DetailResponse{}, classify(err)
	}
	return result, nil
}

func (r *TxReader) Me(ctx context.Context, actor string, ids []string) (application.MeResponse, error) {
	var result application.MeResponse
	if err := r.snapshot(ctx).me(actor, ids, &result); err != nil {
		return application.MeResponse{}, classify(err)
	}
	return result, nil
}

func (r *TxReader) Baselines(ctx context.Context, refs []application.RevisionRef) (map[application.RevisionRef]v2.LegacyBaseline, error) {
	result, err := r.snapshot(ctx).baselines(refs)
	if err != nil {
		return nil, classify(err)
	}
	return result, nil
}
