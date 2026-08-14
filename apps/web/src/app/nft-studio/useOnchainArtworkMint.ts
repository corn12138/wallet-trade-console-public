import { usePersistTransactionLifecycle } from '@/hooks/web3/usePersistTransactionLifecycle';
import { CONTRACT_ABIS } from '@/lib/web3/contracts';
import { useEffect, useState } from 'react';
import { decodeEventLog } from 'viem';
import { useChainId, useWaitForTransactionReceipt, useWriteContract } from 'wagmi';
import type { OnchainArtworkDraft, SubmissionNotice } from './nft-studio.types';
import { getTransactionErrorMessage } from './nft-studio.utils';

interface UseOnchainArtworkMintParams {
  contractAddress?: `0x${string}`;
  isConnected: boolean;
  walletAddress?: `0x${string}`;
  t: (key: string, values?: Record<string, string | number>) => string;
}

export function useOnchainArtworkMint({
  contractAddress,
  isConnected,
  walletAddress,
  t,
}: UseOnchainArtworkMintParams) {
  const chainId = useChainId();
  const [notice, setNotice] = useState<SubmissionNotice | null>(null);
  const [mintedTokenId, setMintedTokenId] = useState<string | null>(null);
  const [hash, setHash] = useState<`0x${string}` | undefined>();
  const [trackingMetadata, setTrackingMetadata] = useState<Record<string, unknown> | null>(null);

  const { writeContractAsync, isPending, error } = useWriteContract();
  const {
    isLoading: isWaiting,
    isSuccess,
    data: receipt,
    error: receiptError,
  } = useWaitForTransactionReceipt({ hash });

  usePersistTransactionLifecycle({
    chainId,
    hash,
    fromAddress: walletAddress,
    toAddress: contractAddress,
    contractAddress,
    value: '0',
    txType: 'mint-onchain-artwork',
    metadata: trackingMetadata,
    receipt,
    enabled: Boolean(walletAddress && contractAddress && hash),
  });

  const mintArtwork = async (draft: OnchainArtworkDraft) => {
    if (!isConnected || !walletAddress) {
      setNotice({ tone: 'error', message: t('walletRequired') });
      return;
    }

    if (!contractAddress) {
      setNotice({ tone: 'error', message: t('missingOnchainContract') });
      return;
    }

    if (!draft.title.trim() || !draft.caption.trim() || !draft.recipient.trim()) {
      setNotice({ tone: 'error', message: t('missingOnchainFields') });
      return;
    }

    setMintedTokenId(null);
    setTrackingMetadata({
      recipient: draft.recipient.trim(),
      title: draft.title.trim(),
      accentColor: draft.accentColor.trim(),
    });
    setNotice({ tone: 'info', message: t('awaitingWallet') });

    try {
      const nextHash = await writeContractAsync({
        address: contractAddress,
        abi: CONTRACT_ABIS.OnchainArtworkNFT,
        functionName: 'mintArtwork',
        args: [
          draft.recipient as `0x${string}`,
          draft.title.trim(),
          draft.caption.trim(),
          draft.accentColor.trim(),
        ],
      });

      setHash(nextHash);
      setNotice({ tone: 'info', message: t('transactionSubmitted') });
    } catch (submitError) {
      setNotice({
        tone: 'error',
        message: getTransactionErrorMessage(submitError, t('mintFailed')),
      });
    }
  };

  useEffect(() => {
    if (!error) {
      return;
    }

    setNotice({
      tone: 'error',
      message: getTransactionErrorMessage(error, t('mintFailed')),
    });
  }, [error, t]);

  useEffect(() => {
    if (!hash || !isWaiting) {
      return;
    }

    setNotice({ tone: 'info', message: t('confirmingMint') });
  }, [hash, isWaiting, t]);

  useEffect(() => {
    if (!receiptError) {
      return;
    }

    setNotice({
      tone: 'error',
      message: getTransactionErrorMessage(receiptError, t('mintFailed')),
    });
  }, [receiptError, t]);

  useEffect(() => {
    if (!isSuccess || !receipt) {
      return;
    }

    let nextTokenId: string | null = null;

    for (const log of receipt.logs) {
      try {
        const decoded = decodeEventLog({
          abi: CONTRACT_ABIS.OnchainArtworkNFT,
          data: log.data,
          topics: log.topics,
        });

        if (decoded.eventName === 'Transfer') {
          nextTokenId = decoded.args.tokenId?.toString() || null;
          break;
        }
      } catch {
        continue;
      }
    }

    setMintedTokenId(nextTokenId);
    setNotice({
      tone: 'success',
      message: nextTokenId
        ? t('mintSuccessWithTokenId', { tokenId: nextTokenId })
        : t('mintSuccess'),
    });
  }, [isSuccess, receipt, t]);

  return {
    mintArtwork,
    mintedTokenId,
    notice,
    isBusy: isPending || isWaiting,
  };
}
