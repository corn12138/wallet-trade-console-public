package campaign

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

// pagedReader serves the given pages in order; every page but the last links
// to the next with a fresh cursor unless a test overrides it.
func pagedReader(t *testing.T, pages [][]application.PublicCampaignView) *publishedProbe {
	p := compatibilityReader(compatibilityView(t, "unused"))
	p.listFn = func(_ context.Context, q application.ListQuery) (application.ListResponse, error) {
		if !q.LegacyRepresentable {
			t.Error("the adapter must ask the source for legacy-representable rows")
		}
		index := 0
		if q.Cursor != "" {
			fmt.Sscanf(q.Cursor, "page-%d", &index)
		}
		if index >= len(pages) {
			return application.ListResponse{}, errors.New("read past the last page")
		}
		rows := pages[index]
		if len(rows) > q.Limit {
			rows = rows[:q.Limit]
		}
		page := compatibilityPage(rows...)
		if index+1 < len(pages) {
			next := fmt.Sprintf("page-%d", index+1)
			page.NextCursor = &next
		}
		return page, nil
	}
	return p
}

func paused(t *testing.T, id string) application.PublicCampaignView {
	view := compatibilityView(t, id)
	view.Mode = "paused"
	return view
}

func ids(rows []Campaign) string {
	out := []string{}
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return strings.Join(out, ",")
}

// The legacy list protocol (docs/campaign-legacy-list-protocol-2026-10-09.md):
// unrepresentable campaigns are skipped without consuming the limit, paging
// continues, and every way of not knowing the full prefix fails explicitly.
func TestLegacyListProtocolSkipsUnrepresentableAndKeepsPaging(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		pages [][]application.PublicCampaignView
		want  string
	}{
		{"hidden first item, enabled successor on next page", 1, [][]application.PublicCampaignView{{paused(t, "h1")}, {compatibilityView(t, "s1")}}, "s1"},
		{"whole first page hidden", 2, [][]application.PublicCampaignView{{paused(t, "h1"), paused(t, "h2")}, {compatibilityView(t, "s1"), compatibilityView(t, "s2")}}, "s1,s2"},
		{"several hidden pages", 1, [][]application.PublicCampaignView{{paused(t, "h1")}, {paused(t, "h2")}, {paused(t, "h3")}, {compatibilityView(t, "s1")}}, "s1"},
		{"order preserved around hidden items", 3, [][]application.PublicCampaignView{{compatibilityView(t, "s1"), paused(t, "h1"), compatibilityView(t, "s2")}, {compatibilityView(t, "s3")}}, "s1,s2,s3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, err := compatibilityService(&stubCampaignReader{}, pagedReader(t, c.pages)).list(t.Context(), "", c.limit, "")
			if err != nil || ids(rows) != c.want {
				t.Fatalf("rows=%s err=%v", ids(rows), err)
			}
		})
	}
}

func TestLegacyListProtocolMergesLegacyOnlyAfterVersionedSourceEnds(t *testing.T) {
	repo := &stubCampaignReader{rows: []Campaign{{ID: "legacy-a", Status: "active"}}, counts: map[string]int{"legacy-a": 2}}
	p := pagedReader(t, [][]application.PublicCampaignView{{paused(t, "h1"), compatibilityView(t, "s1")}})
	rows, err := compatibilityService(repo, p).list(t.Context(), "", 3, "")
	if err != nil || ids(rows) != "s1,legacy-a" || repo.limit != 2 {
		t.Fatalf("rows=%s limit=%d err=%v", ids(rows), repo.limit, err)
	}
	conflict := &stubCampaignReader{rows: []Campaign{{ID: "h1", Status: "active"}}, counts: map[string]int{"h1": 1}}
	_, err = compatibilityService(conflict, pagedReader(t, [][]application.PublicCampaignView{{paused(t, "h1")}})).list(t.Context(), "", 3, "")
	compatibilityError(t, err, 503)
}

func TestLegacyListProtocolFailsWhenThePrefixCannotBeKnown(t *testing.T) {
	endless := compatibilityReader(compatibilityView(t, "unused"))
	n := 0
	endless.listFn = func(_ context.Context, q application.ListQuery) (application.ListResponse, error) {
		n++
		page := compatibilityPage(paused(t, fmt.Sprintf("h%d", n)))
		next := fmt.Sprintf("page-%d", n)
		page.NextCursor = &next
		return page, nil
	}
	rows, err := compatibilityService(&stubCampaignReader{}, endless).list(t.Context(), "", 1, "")
	compatibilityError(t, err, 503)
	if rows != nil || n > maxListLimit {
		t.Fatalf("budget exhaustion must fail, not return a short list: rows=%v pages=%d", rows, n)
	}

	stuck := pagedReader(t, [][]application.PublicCampaignView{{paused(t, "h1")}, {compatibilityView(t, "s1")}})
	stuck.listFn = func(_ context.Context, q application.ListQuery) (application.ListResponse, error) {
		page := compatibilityPage(paused(t, "h1"))
		same := "page-0"
		page.NextCursor = &same
		if q.Cursor == "page-0" {
			page = compatibilityPage(paused(t, "h2"))
			page.NextCursor = &same
		}
		return page, nil
	}
	_, err = compatibilityService(&stubCampaignReader{}, stuck).list(t.Context(), "", 1, "")
	compatibilityError(t, err, 503)

	broken := pagedReader(t, [][]application.PublicCampaignView{{paused(t, "h1")}, {compatibilityView(t, "s1")}})
	inner := broken.listFn
	broken.listFn = func(ctx context.Context, q application.ListQuery) (application.ListResponse, error) {
		if q.Cursor != "" {
			return application.ListResponse{}, compatibilityUnavailable("dependency down")
		}
		return inner(ctx, q)
	}
	_, err = compatibilityService(&stubCampaignReader{}, broken).list(t.Context(), "", 1, "")
	compatibilityError(t, err, 503)
}
