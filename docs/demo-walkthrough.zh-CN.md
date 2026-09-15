# 3 分钟演示｜从页面到源码

[项目导览](./project-guide.zh-CN.md) · [项目首页](../README.md) · [在线首页](https://wallet-trade-console-web.vercel.app/)

## 无需钱包的浏览路径

1. **[首页](https://wallet-trade-console-web.vercel.app/)**：了解测试网产品，向下查看市场概览和行情预览。
2. **[BTC 永续终端](https://wallet-trade-console-web.vercel.app/trade?symbol=BTC-USD)**：切换市场与 K 线周期，查看数据来源、交易参数和空盘口提示。图表的公开现货参考价与本产品预言机价格是不同数据；个人持仓与记录通过钱包登录后查看。
3. **[Swap](https://wallet-trade-console-web.vercel.app/swap)**：在 mUSDC → mWETH 输入 `1`，查看输出金额、最少获得、滑点和报价诊断。实时报价有 Router 来源；降级时不可兑换。连接按钮前即可完成这一段浏览。
4. **返回实现**：查看[交易意图与恢复案例](./project-guide.zh-CN.md#2-swap-签前输入冻结与异常恢复)。手机通过左上菜单导航；桌面侧栏悬停展开、移开收起。

这一条路径只读取数据、修改页面参数，不需要签名、代币授权或链上交易。

<a id="verification"></a>

## 版本与实测记录

核对日期：2026-09-15，Asia/Shanghai。桌面 1440×1000、手机模拟视口 390×844；手机验证来自 Chromium，未覆盖真机钱包。

| 范围 | 核对结果 |
|---|---|
| 已发布版本 | 公开完整源码：[eea5fbf](https://github.com/corn12138/wallet-trade-console-public/commit/eea5fbf34dbd5a2478d67accfe423a5470941bff)，公开 CI 通过后合入。前端部署 revision `63d29fb`；Go API 镜像源码 revision `66cdf83`、镜像摘要前缀 `c4ca8aa0de85`。两者应用源码与验证候选一致。 |
| 线上只读浏览 | 首页、BTC 交易页和 Swap 的桌面与手机视口均可浏览，无横向溢出；手机头部与连接按钮正常排列，图表信息条位于画布上方。未连接钱包时按 Sepolia 市场链读取，BTC 预言机价格为 67,000；现货图表来源单独标识。首页已使用测试网与实际支持钱包的说明。 |
| 线上 Swap | 连续 3 次 API 请求均返回 Router 实时报价，约 0.000997 mWETH / 1 mUSDC。浏览器还观察到一次“输出金额读取失败”的降级，输出框不填入报价数值，汇率、最少获得和价格影响显示 `—`，下一次自动刷新恢复实时报价；降级始终不可执行。 |
| 本地验证 | 前端 307 项测试、类型检查和生产构建；Go 竞态测试及隔离 PostgreSQL、Redis 集成；Foundry 95 项测试、Hardhat Router 兼容性 6 项测试及 realtime 客户端 E2E 通过。缺失价格与零值显示 `—` 的分支由本地测试覆盖。 |
| 本次边界 | 仅发布前端与 Go API，数据库结构和现有 worker 版本未改变；未连接钱包、未签名、未授权、未发送链上交易。手机是 Chromium 模拟视口；真机钱包、真实 AI 模型调用和完整链上成交不在本次验证范围。 |

Swap 的线上诊断已能区分 RPC 未配置、金额或储备读取超时、RPC 拒绝、读取失败与无效数据。本次观察到的是金额读取失败后自动恢复；这不能进一步确认具体节点故障原因，也不保证公共 RPC 持续可用。

### 对应测试与复现

```bash
pnpm install --frozen-lockfile
pnpm --filter @wallet-trade/database db:generate
pnpm type-check
pnpm test
pnpm build
(cd services/api-go && go vet ./... && go test -race ./...)
(cd contracts-foundry && forge test)
pnpm --dir contracts test
```

PostgreSQL、Redis 和链上集成测试需要显式配置本地测试依赖。默认跳过不代表通过；[公开 CI](../.github/workflows/public-ci.yml)配置数据库与 Redis 验证，链上钱包集成测试保持单独启用。

## 两笔已有 Sepolia 交易

### 代币兑换：mWBTC → mWETH

[查看交易与 Receipt Event Logs](https://sepolia.etherscan.io/tx/0xb2f2cf5b2ab03d83d301f78264dd24e30f90799fa7b4eb281e42d7791061943e#eventlog)

- Etherscan 状态：**Success**；区块 `11250139`；时间 2026-07-11 12:40:48 UTC。
- 调用 Router `0xe0C55ff91EcE0ACC7ddAfA9069ba5B0cdBBC3b00` 的 `swapExactTokensForTokens`，selector `0x38ed1739`。
- Receipt 包含两个 `Transfer`、`Sync` 和 `Swap`；约 0.01 Mock WBTC 换得 0.328496543 Mock WETH。Swap 日志来自交易对 `0x2dcd2b1cb75835a2c4c37b3cd5be5d6cc32b16ea`。
- 对照：[Router](../contracts-foundry/src/periphery/Router.sol) · [Swap 事件定义](../contracts-foundry/src/interfaces/ITradingPair.sol)。

### 永续开仓：BTC 多仓

[查看交易与 Receipt Event Logs](https://sepolia.etherscan.io/tx/0x7e51669da83765935daf608d445618060b41ae4b5a9dd542972a90ba017fbccf#eventlog)

- Etherscan 状态：**Success**；区块 `11250141`；时间 2026-07-11 12:41:12 UTC。
- 调用 PositionManager `0x020347BA9cfF1c7635327C7b8fB0e25C0FFd26c5` 的 `openPosition`，selector `0x6c560004`。
- PerpMarket `0x04330ddd8a4c51c0eabf990cd8f18764e7661843` 发出 `IncreasePosition`：多仓，抵押量 100（18 位精度），名义仓位 300 USD，价格 67,000 USD（USD/价格均为 30 位精度）。
- 对照：[PositionManager](../contracts-foundry/src/periphery/PositionManager.sol) · [事件定义](../contracts-foundry/src/core/PerpMarket.sol)。浏览器自动解码的两个地址字段名称与源码顺序不同，此处按 calldata 和事件布局核对。

两笔交易由区块浏览器详情和日志核验；本次公共 RPC 没有同时返回对应回执，未形成独立 RPC 交叉验证。历史回执证明相应调用结果，不证明当前端到端验收、9 月机制当时已存在、部署字节码等同当前源码或个人作者归属。

[返回技术案例](./project-guide.zh-CN.md) · [返回项目首页](../README.md)
