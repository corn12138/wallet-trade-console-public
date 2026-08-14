import type {
    MobileContainerKind,
    MobileRouteHandledBy,
    MobileRuntimeKind,
    MobileSurfaceId,
} from './runtime.contract';

export interface MobileRouteResolveRequest {
    appUrl: string;
    source?: string;
    container?: MobileContainerKind;
    appVersion?: string;
    locale?: string;
    origin?: string;
}

export interface MobileRouteResolveResult {
    appUrl: string;
    surfaceId: MobileSurfaceId;
    runtimePlane: MobileRuntimeKind;
    requiresLogin: boolean;
    fallback?: string;
    handledBy: MobileRouteHandledBy;
    externalUrl?: string;
}
