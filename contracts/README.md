# Hardhat 兼容层

`contracts-foundry/src/` 是当前 Solidity 唯一真源。这个目录暂时保留 Hardhat 3
编译器、TypeScript 部署/运维脚本和 deployment registry；`contracts/src/` 只是
这些能力所需的兼容镜像，不应单独修改。合约业务测试已全部迁入 Foundry，
Hardhat test runner 已退役。

Hardhat 目前仍不可删除：还没有把 Foundry broadcast 转换成现有 deployment JSON
的适配器，部署与运维脚本也仍依赖 Hardhat runtime。完整迁移边界见
`docs/migration/contracts-foundry/00-overview.md`。

CI 已把本目录标为 transitional compatibility job，并同时检查 Foundry 真源与本目录
共有 Solidity 文件的内容一致性。

共有合约必须先在 `contracts-foundry/src/` 修改，再从仓库根目录运行
`pnpm sync:hardhat-compat`。该命令只单向刷新兼容镜像，不删除 Hardhat 独有文件。

当前 active 基线：

- Node.js `22.10+` 的 LTS 偶数版本
- Hardhat `3.x`（当前锁文件解析为 `3.3.0`）
- ESM 项目模式

## 项目结构

```
contracts/
├── src/                    # contracts-foundry/src 的临时兼容镜像
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
├── scripts/               # 仍在使用的部署、mint 和产物同步脚本
├── deployments/           # packages/shared 镜像的部署登记源
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
# 编译兼容镜像
pnpm compile

# 运行不依赖 Hardhat test runner 的路由兼容辅助函数测试
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

- `pnpm sync-contracts` 只同步 deployment JSON 和生成地址文件，不同步 Solidity 源码
- `pnpm compile` 与 `pnpm test` 不代表全部部署/运维脚本通过 TypeScript 静态检查；该门禁随 ops 迁移关闭
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

下面仍是当前测试网部署兼容路径，不代表 Hardhat 是合约真源，也不构成外部验收证明。

- `pnpm --filter web3-contracts deploy:sepolia`
  只部署当前 web `swap` 页面需要的 AMM + mock tokens + liquidity。
- `pnpm --filter web3-contracts deploy:all-sepolia`
  一次性覆盖当前 web 已经接入的 `swap`、`create-token`、`trade`、`nft-studio` 合约能力。
- 部署完成后会自动同步到：
  - `contracts/deployments/*.json`
  - `packages/shared/deployments/*.json`
  - `packages/shared/src/web3/contract-addresses.generated.ts`

这意味着当前 `web` 和 `api` 会直接读取新的部署结果，不再需要手动改前端地址文件。

## 删除门槛

删除 Hardhat 前必须同时完成：

- 将 Foundry broadcast 转换并同步为现有 deployment JSON 和生成地址文件
- 迁移仍依赖 Hardhat runtime 的 deploy、mint、add-liquidity 等运维脚本
- 补齐 storage layout、coverage、slither 和 gas 门禁
- 在获得单独授权后完成测试网部署以及 UI、Indexer、Relayer 的外部验收

在这些门槛关闭前，应保留本目录，但不要继续向这里增加新的合约实现。
