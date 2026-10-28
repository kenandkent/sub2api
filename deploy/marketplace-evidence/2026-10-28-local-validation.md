# 共享账号市场本地验证记录

日期：2026-10-28；分支：`mysubapi`；执行：Codex 主 agent 与 GPT-6.1 Terra / ultra 子 agent，使用项目内隔离 worktree。用户授权范围为本地实现、测试与提交；线上服务未部署。当前业务开关默认关闭。

## 环境与口径

- macOS arm64、Go 1.27.3；通过项目内 `.superpowers/bin/go` 运行，缓存与临时文件放在项目内。前端使用已安装的 Node 运行时及 node_modules 工具。
- PostgreSQL 18.1、Redis 8.4 使用一次性 Docker/Testcontainers。财务仓储测试需要提交事务，使用从迁移模板复制的独立测试数据库；测试不删除共享基线数据、不取消 FK 或不可变约束。
- 真实应用进程测试启动编译后的完整 Go 服务，使用真实路由、API Key 鉴权、已发布价格目录、调度缓存、加密凭据运行时、冻结/捕获/账本。上游为本机 OpenAI 格式 HTTP 替身；macOS 沙箱限制回环网络及单次运行目录写入，子进程不继承现有应用配置。
- 账号审核、充值来源、供应者状态由 fixture 构造；这些用例不等同于真实账号授权、支付方退款/拒付或银行转账。真实 OAuth 服务端 PKCE 的绑定、令牌交换、重放、代理与加密由本地假 OAuth/仓储回归验证。
- 日志摘要和指纹见 [验证清单](verification-manifest.json)。原始日志在本项目 `.superpowers/`；它们不作为可部署配置，证据不保存真实凭据、收款明文或用户对话。

## 执行入口

以下为本次实际工具对应的命令。标准环境可将 wrapper 替换为相同版本的 `go`。

```sh
./.superpowers/bin/go -C backend test -p 2 -tags unit ./... -count=1 -timeout=10m
./.superpowers/bin/go -C backend test -p 2 -tags integration ./... -count=1 -timeout=10m
./.superpowers/bin/go -C backend test -p 2 ./... -count=1 -timeout=10m
./.superpowers/bin/go -C backend test -p 2 -tags integration ./internal/repository -count=1 -timeout=10m
./.superpowers/bin/go -C backend test -tags 'unit integration' -v ./internal/handler -run '^TestMarketplaceGatewayRealInfrastructure$' -count=1 -timeout=5m
./.superpowers/bin/go -C backend build -o ../.superpowers/live-e2e/sub2api ./cmd/server
./.superpowers/bin/go -C backend test -tags 'unit integration local_e2e' -v ./cmd/server -run '^TestMarketplaceLocalServerE2E$' -count=1
MARKETPLACE_E2E_NORMAL_ORDER=1 ./.superpowers/bin/go -C backend test -tags 'unit integration local_e2e' -v ./cmd/server -run '^TestMarketplaceLocalServerE2E$' -count=1
```

`local_e2e` 用例只在 macOS 有回环/文件沙箱时执行；其他平台明确 skip，不能记为通过。CI 显式加入 `unit integration` 组合标签的 handler 用例。单独运行 unit 与 integration 不会选中组合标签文件。

前端执行 `vitest run`、`vue-tsc --noEmit`、`vite build`、`eslint .`；i18n 完整性包含于全量 Vitest。部署验证完整执行 CI 的 Python、Compose 环境/依赖/生产/安全/资源和 Caddy 检查。检查的输出结果集中见清单；无匹配测试的包不计作测试通过数量。

## 优先财务回归

