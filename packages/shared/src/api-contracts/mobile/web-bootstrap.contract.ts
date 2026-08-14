import type { MobileHostCapabilityKey } from './host-api.contract';
import type {
    MobileHostNamespace,
    MobileRuntimeFlagValue,
    MobileSurfaceId,
    MobileTheme,
} from './runtime.contract';

export interface MobileWebBootstrapRequest {
    path?: string;
    appUrl?: string;
    origin?: string;
    surfaceId?: MobileSurfaceId;
}

export interface MobileWebBootstrapUser {
    id?: string;
    displayName?: string;
    avatarUrl?: string;
}

export interface MobileWebBootstrapResponse {
    generatedAt: string;
    bridgeVersion: string;
    user?: MobileWebBootstrapUser;
    sessionExpiresAt?: string;
    theme: MobileTheme;
    locale: string;
    flags: Record<string, MobileRuntimeFlagValue>;
    disabledNamespaces: MobileHostNamespace[];
    supports: Partial<Record<MobileHostCapabilityKey, boolean>>;
    pageContext: Record<string, unknown>;
}
