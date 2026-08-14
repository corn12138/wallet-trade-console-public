import type {
    MobileContainerKind,
    MobileHostNamespace,
    MobileRuntimeConfig,
    MobileTheme,
} from './runtime.contract';

export interface MobileSessionContext {
    isLoggedIn: boolean;
    userId?: string;
    locale: string;
    theme: MobileTheme;
    appVersion?: string;
    container: MobileContainerKind;
    disabledNamespaces: MobileHostNamespace[];
}

export interface MobileRuntimeBootstrap
    extends MobileRuntimeConfig,
        Partial<Omit<MobileSessionContext, 'disabledNamespaces'>> {}

export interface MobileSessionRequirementResult {
    granted: boolean;
    reason?: string;
}
