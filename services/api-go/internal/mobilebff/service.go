package mobilebff

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// ErrLoginRequired → 401. ErrBadRequest → 400.
var (
	ErrLoginRequired = errors.New("login-required")
	ErrBadRequest    = errors.New("bad request")
)

// Session is the login snapshot the BFF needs (from the mobile control plane).
type Session struct {
	IsLoggedIn bool
	UserID     string
	Locale     string
	Theme      string
	Container  string
	AppVersion string
}

// SessionProvider resolves a request to a Session (adapter over mobilecontrol).
type SessionProvider interface {
	MobileSession(r *http.Request) Session
}

// CampaignInfo is the minimal campaign projection the BFF uses.
type CampaignInfo struct {
	ID          string
	Title       string
	Description string
	Banner      string
}

// CampaignSource is an optional adapter over the Go campaign service.
type CampaignSource interface {
	ActiveCampaigns(ctx context.Context) []CampaignInfo
	FindCampaign(ctx context.Context, id string) (CampaignInfo, bool)
}

// SettingsSource is an optional adapter over the Go settings service.
type SettingsSource interface {
	NotificationsEnabled(ctx context.Context, owner string) bool
	MarketAlerts(ctx context.Context, owner string) bool
}

// Service is the mobile-bff aggregation logic.
type Service struct {
	repo      Store
	session   SessionProvider
	campaigns CampaignSource // optional
	settings  SettingsSource // optional
}

// NewService wires the repo + providers. campaigns/settings may be nil.
func NewService(repo Store, session SessionProvider, campaigns CampaignSource, settings SettingsSource) *Service {
	return &Service{repo: repo, session: session, campaigns: campaigns, settings: settings}
}

// MobileSession exposes the session snapshot for a request.
func (s *Service) sessionFor(r *http.Request) Session {
	if s.session == nil {
		return Session{Locale: "zh-CN", Theme: "system", Container: "browser"}
	}
	return s.session.MobileSession(r)
}

func (s *Service) activeCampaigns(ctx context.Context) []CampaignInfo {
	if s.campaigns == nil {
		return nil
	}
	return s.campaigns.ActiveCampaigns(ctx)
}

// ---- feed/home -------------------------------------------------------------

func (s *Service) GetFeedHome(ctx context.Context) FeedHomeResponse {
	articles, _ := s.repo.FeedArticles(ctx, 6)
	campaigns := s.activeCampaigns(ctx)
	tags, _ := s.repo.TopTags(ctx, 6)
	sortedTags := sortTagsByCount(tags)

	articleCards := mapArticleCards(s, articles)
	heroArticle := s.createFallbackArticleCard("quietude-001")
	if len(articleCards) > 0 {
		heroArticle = articleCards[0]
	}
	insightCards := sliceCards(articleCards, 1, 4)
	if len(insightCards) == 0 {
		insightCards = []Card{s.createFallbackArticleCard("quietude-002"), s.createFallbackArticleCard("quietude-003")}
	}

	var focusTitle, focusSummary string
	if len(campaigns) > 0 {
		focusTitle = campaigns[0].Title
		focusSummary = campaigns[0].Description
	}
	if focusTitle == "" {
		focusTitle = "The Scholar Path"
	}
	if focusSummary == "" {
		focusSummary = "A guided sequence across reading, annotation, and reflective writing surfaces."
	}
	chips := []string{}
	for _, t := range firstTags(sortedTags, 3) {
		chips = append(chips, t.Name)
	}
	weeklyFocus := WeeklyFocus{
		Slug:            defaultTopicSlug,
		Title:           focusTitle,
		Summary:         focusSummary,
		Chips:           chips,
		PrimaryAction:   Action{Label: "BEGIN PATH", AppURL: "app://topic/landing?slug=" + defaultTopicSlug, RuntimeHint: "web"},
		SecondaryAction: &Action{Label: "VIEW SYLLABUS", AppURL: "app://topic/landing?slug=" + defaultTopicSlug, RuntimeHint: "web"},
	}

	mixedGrid := sliceCards(articleCards, 4, 5)
	mixedGrid = append(mixedGrid, defaultVideoResults()[0])
	for _, t := range firstTags(sortedTags, 1) {
		mixedGrid = append(mixedGrid, s.topicCardFromTag(t, "topic:", fmt.Sprintf("%d archived essays", t.ArticleCount),
			"A living path of related essays, notes, and topical guidance."))
	}
	mixedGrid = truncateCards(mixedGrid, 3)

	trendingTopics := []Card{}
	for _, t := range firstTags(sortedTags, 3) {
		trendingTopics = append(trendingTopics, s.topicCardFromTag(t, "tag:", fmt.Sprintf("%d essays", t.ArticleCount),
			"Curated topic path available in the first-party web runtime."))
	}

	if len(mixedGrid) == 0 {
		mixedGrid = []Card{defaultVideoResults()[0], defaultSearchTopicCards()[0]}
	}
	if len(trendingTopics) == 0 {
		trendingTopics = defaultSearchTopicCards()
	}

	return FeedHomeResponse{
		GeneratedAt:    isoNow(),
		HeroArticle:    heroArticle,
		InsightCards:   insightCards,
		WeeklyFocus:    weeklyFocus,
		MixedGrid:      mixedGrid,
		TrendingTopics: trendingTopics,
	}
}

