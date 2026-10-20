package mobilebff

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type stubCampaignSource struct {
	rows        []CampaignInfo
	activeErr   error
	findErr     error
	activeCalls int
	findIDs     []string
}

func (s *stubCampaignSource) ActiveCampaigns(context.Context) ([]CampaignInfo, error) {
	s.activeCalls++
	return s.rows, s.activeErr
}

func (s *stubCampaignSource) FindCampaign(_ context.Context, id string) (CampaignInfo, bool, error) {
	s.findIDs = append(s.findIDs, id)
	if s.findErr != nil {
		return CampaignInfo{}, false, s.findErr
	}
	for _, row := range s.rows {
		if row.ID == id {
			return row, true, nil
		}
	}
	return CampaignInfo{}, false, nil
}

func TestCampaignDependencyFailuresReachHTTP(t *testing.T) {
	for _, cause := range []error{errors.New("database credentials must not reach response"), context.DeadlineExceeded, ErrNotFound, ErrLoginRequired, ErrBadRequest} {
		for _, tc := range []struct{ method, path, body, cache string }{
			{http.MethodGet, "/api/mobile/feed/home", "", "no-store"},
			{http.MethodGet, "/api/mobile/profile/home", "", "private, no-store"},
			{http.MethodGet, "/api/mobile/settings/index", "", "private, no-store"},
			{http.MethodGet, "/api/mobile/topic/campaign-id", "", "no-store"},
			{http.MethodPost, "/api/mobile/share/prepare", `{"resourceType":"campaign","resourceId":"campaign-id"}`, "no-store"},
		} {
			t.Run(tc.path+"/"+cause.Error(), func(t *testing.T) {
				source := &stubCampaignSource{activeErr: cause, findErr: cause}
				svc := NewService(newStore(), stubSession{}, source, nil)
				rec := req(t, router(svc), tc.method, tc.path, tc.body)
				if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"message\":\"campaign data unavailable\",\"statusCode\":503}\n" {
					t.Fatalf("dependency failure became %d %s", rec.Code, rec.Body.String())
				}
				if got := rec.Header().Get("Cache-Control"); got != tc.cache {
					t.Fatalf("cache = %q, want %q", got, tc.cache)
				}
			})
		}
	}
}

func TestCampaignSourceOptionalAndTrueNotFoundRemainDistinct(t *testing.T) {
	for _, source := range []CampaignSource{nil, &stubCampaignSource{}} {
		svc := NewService(newStore(), stubSession{}, source, nil)
		if _, err := svc.GetFeedHome(context.Background()); err != nil {
			t.Fatalf("optional or empty source: %v", err)
		}
		rec := req(t, router(svc), http.MethodPost, "/api/mobile/share/prepare", `{"resourceType":"campaign","resourceId":"missing"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("missing campaign = %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestCampaignShareIdentitySurvivesTitleChange(t *testing.T) {
	id := "campaign-id/with?reserved&characters"
	source := &stubCampaignSource{rows: []CampaignInfo{{ID: id, Title: "Original title"}}}
	svc := NewService(newStore(), stubSession{}, source, nil)
	request := SharePrepareRequest{ResourceType: "campaign", ResourceID: id}
	before, err := svc.PrepareShare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	source.rows[0].Title = "Renamed title"
	after, err := svc.PrepareShare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if before.URL != after.URL || after.Title != "Renamed title" || after.ResourceID != id {
		t.Fatalf("identity drift: before=%+v after=%+v", before, after)
	}
	outer, err := url.Parse(after.URL)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := url.Parse(outer.Query().Get("appUrl"))
	if err != nil || inner == nil || inner.Query().Get("id") != id || inner.Query().Has("slug") {
		t.Fatalf("share did not preserve stable ID: %+v, %v", inner, err)
	}
	if source.activeCalls != 0 || len(source.findIDs) != 2 || source.findIDs[0] != id {
		t.Fatalf("detail lookup depended on bounded active list: %+v", source)
	}
	_, err = svc.PrepareShare(context.Background(), SharePrepareRequest{ResourceType: "campaign", ResourceID: "renamed-title"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("mutable title became lookup identity: %v", err)
	}
}

func TestCampaignFeedAndTopicUseStableIdentity(t *testing.T) {
	source := &stubCampaignSource{rows: []CampaignInfo{{ID: "campaign-stable-id", Title: "Editable title", Description: "Campaign text"}}}
	svc := NewService(newStore(), stubSession{}, source, nil)
	feed, err := svc.GetFeedHome(context.Background())
	if err != nil || feed.WeeklyFocus.Slug != "campaign-stable-id" || !strings.Contains(feed.WeeklyFocus.PrimaryAction.AppURL, "slug=campaign-stable-id") {
		t.Fatalf("feed campaign identity: %+v, %v", feed.WeeklyFocus, err)
	}
	topic, err := svc.GetTopicLanding(context.Background(), "campaign-stable-id")
	if err != nil || topic.Title != "Editable title" || topic.Summary != "Campaign text" {
		t.Fatalf("campaign topic lookup: %+v, %v", topic, err)
	}
	if source.activeCalls != 1 || len(source.findIDs) != 1 || source.findIDs[0] != "campaign-stable-id" {
		t.Fatalf("topic must resolve stable ID directly: %+v", source)
	}
}
