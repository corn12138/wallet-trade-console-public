package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Durable campaign interaction: joins land in campaign_participants and
// reminder intents in campaign_reminders, both unique on
// (campaign_id, wallet_address) so repeated clicks are idempotent. The
// wallet identity comes from the SIWE web3 guard — never from the body.
//
// Reminders are stored intent ONLY. There is no email/push sender in this
// repo, so no response or copy may claim delivery.

var walletRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

// WalletResolver extracts an OPTIONAL authenticated wallet from a request
// (public reads use it to enrich responses; "" = anonymous). httpx builds it
// from the web3 verifier.
type WalletResolver func(r *http.Request) string

// WalletState is the per-wallet campaign state.
type WalletState struct {
	IsParticipating bool
	HasReminder     bool
}

// Join records participation. Idempotent; reports whether the row is new.
func (r *Repository) Join(ctx context.Context, campaignID, wallet string) (bool, error) {
	if r.pool == nil {
		return false, ErrPoolUnavailable
	}
	if err := r.assertCampaignExists(ctx, campaignID); err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO campaign_participants (id, campaign_id, wallet_address, joined_at)
		VALUES (gen_random_uuid()::text, $1, $2, NOW())
		ON CONFLICT (campaign_id, wallet_address) DO NOTHING
	`, campaignID, strings.ToLower(wallet))
	if err != nil {
		return false, fmt.Errorf("insert campaign_participant: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Leave removes participation (idempotent).
func (r *Repository) Leave(ctx context.Context, campaignID, wallet string) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM campaign_participants WHERE campaign_id = $1 AND wallet_address = $2
	`, campaignID, strings.ToLower(wallet))
	if err != nil {
		return fmt.Errorf("delete campaign_participant: %w", err)
	}
	return nil
}

// SetReminder stores reminder intent. Idempotent; reports newness.
func (r *Repository) SetReminder(ctx context.Context, campaignID, wallet string) (bool, error) {
	if r.pool == nil {
		return false, ErrPoolUnavailable
	}
	if err := r.assertCampaignExists(ctx, campaignID); err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO campaign_reminders (id, campaign_id, wallet_address, created_at)
		VALUES (gen_random_uuid()::text, $1, $2, NOW())
		ON CONFLICT (campaign_id, wallet_address) DO NOTHING
	`, campaignID, strings.ToLower(wallet))
	if err != nil {
		return false, fmt.Errorf("insert campaign_reminder: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RemoveReminder deletes reminder intent (idempotent).
func (r *Repository) RemoveReminder(ctx context.Context, campaignID, wallet string) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM campaign_reminders WHERE campaign_id = $1 AND wallet_address = $2
	`, campaignID, strings.ToLower(wallet))
	if err != nil {
		return fmt.Errorf("delete campaign_reminder: %w", err)
	}
	return nil
}

func (r *Repository) assertCampaignExists(ctx context.Context, campaignID string) error {
	var one int
	err := r.pool.QueryRow(ctx, `SELECT 1 FROM launchpad_campaigns WHERE id = $1`, campaignID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check campaign %s: %w", campaignID, err)
	}
	return nil
}

