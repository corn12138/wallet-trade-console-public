'use client';

import { ConnectButton } from '@/components/web3';
import { useDisplayChainId } from '@/hooks/useDisplayChainId';
import { getTokenFactoryAddress } from '@/lib/web3/contracts';
import { useTranslations } from 'next-intl';
import { useRouter } from 'next/navigation';
import { useRef, useState } from 'react';
import { useAccount } from 'wagmi';
import { Icon } from '@/app/_atlas/Icon';
import { ProductStatusHint } from '@/app/_atlas/ProductStatus';
import { CreateTokenFormPanel } from './CreateTokenFormPanel';
import { CreateTokenPreviewPanel } from './CreateTokenPreviewPanel';
import { CreateTokenStatusNotice } from './CreateTokenStatusNotice';
import { INITIAL_TOKEN_FORM } from './create-token.types';
import type { LaunchType, TokenForm } from './create-token.types';
import { useCreateTokenSubmission } from './useCreateTokenSubmission';

// Real create-token route (restored from the original implementation; the previous
// `_atlas` re-export was a mock that faked success with Math.random()/setTimeout and a
// hardcoded address). This page submits a REAL wallet-signed transaction to the
// TokenFactory on the connected chain, waits for the on-chain receipt, decodes the
// TokenCreated event for the real token address, then persists metadata via POST /token
// (the only persistence path). It is a Sepolia *testnet* flow — not mainnet/real-money.
// UI: Atlas design system (block/field/seg/pill) — no legacy dark surface classes.
export default function CreateTokenPage() {
  const t = useTranslations();
  const router = useRouter();
  const { isConnected, address } = useAccount();
  // Display/read default (Sepolia when no wallet); writes require a connected
  // wallet, at which point this is the wallet's real chain.
  const { chainId } = useDisplayChainId();
  const imageInputRef = useRef<HTMLInputElement>(null);
  const bannerInputRef = useRef<HTMLInputElement>(null);

  const [launchType, setLaunchType] = useState<LaunchType>('newcoin');
  const [form, setForm] = useState<TokenForm>(INITIAL_TOKEN_FORM);

  // Explicit availability gate: if the connected chain has no TokenFactory deployment,
  // creation is genuinely unavailable — we say so instead of letting the user submit.
  const factoryAddress = getTokenFactoryAddress(chainId);
  const isCreateAvailable = Boolean(factoryAddress);

  const { handleSubmit, isBusy, notice } = useCreateTokenSubmission({
    chainId,
    form,
    isConnected,
    walletAddress: address,
    onCreated: (tokenAddress) => router.push(`/token/${tokenAddress}`),
    t,
  });

  const handleImageChange = (
    event: React.ChangeEvent<HTMLInputElement>,
    type: 'image' | 'banner'
  ) => {
    const file = event.target.files?.[0];
    if (!file) {
      return;
    }

    const reader = new FileReader();
    reader.onloadend = () => {
      setForm((current) => ({
        ...current,
        [type]: file,
        [`${type}Preview`]: reader.result as string,
      }));
    };
    reader.readAsDataURL(file);
  };

  const toggleTag = (tag: string) => {
    setForm((current) => {
      if (current.tags.includes(tag)) {
        return { ...current, tags: current.tags.filter((item) => item !== tag) };
      }

      if (current.tags.length >= 3) {
        return current;
      }

      return { ...current, tags: [...current.tags, tag] };
    });
  };

  return (
    <div className="col gap-24">
      {/* Honest simulation/testnet boundary — never imply mainnet or real money. */}
      <section className="block bg-y" style={{ padding: 16 }}>
        <div className="row gap-10" style={{ alignItems: 'flex-start' }}>
          <Icon name="warn" size={18} style={{ flex: 'none', marginTop: 2 }} />
          <div style={{ fontFamily: 'var(--mf)', fontSize: 13, lineHeight: 1.55 }}>
            <b>{t('createToken.sepoliaNoticeTitle')}</b> {t('createToken.sepoliaNoticeBody')}
          </div>
        </div>
      </section>

      {!isCreateAvailable && (
        <section className="block bg-o" style={{ padding: 16 }}>
          <div className="row gap-10" style={{ alignItems: 'flex-start' }}>
            <Icon name="warn" size={18} style={{ flex: 'none', marginTop: 2 }} />
            <div style={{ fontFamily: 'var(--mf)', fontSize: 13, lineHeight: 1.55 }}>
              {t('createToken.unavailableNotice', { chainId })}
            </div>
          </div>
        </section>
      )}

      {/* Backend-sourced reason the upload/deploy path may be disabled. */}
      <ProductStatusHint codes={['MEDIA_UNCONFIGURED', 'SWAP_ROUTER_UNCONFIGURED']} />

      <div className="builder-grid">
        <CreateTokenFormPanel
          form={form}
          launchType={launchType}
          imageInputRef={imageInputRef}
          bannerInputRef={bannerInputRef}
          onLaunchTypeChange={setLaunchType}
          onFormChange={(updater) => setForm((current) => updater(current))}
          onImageChange={handleImageChange}
          onToggleTag={toggleTag}
          t={t}
        />

        <div style={{ position: 'sticky', top: 84 }}>
          <CreateTokenPreviewPanel form={form} t={t} />

          <div className="col gap-10 mt-14">
            <CreateTokenStatusNotice notice={notice} />

            {isConnected ? (
              <button
                onClick={handleSubmit}
                disabled={
                  !isCreateAvailable ||
                  !form.symbol ||
                  !form.name ||
                  form.tags.length === 0 ||
                  isBusy
                }
                className="btn btn-g"
                style={{ width: '100%' }}
                data-testid="create-token-submit"
              >
                {isBusy && <span className="spinner" />}
                {isBusy ? t('createToken.creating') : t('createToken.createCoin')}
              </button>
            ) : (
              <ConnectButton />
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
