import { describe, expect, it } from 'vitest';
import { CampaignAPIError } from './ports';
import { campaignId, detail, fixture, joinIntent, me, response } from './command-test-fixtures';

describe('campaign command response evidence', () => {
  it('retains the key when a shape-valid original outcome contradicts the submitted operation', async () => {
    const f = fixture();
    f.api.execute.mockImplementationOnce(async (_session, _id, command) => {
      const result = response(command); result.original.state!.status = 'left'; return result;
    });
    await f.controller.confirm(joinIntent);
    expect(f.controller.getSnapshot()).toMatchObject({ status: 'unknown', response: null, error: { code: 'CAMPAIGN_RESPONSE_INVALID' } });
    await f.controller.confirm(joinIntent);
    expect(f.api.execute).toHaveBeenCalledTimes(1);
  });

  it('does not authorize retry from a Me participation value in the reminder state family', async () => {
    const f = fixture(); f.api.execute.mockRejectedValueOnce(new Error('offline'));
    await f.controller.confirm(joinIntent);
    f.api.me.mockResolvedValueOnce(me('1', 'set'));
    await f.controller.reconcile(); await f.controller.retry();
    expect(f.controller.getSnapshot()).toMatchObject({ status: 'unknown', me: null, canRetry: false, error: { code: 'CAMPAIGN_RESPONSE_INVALID' } });
    expect(f.api.execute).toHaveBeenCalledTimes(1);
  });

  it('requires available published terms before a reviewed positive action can get a new key', async () => {
    const f = fixture(); f.api.execute.mockRejectedValueOnce(new CampaignAPIError('CAMPAIGN_VERSION_CONFLICT', 409));
    const content = detail('2'); content.item.content = null;
    f.api.detail.mockResolvedValue(content); f.api.me.mockResolvedValue(me('0', 'joined', '2'));
    await f.controller.confirm(joinIntent);
    await f.controller.confirm({ ...joinIntent, expectedPublicationVersion: '2' });
    expect(f.controller.getSnapshot()).toMatchObject({ status: 'requiresReview', error: { code: 'CAMPAIGN_REVIEW_REQUIRED' } });
    expect(f.api.execute).toHaveBeenCalledTimes(1);
  });

  it('recovers a failed review read without executing the command or changing its key', async () => {
    const f = fixture(); f.api.execute.mockRejectedValueOnce(new CampaignAPIError('CAMPAIGN_VERSION_CONFLICT', 409));
    f.api.me.mockRejectedValueOnce(new CampaignAPIError('CAMPAIGN_DATA_UNAVAILABLE', 503));
    await f.controller.confirm(joinIntent);
    const original = f.controller.getSnapshot().command;
    expect(f.controller.getSnapshot()).toMatchObject({ status: 'requiresReview', reviewReady: false });
    await f.controller.reconcile();
    expect(f.controller.getSnapshot()).toMatchObject({ status: 'requiresReview', reviewReady: true, command: original });
    expect(f.api.command).toHaveBeenLastCalledWith(expect.anything(), campaignId, 'join', original!.input.clientCommandId, expect.any(AbortSignal));
    expect(f.api.execute).toHaveBeenCalledTimes(1);
  });
});
