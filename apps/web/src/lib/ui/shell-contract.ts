import type { ReactNode } from 'react';
import type { ChainTokenConfig } from '@wallet-trade/shared';
import type { TxErrorCode, TxInvalidationReason, TxLifecycleState, TxPhase, TxReviewFacts } from '@/hooks/web3/txIntent/types';

export type WalletState = 'disconnected' | 'connecting' | 'siwe' | 'connected' | 'wrong-network';
export type WalletInfo = { address: string; chainId: number; balance: number } | null;
export type ToastKind = 'ok' | 'warn' | 'err';
export type ToastItem = { id: string; msg: string; kind: ToastKind };

export interface TokenPickerProps {
  tokens: ChainTokenConfig[];
  exclude?: string;
  onPick: (token: ChainTokenConfig) => void;
}

export interface LegacyTransactionProps {
  mode?: 'legacy';
  stage: 'submit' | 'pending' | 'confirmed' | 'failed';
  title?: string;
  summary?: string;
  hash?: string;
  explorerUrl?: string | null;
  error?: string;
}

// Only registered presentation contracts cross the shell boundary. Transaction
// callbacks stay with their lifecycle owner and must never come from campaign JSON.
export type ModalState =
  | { kind: 'wallet' | 'chain' | 'siwe' }
  | { kind: 'tokenPicker'; props: TokenPickerProps }
  | { kind: 'tx'; props: TxIntentModalProps | LegacyTransactionProps }
  | null;
export type ModalKind = NonNullable<ModalState>['kind'];

export interface TxIntentModalProps {
  mode: 'intent';
  phase: TxPhase;
  state: TxLifecycleState;
  title: string;
  stepTitle?: string;
  step?: { current: number; total: number };
  review: TxReviewFacts | null;
  expiresAt?: number;
  expectsIndexing: boolean;
  indexing: 'not-expected' | 'pending' | 'indexed' | 'timed-out';
  hash?: string | null;
  replacedByHash?: string | null;
  explorerUrl?: string | null;
  errorCode?: TxErrorCode | null;
  invalidationReason?: TxInvalidationReason | null;
  technicalHint?: string | null;
  summary?: string;
  preflight?: ReactNode;
  signLabel?: string;
  signDisabled?: boolean;
  onSign?: () => void;
  onRetry?: () => void;
}
