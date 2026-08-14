'use client';

import { buildApiUrl } from '@/lib/api/base-url';
import { buildTransactionExplorerUrl } from '@/lib/web3/explorer';
import { useQuery } from '@tanstack/react-query';
import { formatDistanceToNow } from 'date-fns';
import { formatEther } from 'viem';

interface TradeHistoryProps {
    tokenAddress: string;
    chainId?: number;
}

interface Trade {
    id: string;
    type: 'BUY' | 'SELL';
    tokenAmount: string;
    ethAmount: string;
    price: number;
    userAddress: string;
    transactionHash: string;
    timestamp: string;
}

export function TradeHistory({ tokenAddress, chainId }: TradeHistoryProps) {
    const { data: trades, isLoading } = useQuery({
        queryKey: ['trades', tokenAddress],
        queryFn: async () => {
            const res = await fetch(buildApiUrl(`/token/address/${tokenAddress}/trades?limit=50`));
            if (!res.ok) throw new Error('Failed to fetch trades');
            return res.json() as Promise<Trade[]>;
        },
        refetchInterval: 5000,
    });

    if (isLoading) return <div className="card p-4 animate-pulse h-64"></div>;

    return (
        <div className="card">
            <div className="border-b border-subtle p-4">
                <h3 className="font-bold">Recent Trades</h3>
            </div>
            <div className="max-h-[400px] overflow-y-auto">
                <table className="w-full text-sm">
                    <thead className="sticky top-0 bg-(--color-background-card) text-(--color-text-muted)">
                        <tr>
                            <th className="p-3 text-left">Type</th>
                            <th className="p-3 text-right">ETH</th>
                            <th className="p-3 text-right">Tokens</th>
                            <th className="p-3 text-right">Date</th>
                            <th className="p-3 text-right">Tx</th>
                        </tr>
                    </thead>
                    <tbody>
                        {trades?.map((trade) => {
                            const explorerUrl = buildTransactionExplorerUrl(trade.transactionHash, chainId);

                            return (
                                <tr key={trade.id} className="border-b border-subtle last:border-0 hover:bg-(--color-background-hover)">
                                    <td className={`p-3 font-medium ${trade.type === 'BUY' ? 'text-(--color-accent)' : 'text-red-500'}`}>
                                        {trade.type}
                                    </td>
                                    <td className="p-3 text-right font-mono">{Number(formatEther(BigInt(trade.ethAmount))).toFixed(4)} ETH</td>
                                    <td className="p-3 text-right font-mono">
                                        {Number(formatEther(BigInt(trade.tokenAmount))).toLocaleString(undefined, { maximumFractionDigits: 0 })}
                                    </td>
                                    <td className="p-3 text-right text-(--color-text-muted)">
                                        {formatDistanceToNow(new Date(trade.timestamp), { addSuffix: true })}
                                    </td>
                                    <td className="p-3 text-right">
                                        {explorerUrl ? (
                                            <a
                                                href={explorerUrl}
                                                target="_blank"
                                                rel="noopener noreferrer"
                                                className="text-(--color-accent) hover:underline"
                                            >
                                                {trade.transactionHash.slice(0, 6)}...
                                            </a>
                                        ) : (
                                            <span>{trade.transactionHash.slice(0, 6)}...</span>
                                        )}
                                    </td>
                                </tr>
                            );
                        })}
                        {trades?.length === 0 && (
                            <tr>
                                <td colSpan={5} className="p-8 text-center text-(--color-text-muted)">No trades yet</td>
                            </tr>
                        )}
                    </tbody>
                </table>
            </div>
        </div>
    );
}
