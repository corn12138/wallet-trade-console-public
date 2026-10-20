package campaign

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var walletRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

// WalletResolver must verify a presented credential; an empty result with an
// Authorization header is authentication failure, not an anonymous request.
type WalletResolver func(*http.Request) string

type WalletState struct {
	IsParticipating bool
	HasReminder     bool
}

// ParticipantCounts returns real join counts per campaign id.
func (r *Repository) ParticipantCounts(ctx context.Context, ids []string) (map[string]int, error) {
	out := map[string]int{}
	db := r.reads()
	if db == nil {
		return nil, ErrPoolUnavailable
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, `
		SELECT campaign_id, COUNT(*)::int FROM campaign_participants
		WHERE campaign_id = ANY($1)
		GROUP BY campaign_id
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("count campaign_participants: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan participant count: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// WalletStates returns the wallet's join/reminder state per campaign id.
func (r *Repository) WalletStates(ctx context.Context, ids []string, wallet string) (map[string]WalletState, error) {
	out := map[string]WalletState{}
	db := r.reads()
	if db == nil {
		return nil, ErrPoolUnavailable
	}
	if len(ids) == 0 || wallet == "" {
		return out, nil
	}
	wallet = strings.ToLower(wallet)
	rows, err := db.Query(ctx, `
		SELECT campaign_id, TRUE, FALSE FROM campaign_participants
		WHERE campaign_id = ANY($1) AND wallet_address = $2
		UNION ALL
		SELECT campaign_id, FALSE, TRUE FROM campaign_reminders
		WHERE campaign_id = ANY($1) AND wallet_address = $2
	`, ids, wallet)
	if err != nil {
		return nil, fmt.Errorf("query wallet campaign state: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var participating, reminder bool
		if err := rows.Scan(&id, &participating, &reminder); err != nil {
			return nil, fmt.Errorf("scan wallet campaign state: %w", err)
		}
		st := out[id]
		st.IsParticipating = st.IsParticipating || participating
		st.HasReminder = st.HasReminder || reminder
		out[id] = st
	}
	return out, rows.Err()
}

func (s *Service) enrichForWallet(ctx context.Context, campaigns []Campaign, wallet string) ([]Campaign, error) {
	if s == nil || s.repo == nil {
		return nil, ErrPoolUnavailable
	}
	if len(campaigns) == 0 {
		return []Campaign{}, nil
	}
	ids := make([]string, len(campaigns))
	for i, c := range campaigns {
		ids[i] = c.ID
	}
	counts, err := s.repo.ParticipantCounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	states := map[string]WalletState{}
	if wallet != "" {
		states, err = s.repo.WalletStates(ctx, ids, wallet)
		if err != nil {
			return nil, err
		}
	}
	// A fresh slice prevents per-wallet enrichment from mutating a shared cache.
	out := append([]Campaign(nil), campaigns...)
	for i := range out {
		out[i].ParticipantCount = counts[out[i].ID]
		out[i].IsParticipating = nil
		out[i].HasReminder = nil
		if wallet != "" {
			state := states[out[i].ID]
			participating, reminder := state.IsParticipating, state.HasReminder
			out[i].IsParticipating = &participating
			out[i].HasReminder = &reminder
		}
	}
	return out, nil
}