1. `TestPaymentFundingRefundRejectsRestoredCashAfterChargeback`：原现金消费、原订单拒付、后续入账偿债、消费退款恢复来源后，不能再从原订单重复退款。
2. `TestPaymentRefundAndChargebackSerializeAgainstOrderCap`：充值退款与拒付竞争不能突破原订单累计上限；包括已预留和已确认退款。
3. `TestLegacyPaymentBonusKeepsCashAndGiftSeparate`：充值现金和同订单赠额独立，赠额不能变成供应结算现金；覆盖奖励与折扣订单。
4. `TestPaymentBonusRefundReservesBothOrderSources`、`TestPaymentBonusChargebackRevokesBothSources`、`TestPaymentBonusRefundMissingOwnGiftCannotUseAnotherGift`、`TestPaymentBonusRefundRoundingTail`：原订单按比例回收赠额、拒绝跨订单替代、处理累计舍入尾差。
5. `TestManualChargebackExactOwnerReplayAndAudit`：管理员手工登记使用数据库订单所有者、精确八位金额、原事件重放及冲突审计。新 HTTP 字段 `credited_amount` 表示经财务核定的入账冲销金额，不能直接抄支付方金额。

两处最初漏洞均观察过修复前失败和修复后通过；独立财务审查未发现新的重要问题。迁移 269 约束之后的充值来源分类，不追溯重写可能存在的原型历史来源；本环境没有已部署原型资金。

## R01–R12 覆盖映射

| 要求 | 实际测试 / 观察 | 结论与范围 |
| --- | --- | --- |
| R01 双模式 | `TestSupplierCapabilitiesAndStatus`；workMode、WorkModeSwitcher、router guard Vitest；真实界面消费者→供应者切换 | 本地通过；同一 userID，模式不改变运行态 |
| R02 权限与凭据 | `TestSupplierApplyAndOwnership`、`TestSupplierAuthorizationBindingAndNoTokenResponse`、`TestSupplierAuthorizationRejectsChangedSessionAndState`、凭据信封/导出 tripwire；HTTP 401/403/404 | 本地通过；不能保证识别不同 API Key 指向同一物理上游账号，仍需人工审核 |
| R03 生命周期 | `TestSupplierAccountRepositoryLifecycle`、`TestSharingOfferOAuthIdentityAndReauthorization`、`TestMarketplaceGatewayQualificationAndDispatchRecheck` | 本地通过；当前可信白名单仅实现的 OpenAI 文字能力 |
| R04 请求并发 | `TestMarketplaceGatewayRealInfrastructure/ten_buyers_and_suppliers_obey_per-account_caps`、`supplier_slot_is_shared_across_handlers` | 真实 PG/Redis，两 handler、10 买方/10 账号，并发 1/3；无用户独占或租约 |
| R05 报价和守恒 | `TestMarketplaceQuoteCacheTokensAreNotDoubleCharged`、`TestMarketplaceQuoteTierAndMultiplierSnapshot`、`TestMarketplaceQuoteSplitConservesRoundedCharge`；真实进程 6 笔 C=S+F | 价格/消费者倍率冻结，缓存去重，金额八位；无宽松 float 分账断言 |
| R06 来源账与迁移 | `TestWalletFundingLegacyMigrationAndBalanceWrites`、`TestWalletFundingLegacyMapsExistingBatchHoldExactlyOnce`、`TestWalletFundingLegacyUnmappedHoldFailsClosed`、`TestWalletFundingLegacyHoldAndOverdraftDoNotStealReservedMoney` | 隔离迁移/兼容写入及恒等式通过；生产数据全量演练未执行 |
| R07 网关与未知执行 | `TestMarketplaceLocalServerE2E` 六种请求；handler uncertain/partial/missing usage、无执行前准备失败切换 | 普通/流/工具及缓存 usage 通过；已经派发后的不确定失败不自动换号或重放 |
| R08 幂等与恢复 | `TestMarketplaceBillingCaptureAtomicReplayAndConservation`、`TestMarketplaceBackgroundReviewsStaleReceiptsWithoutChangingFinancialEvidence`、`TestMarketplaceStaleReviewAllowsKnownUsageCaptureAndReplay`；Redis/DB 故障用例 | 不确定资金保持冻结；滞留扫描只转审核；无资金/收益重复 |
| R09 成熟、退款、账单 | `TestMaturityConcurrentRepeatAndFreeze`、`TestPartialRefundThenMaturityMovesOnlyUnrefundedPending`、`TestMarketplaceRefundAfterPaidCreatesDebt`、原订单退款/拒付测试 | 隔离账务通过；没有真实支付方 webhook，拒付由受控管理员 API 登记 |
| R10 提现安全 | `TestPayoutRefundAndStartSerializeOnWallet`、`TestPayoutPaidRequiresMatchingEvidenceAndUniqueReference`、`TestPayoutFreezeAuditVersionDebtAndReplay`、step-up 与供应/管理员 UI 测试 | 状态机和未知付款冻结通过；银行转账未执行 |
| R11 前端 | 397 文件/3015 测试；typecheck/build/lint；下方人工布局观察 | 代表页面通过；完整六尺寸主题组合截图归档和全键盘/错误态巡检未完成 |
| R12 开关及旧行为 | `TestMarketplacePolicyDefaultDisabled`、`TestMarketplacePolicySimpleModeRejected`、handler `disabled_feature_still_captures_and_drains_existing_work`；既有全量套件 | 默认关闭、旧路径回归、关闭后收尾通过 |

