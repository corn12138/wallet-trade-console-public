'use client';

import { buildApiUrl } from '@/lib/api/base-url';
import { useQuery } from '@tanstack/react-query';
import Link from 'next/link';

interface TokenRequest {
    symbol: string;
    priceChange24h: number;
    address: string;
}

export function TrendingTicker() {
    const { data: trending } = useQuery({
        queryKey: ['trending-tokens'],
        queryFn: async () => {
            const res = await fetch(buildApiUrl('/token/trending?limit=10'));
            if (!res.ok) throw new Error('Failed to fetch trending');
            return res.json() as Promise<TokenRequest[]>;
        },
        refetchInterval: 30000,
    });

    if (!trending || trending.length === 0) return null;

    return (
        <div className="ticker-bar overflow-hidden whitespace-nowrap">
            <div className="inline-flex animate-ticker">
                {[...trending, ...trending].map((item, i) => (
                    <Link key={`${item.symbol}-${i}`} href={`/token/${item.address}`} className="ticker-item inline-flex items-center mx-4">
                        <span style={{ color: 'var(--color-text-primary)' }} className="font-bold mr-1">{item.symbol}</span>
                        <span style={{ color: (item.priceChange24h || 0) >= 0 ? 'var(--color-success)' : 'var(--color-error)' }}>
                            {(item.priceChange24h || 0) >= 0 ? '+' : ''}{(item.priceChange24h || 0).toFixed(2)}%
                        </span>
                    </Link>
                ))}
            </div>
            <style jsx>{`
        .animate-ticker {
          animation: ticker 30s linear infinite;
        }
        @keyframes ticker {
          0% { transform: translateX(0); }
          100% { transform: translateX(-50%); }
        }
        .ticker-bar:hover .animate-ticker {
            animation-play-state: paused;
        }
      `}</style>
        </div>
    );
}
