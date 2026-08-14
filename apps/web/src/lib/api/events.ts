/**
 * @file Events API Client
 * @description 链上事件查询 API 客户端
 */

import type { PaginatedDataResponse } from '@wallet-trade/shared';
import { fetchApi } from './auth-fetch';

// 事件类型
export interface Web3Event {
  id: string;
  eventName: string;
  contractAddress: string;
  actorAddress: string;
  txHash: string;
  blockNumber: string;
  logIndex: number;
  args: Record<string, unknown>;
  chainId: number;
  timestamp: string;
  createdAt: string;
}

export type PaginatedResponse<T> = PaginatedDataResponse<T>;

// 统计响应
export interface StatsResponse {
  eventCounts: { event: string; count: number }[];
  latestBlock: string;
  indexerBlock: string;
  totalEvents: number;
}

export interface TrackedWeb3Transaction {
  id: number;
  chainId: number;
  txHash: string;
  fromAddress: string;
  toAddress: string | null;
  contractAddress: string | null;
  value: string | null;
  gasUsed: string | null;
  gasPrice: string | null;
  blockNumber: string;
  status: string;
  txType: string | null;
  metadata: Record<string, unknown> | null;
  createdAt: string;
}

// 查询参数
export interface GetEventsParams {
  page?: number;
  limit?: number;
  eventName?: string;
  actorAddress?: string;
  contractAddress?: string;
  chainId?: number;
  fromBlock?: number;
  toBlock?: number;
}

/**
 * 解析 API 响应
 */
async function parseResponse<T>(response: Response): Promise<T> {
  if (!response.ok) {
    const error = await response.json().catch(() => ({ message: 'Request failed' }));
    throw new Error(error.message || `HTTP ${response.status}`);
  }
  const data = await response.json();
  // 处理包装响应 { code, message, data }
  return data.data || data;
}

/**
 * 获取事件列表
 */
export async function getEvents(params: GetEventsParams = {}): Promise<PaginatedResponse<Web3Event>> {
  const searchParams = new URLSearchParams();

  if (params.page) searchParams.set('page', String(params.page));
  if (params.limit) searchParams.set('limit', String(params.limit));
  if (params.eventName) searchParams.set('eventName', params.eventName);
  if (params.actorAddress) searchParams.set('actorAddress', params.actorAddress);
  if (params.contractAddress) searchParams.set('contractAddress', params.contractAddress);
  if (params.chainId) searchParams.set('chainId', String(params.chainId));
  if (params.fromBlock) searchParams.set('fromBlock', String(params.fromBlock));
  if (params.toBlock) searchParams.set('toBlock', String(params.toBlock));

  const path = `/web3-events?${searchParams.toString()}`;
  const response = await fetchApi(path);
  return parseResponse<PaginatedResponse<Web3Event>>(response);
}

/**
 * 获取事件统计
 */
export async function getStats(chainId?: number): Promise<StatsResponse> {
  const path = chainId
    ? `/web3-events/stats?chainId=${chainId}`
    : '/web3-events/stats';
  const response = await fetchApi(path);
  return parseResponse<StatsResponse>(response);
}

/**
 * 按交易哈希获取事件
 */
export async function getEventsByTxHash(txHash: string, chainId?: number): Promise<Web3Event[]> {
  const path = chainId
    ? `/web3-events/tx/${txHash}?chainId=${chainId}`
    : `/web3-events/tx/${txHash}`;
  const response = await fetchApi(path);
  return parseResponse<Web3Event[]>(response);
}

/**
 * 获取用户事件
 */
export async function getUserEvents(
  address: string,
  chainId?: number,
  limit?: number
): Promise<Web3Event[]> {
  const searchParams = new URLSearchParams();
  if (chainId) searchParams.set('chainId', String(chainId));
  if (limit) searchParams.set('limit', String(limit));

  const path = `/web3-events/user/${address}?${searchParams.toString()}`;
  const response = await fetchApi(path);
  return parseResponse<Web3Event[]>(response);
}

/**
 * 获取最近交易
 */
export async function getRecentTransactions(
  chainId?: number,
  limit?: number
): Promise<unknown[]> {
  const searchParams = new URLSearchParams();
  if (chainId) searchParams.set('chainId', String(chainId));
  if (limit) searchParams.set('limit', String(limit));

  const path = `/web3-events/transactions?${searchParams.toString()}`;
  const response = await fetchApi(path);
  return parseResponse<unknown[]>(response);
}

export async function reportSubmittedTransaction(input: {
  chainId?: number;
  txHash: string;
  fromAddress?: string;
  toAddress?: string | null;
  contractAddress?: string | null;
  value?: string | null;
  txType?: string | null;
  metadata?: Record<string, unknown> | null;
}): Promise<TrackedWeb3Transaction> {
  const response = await fetchApi('/web3-events/transactions', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseResponse<TrackedWeb3Transaction>(response);
}

export async function reportTransactionReceipt(
  txHash: string,
  input: {
    chainId?: number;
    fromAddress?: string;
    status?: string | null;
    blockNumber?: string | number | bigint | null;
    gasUsed?: string | number | bigint | null;
    gasPrice?: string | number | bigint | null;
    toAddress?: string | null;
    contractAddress?: string | null;
    value?: string | null;
    txType?: string | null;
    metadata?: Record<string, unknown> | null;
  }
): Promise<TrackedWeb3Transaction> {
  const response = await fetchApi(`/web3-events/transactions/${txHash}/receipt`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseResponse<TrackedWeb3Transaction>(response);
}