// ---- article/:id -----------------------------------------------------------

func (s *Service) GetArticleDetail(ctx context.Context, articleID string) (ArticleDetailResponse, error) {
	article, err := s.repo.ArticleByID(ctx, articleID)
	if err != nil {
		return ArticleDetailResponse{}, err
	}
	commentsPreview := s.commentSheetInternal(ctx, nil, articleID, 3)
	related, _ := s.repo.RelatedArticles(ctx, articleID, 3)

	card := s.toArticleCard(article)
	content := ""
	if article.Content != nil {
		content = *article.Content
	}
	if content == "" && article.Summary != nil {
		content = *article.Summary
	}
	publishedAt := ""
	if article.PublishedAt != nil {
		publishedAt = article.PublishedAt.UTC().Format(isoLayout)
	} else {
		publishedAt = article.CreatedAt.UTC().Format(isoLayout)
	}

	relatedCards := mapArticleCards(s, related)
	if len(relatedCards) == 0 {
		relatedCards = []Card{s.createFallbackArticleCard("quietude-002")}
	}

	quote := ""
	if article.Summary != nil && *article.Summary != "" {
		quote = *article.Summary
	} else {
		quote = s.extractQuote(content)
	}

	commentCount := article.CommentCount
	if commentCount == 0 {
		commentCount = commentsPreview.Total
	}

	return ArticleDetailResponse{
		GeneratedAt: isoNow(),
		Article: ArticleDetailArticle{
			Card:            card,
			Content:         content,
			ReadTimeMinutes: s.computeReadTimeMinutes(content),
			PublishedAt:     publishedAt,
			Author:          s.authorFromArticle(article),
			Stats:           ArticleDetailStats{ViewCount: article.ViewCount, CommentCount: commentCount},
		},
		Quote:           quote,
		RelatedArticles: relatedCards,
		CommentsPreview: CommentsPreview{
			ArticleID: commentsPreview.ArticleID,
			Title:     commentsPreview.Title,
			Subtitle:  commentsPreview.Subtitle,
			Total:     commentsPreview.Total,
			Comments:  commentsPreview.Comments,
		},
	}, nil
}

// ---- comment/sheet + comment/create ----------------------------------------

func (s *Service) GetCommentSheet(ctx context.Context, r *http.Request, articleID string) CommentSheetResponse {
	sess := s.sessionFor(r)
	return s.commentSheetInternal(ctx, &sess, articleID, -1)
}

func (s *Service) commentSheetInternal(ctx context.Context, sess *Session, articleID string, previewCount int) CommentSheetResponse {
	title, articleCommentCount, found := s.repo.ArticleTitleAndCommentCount(ctx, articleID)
	comments, _ := s.repo.CommentsByArticle(ctx, articleID)
	tree := s.buildCommentTree(comments)
	if previewCount >= 0 && previewCount < len(tree) {
		tree = tree[:previewCount]
	}
	loggedIn := false
	if sess != nil {
		loggedIn = sess.IsLoggedIn
	}
	total := int64(len(comments))
	if found {
		total = articleCommentCount
	}
	sheetTitle := "Comments"
	if found && title != "" {
		sheetTitle = "Reflections"
	}
	subtitle := "Be the first to respond"
	if found && articleCommentCount > 0 {
		subtitle = fmt.Sprintf("%d Responses from the Academy", articleCommentCount)
	}
	return CommentSheetResponse{
		ArticleID: articleID,
		Title:     sheetTitle,
		Subtitle:  subtitle,
		Total:     total,
		Comments:  tree,
		Composer:  CommentSheetComposer{Placeholder: "Add a reflection to the archive...", RequiresLogin: !loggedIn},
	}
}

func (s *Service) CreateComment(ctx context.Context, r *http.Request, articleID, parentID, content string) (CommentCreateResponse, error) {
	sess := s.sessionFor(r)
	if sess.UserID == "" {
		return CommentCreateResponse{}, ErrLoginRequired
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return CommentCreateResponse{}, fmt.Errorf("%w: Comment content is required", ErrBadRequest)
	}
	published, found := s.repo.ArticlePublished(ctx, articleID)
	if !found || !published {
		return CommentCreateResponse{}, ErrNotFound
	}
	var parent *string
	if parentID != "" {
		parentArticleID, ok := s.repo.CommentArticleID(ctx, parentID)
		if !ok || parentArticleID != articleID {
			return CommentCreateResponse{}, fmt.Errorf("%w: Parent comment does not belong to the target article", ErrBadRequest)
		}
		parent = &parentID
	}
	comment, err := s.repo.CreateComment(ctx, articleID, sess.UserID, parent, content)
	if err != nil {
		return CommentCreateResponse{}, err
	}
	total, _ := s.repo.CommentCount(ctx, articleID)
	return CommentCreateResponse{
		Comment:           s.serializeComment(comment),
		Total:             total,
		PendingModeration: false,
	}, nil
}

// ---- search/index ----------------------------------------------------------

