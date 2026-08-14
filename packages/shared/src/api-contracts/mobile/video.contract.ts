import type {
    MobileBffAction,
    MobileBffAuthor,
    MobileBffCard,
    MobileBffComment,
} from './bff.contract';
import type { MobileRuntimeKind } from './runtime.contract';

export interface MobileVideoPlaybackContext {
    streamUrl: string;
    posterUrl?: string;
    durationMs: number;
    autoplay: boolean;
    muted: boolean;
    runtimeHint?: MobileRuntimeKind;
}

export interface MobileVideoEngagementStats {
    viewCount: number;
    likeCount: number;
    commentCount: number;
    shareCount?: number;
}

export interface MobileVideoFeedItem extends MobileBffCard {
    author: MobileBffAuthor;
    playback: MobileVideoPlaybackContext;
    stats: MobileVideoEngagementStats;
    primaryAction?: MobileBffAction;
    tracking?: Record<string, unknown>;
}

export interface MobileVideoFeedResponse {
    generatedAt: string;
    autoplayEnabled: boolean;
    mutedByDefault: boolean;
    items: MobileVideoFeedItem[];
    nextCursor?: string;
}

export interface MobileVideoDetailResponse {
    generatedAt: string;
    video: MobileVideoFeedItem & {
        body?: string;
        publishedAt?: string;
        transcript?: string;
    };
    commentsPreview?: {
        total: number;
        comments: MobileBffComment[];
    };
    nextUp: MobileBffCard[];
}
