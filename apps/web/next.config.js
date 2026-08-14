const path = require('path');
const createNextIntlPlugin = require('next-intl/plugin');

const withNextIntl = createNextIntlPlugin('./src/i18n/request.ts');

const DEFAULT_LOCAL_API_REWRITE_ORIGIN = 'http://127.0.0.1:8090';

function stripTrailingSlash(value) {
  return value.endsWith('/') ? value.slice(0, -1) : value;
}

function getWalletTradeApiRewriteOrigin() {
  const configured = process.env.WALLET_TRADE_API_REWRITE_ORIGIN?.trim();
  if (configured) {
    return stripTrailingSlash(configured);
  }

  // A hosted deployment must choose its own upstream explicitly. Falling back
  // to a maintainer-owned origin would couple public forks to private
  // infrastructure and could send their server-side traffic to the wrong API.
  if (process.env.VERCEL === '1') {
    throw new Error('WALLET_TRADE_API_REWRITE_ORIGIN is required on Vercel');
  }

  return DEFAULT_LOCAL_API_REWRITE_ORIGIN;
}

/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  transpilePackages: ['@wallet-trade/shared'],
  // Dev-only: Next 16 blocks cross-origin dev-resource requests by default,
  // and it treats 127.0.0.1 as a different origin than localhost. The strict
  // browser smoke (scripts/strict/frontend-realdata-browser-smoke.py) drives
  // the app via http://127.0.0.1:3002 — the origin on the Go API's CORS dev
  // allowlist — so allow it here too. No effect on production builds.
  allowedDevOrigins: ['127.0.0.1'],
  async rewrites() {
    if (process.env.VERCEL !== '1' && process.env.WALLET_TRADE_ENABLE_API_REWRITE !== '1') {
      return [];
    }

    const apiRewriteOrigin = getWalletTradeApiRewriteOrigin();

    return [
      {
        source: '/api/:path*',
        destination: `${apiRewriteOrigin}/api/:path*`,
      },
      {
        source: '/socket.io/:path*',
        destination: `${apiRewriteOrigin}/socket.io/:path*`,
      },
    ];
  },
  webpack: (config, { isServer }) => {
    if (!isServer) {
      config.resolve.fallback = {
        ...config.resolve.fallback,
        fs: false,
        path: false,
      };
    }
    config.resolve.alias = {
      ...config.resolve.alias,
      '@': path.resolve(__dirname, './src'),
    };
    return config;
  },
};

module.exports = withNextIntl(nextConfig);
