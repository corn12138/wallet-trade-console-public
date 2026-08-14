export interface TokenData {
    id: string;
    address?: string | null;
    chainId: number;
    symbol: string;
    name: string;
    description?: string | null;
    image?: string | null;
    banner?: string | null;
    status: string;
    marketCap?: string | null; // normalized NATIVE decimal string
    volume24h?: string | null; // normalized NATIVE decimal string
    priceChange24h?: number | null;
    creatorAddress: string;
    bondingCurve?: string | null;
    website?: string | null;
    twitter?: string | null;
    telegram?: string | null;
    discord?: string | null;
    launchedAt?: string | null;
    createdAt: string;
}

export interface TokenStats {
    tokenId: string;
    address: string;
    latestPrice: number | null;
    volume24h: string; // decimal string from GET /stats
    priceChange24h: number;
}

export interface SidebarToken {
    id: string;
    address?: string | null;
    symbol: string;
    name: string;
    image?: string | null;
    marketCap?: string | null; // normalized NATIVE decimal string
    volume24h?: string | null; // normalized NATIVE decimal string
    priceChange24h?: number | null;
    createdAt: string;
}

export interface TradeNotice {
    tone: 'success' | 'error' | 'info';
    title: string;
    message: string;
}
