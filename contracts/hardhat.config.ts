import * as dotenv from "dotenv";
import { defineConfig } from "hardhat/config";
import hardhatEthers from "@nomicfoundation/hardhat-ethers";
import hardhatEthersChaiMatchers from "@nomicfoundation/hardhat-ethers-chai-matchers";
import hardhatMocha from "@nomicfoundation/hardhat-mocha";
import hardhatNetworkHelpers from "@nomicfoundation/hardhat-network-helpers";
import hardhatTypechain from "@nomicfoundation/hardhat-typechain";

dotenv.config();

// Hardhat checks process.env.http_proxy (lowercase) for proxy support.
// Forward from HTTP_PROXY (uppercase, set by Clash/system) if needed.
if (!process.env.http_proxy && process.env.HTTP_PROXY) {
  process.env.http_proxy = process.env.HTTP_PROXY;
}
if (!process.env.https_proxy && process.env.HTTPS_PROXY) {
  process.env.https_proxy = process.env.HTTPS_PROXY;
}

export default defineConfig({
  plugins: [
    hardhatEthers,
    hardhatEthersChaiMatchers,
    hardhatMocha,
    hardhatNetworkHelpers,
    hardhatTypechain,
  ],
  defaultNetwork: "hardhat",
  solidity: {
    version: "0.8.28",
    settings: {
      optimizer: {
        enabled: true, // 启用优化
        runs: 100, // 优化运行次数
      },
      viaIR: true, // 使用 IR 优化
    },
  },
  paths: {
    sources: "./src",
    tests: "./test",
    cache: "./cache",
    artifacts: "./artifacts",
  },
  networks: {
    hardhat: {
      type: "edr-simulated",
      chainType: "l1",
      chainId: 31337,
    },
    localhost: {
      type: "http",
      chainType: "l1",
      url: "http://127.0.0.1:8545",
      chainId: 31337,
    },
    sepolia: {
      type: "http",
      chainType: "l1",
      url: process.env.SEPOLIA_RPC_URL || process.env.SEPOLIA_HTTPS_RPC || "https://rpc.sepolia.org",
      accounts: process.env.PRIVATE_KEY ? [process.env.PRIVATE_KEY] : [],
      chainId: 11155111,
    },
  },
  typechain: {
    outDir: "./typechain-types",
  },
});