func (s *Service) GetSearchIndex(ctx context.Context, query string, limit int) SearchIndexResponse {
	limit = normalizeSearchResultLimit(limit)
	normalized := strings.TrimSpace(query)
	var articles []ArticleRow
	var tags []TagRow
	var users []UserRow
	select {
	case searchQuerySlots <- struct{}{}:
		func() {
			defer func() { <-searchQuerySlots }()
			queryCtx, cancel := context.WithTimeout(ctx, searchQueryTimeout)
			defer cancel()
			articles, _ = s.repo.SearchArticles(queryCtx, normalized, limit)
			tags, _ = s.repo.TopTags(queryCtx, limit)
			users, _ = s.repo.TopUsers(queryCtx, limit)
		}()
	default:
		// Existing static fallbacks below keep the endpoint useful while the
		// database fan-out is saturated by other anonymous searches.
	}

	sortedTags := sortTagsByCount(tags)
	trendingPaths := []Card{}
	for _, t := range firstTags(sortedTags, limit) {
		trendingPaths = append(trendingPaths, s.topicCardFromTag(t, "topic:", fmt.Sprintf("%d scholars following", t.ArticleCount),
			"A curated thematic path spanning essays, discussion, and video."))
	}
	if len(trendingPaths) == 0 {
		trendingPaths = truncateCards(defaultSearchTopicCards(), limit)
	}

	articleResults := mapArticleCards(s, articles)
	if len(articleResults) == 0 {
		articleResults = []Card{s.createFallbackArticleCard("quietude-001")}
	}

	videoLimit := max(1, min(limit, 2))
	videoResults := truncateCards(defaultVideoResults(), videoLimit)

	creators := []CreatorResult{}
	for _, u := range users {
		if u.ArticleCount <= 0 {
			continue
		}
		if len(creators) >= limit {
			break
		}
		creators = append(creators, CreatorResult{Author: s.authorFromUser(u, ""), AppURL: "app://profile/home"})
	}
	if len(creators) == 0 {
		creators = []CreatorResult{{
			Author: Author{Name: "Chen Liang", Role: "Master Artisan", AvatarURL: defaultProfileAvatarURL},
			AppURL: "app://profile/home",
		}}
	}

	return SearchIndexResponse{
		GeneratedAt:    isoNow(),
		Query:          normalized,
		QuickAccess:    defaultSearchQuickAccess,
		RecentHistory:  defaultSearchHistory,
		TrendingPaths:  trendingPaths,
		ArticleResults: articleResults,
		VideoResults:   videoResults,
		CreatorResults: creators,
	}
}

// ---- message/center + message/read -----------------------------------------

func (s *Service) GetMessageCenter(ctx context.Context, r *http.Request) MessageCenterResponse {
	sess := s.sessionFor(r)
	var conversations []ConversationRow
	var unreadCount int64
	if sess.UserID != "" {
		conversations, _ = s.repo.ConversationsWithLatest(ctx, sess.UserID, 3)
		unreadCount, _ = s.repo.UnreadIncomingCount(ctx, sess.UserID)
	}

	items := []Card{}
	if len(conversations) > 0 {
		for index, c := range conversations {
			isAssistant := c.LatestRole != nil && *c.LatestRole == "ASSISTANT"
			subtitle := "DIRECT MESSAGE"
			if isAssistant {
				subtitle = "SCHOLARLY REPLY"
			}
			desc := "A new correspondence thread is ready for review."
			if c.LatestContent != nil && *c.LatestContent != "" {
				desc = *c.LatestContent
			}
			title := c.Title
			if title == "" {
				title = "Untitled Correspondence"
			}
			appURL := "app://article/detail?id=quietude-001"
			hint := "flutter"
			if index != 0 {
				appURL = "app://video/feed?id=reference-scroll"
				hint = "native_media"
			}
			updated := c.UpdatedAt
			if updated.IsZero() {
				updated = c.CreatedAt
			}
			items = append(items, Card{
				ID: c.ID, Title: title, Subtitle: subtitle, Description: desc,
				ImageURL: defaultAvatarURL, Eyebrow: s.toRelativeTime(&updated),
				AppURL: appURL, RuntimeHint: hint,
			})
		}
	} else {
		items = []Card{
			{ID: "msg-fallback-1", Title: `Dr. Elena Thorne commented on "The Echoes of Han"`, Subtitle: "PEER REVIEW REPLY",
				Description: "Your analysis offers a compelling perspective. Several timeline notes have been suggested.",
				ImageURL:    defaultAvatarURL, Eyebrow: "2h ago", AppURL: "app://article/detail?id=quietude-001", RuntimeHint: "flutter"},
			{ID: "msg-fallback-2", Title: "Chronicle Keeper Certification", Subtitle: "ARCHIVE ACHIEVEMENT",
				Description: "You have successfully cataloged 100 manuscripts. Status updated to Senior Archivist.",
				ImageURL:    defaultProfileAvatarURL, Eyebrow: "Yesterday", AppURL: "app://topic/landing?slug=" + defaultTopicSlug, RuntimeHint: "web"},
		}
	}

	directUnread, replyUnread := 0, 0
	for _, c := range conversations {
		isAssistant := c.LatestRole != nil && *c.LatestRole == "ASSISTANT"
		isUnread := c.LatestReadAt == nil && (c.LatestUserID == nil || *c.LatestUserID != sess.UserID)
		if !isUnread {
			continue
		}
		if isAssistant {
			replyUnread++
		} else {
			directUnread++
		}
	}

	summaryUnread := int(unreadCount)
	if summaryUnread == 0 {
		summaryUnread = len(items)
	}
	statusLabel := "Guest Scholar"
	if sess.IsLoggedIn {
		statusLabel = "Level IV Scholar"
	}
	filters := []MessageCenterFilter{
		{ID: "all", Label: "All Updates", Active: true},
		{ID: "direct", Label: "Direct Messages"},
		{ID: "reply", Label: "Scholarly Replies"},
		{ID: "system", Label: "System Alerts"},
		{ID: "archived", Label: "Archived"},
	}
	if directUnread > 0 {
		filters[1].Badge = fmt.Sprintf("%d", directUnread)
	}
	if replyUnread > 0 {
		filters[2].Badge = fmt.Sprintf("%d", replyUnread)
	}

	return MessageCenterResponse{
		GeneratedAt: isoNow(),
		Summary:     MessageCenterSummary{UnreadCount: summaryUnread, InquiryCtaLabel: "NEW INQUIRY", StatusLabel: statusLabel},
		Filters:     filters,
		Items:       items,
		Recommendations: []Card{
			{ID: "recommendation-topic", Title: "Research Circle", Subtitle: "New discussion in Silk Road Cartography",
				Description: "A topic-driven thread ready in the first-party web runtime.",
				AppURL:      "app://topic/landing?slug=" + defaultTopicSlug, RuntimeHint: "web"},
			defaultVideoResults()[1],
		},
	}
}

