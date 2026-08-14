import type { MobileRuntimeKind } from './runtime.contract';

export interface MobileAnalyticsEvent {
    name: string;
    source?: string;
    surfaceId?: string;
    runtimePlane?: MobileRuntimeKind;
    requestId?: string;
    sentAt?: string;
    payload?: Record<string, unknown>;
}

export interface MobileAnalyticsTrackPayload {
    event: string;
    payload?: Record<string, unknown>;
}

export interface MobileAnalyticsIngestRequest {
    events: MobileAnalyticsEvent[];
}

export interface MobileAnalyticsIngestResult {
    accepted: number;
    dropped: number;
    requestId?: string;
}
