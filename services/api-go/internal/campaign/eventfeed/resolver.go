package eventfeed

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type resolver struct{ pool *pgxpool.Pool }

func (r resolver) resolve(ctx context.Context, id, family, owner string) (string, error) {
	if r.pool == nil {
		return "", failure("CAMPAIGN_UNAVAILABLE", 503)
	}
	var visible bool
	var scope *string
	err := r.pool.QueryRow(ctx, `SELECT p.active_revision_id IS NOT NULL OR ($2<>'' AND EXISTS
 (SELECT 1 FROM campaign_interaction_states s WHERE s.campaign_id=p.campaign_id AND s.wallet_address=$2)),
 (SELECT scope_id::text FROM campaign_private_scopes WHERE wallet_address=$2)
 FROM campaign_publications p WHERE p.campaign_id=$1`, id, owner).Scan(&visible, &scope)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !visible) {
		return "", failure("CAMPAIGN_NOT_FOUND", 404)
	}
	if err != nil {
		return "", failure("CAMPAIGN_UNAVAILABLE", 503)
	}
	visibility := "public"
	if family == "personal" {
		family = "interaction"
		// A wallet with no prior interaction has no private stream yet. This
		// impossible UUID scope reads an empty stream without creating state.
		visibility = "absent"
		if scope != nil {
			visibility = *scope
		}
	}
	raw, _ := json.Marshal([]string{"campaign", id, family, visibility})
	return string(raw), nil
}