func (s *Service) MarkMessagesRead(ctx context.Context, r *http.Request, req MessageReadRequest) (MessageReadResponse, error) {
	sess := s.sessionFor(r)
	if sess.UserID == "" {
		return MessageReadResponse{}, ErrLoginRequired
	}
	messageIDs := dedupeNonEmpty(req.MessageIDs)
	conversationIDs := dedupeNonEmpty(req.ConversationIDs)
	ids, _ := s.repo.FindUnreadMessageIDs(ctx, sess.UserID, messageIDs, conversationIDs)
	targetIDs := dedupeNonEmpty(ids)
	if len(targetIDs) > 0 {
		if err := s.repo.MarkMessagesRead(ctx, targetIDs); err != nil {
			return MessageReadResponse{}, err
		}
	}
	unread, _ := s.repo.UnreadIncomingCount(ctx, sess.UserID)
	return MessageReadResponse{UpdatedCount: len(targetIDs), UnreadCount: unread}, nil
}

// ---- profile/home ----------------------------------------------------------

func (s *Service) GetProfileHome(ctx context.Context, r *http.Request) ProfileHomeResponse {
	sess := s.sessionFor(r)
	var user *UserRow
	var authored []ArticleRow
	if sess.UserID != "" {
		if u, err := s.repo.UserProfile(ctx, sess.UserID); err == nil {
			user = &u
			authored, _ = s.repo.AuthoredArticles(ctx, sess.UserID, 4)
		}
	}

	feed := s.GetFeedHome(ctx)
	authoredCards := mapArticleCards(s, authored)
	featuredArticle := feed.HeroArticle
	if len(authoredCards) > 0 {
		featuredArticle = authoredCards[0]
	}
	archive := sliceCards(feed.InsightCards, 0, 3)
	if len(authoredCards) > 1 {
		archive = sliceCards(authoredCards, 1, 4)
	}

	accessLabel := "Guest Access"
	role := "Guest Scholar"
	if sess.IsLoggedIn {
		accessLabel = "Scholar Access"
		role = "Scholar in Residence"
	}
	name := "Guest Scholar"
	avatar := defaultProfileAvatarURL
	headline := "Building a personal archive that balances longform reading, annotations, and quiet study."
	var articleStat, commentStat, convoStat any = len(archive), 0, 0
	author := Author{Name: name, Role: role, AvatarURL: avatar}
	if user != nil {
		author = s.authorFromUser(*user, role)
		name = author.Name
		if user.Avatar != nil && *user.Avatar != "" {
			avatar = *user.Avatar
		}
		if user.Bio != nil && *user.Bio != "" {
			headline = *user.Bio
		}
		articleStat = user.ArticleCount
		commentStat = user.CommentCount
		convoStat = user.ConversationCount
	}
	author.Name = name
	author.AvatarURL = avatar

	return ProfileHomeResponse{
		GeneratedAt: isoNow(),
		AccessLabel: accessLabel,
		Profile: ProfileBlock{
			Author:   author,
			Headline: headline,
			Stats: []ProfileStat{
				{Label: "Published Essays", Value: articleStat},
				{Label: "Annotations", Value: commentStat},
				{Label: "Dialogues", Value: convoStat},
			},
			SettingsAction: &Action{Label: "OPEN SETTINGS", AppURL: "app://settings/index", RuntimeHint: "flutter"},
		},
		FeaturedArticle: featuredArticle,
		FeaturedVideo:   defaultVideoResults()[0],
		Archive:         archive,
	}
}

// ---- settings/index --------------------------------------------------------

