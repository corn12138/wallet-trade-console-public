'use client';

import { useContractConfig } from '@/hooks/useContractConfig';
import { useDisplayChainId } from '@/hooks/useDisplayChainId';
import { useTranslations } from 'next-intl';
import { useAccount } from 'wagmi';
import { Icon } from '@/app/_atlas/Icon';
import { ProductStatusHint } from '@/app/_atlas/ProductStatus';
import { IpfsMetadataPanel } from './IpfsMetadataPanel';
import { NftContractStatusCard } from './NftContractStatusCard';
import { NftStudioHeader } from './NftStudioHeader';
import { OnchainArtworkPanel } from './OnchainArtworkPanel';

// Real NFT studio route (restored from the original implementation; the previous
// `_atlas` re-export was a mock that faked mint success with setTimeout, a fake token
// number "NFT #0042", and a hardcoded contract "0x4f9…2c01"). This page resolves the
// real OnchainArtworkNFT / IpfsArtworkNFT deployment addresses for the connected chain
// and mints via real wallet-signed transactions (useOnchainArtworkMint /
// useIpfsArtworkMint). When a contract is not deployed on the chain, the status card and
// panels render an explicit unavailable state — no fake mint is ever shown. Sepolia
// testnet flow, not mainnet. UI: Atlas design system, no legacy dark surfaces.
export default function NftStudioPage() {
  const t = useTranslations('nftStudio');
  // Display/read default (Sepolia when no wallet); minting still requires a
  // connected wallet, at which point this is the wallet's real chain.
  const { chainId } = useDisplayChainId();
  const { address, isConnected } = useAccount();
  const { config, getAddress, isLoading } = useContractConfig(chainId);

  const onchainAddress = getAddress('OnchainArtworkNFT');
  const ipfsAddress = getAddress('IpfsArtworkNFT');
  const collectionOwner =
    config?.chains?.[chainId]?.deployer as `0x${string}` | undefined;

  return (
    <div className="col gap-24">
      <NftStudioHeader />

      <section className="block bg-y" style={{ padding: 16 }}>
        <div className="row gap-10" style={{ alignItems: 'flex-start' }}>
          <Icon name="warn" size={18} style={{ flex: 'none', marginTop: 2 }} />
          <div style={{ fontFamily: 'var(--mf)', fontSize: 13, lineHeight: 1.55 }}>
            <b>{t('sepoliaNoticeTitle')}</b> {t('sepoliaNoticeBody')}
          </div>
        </div>
      </section>

      <NftContractStatusCard
        chainId={chainId}
        onchainAddress={onchainAddress}
        ipfsAddress={ipfsAddress}
        collectionOwner={collectionOwner}
        isLoading={isLoading}
      />

      {/* Backend-sourced reason the artwork upload path may be disabled. */}
      <ProductStatusHint codes={['MEDIA_UNCONFIGURED']} />

      <section className="block tight" style={{ background: 'var(--bg-2)', boxShadow: 'none' }}>
        <div style={{ fontFamily: 'var(--mf)', fontSize: 13, lineHeight: 1.6 }}>
          <b style={{ fontFamily: 'var(--df)' }}>{t('workflowTitle')}:</b>{' '}
          {t('workflowDescription')}
        </div>
      </section>

      <OnchainArtworkPanel
        contractAddress={onchainAddress}
        walletAddress={address}
        collectionOwner={collectionOwner}
        isConnected={isConnected}
      />

      <IpfsMetadataPanel
        contractAddress={ipfsAddress}
        walletAddress={address}
        collectionOwner={collectionOwner}
        isConnected={isConnected}
      />
    </div>
  );
}
