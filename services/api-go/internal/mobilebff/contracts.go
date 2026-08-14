// Package mobilebff is the Go port of legacy NestJS mobile-bff/ — the 13
// aggregation routes under /api/mobile that power the separate mobile/H5 app:
//
//	GET  /api/mobile/feed/home        GET  /api/mobile/settings/index
//	GET  /api/mobile/article/:id      GET  /api/mobile/topic/:slug
//	GET  /api/mobile/comment/sheet    GET  /api/mobile/video/feed
//	POST /api/mobile/comment/create   GET  /api/mobile/video/:id
//	GET  /api/mobile/search/index     POST /api/mobile/share/prepare
//	GET  /api/mobile/message/center
//	POST /api/mobile/message/read     GET  /api/mobile/profile/home
//
// All @Public in NestJS; comment/create + message/* + message-center + profile
// read the session (via the mobile control plane) and 401 when logged-out where
// the reference does. Responses mirror packages/shared/src/api-contracts/mobile/
// *.contract.ts and are served RAW (ADR 0005). The card assembly + fixtures are
// a faithful port of mobile-bff.service.ts + mobile-bff.fixtures.ts.
package mobilebff

// Card mirrors MobileBffCard.
type Card struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Subtitle    string   `json:"subtitle,omitempty"`
	Description string   `json:"description,omitempty"`
	ImageURL    string   `json:"imageUrl,omitempty"`
	Eyebrow     string   `json:"eyebrow,omitempty"`
	Meta        string   `json:"meta,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	AppURL      string   `json:"appUrl,omitempty"`
	RuntimeHint string   `json:"runtimeHint,omitempty"`
}

// Action mirrors MobileBffAction.
type Action struct {
	Label       string `json:"label"`
	AppURL      string `json:"appUrl"`
	RuntimeHint string `json:"runtimeHint,omitempty"`
}

// Author mirrors MobileBffAuthor.
type Author struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Role      string `json:"role,omitempty"`
	AvatarURL string `json:"avatarUrl,omitempty"`
	Bio       string `json:"bio,omitempty"`
}

// Comment mirrors MobileBffComment.
type Comment struct {
	ID          string    `json:"id"`
	Author      Author    `json:"author"`
	Body        string    `json:"body"`
	Timestamp   string    `json:"timestamp"`
	LikeCount   int       `json:"likeCount"`
	Highlighted bool      `json:"highlighted"`
	Replies     []Comment `json:"replies"`
}

// CreatorResult is MobileBffAuthor & { appUrl? }.
type CreatorResult struct {
	Author
	AppURL string `json:"appUrl,omitempty"`
}

// WeeklyFocus is the feed-home weeklyFocus block.
type WeeklyFocus struct {
	Slug            string   `json:"slug"`
	Title           string   `json:"title"`
	Summary         string   `json:"summary"`
	Chips           []string `json:"chips"`
	PrimaryAction   Action   `json:"primaryAction"`
	SecondaryAction *Action  `json:"secondaryAction,omitempty"`
}

// FeedHomeResponse mirrors MobileFeedHomeResponse.
type FeedHomeResponse struct {
	GeneratedAt    string      `json:"generatedAt"`
	HeroArticle    Card        `json:"heroArticle"`
	InsightCards   []Card      `json:"insightCards"`
	WeeklyFocus    WeeklyFocus `json:"weeklyFocus"`
	MixedGrid      []Card      `json:"mixedGrid"`
	TrendingTopics []Card      `json:"trendingTopics"`
}

// ArticleDetailArticle is MobileBffCard & { content, readTimeMinutes, ... }.
type ArticleDetailArticle struct {
	Card
	Content         string             `json:"content"`
	ReadTimeMinutes int                `json:"readTimeMinutes"`
	PublishedAt     string             `json:"publishedAt,omitempty"`
	Author          Author             `json:"author"`
	Stats           ArticleDetailStats `json:"stats"`
}

type ArticleDetailStats struct {
	ViewCount    int64 `json:"viewCount"`
	CommentCount int64 `json:"commentCount"`
}

// CommentsPreview is the nested comment preview in article detail.
type CommentsPreview struct {
	ArticleID string    `json:"articleId"`
	Title     string    `json:"title"`
	Subtitle  string    `json:"subtitle"`
	Total     int64     `json:"total"`
	Comments  []Comment `json:"comments"`
}

// ArticleDetailResponse mirrors MobileArticleDetailResponse.
type ArticleDetailResponse struct {
	GeneratedAt     string               `json:"generatedAt"`
	Article         ArticleDetailArticle `json:"article"`
	Quote           string               `json:"quote,omitempty"`
	RelatedArticles []Card               `json:"relatedArticles"`
	CommentsPreview CommentsPreview      `json:"commentsPreview"`
}

// CommentSheetComposer is the comment-sheet composer block.
type CommentSheetComposer struct {
	Placeholder   string `json:"placeholder"`
	RequiresLogin bool   `json:"requiresLogin"`
}

// CommentSheetResponse mirrors MobileCommentSheetResponse.
type CommentSheetResponse struct {
	ArticleID string               `json:"articleId"`
	Title     string               `json:"title"`
	Subtitle  string               `json:"subtitle"`
	Total     int64                `json:"total"`
	Comments  []Comment            `json:"comments"`
	Composer  CommentSheetComposer `json:"composer"`
}

// CommentCreateResponse mirrors MobileCommentCreateResponse.
type CommentCreateResponse struct {
	Comment           Comment `json:"comment"`
	Total             int64   `json:"total"`
	PendingModeration bool    `json:"pendingModeration"`
}

// SearchIndexResponse mirrors MobileSearchIndexResponse.
type SearchIndexResponse struct {
	GeneratedAt    string          `json:"generatedAt"`
	Query          string          `json:"query,omitempty"`
	QuickAccess    []string        `json:"quickAccess"`
	RecentHistory  []string        `json:"recentHistory"`
	TrendingPaths  []Card          `json:"trendingPaths"`
	ArticleResults []Card          `json:"articleResults"`
	VideoResults   []Card          `json:"videoResults"`
	CreatorResults []CreatorResult `json:"creatorResults"`
}

// MessageCenterFilter is one message-center filter chip.
type MessageCenterFilter struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Badge  string `json:"badge,omitempty"`
	Active bool   `json:"active,omitempty"`
}

// MessageCenterSummary is the message-center summary block.
type MessageCenterSummary struct {
	UnreadCount     int    `json:"unreadCount"`
	InquiryCtaLabel string `json:"inquiryCtaLabel"`
	StatusLabel     string `json:"statusLabel"`
}

// MessageCenterResponse mirrors MobileMessageCenterResponse.
type MessageCenterResponse struct {
	GeneratedAt     string                `json:"generatedAt"`
	Summary         MessageCenterSummary  `json:"summary"`
	Filters         []MessageCenterFilter `json:"filters"`
	Items           []Card                `json:"items"`
	Recommendations []Card                `json:"recommendations"`
}

// MessageReadRequest mirrors MobileMessageReadRequest.
type MessageReadRequest struct {
	MessageIDs      []string `json:"messageIds"`
	ConversationIDs []string `json:"conversationIds"`
}

// MessageReadResponse mirrors MobileMessageReadResponse.
type MessageReadResponse struct {
	UpdatedCount int   `json:"updatedCount"`
	UnreadCount  int64 `json:"unreadCount"`
}

// ProfileStat is one profile stat.
type ProfileStat struct {
	Label string `json:"label"`
	Value any    `json:"value"`
}

// ProfileBlock is MobileBffAuthor & { headline?, stats, settingsAction? }.
type ProfileBlock struct {
	Author
	Headline       string        `json:"headline,omitempty"`
	Stats          []ProfileStat `json:"stats"`
	SettingsAction *Action       `json:"settingsAction,omitempty"`
}

// ProfileHomeResponse mirrors MobileProfileHomeResponse.
type ProfileHomeResponse struct {
	GeneratedAt     string       `json:"generatedAt"`
	AccessLabel     string       `json:"accessLabel"`
	Profile         ProfileBlock `json:"profile"`
	FeaturedArticle Card         `json:"featuredArticle"`
	FeaturedVideo   Card         `json:"featuredVideo"`
	Archive         []Card       `json:"archive"`
}

// SettingsSectionItem mirrors MobileSettingsSectionItem (value is string|bool).
type SettingsSectionItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Value    any    `json:"value,omitempty"`
	Icon     string `json:"icon,omitempty"`
}

type SettingsSection struct {
	ID    string                `json:"id"`
	Title string                `json:"title"`
	Badge string                `json:"badge,omitempty"`
	Items []SettingsSectionItem `json:"items"`
}

// SettingsIndexResponse mirrors MobileSettingsIndexResponse.
type SettingsIndexResponse struct {
	GeneratedAt  string            `json:"generatedAt"`
	VersionLabel string            `json:"versionLabel"`
	Sections     []SettingsSection `json:"sections"`
	Actions      []Action          `json:"actions"`
}

// TopicLandingResponse mirrors MobileTopicLandingResponse.
type TopicLandingResponse struct {
	GeneratedAt      string   `json:"generatedAt"`
	Slug             string   `json:"slug"`
	Title            string   `json:"title"`
	Summary          string   `json:"summary"`
	HeroImageURL     string   `json:"heroImageUrl,omitempty"`
	Syllabus         []string `json:"syllabus"`
	FeaturedArticles []Card   `json:"featuredArticles"`
	FeaturedVideo    *Card    `json:"featuredVideo,omitempty"`
	PrimaryAction    Action   `json:"primaryAction"`
	SecondaryAction  *Action  `json:"secondaryAction,omitempty"`
}

// VideoAuthor is the nested video author.
type VideoAuthor struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	AvatarURL string `json:"avatarUrl"`
}

// VideoPlayback mirrors MobileVideoPlaybackContext.
type VideoPlayback struct {
	StreamURL   string `json:"streamUrl"`
	PosterURL   string `json:"posterUrl,omitempty"`
	DurationMs  int    `json:"durationMs"`
	Autoplay    bool   `json:"autoplay"`
	Muted       bool   `json:"muted"`
	RuntimeHint string `json:"runtimeHint,omitempty"`
}

// VideoStats mirrors MobileVideoEngagementStats.
type VideoStats struct {
	ViewCount    int `json:"viewCount"`
	LikeCount    int `json:"likeCount"`
	CommentCount int `json:"commentCount"`
	ShareCount   int `json:"shareCount,omitempty"`
}

// VideoFeedItem mirrors MobileVideoFeedItem (Card + author/playback/stats).
type VideoFeedItem struct {
	Card
	Author        VideoAuthor    `json:"author"`
	Playback      VideoPlayback  `json:"playback"`
	Stats         VideoStats     `json:"stats"`
	PrimaryAction *Action        `json:"primaryAction,omitempty"`
	Tracking      map[string]any `json:"tracking,omitempty"`
}

// VideoFeedResponse mirrors MobileVideoFeedResponse.
type VideoFeedResponse struct {
	GeneratedAt     string          `json:"generatedAt"`
	AutoplayEnabled bool            `json:"autoplayEnabled"`
	MutedByDefault  bool            `json:"mutedByDefault"`
	Items           []VideoFeedItem `json:"items"`
	NextCursor      string          `json:"nextCursor,omitempty"`
}

// VideoDetailVideo is VideoFeedItem & { body?, publishedAt?, transcript? }.
type VideoDetailVideo struct {
	VideoFeedItem
	Body        string `json:"body,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	Transcript  string `json:"transcript,omitempty"`
}

// VideoDetailResponse mirrors MobileVideoDetailResponse.
type VideoDetailResponse struct {
	GeneratedAt string           `json:"generatedAt"`
	Video       VideoDetailVideo `json:"video"`
	NextUp      []Card           `json:"nextUp"`
}

// SharePrepareRequest mirrors MobileSharePrepareRequest.
type SharePrepareRequest struct {
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
	SurfaceID    string `json:"surfaceId"`
	Source       string `json:"source"`
}

// SharePrepareResponse mirrors MobileSharePrepareResponse.
type SharePrepareResponse struct {
	ResourceType string         `json:"resourceType"`
	ResourceID   string         `json:"resourceId"`
	Title        string         `json:"title"`
	Text         string         `json:"text,omitempty"`
	URL          string         `json:"url"`
	ImageURL     string         `json:"imageUrl,omitempty"`
	Tracking     map[string]any `json:"tracking,omitempty"`
}
