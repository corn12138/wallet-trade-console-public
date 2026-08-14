/**
 * @file Web3 模块导出
 * @description 统一导出 Web3 相关配置和工具
 */

// 配置
export { wagmiConfig, supportedChains, localAnvil } from './config';

// Provider
export { Web3Provider } from './provider';

// Auth
export { useAuth, AuthProvider } from './auth-provider';
export type {
  AuthNonceChallenge,
  AuthVerifyResponse,
  WalletAuthErrorCode,
  WalletAuthStatus,
} from './auth.types';

// 合约配置
export {
  CONTRACT_ADDRESSES,
  erc20Abi,
  tradingPairAbi,
  tradingPairFactoryAbi,
} from './contracts';

// 重导出 wagmi hooks（方便使用）
export {
  useAccount,
  useConnect,
  useDisconnect,
  useBalance,
  useChainId,
  useSwitchChain,
  useReadContract,
  useWriteContract,
  useWaitForTransactionReceipt,
  useSimulateContract,
  useWatchContractEvent,
} from 'wagmi';

// 重导出 viem 工具函数
export {
  formatEther,
  formatUnits,
  parseEther,
  parseUnits,
  type Address,
} from 'viem';
