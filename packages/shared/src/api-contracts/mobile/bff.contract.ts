import type { MobileRuntimeKind, MobileSurfaceId } from './runtime.contract';

export interface MobileBffAction {
    label: string;
    appUrl: string;
    runtimeHint?: MobileRuntimeKind;
}

export interface MobileBffAuthor {
    id?: string;
    name: string;
    role?: string;
    avatarUrl?: string;
    bio?: string;
}

export interface MobileBffCard {
    id: string;
    title: string;
    subtitle?: string;
    description?: string;
    imageUrl?: string;
    eyebrow?: string;
    meta?: string;
    tags?: string[];
    appUrl?: string;
    runtimeHint?: MobileRuntimeKind;
}

export interface MobileBffComment {
    id: string;
    author: MobileBffAuthor;
    body: string;
    timestamp: string;
    likeCount: number;
    highlighted?: boolean;
    replies?: MobileBffComment[];
}

export interface MobileFeedHomeResponse {
    generatedAt: string;
    heroArticle: MobileBffCard;
    insightCards: MobileBffCard[];
    weeklyFocus: {
        slug: string;
        title: string;
        summary: string;
        chips: string[];
        primaryAction: MobileBffAction;
        secondaryAction?: MobileBffAction;
    };
    mixedGrid: MobileBffCard[];
    trendingTopics: MobileBffCard[];
}

export interface MobileArticleDetailResponse {
    generatedAt: string;
    article: MobileBffCard & {
        content: string;
        readTimeMinutes: number;
        publishedAt?: string;
        author: MobileBffAuthor;
        stats: {
            viewCount: number;
            commentCount: number;
        };
    };
    quote?: string;
    relatedArticles: MobileBffCard[];
    commentsPreview: {
        articleId: string;
        title: string;
        subtitle: string;
        total: number;
        comments: MobileBffComment[];
    };
}

export interface MobileSearchIndexResponse {
    generatedAt: string;
    query?: string;
    quickAccess: string[];
    recentHistory: string[];
    trendingPaths: MobileBffCard[];
    articleResults: MobileBffCard[];
    videoResults: MobileBffCard[];
    creatorResults: Array<MobileBffAuthor & { appUrl?: string }>;
}

export interface MobileMessageCenterResponse {
    generatedAt: string;
    summary: {
        unreadCount: number;
        inquiryCtaLabel: string;
        statusLabel: string;
    };
    filters: Array<{
        id: string;
        label: string;
        badge?: string;
        active?: boolean;
    }>;
    items: MobileBffCard[];
    recommendations: MobileBffCard[];
}

export interface MobileMessageReadRequest {
    messageIds?: string[];
    conversationIds?: string[];
}

export interface MobileMessageReadResponse {
    updatedCount: number;
    unreadCount?: number;
}

export interface MobileProfileHomeResponse {
    generatedAt: string;
    accessLabel: string;
    profile: MobileBffAuthor & {
        headline?: string;
        stats: Array<{
            label: string;
            value: string | number;
        }>;
        settingsAction?: MobileBffAction;
    };
    featuredArticle: MobileBffCard;
    featuredVideo: MobileBffCard;
    archive: MobileBffCard[];
}

export interface MobileSettingsSectionItem {
    id: string;
    title: string;
    subtitle?: string;
    value?: string | boolean;
    icon?: string;
}

export interface MobileSettingsSection {
    id: string;
    title: string;
    badge?: string;
    items: MobileSettingsSectionItem[];
}

export interface MobileSettingsIndexResponse {
    generatedAt: string;
    versionLabel: string;
    sections: MobileSettingsSection[];
    actions: MobileBffAction[];
}

export interface MobileTopicLandingResponse {
    generatedAt: string;
    slug: string;
    title: string;
    summary: string;
    heroImageUrl?: string;
    syllabus: string[];
    featuredArticles: MobileBffCard[];
    featuredVideo?: MobileBffCard;
    primaryAction: MobileBffAction;
    secondaryAction?: MobileBffAction;
}

export interface MobileSharePrepareRequest {
    resourceType: 'article' | 'video' | 'topic' | 'campaign' | 'link';
    resourceId: string;
    surfaceId?: MobileSurfaceId;
    source?: string;
}

export interface MobileSharePrepareResponse {
    resourceType: MobileSharePrepareRequest['resourceType'];
    resourceId: string;
    title: string;
    text?: string;
    url: string;
    imageUrl?: string;
    tracking?: Record<string, unknown>;
}
