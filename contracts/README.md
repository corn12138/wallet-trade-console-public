# Web3 Trading Contracts

基于 Hardhat 3 的 DeFi AMM 智能合约项目。

当前 active 基线：

- Node.js `22.10+` 的 LTS 偶数版本
- Hardhat `3.1.x`
- ESM 项目模式

## 项目结构

```
contracts/
├── src/                    # Solidity 合约源码
│   ├── core/              # 核心合约
│   │   ├── TradingPair.sol
│   │   └── TradingPairFactory.sol
│   ├── periphery/         # 外围合约
│   │   ├── Router.sol
│   │   └── TradingLibrary.sol
│   ├── tokens/            # 测试代币
│   │   ├── MockERC20.sol
│   │   └── TestTokens.sol
│   └── interfaces/        # 接口定义
├── scripts/               # 部署脚本
│   └── deploy.ts
├── test/                  # 测试文件
│   └── TradingPair.test.ts
└── hardhat.config.ts      # Hardhat 配置
```

## 安装

```bash
cd /path/to/wallet-trade-console
pnpm install
```

说明：

- 当前 `contracts/` 已经纳入 workspace，优先在仓库根目录执行 `pnpm install`
- 如果你是从旧仓库剥离后新加了 `contracts/`，需要重新跑一次根目录 `pnpm install`，否则会出现 `hardhat: command not found`
- Sepolia 脚本默认读取根 shell 环境里的 `SEPOLIA_RPC_URL` 和 `PRIVATE_KEY`

## 常用命令

```bash
# 编译合约
pnpm compile

# 运行测试
pnpm test

# 同步部署地址到共享 SDK
pnpm sync-contracts

# 启动本地节点
pnpm node

# 部署到本地节点
pnpm deploy:local

# 部署到 Sepolia 测试网
pnpm deploy:sepolia

# 为当前 web 全量部署 Sepolia 能力
pnpm deploy:all-sepolia
```

说明：

- `solidity-coverage` 与 `hardhat-gas-reporter` 已从 active surface 移除，因为它们当前只兼容 Hardhat 2
- `hardhat-verify` 已从 active surface 移除，以避免继续携带 `ethers v5 / bn.js` 的旧验证工具链树
- 如需区块浏览器验证，请在独立验证环境中执行，不要把验证插件重新并回主线
- 如需恢复覆盖率或 gas report，必须在后续独立工具链波次中引入 Hardhat 3 兼容方案

## 本地开发流程

1. 启动本地 Hardhat 节点:
```bash
pnpm node
```

2. 在新终端部署合约:
```bash
pnpm deploy:local
```

3. MetaMask 配置:
   - 网络名称: Hardhat Local
   - RPC URL: http://127.0.0.1:8545
   - Chain ID: 31337
   - 货币符号: ETH

4. 导入测试账户私钥获取测试 ETH

## 核心概念

### AMM (自动做市商)

使用恒定乘积公式: `x * y = k`

- 价格由储备比例决定
- 交易越大，滑点越高
- 0.3% 手续费

### 合约架构

- **TradingPair**: AMM 交易对，处理流动性和交换
- **TradingPairFactory**: 工厂合约，创建新交易对
- **Router**: 用户接口，简化交互操作

## 当前仓库的 Sepolia 工作流

- `pnpm --filter web3-contracts deploy:sepolia`
  只部署当前 web `swap` 页面需要的 AMM + mock tokens + liquidity。
- `pnpm --filter web3-contracts deploy:all-sepolia`
  一次性覆盖当前 web 已经接入的 `swap`、`create-token`、`trade`、`nft-studio` 合约能力。
- 部署完成后会自动同步到：
  - `contracts/deployments/*.json`
  - `packages/shared/deployments/*.json`
  - `packages/shared/src/web3/contract-addresses.generated.ts`

这意味着当前 `web` 和 `api` 会直接读取新的部署结果，不再需要手动改前端地址文件。