func (s *Service) GetSettingsIndex(ctx context.Context, r *http.Request, ownerAddress string) SettingsIndexResponse {
	sess := s.sessionFor(r)
	profile := s.GetProfileHome(ctx, r)
	notificationsEnabled := false
	marketAlerts := false
	if s.settings != nil {
		notificationsEnabled = s.settings.NotificationsEnabled(ctx, ownerAddress)
		marketAlerts = s.settings.MarketAlerts(ctx, ownerAddress)
	}

	versionLabel := sess.AppVersion
	if versionLabel == "" {
		versionLabel = os.Getenv("APP_VERSION")
	}
	if versionLabel == "" {
		versionLabel = "2.4.0"
	}
	securitySubtitle := "Guest mode"
	if sess.IsLoggedIn {
		securitySubtitle = "Authenticated session detected"
	}
	ownerSubtitle := "Not linked"
	if ownerAddress != "" {
		ownerSubtitle = ownerAddress
	}
	digest := "Manual"
	if marketAlerts {
		digest = "Daily"
	}

	return SettingsIndexResponse{
		GeneratedAt:  isoNow(),
		VersionLabel: "DIGITAL INKSTONE VERSION " + versionLabel,
		Sections: []SettingsSection{
			{ID: "account-security", Title: "Account & Security", Items: []SettingsSectionItem{
				{ID: "scholar-identity", Title: "Scholar Identity", Subtitle: profile.Profile.Name, Icon: "person_outline_rounded"},
				{ID: "security-credentials", Title: "Security Credentials", Subtitle: securitySubtitle, Icon: "verified_user_outlined"},
			}},
			{ID: "reading-experience", Title: "Reading Experience", Badge: "SIGNATURE", Items: []SettingsSectionItem{
				{ID: "theme", Title: "Theme", Subtitle: "Resolved from session context", Value: sess.Theme, Icon: "auto_stories_rounded"},
				{ID: "typography", Title: "Typography Preference", Subtitle: "Signature serif layout for longform reading", Value: "Noto Serif", Icon: "format_size_rounded"},
			}},
			{ID: "notifications", Title: "Notifications", Items: []SettingsSectionItem{
				{ID: "notifications-enabled", Title: "Scholarly Alerts", Subtitle: "Archival updates and mentions", Value: notificationsEnabled, Icon: "notifications_active_outlined"},
				{ID: "market-alerts", Title: "Digest Frequency", Subtitle: "Security, product, and peer review alerts", Value: digest, Icon: "schedule_rounded"},
			}},
			{ID: "privacy-archive", Title: "Privacy & Archive", Items: []SettingsSectionItem{
				{ID: "owner-address", Title: "Linked Owner Address", Subtitle: ownerSubtitle, Icon: "visibility_off_outlined"},
				{ID: "data-export", Title: "Data Export", Subtitle: "Prepare personal archive and preference bundle", Value: "Ready", Icon: "download_rounded"},
			}},
		},
		Actions: []Action{{Label: "RETURN TO PROFILE", AppURL: "app://profile/home", RuntimeHint: "flutter"}},
	}
}

// ---- topic/:slug -----------------------------------------------------------

func (s *Service) GetTopicLanding(ctx context.Context, slug string) TopicLandingResponse {
	tag, tagArticles, tagFound := s.repo.TagWithArticles(ctx, slug, 3)
	campaigns := s.activeCampaigns(ctx)
	fallbackArticles, _ := s.repo.FeedArticles(ctx, 3)

	var campaign *CampaignInfo
	for i := range campaigns {
		if normalizeSlug(campaigns[i].Title) == slug {
			campaign = &campaigns[i]
			break
		}
	}

	sourceArticles := fallbackArticles
	if tagFound && len(tagArticles) > 0 {
		sourceArticles = tagArticles
	}
	featured := mapArticleCards(s, sourceArticles)

	title := humanizeSlug(slug)
	summary := "A first-party web runtime topic surface that groups essays, sequences, and companion media."
	hero := defaultTopicHeroImageURL
	if campaign != nil {
		if campaign.Title != "" {
			title = campaign.Title
		}
		if campaign.Description != "" {
			summary = campaign.Description
		}
		if campaign.Banner != "" {
			hero = campaign.Banner
		}
	} else if tagFound {
		if tag.Name != "" {
			title = tag.Name
		}
		if tag.Description != nil && *tag.Description != "" {
			summary = *tag.Description
		}
	}

	if len(featured) == 0 {
		featured = []Card{s.createFallbackArticleCard("quietude-001")}
	}
	secondaryAppURL := "app://article/detail?id=quietude-001"
	secondaryHint := "flutter"
	if len(featured) > 0 {
		if featured[0].AppURL != "" {
			secondaryAppURL = featured[0].AppURL
		}
		if featured[0].RuntimeHint != "" {
			secondaryHint = featured[0].RuntimeHint
		}
	}
	video := defaultVideoResults()[0]

	return TopicLandingResponse{
		GeneratedAt:      isoNow(),
		Slug:             slug,
		Title:            title,
		Summary:          summary,
		HeroImageURL:     hero,
		Syllabus:         defaultSyllabus,
		FeaturedArticles: featured,
		FeaturedVideo:    &video,
		PrimaryAction:    Action{Label: "OPEN TOPIC", AppURL: "app://topic/landing?slug=" + slug, RuntimeHint: "web"},
		SecondaryAction:  &Action{Label: "READ LEAD ESSAY", AppURL: secondaryAppURL, RuntimeHint: secondaryHint},
	}
}

// ---- video/feed + video/:id ------------------------------------------------

