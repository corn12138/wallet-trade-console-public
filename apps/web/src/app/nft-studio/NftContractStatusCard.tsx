import { supportedChains } from '@/lib/web3/config';
import { useTranslations } from 'next-intl';
import { getDeployCommand } from './nft-studio.utils';

interface NftContractStatusCardProps {
  chainId: number;
  onchainAddress?: string;
  ipfsAddress?: string;
  collectionOwner?: string;
  isLoading: boolean;
}

function StatusBadge({
  ready,
  readyLabel,
  missingLabel,
}: {
  ready: boolean;
  readyLabel: string;
  missingLabel: string;
}) {
  return (
    <span
      className={'pill flat'}
      style={{
        padding: '3px 10px',
        fontSize: 10,
        background: ready ? 'var(--g)' : 'var(--y)',
      }}
    >
      {ready ? readyLabel : missingLabel}
    </span>
  );
}

export function NftContractStatusCard({
  chainId,
  onchainAddress,
  ipfsAddress,
  collectionOwner,
  isLoading,
}: NftContractStatusCardProps) {
  const t = useTranslations('nftStudio');
  const activeChain = supportedChains.find((chain) => chain.id === chainId);

  const items = [
    {
      key: 'onchain',
      title: t('onchainTitle'),
      description: t('onchainDescription'),
      address: onchainAddress,
      command: getDeployCommand('onchain', chainId),
    },
    {
      key: 'ipfs',
      title: t('ipfsTitle'),
      description: t('ipfsDescription'),
      address: ipfsAddress,
      command: getDeployCommand('ipfs', chainId),
    },
  ] as const;

  return (
    <section className="block">
      <div>
        <h2 className="h-display" style={{ fontSize: 22 }}>{t('contractsTitle')}</h2>
        <p style={{ marginTop: 6, fontSize: 13, color: 'var(--ink-2)', lineHeight: 1.5 }}>
          {t('contractsDescription', {
            chain: activeChain?.name || `Chain ${chainId}`,
            chainId,
          })}
        </p>
      </div>

      {collectionOwner && (
        <div className="block tight mt-14" style={{ background: 'var(--y)', boxShadow: 'none', padding: '12px 14px' }}>
          <div style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 12 }}>{t('ownerOnlyLabel')}</div>
          <div className="mono" style={{ marginTop: 4, fontSize: 11, wordBreak: 'break-all' }}>
            {collectionOwner}
          </div>
        </div>
      )}

      <div className="grid-2 mt-14" style={{ gap: 14 }}>
        {items.map((item) => {
          const ready = Boolean(item.address);

          return (
            <article
              key={item.key}
              className="block tight"
              style={{ background: 'var(--bg-2)', boxShadow: 'none' }}
            >
              <div className="row between" style={{ alignItems: 'flex-start', gap: 10 }}>
                <div>
                  <h3 style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 15, margin: 0 }}>{item.title}</h3>
                  <p style={{ marginTop: 4, fontSize: 12.5, color: 'var(--ink-2)', lineHeight: 1.5 }}>{item.description}</p>
                </div>
                <StatusBadge
                  ready={ready}
                  readyLabel={t('statusReady')}
                  missingLabel={t('statusMissing')}
                />
              </div>

              <div
                className="mono"
                style={{
                  marginTop: 12,
                  padding: '10px 12px',
                  borderRadius: 10,
                  border: '2px solid var(--ink)',
                  background: 'var(--paper)',
                  fontSize: 11,
                  wordBreak: 'break-all',
                }}
              >
                {isLoading ? t('loadingAddress') : item.address || t('missingAddress')}
              </div>

              {!ready && (
                <div className="mt-14">
                  <div className="eyebrow" style={{ marginBottom: 6 }}>
                    {t('deployCommand')}
                  </div>
                  <pre
                    className="mono"
                    style={{
                      overflowX: 'auto',
                      margin: 0,
                      padding: '10px 12px',
                      borderRadius: 10,
                      border: '2px dashed var(--ink)',
                      background: 'var(--paper)',
                      fontSize: 11,
                      lineHeight: 1.5,
                    }}
                  >
                    {item.command}
                  </pre>
                </div>
              )}
            </article>
          );
        })}
      </div>
    </section>
  );
}