用户对 T16 的最新选择由 `TestExecutionIncidentRecordsEvidenceWithoutFinancialEffects` 和 `ExecutionIncidentDialog.spec.ts` 覆盖：误解冻后只登记不可变证据，人工核定补偿。登记与查询有管理员 JWT/TOTP、相同键重放/冲突及审计正文保护；不改变 receipt、attempt、原审计、余额、冻结、来源分配、capture、钱包或账本。

## 本地真实应用与界面观察

完整应用另以独立数据目录和端口启动过门户。JWT 登录、capabilities、supplier options/profile/overview/accounts/earnings/statements/payouts、marketplace catalog/balance 和管理目录返回正常。供应账号使用假 Key 进行了创建、更新、提交审核、撤销；旧版本写入返回 409，越过已发布并发上限返回 400。跨归属访问 404、未登录 401、普通用户访问管理接口 403。假 Key 没有发送给真实上游。

本轮真实进程发现并修复调度缓存丢失 `OwnerUserID`：缓存预热后普通或工具请求会返回 503，调换顺序证明与工具模板无关。缓存投影修复后两种顺序均通过六种请求，归属仍不向插件快照公开。

浏览器：Codex IAB。使用临时同源 iframe 把应用视口设为 360/768/1440px；只缩放外层展示，不改变应用实际断点。人工观察包括：

| 视口 / 主题 | 页面及行为 |
| --- | --- |
| 1440 浅色 | 供应概览、金额/在线数；管理供应申请及审核状态 |
| 1440 深色 | 管理审核弹窗、必填原因、空原因禁止确认；Enter 打开、Escape 关闭、焦点回原按钮 |
| 768 浅色 | 政策与资金对账；修改政策后离开提示，取消保留草稿；对账空态/零差额 |
| 768 深色 | 供应概览和收益，待成熟/可用/提现冻结清晰区分 |
| 360 浅色 | 账号卡片/长账号名/分享向导、账单空态；执行异常登记说明、滚动/底部换行按钮、关闭后焦点返回 |
| 360 深色 | 移动抽屉与主题、提现最小额和无已验证收款方式/零可用余额的禁用状态 |

人工检查曾发现概览 DTO/列表金额为空、按钮缺少基础类、状态文案及政策未保存保护问题，已修复并添加对应回归。政策提交期间禁止编辑/关闭，延期 POST 回归通过。

这些是实际观察记录，未保存完整截图文件，不能替代发布门槛要求的全页面、双主题、全部键盘/焦点及错误态矩阵。临时验收进程与对应 `marketplace-live-smoke` PostgreSQL/Redis 已停止并移除；原日常开发环境没有用于测试数据。

## 未执行的运营验证

- 真实账号授权/真实模型调用、真实支付方退款或拒付、真实银行付款。
- 生产数据逐用户迁移、备份恢复、多实例密钥恢复/轮换、生产滚动升级和旧缓存重建。
- 商业费率/供应协议/供应白名单、支付方原始金额到 `credited_amount` 的人工映射规则审批。
- 完整 UI 截图矩阵和辅助技术验收。

以上门槛未满足前继续保持市场默认关闭，不将“本地软件检查通过”写成“生产启用验收通过”。