func (s *Service) GetVideoFeed(cursor string, limit int) VideoFeedResponse {
	all := s.buildVideoFeedItems()
	if limit <= 0 {
		limit = 4
	}
	limit = max(1, min(limit, 8))
	start := 0
	if cursor != "" {
		for i, item := range all {
			if item.ID == cursor {
				start = i + 1
				break
			}
		}
		if start > len(all) {
			start = len(all)
		}
	}
	end := min(start+limit, len(all))
	items := all[start:end]
	resp := VideoFeedResponse{
		GeneratedAt:     isoNow(),
		AutoplayEnabled: true,
		MutedByDefault:  false,
		Items:           items,
	}
	if start+limit < len(all) && end > 0 {
		resp.NextCursor = all[end-1].ID
	}
	return resp
}

func (s *Service) GetVideoDetail(videoID string) VideoDetailResponse {
	items := s.buildVideoFeedItems()
	var video VideoFeedItem
	found := false
	for _, item := range items {
		if item.ID == videoID {
			video = item
			found = true
			break
		}
	}
	if !found {
		video = s.createFallbackVideoItem(videoID, 0, nil)
	}
	body := video.Description
	if body == "" {
		body = "A native-media runtime entry prepared for immersive playback and companion reading."
	}
	nextUp := []Card{}
	for _, item := range items {
		if item.ID == video.ID {
			continue
		}
		if len(nextUp) >= 2 {
			break
		}
		nextUp = append(nextUp, s.toVideoCard(item))
	}
	return VideoDetailResponse{
		GeneratedAt: isoNow(),
		Video: VideoDetailVideo{
			VideoFeedItem: video,
			Body:          body,
			PublishedAt:   isoNow(),
			Transcript:    "Transcript preview for " + video.Title + ".",
		},
		NextUp: nextUp,
	}
}

// ---- share/prepare ---------------------------------------------------------

func (s *Service) PrepareShare(ctx context.Context, req SharePrepareRequest) (SharePrepareResponse, error) {
	tracking := map[string]any{}
	if req.SurfaceID != "" {
		tracking["surfaceId"] = req.SurfaceID
	}
	if req.Source != "" {
		tracking["source"] = req.Source
	}

	switch req.ResourceType {
	case "article":
		article, err := s.repo.ArticleByID(ctx, req.ResourceID)
		if err != nil {
			return SharePrepareResponse{}, ErrNotFound
		}
		text := ""
		if article.Summary != nil && *article.Summary != "" {
			text = *article.Summary
		} else if article.Content != nil {
			text = s.extractQuote(*article.Content)
		}
		img := defaultArticleImageURL
		if article.FeaturedImage != nil && *article.FeaturedImage != "" {
			img = *article.FeaturedImage
		}
		return SharePrepareResponse{ResourceType: "article", ResourceID: article.ID, Title: article.Title,
			Text: text, URL: s.buildShareURL("app://article/detail?id=" + article.ID), ImageURL: img, Tracking: tracking}, nil
	case "video":
		var video VideoFeedItem
		found := false
		for _, item := range s.buildVideoFeedItems() {
			if item.ID == req.ResourceID {
				video = item
				found = true
				break
			}
		}
		if !found {
			video = s.createFallbackVideoItem(req.ResourceID, 0, nil)
		}
		vt := map[string]any{}
		for k, v := range tracking {
			vt[k] = v
		}
		vt["runtimeHint"] = video.RuntimeHint
		return SharePrepareResponse{ResourceType: "video", ResourceID: video.ID, Title: video.Title,
			Text: video.Description, URL: s.buildShareURL("app://video/detail?id=" + video.ID), ImageURL: video.ImageURL, Tracking: vt}, nil
	case "topic":
		tag, ok := s.repo.TagForShare(ctx, req.ResourceID)
		slug := normalizeSlug(req.ResourceID)
		title := humanizeSlug(defaultTopicSlug)
		text := "A curated scholarly path spanning essays, annotations, and companion media."
		resourceID := req.ResourceID
		if ok {
			resourceID = tag.ID
			if tag.Slug != nil && *tag.Slug != "" {
				slug = *tag.Slug
			} else if tag.Name != "" {
				slug = normalizeSlug(tag.Name)
			}
			if tag.Name != "" {
				title = tag.Name
			}
			if tag.Description != nil && *tag.Description != "" {
				text = *tag.Description
			}
		}
		if slug == "" {
			slug = defaultTopicSlug
		}
		if !ok {
			title = humanizeSlug(slug)
		}
		return SharePrepareResponse{ResourceType: "topic", ResourceID: resourceID, Title: title,
			Text: text, URL: s.buildShareURL("app://topic/landing?slug=" + slug), Tracking: tracking}, nil
	case "campaign":
		campaign, ok := s.findCampaignForShare(ctx, req.ResourceID)
		if !ok {
			return SharePrepareResponse{}, ErrNotFound
		}
		slug := normalizeSlug(campaign.Title)
		text := "Campaign update prepared for cross-runtime sharing."
		if campaign.Description != "" {
			text = campaign.Description
		}
		resp := SharePrepareResponse{ResourceType: "campaign", ResourceID: campaign.ID, Title: campaign.Title,
			Text: text, URL: s.buildShareURL("app://campaign/detail?slug=" + slug), Tracking: tracking}
		if campaign.Banner != "" {
			resp.ImageURL = campaign.Banner
		}
		return resp, nil
	default: // "link" and anything else
		text := "Cross-runtime link prepared for sharing."
		if req.Source != "" {
			text = "Shared from " + req.Source
		}
		return SharePrepareResponse{ResourceType: "link", ResourceID: req.ResourceID, Title: "Shared Link",
			Text: text, URL: s.normalizeLinkTarget(req.ResourceID), Tracking: tracking}, nil
	}
}

