import { fetchApi } from '@/lib/api/auth-fetch';
import { uploadMediaFile } from '@/lib/api/media';
import { usePersistTransactionLifecycle } from '@/hooks/web3/usePersistTransactionLifecycle';
import { CONTRACT_ABIS, getTokenFactoryAddress } from '@/lib/web3/contracts';
import { useCallback, useEffect, useRef, useState } from 'react';
import { decodeEventLog, parseEther } from 'viem';
import { useWaitForTransactionReceipt, useWriteContract } from 'wagmi';
import type { SubmissionNotice, TokenForm } from './create-token.types';

interface UseCreateTokenSubmissionParams {
  chainId: number;
  form: TokenForm;
  isConnected: boolean;
  walletAddress?: `0x${string}`;
  onCreated: (tokenAddress: string) => void;
  t: (key: string, values?: Record<string, string | number>) => string;
}

interface SubmittedDraft {
  form: TokenForm;
  assetUrls: {
    image: string;
    banner: string;
  };
}

// Upload the user's selected icon/banner through the media service and return
// the DURABLE stored URLs that go into both the contract args and the /token
// row. No file selected ⇒ empty string (an honest "no image"), never a
// generated placeholder. Any upload failure aborts token creation.
async function uploadSelectedAssets(form: TokenForm): Promise<{ image: string; banner: string }> {
  const [image, banner] = await Promise.all([
    form.image ? uploadMediaFile(form.image).then((m) => m.url) : Promise.resolve(''),
    form.banner ? uploadMediaFile(form.banner).then((m) => m.url) : Promise.resolve(''),
  ]);
  return { image, banner };
}

export function useCreateTokenSubmission({
  chainId,
  form,
  isConnected,
  walletAddress,
  onCreated,
  t,
}: UseCreateTokenSubmissionParams) {
  const submittedDraftRef = useRef<SubmittedDraft | null>(null);
  const [notice, setNotice] = useState<SubmissionNotice | null>(null);
  const factoryAddress = getTokenFactoryAddress(chainId);

  const { writeContract, data: hash, isPending, error: writeError } = useWriteContract();
  const { isLoading: isWaiting, isSuccess: isConfirmed, data: receipt } =
    useWaitForTransactionReceipt({
      hash,
    });

  usePersistTransactionLifecycle({
    chainId,
    hash,
    fromAddress: walletAddress,
    toAddress: factoryAddress,
    contractAddress: factoryAddress,
    value: parseEther('0.001'),
    txType: 'create-token',
    metadata: submittedDraftRef.current
      ? {
          name: submittedDraftRef.current.form.name,
          symbol: submittedDraftRef.current.form.symbol,
          hasMargin: submittedDraftRef.current.form.hasMargin,
          hasReservation: submittedDraftRef.current.form.hasReservation,
          isOfficial: submittedDraftRef.current.form.isOfficial,
        }
      : null,
    receipt,
    enabled: Boolean(walletAddress && factoryAddress && hash),
  });

  const handleSubmit = useCallback(async () => {
    if (!isConnected || !walletAddress) {
      setNotice({
        tone: 'error',
        message: t('createToken.walletRequired'),
      });
      return;
    }

    if (!factoryAddress) {
      setNotice({
        tone: 'error',
        message: t('createToken.missingFactory'),
      });
      return;
    }

    // 1) Media first: the selected files must be stored durably BEFORE any
    //    wallet interaction so the contract args carry the real URLs. Upload
    //    failure blocks the whole flow — no placeholder substitution.
    let assetUrls: { image: string; banner: string };
    try {
      if (form.image || form.banner) {
        setNotice({
          tone: 'info',
          message: t('createToken.uploadingMedia'),
        });
      }
      assetUrls = await uploadSelectedAssets(form);
    } catch (error) {
      console.error('Error uploading token media:', error);
      setNotice({
        tone: 'error',
        message: t('createToken.mediaUploadFailed', {
          message: error instanceof Error ? error.message : '',
        }),
      });
      return;
    }

    try {
      submittedDraftRef.current = {
        form: { ...form, tags: [...form.tags] },
        assetUrls,
      };

      setNotice({
        tone: 'info',
        message: t('createToken.awaitingWallet'),
      });

      writeContract({
        address: factoryAddress,
        abi: CONTRACT_ABIS.TokenFactory,
        functionName: 'createAndLaunch',
        args: [
          form.name,
          form.symbol,
          form.description,
          assetUrls.image,
          assetUrls.banner,
          parseEther('0'),
        ],
        value: parseEther('0.001'),
      });
    } catch (error) {
      console.error('Error creating token:', error);
      setNotice({
        tone: 'error',
        message: t('createToken.failedToStart'),
      });
    }
  }, [factoryAddress, form, isConnected, t, walletAddress, writeContract]);

  useEffect(() => {
    if (!writeError) {
      return;
    }

    setNotice({
      tone: 'error',
      message: writeError.message,
    });
  }, [writeError]);

  useEffect(() => {
    if (!isConfirmed || !receipt || !submittedDraftRef.current) {
      return;
    }

    const syncWithBackend = async () => {
      const submittedDraft = submittedDraftRef.current;
      if (!submittedDraft) {
        return;
      }

      setNotice({
        tone: 'info',
        message: t('createToken.syncingBackend'),
      });

      try {
        let tokenAddress: string | undefined;
        let bondingCurveAddress: string | undefined;

        for (const log of receipt.logs) {
          try {
            const decoded = decodeEventLog({
              abi: CONTRACT_ABIS.TokenFactory,
              data: log.data,
              topics: log.topics,
            });

            if (decoded.eventName === 'TokenCreated') {
              tokenAddress = decoded.args.token;
              bondingCurveAddress = decoded.args.bondingCurve;
              break;
            }
          } catch {
            continue;
          }
        }

        if (!tokenAddress) {
          throw new Error(t('createToken.tokenCreatedEventMissing'));
        }

        const response = await fetchApi('/token', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({
            symbol: submittedDraft.form.symbol,
            name: submittedDraft.form.name,
            description: submittedDraft.form.description,
            image: submittedDraft.assetUrls.image,
            banner: submittedDraft.assetUrls.banner,
            tags: submittedDraft.form.tags,
            launchType: 'NEW_COIN',
            preBuyPercent: submittedDraft.form.preBuyPercent,
            hasMargin: submittedDraft.form.hasMargin,
            hasReservation: submittedDraft.form.hasReservation,
            isOfficial: submittedDraft.form.isOfficial,
            chainId,
            contractAddress: tokenAddress,
            bondingCurveAddress,
            transactionHash: hash,
          }),
        });

        if (!response.ok) {
          throw new Error(t('createToken.backendSyncFailed'));
        }

        setNotice({
          tone: 'success',
          message: t('createToken.createdSuccess'),
        });
        submittedDraftRef.current = null;
        onCreated(tokenAddress);
      } catch (error) {
        console.error('Error syncing token metadata:', error);
        setNotice({
          tone: 'error',
          message:
            error instanceof Error ? error.message : t('createToken.finalizeError'),
        });
      }
    };

    void syncWithBackend();
  }, [chainId, hash, isConfirmed, onCreated, receipt, t]);

  return {
    handleSubmit,
    hash,
    isBusy: isPending || isWaiting,
    isConfirming: isPending,
    isWaiting,
    notice,
  };
}
