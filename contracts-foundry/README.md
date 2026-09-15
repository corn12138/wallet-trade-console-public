# Web3 Trading Contracts (Foundry)

基于 Foundry 的 Web3 合约工程，以 `contracts-foundry/src/` 为合约真源，`contracts/src/` 为 Hardhat 兼容镜像。覆盖：

- AMM `swap` 与流动性
- `create-token` Launchpad 与 BondingCurve
- `trade` 永续合约
- `nft-studio` 的 onchain / IPFS mint

## 项目结构

```
contracts-foundry/
├── src/                    # Solidity 合约源码
│   ├── core/              # 核心合约
│   │   ├── TradingPair.sol
│   │   ├── TradingPairFactory.sol
│   │   ├── PerpOracle.sol
│   │   ├── PerpVault.sol
│   │   └── PerpMarket.sol
│   ├── launchpad/         # Launchpad / BondingCurve
│   ├── nft/               # NFT Studio 合约
│   ├── periphery/         # 外围合约
│   │   ├── Router.sol
│   │   ├── TradingLibrary.sol
│   │   └── PositionManager.sol
│   ├── tokens/            # 测试代币
│   │   ├── MockERC20.sol
│   │   └── TestTokens.sol
│   └── interfaces/        # 接口定义
├── script/                # 部署脚本 (Solidity)
│   └── Deploy.s.sol
├── test/                  # 测试文件 (Solidity)
│   ├── TradingPair.t.sol
│   └── FeatureParity.t.sol
├── lib/                   # 已纳入公开源码的依赖与许可证
│   ├── forge-std/
│   └── openzeppelin-contracts/
└── foundry.toml           # Foundry 配置
```

## 安装

```bash
# 安装 Foundry (如果未安装)
curl -L https://foundry.paradigm.xyz | bash
foundryup

# 公开镜像已包含编译所需的依赖源码与许可证
cd contracts-foundry
forge build
```

## 部署配置

Launchpad 毕业流程需要支持 `addLiquidityETH` 的 V2 Router。设置 `LAUNCHPAD_DEX_ROUTER` 后，部署脚本会先检查其代码、方法及 WETH/factory 元数据。项目的 ERC20 Router 不能替代它。`PRIVATE_KEY` 始终从环境读取；本导览的只读演示无需部署或签名。

## 常用命令

```bash
# 编译合约
forge build

# 运行测试
forge test

# 运行测试 (详细输出)
forge test -vvv

# 运行特定测试
forge test --match-test test_SwapToken0ForToken1

# Fuzz 测试
forge test --match-test testFuzz

# Gas 报告
forge test --gas-report

# 代码覆盖率
forge coverage

# 格式化代码
forge fmt

# 启动本地节点 (Anvil)
anvil

# 部署到本地节点
export PRIVATE_KEY=<one-of-your-local-Anvil-private-keys>
forge script script/Deploy.s.sol:DeployLocalScript --rpc-url http://127.0.0.1:8545 --broadcast

# 部署到 Sepolia
PRIVATE_KEY=0x... forge script script/Deploy.s.sol:DeploySepoliaScript --rpc-url "$SEPOLIA_RPC_URL" --broadcast
```

## Foundry vs Hardhat 对比

| 特性 | Foundry | Hardhat |
|------|---------|---------|
| 语言 | Rust + Solidity | JavaScript/TypeScript |
| 测试语言 | Solidity | TypeScript |
| 编译速度 | 极快 | 中等 |
| Fuzz 测试 | 内置 | 需要插件 |
| 调试工具 | forge debug | hardhat console |
| 依赖管理 | git submodules | npm packages |
| Gas 快照 | forge snapshot | hardhat-gas-reporter |

## 测试特性

### 标准测试
```solidity
function test_AddLiquidity() public {
    // 标准测试，函数名以 test_ 开头
}
```

### Fuzz 测试 (Foundry 特色)
```solidity
function testFuzz_Swap(uint256 amount) public {
    // Foundry 自动生成随机输入进行模糊测试
    amount = bound(amount, 1 ether, 10_000 ether);
    // ...
}
```

### Cheatcodes
```solidity
// 切换调用者
vm.prank(alice);

// 持续切换调用者
vm.startPrank(alice);
// ... 多个调用
vm.stopPrank();

// 期望 revert
vm.expectRevert("Error message");

// 设置区块时间
vm.warp(block.timestamp + 1 days);

// 设置区块号
vm.roll(block.number + 100);
```

## 本地开发流程

1. 启动 Anvil:
```bash
anvil
```

2. 部署合约:
```bash
export PRIVATE_KEY=<one-of-your-local-Anvil-private-keys>
forge script script/Deploy.s.sol:DeployLocalScript --rpc-url http://127.0.0.1:8545 --broadcast
```

3. MetaMask 配置:
   - 网络名称: Anvil Local
   - RPC URL: http://127.0.0.1:8545
   - Chain ID: 31337
   - 货币符号: ETH
