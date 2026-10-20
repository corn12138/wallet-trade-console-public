//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func reader(pool *pgxpool.Pool) *application.QueryService {
	return application.NewQueryService(postgres.New(pool), allowed{}, caps)
}
func TestQueryPublicDraftIsolationSortFilterAndCursor(t *testing.T) {
	pool := poolFor(t)
	write, read := service(pool), reader(pool)
	ctx := context.Background()
	now := time.Now()
	draftOnly := draft(t, write, now.Add(time.Hour), now.Add(2*time.Hour))
	if _, err := read.Detail(ctx, draftOnly.CampaignID, v2.Variants{}); err == nil {
		t.Fatal("draft exposed publicly")
	} else {
		expectCode(t, err, "CAMPAIGN_NOT_FOUND")
	}
	ids := map[string]int64{}
	for _, order := range []int64{9998, 9997, 9999} {
		c := command(t, "create", "")
		value := input(now.Add(-time.Hour), now.Add(time.Hour))
		value.DisplayContent.SortOrder = order
		c.Revision = &value
		r := publish(t, write, execute(t, write, admin, c).Original)
		ids[r.CampaignID] = order
	}
	seen := map[string]bool{}
	cursor := ""
	lastOrder := int64(-1)
	lastID := ""
	found := 0
	for page := 0; page < 200; page++ {
		response, err := read.List(ctx, application.ListQuery{Limit: 2, Phase: "active", Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if response.ServerNow != response.AsOf {
			t.Fatal("mixed snapshot clocks")
		}
		for _, item := range response.Items {
			if item.EffectivePhase != "active" || item.Content == nil {
				t.Fatal("phase filter mismatch")
			}
			order := item.Content.DisplayContent.SortOrder
			if seen[item.ID] || order < lastOrder || order == lastOrder && item.ID <= lastID {
				t.Fatal("unstable cursor order")
			}
			seen[item.ID] = true
			lastID, lastOrder = item.ID, order
			if item.ID == draftOnly.CampaignID {
				t.Fatal("list exposed draft")
			}
			if _, ok := ids[item.ID]; ok {
				found++
			}
			raw, _ := json.Marshal(item)
			if strings.Contains(string(raw), wallet) || strings.Contains(string(raw), "acceptedAt") {
				t.Fatal("public owner leak")
			}
		}
		if response.NextCursor == nil {
			break
		}
		cursor = *response.NextCursor
	}
	if found != 3 {
		t.Fatalf("expected three sorted fixtures, found %d", found)
	}
	if cursor != "" {
		_, err := read.List(ctx, application.ListQuery{Limit: 2, Phase: "ended", Cursor: cursor})
		expectCode(t, err, "CAMPAIGN_INVALID_INPUT")
	}
}
func TestQueryPreviewOwnershipWithdrawnCapabilityAndPublishedCounts(t *testing.T) {
	pool := poolFor(t)
	write, read := service(pool), reader(pool)
	ctx := context.Background()
	now := time.Now()
	first := draft(t, write, now.Add(-time.Hour), now.Add(time.Hour))
	other := draft(t, write, now.Add(time.Hour), now.Add(2*time.Hour))
	preview, err := read.Preview(ctx, admin, first.CampaignID, first.RevisionID)
	if err != nil || preview.Item.IsPublished {
		t.Fatalf("candidate preview: %v", err)
	}
	_, err = read.Preview(ctx, admin, other.CampaignID, first.RevisionID)
	expectCode(t, err, "CAMPAIGN_NOT_FOUND")
	first = publish(t, write, first)
	execute(t, write, wallet, interaction(t, "join", first.CampaignID, first.PublicationVersion, 0))
	detail, err := read.Detail(ctx, first.CampaignID, v2.Variants{})
	if err != nil || detail.Item.ParticipantCount == nil || *detail.Item.ParticipantCount != "1" {
		t.Fatalf("real count missing: %v", err)
	}
	withdrawn := application.NewQueryService(postgres.New(pool), allowed{}, func() v2.Capabilities { return v2.Capabilities{} })
	detail, err = withdrawn.Detail(ctx, first.CampaignID, v2.Variants{})
	if err != nil || detail.Item.ContentState != "unsupported" || detail.Item.Content != nil || detail.Item.RevisionID != first.RevisionID || detail.Item.EffectivePhase != "active" {
		t.Fatalf("withdrawn metadata not retained: %v", err)
	}
	preview, err = withdrawn.Preview(ctx, admin, first.CampaignID, first.RevisionID)
	if err != nil || !preview.Item.IsPublished || preview.Item.ContentState != "unsupported" || preview.Item.Content != nil {
		t.Fatalf("withdrawn preview: %v", err)
	}
}

func TestQueryMultibytePageUsesResponseBudgetWithoutSkippingRows(t *testing.T) {
	pool := poolFor(t)
	write, read := service(pool), reader(pool)
	ctx := context.Background()
	now := time.Now()
	ids := map[string]bool{}
	copy := v2.LocalizedCopy{Title: strings.Repeat("😀", 120), Description: strings.Repeat("😀", 2000), Rules: strings.Repeat("😀", 2000), RewardDescription: strings.Repeat("😀", 500)}
	for index := 0; index < 26; index++ {
		value := input(now.Add(-3*time.Hour), now.Add(-2*time.Hour))
		value.MaterialTerms.Copy = v2.Translations{En: copy, Zh: copy}
		value.DisplayContent.SortOrder = 9999
		c := command(t, "create", "")
		c.Revision = &value
		result := publish(t, write, execute(t, write, admin, c).Original)
		ids[result.CampaignID] = true
	}
	cursor := ""
	bounded := false
	for page := 0; page < 200; page++ {
		response, err := read.List(ctx, application.ListQuery{Limit: 50, Phase: "ended", Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var decoded v2.QueryListResponse
		if err = v2.DecodeContractResponse("QueryListResponse", raw, &decoded); err != nil {
			t.Fatalf("legal page exceeds response contract: %v", err)
		}
		for _, item := range response.Items {
			delete(ids, item.ID)
		}
		if response.NextCursor == nil {
			break
		}
		if len(response.Items) < 50 {
			bounded = true
		}
		cursor = *response.NextCursor
	}
	if !bounded || len(ids) != 0 {
		t.Fatalf("byte-budget cursor omitted rows: bounded=%v remaining=%d", bounded, len(ids))
	}
}

// The legacy-representable list is filtered in SQL: paused and archived
// campaigns never reach the compatibility adapter, and its cursors cannot be
// replayed against the public list (or the reverse).
func TestLegacyRepresentableListFiltersInSQLAndScopesCursors(t *testing.T) {
	pool := poolFor(t)
	s, store := service(pool), postgres.New(pool)
	start, end := time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
	enabled := publish(t, s, draft(t, s, start, end))
	paused := publish(t, s, draft(t, s, start, end))
	archived := publish(t, s, draft(t, s, start, end))
	for _, item := range []struct {
		result application.Result
		op     string
	}{{paused, "pause"}, {archived, "archive"}} {
		c := command(t, item.op, item.result.CampaignID)
		c.ExpectedPublication = pointer(1)
		execute(t, s, admin, c)
	}
	seen := func(query application.ListQuery) map[string]bool {
		ids := map[string]bool{}
		for {
			page, err := store.List(context.Background(), query, caps())
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range page.Items {
				ids[item.ID] = true
				if query.LegacyRepresentable && item.Mode != "enabled" {
					t.Fatalf("filtered list returned mode %s", item.Mode)
				}
			}
			if page.NextCursor == nil {
				return ids
			}
			query.Cursor = *page.NextCursor
		}
	}
	all := seen(application.ListQuery{Limit: 50})
	filtered := seen(application.ListQuery{Limit: 50, LegacyRepresentable: true})
	if !all[paused.CampaignID] || !all[archived.CampaignID] || !filtered[enabled.CampaignID] || filtered[paused.CampaignID] || filtered[archived.CampaignID] {
		t.Fatal("the public list keeps every published campaign; the legacy list only enabled ones")
	}
	page, err := store.List(context.Background(), application.ListQuery{Limit: 1}, caps())
	if err != nil || page.NextCursor == nil {
		t.Fatalf("need a public cursor: %v", err)
	}
	_, err = store.List(context.Background(), application.ListQuery{Limit: 1, Cursor: *page.NextCursor, LegacyRepresentable: true}, caps())
	expectCode(t, err, "CAMPAIGN_INVALID_INPUT")
}
