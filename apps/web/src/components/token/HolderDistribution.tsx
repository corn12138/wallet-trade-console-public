'use client';

import { buildApiUrl } from '@/lib/api/base-url';
import { buildAddressExplorerUrl } from '@/lib/web3/explorer';
import { useQuery } from '@tanstack/react-query';
import { formatEther } from 'viem';

interface HolderDistributionProps {
    tokenAddress: string;
    chainId?: number;
}

interface Holder {
    id: string;
    userAddress: string;
    balance: string;
}

export function HolderDistribution({ tokenAddress, chainId }: HolderDistributionProps) {
    const { data: holders, isLoading } = useQuery({
        queryKey: ['holders', tokenAddress],
        queryFn: async () => {
            const res = await fetch(buildApiUrl(`/token/address/${tokenAddress}/holders?limit=50`));
            if (!res.ok) throw new Error('Failed to fetch holders');
            return res.json() as Promise<Holder[]>;
        },
    });

    if (isLoading) return <div className="card p-4 animate-pulse h-64"></div>;

    // Calculate total supply (approximate from holders or fetch from contract - for percentage)
    // For now just show raw amounts
    const totalHeld = holders?.reduce((acc, h) => acc + BigInt(h.balance), 0n) || 1n;

    return (
        <div className="card">
            <div className="border-b border-subtle p-4">
                <h3 className="font-bold">Top Holders</h3>
            </div>
            <div className="max-h-[400px] overflow-y-auto">
                <table className="w-full text-sm">
                    <thead className="sticky top-0 bg-(--color-background-card) text-(--color-text-muted)">
                        <tr>
                            <th className="p-3 text-left">Rank</th>
                            <th className="p-3 text-left">Address</th>
                            <th className="p-3 text-right">Balance</th>
                            <th className="p-3 text-right">%</th>
                        </tr>
                    </thead>
                    <tbody>
                        {holders?.map((holder, index) => {
                            const balance = BigInt(holder.balance);
                            const percentage = (Number(balance * 10000n / totalHeld) / 100).toFixed(2);
                            const explorerUrl = buildAddressExplorerUrl(holder.userAddress, chainId);

                            return (
                                <tr key={holder.id} className="border-b border-subtle last:border-0 hover:bg-(--color-background-hover)">
                                    <td className="p-3 text-(--color-text-muted)">{index + 1}</td>
                                    <td className="p-3 font-mono">
                                        {explorerUrl ? (
                                            <a
                                                href={explorerUrl}
                                                target="_blank"
                                                rel="noopener noreferrer"
                                                className="hover:text-(--color-accent)"
                                            >
                                                {holder.userAddress.slice(0, 6)}...{holder.userAddress.slice(-4)}
                                            </a>
                                        ) : (
                                            <span>{holder.userAddress.slice(0, 6)}...{holder.userAddress.slice(-4)}</span>
                                        )}
                                    </td>
                                    <td className="p-3 text-right font-mono">
                                        {Number(formatEther(balance)).toLocaleString(undefined, { maximumFractionDigits: 0 })}
                                    </td>
                                    <td className="p-3 text-right text-(--color-text-muted)">{percentage}%</td>
                                </tr>
                            );
                        })}
                        {holders?.length === 0 && (
                            <tr>
                                <td colSpan={4} className="p-8 text-center text-(--color-text-muted)">No holders found</td>
                            </tr>
                        )}
                    </tbody>
                </table>
            </div>
        </div>
    );
}
