/**
 * @file wagmi 配置
 * @description Web3 钱包连接配置
 *
 * 关键概念：
 * - chains: 支持的区块链网络
 * - connectors: 钱包连接器（MetaMask, WalletConnect 等）
 * - transports: RPC 传输层
 */

import { injected } from '@wagmi/core';
import { http, createConfig, createStorage, cookieStorage } from 'wagmi';
import { mainnet, sepolia, baseSepolia, arbitrumSepolia, localhost } from 'wagmi/chains';

/**
 * 自定义本地开发链配置
 * Anvil 默认端口 8545
 */
const localAnvil = {
  ...localhost,
  id: 31337,
  name: 'Anvil Local',
  nativeCurrency: {
    decimals: 18,
    name: 'Ether',
    symbol: 'ETH',
  },
  rpcUrls: {
    default: { http: ['http://127.0.0.1:8545'] },
  },
} as const;

/**
 * wagmi 配置
 *
 * 支持的链：
 * - mainnet: 以太坊主网
 * - sepolia: Sepolia 测试网
 * - baseSepolia: 桥的第二条链（BridgeGateway 已部署，84532）
 * - arbitrumSepolia: 桥的第三条链（BridgeGateway 已部署，421614）
 * - localAnvil: 本地 Anvil 开发网络
 *
 * 注意：桥的目标链下拉框只列 supportedChains。一条链在链上部署了网关、
 * 但没进这个数组，用户就选不到它 —— 后端会诚实回答路由可用，前端却给不出
 * 入口。加链和部署网关是同一件事的两半。
 */
export const wagmiConfig = createConfig({
  // A disconnected visitor starts in the same testnet context as the public demo.
  chains: [sepolia, mainnet, baseSepolia, arbitrumSepolia, localAnvil],
  multiInjectedProviderDiscovery: true,

  // 连接器配置 - 仅使用 injected（支持 MetaMask, Rabby, OKX 等浏览器钱包）
  // 如需添加 WalletConnect/Coinbase，需要安装对应的 SDK 包
  connectors: [
    injected({
      shimDisconnect: true,
    }),
  ],

  // RPC 传输配置
  transports: {
    [mainnet.id]: http(process.env.NEXT_PUBLIC_MAINNET_RPC_URL),
    [sepolia.id]: http(process.env.NEXT_PUBLIC_SEPOLIA_RPC_URL),
    // Keyless public RPC by default; override for a rate-limited provider.
    [baseSepolia.id]: http(
      process.env.NEXT_PUBLIC_BASE_SEPOLIA_RPC_URL || 'https://base-sepolia-rpc.publicnode.com',
    ),
    [arbitrumSepolia.id]: http(
      process.env.NEXT_PUBLIC_ARBITRUM_SEPOLIA_RPC_URL || 'https://arbitrum-sepolia-rpc.publicnode.com',
    ),
    [localAnvil.id]: http('http://127.0.0.1:8545'),
  },

  // SSR 兼容存储（使用 cookie 避免水合问题）
  storage: createStorage({
    storage: cookieStorage,
  }),

  // SSR 模式
  ssr: true,
});

// 导出链配置供其他地方使用
export const supportedChains = [mainnet, sepolia, baseSepolia, arbitrumSepolia, localAnvil];
export { localAnvil };

// 类型导出
declare module 'wagmi' {
  interface Register {
    config: typeof wagmiConfig;
  }
}
