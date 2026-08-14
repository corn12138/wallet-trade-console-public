export type MobileRuntimeKind =
    | 'native_shell'
    | 'flutter'
    | 'web'
    | 'native_media'
    | 'native'
    | 'system_browser';

export type MobileHostNamespace =
    | 'route'
    | 'session'
    | 'device'
    | 'ui'
    | 'share'
    | 'analytics'
    | 'media'
    | 'comment'
    | 'video';

export type MobileTheme = 'light' | 'dark' | 'system';

export type MobileContainerKind =
    | 'browser'
    | 'native-webview'
    | 'flutter-webview';

export type MobileRuntimePhase =
    | 'create'
    | 'prewarm'
    | 'attach'
    | 'active'
    | 'background'
    | 'detach'
    | 'recycle'
    | 'destroy';

export type MobileKnownSurfaceId =
    | 'feed-home'
    | 'article-detail'
    | 'comment-sheet'
    | 'search-index'
    | 'message-center'
    | 'profile-home'
    | 'settings-index'
    | 'video-feed'
    | 'video-detail'
    | 'topic-landing'
    | 'campaign-detail'
    | 'help-detail'
    | 'legal-detail'
    | 'native-settings';

export type MobileSurfaceId = MobileKnownSurfaceId | (string & {});

export type MobileRouteHandledBy =
    | 'web-container'
    | 'flutter-container'
    | 'native-media-container'
    | 'native-container'
    | 'system-browser'
    | 'host-shell'
    | (string & {});

export type MobileRuntimeFlagValue = boolean | number | string;

export type MobileRouteRuntimeOverrideMap =
    Partial<Record<MobileSurfaceId, MobileRuntimeKind>>;

export interface MobileRuntimeConfig {
    bootstrapVersion: string;
    disabledNamespaces: MobileHostNamespace[];
    routeRuntimeOverrides: MobileRouteRuntimeOverrideMap;
    killedRoutes: MobileSurfaceId[];
    flags: Record<string, MobileRuntimeFlagValue>;
    firstPartyWebOrigins: string[];
    observabilitySampleRate: number;
}