// ParticipantCounts returns real join counts per campaign id.
func (r *Repository) ParticipantCounts(ctx context.Context, ids []string) (map[string]int, error) {
	out := map[string]int{}
	if r.pool == nil || len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
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
	if r.pool == nil || len(ids) == 0 || wallet == "" {
		return out, nil
	}
	wallet = strings.ToLower(wallet)
	rows, err := r.pool.Query(ctx, `
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

// --- service + handlers -----------------------------------------------------

// enrichForWallet fills ParticipantCount for every campaign and, when a
// wallet is known, IsParticipating/HasReminder. Enrichment failures degrade
// silently (the base list still renders) — they are read-side decorations.
func (s *Service) enrichForWallet(ctx context.Context, campaigns []Campaign, wallet string) []Campaign {
	ids := make([]string, 0, len(campaigns))
	for _, c := range campaigns {
		ids = append(ids, c.ID)
	}
	counts, err := s.repo.ParticipantCounts(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "campaign: participant counts failed", "err", err)
		counts = map[string]int{}
	}
	states := map[string]WalletState{}
	if wallet != "" {
		states, err = s.repo.WalletStates(ctx, ids, wallet)
		if err != nil {
			slog.WarnContext(ctx, "campaign: wallet states failed", "err", err)
			states = map[string]WalletState{}
		}
	}
	for i := range campaigns {
		c := &campaigns[i]
		c.ParticipantCount = counts[c.ID]
		if wallet != "" {
			st := states[c.ID]
			participating, reminder := st.IsParticipating, st.HasReminder
			c.IsParticipating = &participating
			c.HasReminder = &reminder
		}
	}
	return campaigns
}

// RegisterInteractionRoutes mounts the wallet-auth join/reminder mutations.
// guard is the SIWE web3 middleware; when nil (dev without JWT_SECRET) the
// routes still mount but the in-handler wallet check answers 401 —
// defense-in-depth, a wallet identity is always required for durable rows.
func RegisterInteractionRoutes(r chi.Router, svc *Service, guard func(http.Handler) http.Handler) {
	group := chi.NewRouter()
	group.Post("/{id}/join", svc.handleJoin)
	group.Delete("/{id}/join", svc.handleLeave)
	group.Post("/{id}/reminder", svc.handleSetReminder)
	group.Delete("/{id}/reminder", svc.handleRemoveReminder)
	if guard != nil {
		r.Group(func(g chi.Router) {
			g.Use(guard)
			g.Mount("/", group)
		})
		return
	}
	r.Mount("/", group)
}

func requireWallet(w http.ResponseWriter, r *http.Request) (string, bool) {
	wallet := strings.ToLower(auth.AddressFromContext(r.Context()))
	if !walletRE.MatchString(wallet) {
		writeInteractionError(w, http.StatusUnauthorized, "wallet authentication required")
		return "", false
	}
	return wallet, true
}

func (s *Service) handleJoin(w http.ResponseWriter, r *http.Request) {
	wallet, ok := requireWallet(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	joined, err := s.repo.Join(r.Context(), id, wallet)
	if !s.writeInteractionResult(w, r, err, "join") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaignId": id, "walletAddress": wallet,
		"isParticipating": true, "created": joined,
	})
}

func (s *Service) handleLeave(w http.ResponseWriter, r *http.Request) {
	wallet, ok := requireWallet(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	err := s.repo.Leave(r.Context(), id, wallet)
	if !s.writeInteractionResult(w, r, err, "leave") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaignId": id, "walletAddress": wallet, "isParticipating": false,
	})
}

func (s *Service) handleSetReminder(w http.ResponseWriter, r *http.Request) {
	wallet, ok := requireWallet(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	created, err := s.repo.SetReminder(r.Context(), id, wallet)
	if !s.writeInteractionResult(w, r, err, "set reminder") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaignId": id, "walletAddress": wallet,
		"hasReminder": true, "created": created,
		// Honest contract: intent stored, nothing is sent anywhere (yet).
		"delivery": "none",
	})
}

func (s *Service) handleRemoveReminder(w http.ResponseWriter, r *http.Request) {
	wallet, ok := requireWallet(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	err := s.repo.RemoveReminder(r.Context(), id, wallet)
	if !s.writeInteractionResult(w, r, err, "remove reminder") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"campaignId": id, "walletAddress": wallet, "hasReminder": false,
	})
}

// writeInteractionResult maps repo errors to HTTP; returns true when the
// caller should write its success payload.
func (s *Service) writeInteractionResult(w http.ResponseWriter, r *http.Request, err error, op string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrNotFound):
		writeInteractionError(w, http.StatusNotFound, "campaign not found")
	case errors.Is(err, ErrPoolUnavailable):
		writeInteractionError(w, http.StatusServiceUnavailable, "campaign store unavailable")
	default:
		slog.ErrorContext(r.Context(), "campaign "+op+" failed", "err", err)
		writeInteractionError(w, http.StatusInternalServerError, "campaign "+op+" failed")
	}
	return false
}

func writeInteractionError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": status, "message": message})
}
