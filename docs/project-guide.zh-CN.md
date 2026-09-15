# Wallet Trade Console｜项目与技术导览

[项目首页](../README.md) · [在线演示](https://wallet-trade-console-web.vercel.app/) · [演示步骤与交易记录](./demo-walkthrough.zh-CN.md) · [架构](./architecture.md)

<a id="hr-overview"></a>

## HR：60 秒了解项目

**Wallet Trade Console 是一个面向测试网的 Web3 交易终端。** 用户可以在同一个工作台查看行情、获取代币兑换报价、设置永续交易参数，并通过钱包确认交易、追踪执行结果。它重点解决交易过程中“我正在确认什么、现在等在哪一步、异常后怎么继续”的交互问题。

**我的职责以 Web3 前端为主：** 钱包连接与 SIWE 登录、交易 Hook 和状态反馈、Swap 报价与授权交互、行情图表及回归验证，同时参与 Go API、合约调用与 Indexer 数据回显联调。以下展示项目中可核对的实现；个人职责不等同于独立完成整个仓库。

### 三个代表性成果

- **账户身份清楚：** 将连接钱包与业务登录分开，切换账户或断连时清理旧认证，避免页面继续沿用旧身份。
- **确认内容可追踪：** Swap 确认时冻结交易输入，签名前检查变化；拒签、回滚、交易替换和索引等待各有状态与恢复入口。
- **解释服务可降级：** AI 只解释服务端确定性规则结果；模型不可用时返回静态说明，规则结果仍然保留。

**快速查看：** [看交易终端](https://wallet-trade-console-web.vercel.app/trade?symbol=BTC-USD) → [看 Swap 报价](https://wallet-trade-console-web.vercel.app/swap) → [看一笔历史 Swap](https://sepolia.etherscan.io/tx/0xb2f2cf5b2ab03d83d301f78264dd24e30f90799fa7b4eb281e42d7791061943e)。前两个入口均可先不连接钱包。

## 技术面试官：5 分钟找到实现

| 阅读顺序 | 重点 | 实现入口 |
|---|---|---|
| 1 分钟 | 前端、API、Indexer、合约如何分工 | [架构](./architecture.md) |
| 1 分钟 | 钱包身份与账户切换 | [认证 Provider](../apps/web/src/lib/web3/auth-provider.tsx) |
| 2 分钟 | 冻结输入、发送前核对与异常恢复 | [交易意图 Hook](../apps/web/src/hooks/web3/txIntent/useTxIntent.ts) |
| 1 分钟 | AI 与规则分工、演示证据 | [解释接口](../services/api-go/internal/ai/handlers.go) · [演示记录](./demo-walkthrough.zh-CN.md) |

### 1. 钱包身份与账户切换

**问题：** 浏览器拿到钱包地址，并不意味着服务端已经认证该地址。切换账户后沿用旧 token，会混淆钱包身份与业务会话。

**方案：** nonce → SIWE 消息 → 钱包签名 → 服务端验签建立会话。前端单独管理签名、验签和认证状态；恢复会话时核对地址，断连或地址变化时清理认证。服务端校验消息约束并消费 nonce；Redis `GETDEL` 用于跨进程的一次性消费。

**取舍与结果：** 换账户后需要重新认证，但会话归属更明确。测试覆盖 nonce 重放、地址与 domain 不匹配、跨实例并发消费；这些是具体状态分支的保证，不延伸为所有业务缓存都已完成账户隔离。

源码：[认证 Provider](../apps/web/src/lib/web3/auth-provider.tsx) · [会话工具](../apps/web/src/lib/web3/auth-provider.utils.ts) · [Nonce 存储](../services/api-go/internal/web3auth/noncestore_redis.go)

测试：[SIWE 校验](../services/api-go/internal/web3auth/siwe_test.go) · [并发与过期](../services/api-go/internal/web3auth/noncestore_test.go) · [Redis 跨实例测试](../services/api-go/internal/web3auth/noncestore_redis_integration_test.go)

### 2. Swap 签前输入冻结与异常恢复

**问题：** 用户阅读确认信息时，账户、网络、金额或报价可能变化。链上已确认的交易，也可能暂时还没有出现在业务查询中。

**方案：** `freezeStep()` 保存目标、calldata、value、链和时效；`send()` 用实时输入重新构建候选，比较请求指纹并检查账户、链与过期条件，交给钱包的仍是冻结字节。时间派生的 deadline 使用确认时刻重建。授权不足时先确认按量授权，再重新报价、确认兑换。

状态机将拒签、回滚、同 nonce 替换、链上确认和索引等待分开。索引等待超时仍保留交易哈希与链上结果；后端根据链上证据核对交易尝试，补回浏览器未上报的回执状态。

**取舍与结果：** 重要参数变化后需要重新确认，换来确认信息与发送参数的一致性。测试覆盖真实输入变化、过期、账户变化、授权下降、报价跌破最低接收量及异常状态；索引超时不会被解释为链上失败。这里不保证 RPC 或 Indexer 永远及时。

源码：[冻结与指纹](../apps/web/src/hooks/web3/txIntent/intent.ts) · [发送与检查](../apps/web/src/hooks/web3/txIntent/useTxIntent.ts) · [状态机](../apps/web/src/hooks/web3/txIntent/machine.ts) · [Swap 接入](../apps/web/src/app/_atlas/pages/SwapPage.tsx) · [服务端恢复](../services/api-go/internal/web3events/reconciler.go)

测试：[输入冻结](../apps/web/src/hooks/web3/txIntent/intent.spec.ts) · [Hook 异常路径](../apps/web/src/hooks/web3/txIntent/useTxIntent.spec.tsx) · [状态转换](../apps/web/src/hooks/web3/txIntent/machine.spec.ts) · [页面门控](../apps/web/src/app/_atlas/pages/SwapPage.spec.tsx) · [持久化恢复](../services/api-go/internal/web3events/actions_integration_test.go)

### 3. AI 解释与确定性规则分工

**问题：** 模型可能超时、限流或输出不合要求的内容；直接解释客户端传入的评审结果也无法保证来源可信。

**方案：** 解释接口接收原始交易输入，绑定已认证 owner，在服务端重新执行确定性评审。规则结果作为权威结果，模型仅补充说明；关闭、缺少配置或输出校验失败时返回静态解释。

**取舍与结果：** 服务端重新评审可能增加 RPC 开销，换取输入和结论来源可追踪。禁用 AI 是支持的运行状态；测试覆盖关闭时不调用模型、重算评审、身份绑定和静态回退。规则覆盖范围不等同于安全审计。

源码：[解释接口](../services/api-go/internal/ai/handlers.go) · [确定性规则](../services/api-go/internal/txreview)

测试：[重算、身份约束与回退](../services/api-go/internal/ai/explain_test.go)

## 贡献时间与验证口径

任职期间的基础工作范围包括交易 Hook、授权后刷新、SIWE 与切换地址清理、行情图表及 Go 分阶段迁移联调。7 月测试网记录、8 月 AI 解释、9 月冻结意图与异常恢复属于后续增强。提交时间用于定位实现版本，不用于推断雇佣关系或个人作者归属。

源码链接随本导览指向同版本文件；在线版本、测试结果及历史交易的适用范围统一见[演示记录](./demo-walkthrough.zh-CN.md#verification)。

[返回概览](#hr-overview) · [开始只读演示](./demo-walkthrough.zh-CN.md) · [返回项目首页](../README.md)