func (s *Service) findCampaignForShare(ctx context.Context, resourceID string) (CampaignInfo, bool) {
	for _, c := range s.activeCampaigns(ctx) {
		if c.ID == resourceID || normalizeSlug(c.Title) == resourceID {
			return c, true
		}
	}
	if s.campaigns != nil {
		return s.campaigns.FindCampaign(ctx, resourceID)
	}
	return CampaignInfo{}, false
}

// ---- helpers ---------------------------------------------------------------

const isoLayout = "2006-01-02T15:04:05.000Z07:00"

func isoNow() string { return time.Now().UTC().Format(isoLayout) }

var whitespaceRE = regexp.MustCompile(`\s+`)

func (s *Service) toArticleCard(a ArticleRow) Card {
	subtitle := "Editorial"
	if a.CategoryName != nil && *a.CategoryName != "" {
		subtitle = *a.CategoryName
	} else if a.AuthorFullName != nil && *a.AuthorFullName != "" {
		subtitle = *a.AuthorFullName
	} else if a.AuthorUsername != nil && *a.AuthorUsername != "" {
		subtitle = *a.AuthorUsername
	}
	content := ""
	if a.Content != nil {
		content = *a.Content
	}
	description := "No summary available yet."
	if a.Summary != nil && *a.Summary != "" {
		description = *a.Summary
	} else if q := s.extractQuote(content); q != "" {
		description = q
	}
	img := defaultArticleImageURL
	if a.FeaturedImage != nil && *a.FeaturedImage != "" {
		img = *a.FeaturedImage
	}
	eyebrow := s.toRelativeTime(&a.CreatedAt)
	if a.PublishedAt != nil {
		eyebrow = s.toRelativeTime(a.PublishedAt)
	}
	readSource := content
	if readSource == "" && a.Summary != nil {
		readSource = *a.Summary
	}
	return Card{
		ID: a.ID, Title: a.Title, Subtitle: subtitle, Description: description, ImageURL: img,
		Eyebrow: eyebrow, Meta: fmt.Sprintf("%d min read", s.computeReadTimeMinutes(readSource)),
		Tags: a.Tags, AppURL: "app://article/detail?id=" + a.ID, RuntimeHint: "flutter",
	}
}

func (s *Service) authorFromArticle(a ArticleRow) Author {
	return authorFrom(a.AuthorID, a.AuthorUsername, a.AuthorFullName, a.AuthorAvatar, a.AuthorBio, "")
}

func (s *Service) authorFromUser(u UserRow, fallbackRole string) Author {
	id := u.ID
	return authorFrom(&id, &u.Username, u.FullName, u.Avatar, u.Bio, fallbackRole)
}

func authorFrom(id, username, fullName, avatar, bio *string, fallbackRole string) Author {
	name := "Guest Scholar"
	if fullName != nil && *fullName != "" {
		name = *fullName
	} else if username != nil && *username != "" {
		name = *username
	}
	role := fallbackRole
	if role == "" {
		if bio != nil && *bio != "" {
			role = *bio
		} else {
			role = "Scholar"
		}
	}
	avatarURL := defaultAvatarURL
	if avatar != nil && *avatar != "" {
		avatarURL = *avatar
	}
	a := Author{Name: name, Role: role, AvatarURL: avatarURL}
	if id != nil {
		a.ID = *id
	}
	if bio != nil && *bio != "" {
		a.Bio = *bio
	}
	return a
}

func (s *Service) createFallbackArticleCard(id string) Card {
	if id == "" {
		id = "quietude-001"
	}
	return Card{
		ID: id, Title: "The Art of Quietude: Finding Silence in a Digital Age", Subtitle: "Philosophy",
		Description: "A design-aligned fallback essay used until mobile BFF content is fully populated.",
		ImageURL:    defaultArticleImageURL, Eyebrow: "Editorial", Meta: "12 min read",
		Tags: []string{"Quietude", "Design System"}, AppURL: "app://article/detail?id=" + id, RuntimeHint: "flutter",
	}
}

func (s *Service) serializeComment(c CommentRow) Comment {
	return Comment{
		ID:          c.ID,
		Author:      authorFrom(c.AuthorID, c.AuthorUsername, c.AuthorFullName, c.AuthorAvatar, c.AuthorBio, "Scholar"),
		Body:        c.Content,
		Timestamp:   s.toRelativeTime(&c.CreatedAt),
		LikeCount:   0,
		Highlighted: false,
		Replies:     []Comment{},
	}
}

