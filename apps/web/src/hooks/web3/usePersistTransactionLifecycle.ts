'use client';

import { useEffect, useRef } from 'react';
import type { TransactionReceipt } from 'viem';
import {
  reportSubmittedTransaction,
  reportTransactionReceipt,
} from '@/lib/api/events';

interface UsePersistTransactionLifecycleParams {
  hash?: `0x${string}`;
  chainId?: number;
  fromAddress?: `0x${string}`;
  toAddress?: `0x${string}` | null;
  contractAddress?: `0x${string}` | null;
  value?: bigint | string | null;
  txType?: string | null;
  metadata?: Record<string, unknown> | null;
  receipt?: TransactionReceipt | null;
  enabled?: boolean;
}

interface PersistedTransactionSnapshot {
  chainId: number;
  fromAddress: `0x${string}`;
  toAddress?: `0x${string}` | null;
  contractAddress?: `0x${string}` | null;
  value?: bigint | string | null;
  txType?: string | null;
  metadata?: Record<string, unknown> | null;
}

export function usePersistTransactionLifecycle({
  hash,
  chainId,
  fromAddress,
  toAddress,
  contractAddress,
  value,
  txType,
  metadata,
  receipt,
  enabled = true,
}: UsePersistTransactionLifecycleParams) {
  const submittedHashRef = useRef<string | null>(null);
  const finalizedKeyRef = useRef<string | null>(null);
  const snapshotMapRef = useRef<Record<string, PersistedTransactionSnapshot>>({});

  useEffect(() => {
    if (!enabled || !hash || !chainId || !fromAddress || !hasWeb3Session()) {
      return;
    }

    if (submittedHashRef.current === hash) {
      return;
    }

    snapshotMapRef.current[hash] = {
      chainId,
      fromAddress,
      toAddress,
      contractAddress,
      value,
      txType,
      metadata,
    };
    submittedHashRef.current = hash;

    void reportSubmittedTransaction({
      chainId,
      txHash: hash,
      fromAddress,
      toAddress,
      contractAddress,
      value: stringifyOptionalValue(value),
      txType,
      metadata,
    }).catch((error) => {
      submittedHashRef.current = null;
      console.error('Failed to persist submitted transaction', error);
    });
  }, [chainId, contractAddress, enabled, fromAddress, hash, metadata, toAddress, txType, value]);

  useEffect(() => {
    if (!enabled || !hash || !chainId || !fromAddress || !receipt || !hasWeb3Session()) {
      return;
    }

    const snapshot = snapshotMapRef.current[hash] ?? {
      chainId,
      fromAddress,
      toAddress,
      contractAddress,
      value,
      txType,
      metadata,
    };
    const receiptKey = `${hash}:${receipt.blockNumber.toString()}:${receipt.status}`;
    if (finalizedKeyRef.current === receiptKey) {
      return;
    }

    finalizedKeyRef.current = receiptKey;

    void reportTransactionReceipt(hash, {
      chainId: snapshot.chainId,
      fromAddress: snapshot.fromAddress,
      status: receipt.status === 'success' ? 'confirmed' : 'failed',
      blockNumber: receipt.blockNumber.toString(),
      gasUsed: receipt.gasUsed.toString(),
      gasPrice: receipt.effectiveGasPrice.toString(),
      toAddress: receipt.to ?? snapshot.toAddress ?? null,
      contractAddress: receipt.contractAddress ?? snapshot.contractAddress ?? null,
      value: stringifyOptionalValue(snapshot.value),
      txType: snapshot.txType,
      metadata: snapshot.metadata,
    }).catch((error) => {
      finalizedKeyRef.current = null;
      console.error('Failed to persist transaction receipt', error);
    });
  }, [chainId, contractAddress, enabled, fromAddress, hash, metadata, receipt, toAddress, txType, value]);
}

function stringifyOptionalValue(value?: bigint | string | null) {
  if (value === undefined) {
    return undefined;
  }

  if (value === null) {
    return null;
  }

  return typeof value === 'bigint' ? value.toString() : value;
}

function hasWeb3Session() {
  if (typeof window === 'undefined') {
    return false;
  }

  return Boolean(window.localStorage.getItem('web3_auth_token'));
}
