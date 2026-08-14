import type { MobileAnalyticsTrackPayload } from './analytics.contract';
import type { MobileSessionContext, MobileSessionRequirementResult } from './session.contract';
import type {
    MobileContainerKind,
    MobileHostNamespace,
    MobileTheme,
} from './runtime.contract';

export interface MobileHostBridgeRequest<TPayload = unknown> {
    namespace: MobileHostNamespace;
    method: string;
    version: string;
    requestId: string;
    payload: TPayload;
}

export interface MobileHostBridgeResponse<TData = unknown> {
    requestId: string;
    ok: boolean;
    code: number;
    message: string;
    data: TData;
}

export type MobileHostCapabilityKey =
    | 'route.open'
    | 'route.close'
    | 'session.getContext'
    | 'session.requireLogin'
    | 'device.getInfo'
    | 'device.getNetworkStatus'
    | 'ui.showToast'
    | 'ui.setNavigationBar'
    | 'share.open'
    | 'analytics.track'
    | 'media.pickImage'
    | 'media.upload'
    | 'comment.openSheet'
    | 'video.enterFullscreen';

export interface MobileCapabilityCheckRequest {
    container?: MobileContainerKind;
    appVersion?: string;
    minimumBridgeVersion?: string;
    requestedCapabilities?: MobileHostCapabilityKey[];
}

export interface MobileCapabilityCheckResult {
    hostApiVersion: string;
    supportedNamespaces: MobileHostNamespace[];
    disabledNamespaces: MobileHostNamespace[];
    supports: Partial<Record<MobileHostCapabilityKey, boolean>>;
}

export interface MobileDeviceInfo {
    platform: string;
    version: string;
    model: string;
    brand: string;
    screenWidth: number;
    screenHeight: number;
    density: number;
}

export interface MobileNetworkStatus {
    isConnected: boolean;
    type: string;
    strength: number;
}

export interface MobileRouteOpenPayload {
    url: string;
    fallback?: string;
    source?: string;
}

export interface MobileRouteClosePayload {
    reason?: string;
}

export type MobileNavigationBarTheme = MobileTheme | 'transparent';

export interface MobileUiShowToastPayload {
    message: string;
    durationMs?: number;
}

export interface MobileUiSetNavigationBarPayload {
    title?: string;
    visible?: boolean;
    theme?: MobileNavigationBarTheme;
    backgroundColor?: string;
    foregroundColor?: string;
    translucent?: boolean;
}

export type MobileShareResourceType =
    | 'article'
    | 'video'
    | 'topic'
    | 'campaign'
    | 'link';

export interface MobileShareOpenPayload {
    resourceType?: MobileShareResourceType;
    resourceId?: string;
    url?: string;
    title?: string;
    text?: string;
    imageUrl?: string;
    source?: string;
}

export interface MobileShareOpenResult {
    handledBy: string;
    dismissed?: boolean;
}

export type MobileMediaPickSource = 'camera' | 'library' | 'mixed';

export interface MobileMediaPickImagePayload {
    maxCount?: number;
    source?: MobileMediaPickSource;
    allowEditing?: boolean;
}

export interface MobilePickedMediaAsset {
    assetId: string;
    uri: string;
    mimeType?: string;
    width?: number;
    height?: number;
    sizeBytes?: number;
}

export interface MobileMediaPickImageResult {
    items: MobilePickedMediaAsset[];
    canceled?: boolean;
}

export interface MobileMediaUploadPayload {
    localAssetIds?: string[];
    uploadToken?: string;
    purpose?: string;
}

export type MobileMediaUploadJobStatus =
    | 'queued'
    | 'uploading'
    | 'completed'
    | 'failed';

export interface MobileMediaUploadJob {
    id: string;
    status: MobileMediaUploadJobStatus;
    remoteUrl?: string;
}

export interface MobileMediaUploadResult {
    jobs: MobileMediaUploadJob[];
}

export interface MobileCommentOpenSheetPayload {
    articleId: string;
    commentId?: string;
    source?: string;
}

export interface MobileCommentOpenSheetResult {
    articleId: string;
    handledBy: string;
}

export interface MobileVideoEnterFullscreenPayload {
    videoId: string;
    autoplay?: boolean;
    muted?: boolean;
    source?: string;
}

export interface MobileVideoEnterFullscreenResult {
    videoId: string;
    handledBy: string;
}

export type MobileHostApiResponseData =
    | MobileSessionContext
    | MobileSessionRequirementResult
    | MobileDeviceInfo
    | MobileNetworkStatus
    | MobileCapabilityCheckResult
    | MobileShareOpenResult
    | MobileMediaPickImageResult
    | MobileMediaUploadResult
    | MobileCommentOpenSheetResult
    | MobileVideoEnterFullscreenResult
    | Record<string, unknown>
    | null;

export type MobileHostApiPayload =
    | MobileRouteOpenPayload
    | MobileRouteClosePayload
    | MobileUiShowToastPayload
    | MobileUiSetNavigationBarPayload
    | MobileShareOpenPayload
    | MobileAnalyticsTrackPayload
    | MobileMediaPickImagePayload
    | MobileMediaUploadPayload
    | MobileCommentOpenSheetPayload
    | MobileVideoEnterFullscreenPayload
    | Record<string, unknown>;