func (s *Service) buildCommentTree(comments []CommentRow) []Comment {
	children := map[string][]CommentRow{}
	topLevel := []CommentRow{}
	for _, c := range comments {
		if c.ParentID != nil && *c.ParentID != "" {
			children[*c.ParentID] = append(children[*c.ParentID], c)
			continue
		}
		topLevel = append(topLevel, c)
	}
	var serialize func(c CommentRow) Comment
	serialize = func(c CommentRow) Comment {
		out := s.serializeComment(c)
		replies := []Comment{}
		for _, child := range children[c.ID] {
			replies = append(replies, serialize(child))
		}
		out.Replies = replies
		return out
	}
	out := []Comment{}
	for _, c := range topLevel {
		out = append(out, serialize(c))
	}
	return out
}

func (s *Service) computeReadTimeMinutes(content string) int {
	words := len(strings.Fields(strings.TrimSpace(content)))
	if words == 0 {
		return 1
	}
	return max(1, int(float64(words)/220.0+0.5))
}

func (s *Service) extractQuote(content string) string {
	normalized := strings.TrimSpace(whitespaceRE.ReplaceAllString(content, " "))
	if normalized == "" {
		return ""
	}
	if len(normalized) > 180 {
		return normalized[:180]
	}
	return normalized
}

func (s *Service) toRelativeTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "just now"
	}
	diff := time.Since(*value)
	mins := int(diff.Minutes())
	if mins < 0 {
		mins = 0
	}
	if mins < 60 {
		return fmt.Sprintf("%dm ago", max(1, mins))
	}
	hours := mins / 60
	if hours < 24 {
		return fmt.Sprintf("%dh ago", hours)
	}
	days := hours / 24
	if days == 1 {
		return "Yesterday"
	}
	return fmt.Sprintf("%dd ago", days)
}

func (s *Service) buildVideoFeedItems() []VideoFeedItem {
	cards := defaultVideoResults()
	out := make([]VideoFeedItem, 0, len(cards))
	for i, card := range cards {
		c := card
		out = append(out, s.createFallbackVideoItem(card.ID, i, &c))
	}
	return out
}

func (s *Service) createFallbackVideoItem(id string, index int, sourceCard *Card) VideoFeedItem {
	base := Card{}
	if sourceCard != nil {
		base = *sourceCard
	} else {
		results := defaultVideoResults()
		base = results[index%len(results)]
	}
	base.ID = id
	base.AppURL = "app://video/detail?id=" + id
	base.RuntimeHint = "native_media"
	authorName := "Curator Lin"
	if index%2 == 0 {
		authorName = "Archivist Shen"
	}
	return VideoFeedItem{
		Card: base,
		Author: VideoAuthor{ID: fmt.Sprintf("video-author-%d", index+1), Name: authorName,
			Role: "Visual Essayist", AvatarURL: defaultAvatarURL},
		Playback: VideoPlayback{StreamURL: fmt.Sprintf("%s/media/video/%s.mp4", s.getFirstPartyOrigin(), id),
			PosterURL: base.ImageURL, DurationMs: 72000 + index*18000, Autoplay: true, Muted: false, RuntimeHint: "native_media"},
		Stats: VideoStats{ViewCount: 12000 + index*1700, LikeCount: 860 + index*90,
			CommentCount: 48 + index*7, ShareCount: 12 + index*3},
		PrimaryAction: &Action{Label: "OPEN DISCUSSION", AppURL: "app://comment/sheet?articleId=quietude-001", RuntimeHint: "flutter"},
		Tracking:      map[string]any{"videoId": id, "surfaceId": "video-feed"},
	}
}

func (s *Service) toVideoCard(item VideoFeedItem) Card {
	return Card{ID: item.ID, Title: item.Title, Subtitle: item.Subtitle, Description: item.Description,
		ImageURL: item.ImageURL, Eyebrow: item.Eyebrow, Meta: item.Meta, Tags: item.Tags,
		AppURL: item.AppURL, RuntimeHint: item.RuntimeHint}
}

func (s *Service) getFirstPartyOrigin() string {
	origin := firstNonEmpty(
		os.Getenv("MOBILE_SHARE_BASE_URL"), os.Getenv("MOBILE_WEB_BASE_URL"),
		os.Getenv("APP_PUBLIC_ORIGIN"), os.Getenv("WEB_PUBLIC_ORIGIN"), "http://127.0.0.1:3002")
	return strings.TrimRight(origin, "/")
}

func (s *Service) buildShareURL(appURL string) string {
	return fmt.Sprintf("%s/open?appUrl=%s", s.getFirstPartyOrigin(), urlQueryEscape(appURL))
}

func (s *Service) normalizeLinkTarget(resourceID string) string {
	if strings.HasPrefix(strings.ToLower(resourceID), "http://") || strings.HasPrefix(strings.ToLower(resourceID), "https://") {
		return resourceID
	}
	path := resourceID
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return s.getFirstPartyOrigin() + path
}

func (s *Service) topicCardFromTag(t TagRow, prefix, subtitle, fallbackDesc string) Card {
	slug := defaultTopicSlug
	if t.Slug != nil && *t.Slug != "" {
		slug = *t.Slug
	}
	desc := fallbackDesc
	if t.Description != nil && *t.Description != "" {
		desc = *t.Description
	}
	idKey := t.ID
	if t.Slug != nil && *t.Slug != "" {
		idKey = *t.Slug
	}
	return Card{
		ID: prefix + idKey, Title: t.Name, Subtitle: subtitle, Description: desc,
		AppURL: "app://topic/landing?slug=" + slug, RuntimeHint: "web",
	}
}
