'use client';

import { useEffect, useMemo, useRef, useSyncExternalStore } from 'react';
import { CampaignCommandController, sameCampaignSession } from '../model/command-controller';
import { EMPTY_COMMAND_STATE, type CampaignCommandIntent } from '../model/command-types';
import type { CampaignAPI, CampaignSession } from '../model/ports';

export type UseCampaignCommandOptions = {
  api: CampaignAPI;
  session: CampaignSession | null;
  campaignId: string;
  onCurrent?: () => void;
  onUnauthorized?: () => void;
};
const subscribeEmpty = () => () => undefined;
const emptySnapshot = () => EMPTY_COMMAND_STATE;

/** One tab retains unresolved keys across route remounts. A new session may
 * read/reconcile its wallet's key, but never inherits another session's result. */
export function useCampaignCommand(options: UseCampaignCommandOptions) {
  const latest = useRef(options);
  latest.current = options;
  const { api, session, campaignId } = options;
  const owner = session?.owner; const generation = session?.generation; const token = session?.token;
  const controller = useMemo(() => {
    if (owner === undefined || generation === undefined || token === undefined) return null;
    const capturedSession = Object.freeze({ owner, generation, token });
    return new CampaignCommandController({ api, session: capturedSession, campaignId,
      isCurrent: () => latest.current.api === api && latest.current.campaignId === campaignId && sameCampaignSession(latest.current.session, capturedSession),
      onCurrent: () => latest.current.onCurrent?.(),
      onUnauthorized: () => latest.current.onUnauthorized?.(),
    });
  }, [api, campaignId, owner, generation, token]);
  const state = useSyncExternalStore(controller?.subscribe ?? subscribeEmpty, controller?.getSnapshot ?? emptySnapshot, emptySnapshot);
  useEffect(() => {
    if (!controller) return;
    controller.activate();
    const current = controller.getSnapshot();
    if (current.status === 'unknown' || current.status === 'requiresReview') void controller.reconcile();
    return () => controller.dispose();
  }, [controller]);
  return {
    state,
    confirm: (intent: CampaignCommandIntent) => controller?.confirm(intent) ?? Promise.resolve(),
    reconcile: () => controller?.reconcile() ?? Promise.resolve(),
    retry: () => controller?.retry() ?? Promise.resolve(),
  };
}
export type { CampaignCommandIntent, CampaignCommandState } from '../model/command-types';
