# Account Sharing Marketplace Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在当前 Sub2API 上实现同一用户的消费者／供应者双模式、账号分享、现有请求并发控制、真实用量扣费、平台抽成和供应者结算。

**Architecture:** 保持单体 Go 网关与 Vue 门户；账号新增所有权和分享资格，共享分组接入不可变报价、资金来源账、请求凭据与原子分账，复用现有 Redis 请求并发。消费者余额与供应收益分开，提现先人工审核和外部付款登记。

**Tech Stack:** Go / Gin / Ent / PostgreSQL / Redis / shopspring decimal；Vue 3 / TypeScript / Pinia / Vue Router / Tailwind / Vitest；沿用现有 Docker Compose 和 CI。

**Spec:** [账号共享市场完整设计](../specs/2026-10-28-account-sharing-marketplace-design.md)。基线 `mysubapi @ dc8cdf597`，2026-10-28。状态：可执行开发计划，尚未实施；本次产出只包含文档。

## Global Constraints

- 项目根 `/Users/ken/workspace/sub2api` 内按任务修改；根外默认只读。派发任何任务都附带这一边界。不写外部临时文件、改全局配置或线上服务。
- 不新增租户、账号租约或单用户独占语义。`accounts.concurrency` 继续限制在途请求，`owner_user_id` 只表示归属与权限。
- 同一 `users.id` 具备两种能力，`role` 不改成供应者，不把普通用户升为管理员。工作区切换不改变运行态。
- 现有自营分组、API 路由、开发环境、Compose、CI 不被替换。共享收费只在 standard 模式开启，默认所有新业务开关关闭。
- 新资金用 decimal / NUMERIC(20,8) / JSON 字符串；不可变分账与幂等、资金来源、未知 usage 收尾是上线前置条件，不留到“后续优化”。
- 无真实账号、支付、线上数据库参与自动测试。真实验证只在另行获准的测试环境进行，不能把文档任务当作部署授权。
- 任务描述中的“新建”文件为规划路径；已有文件先确认当前分支实际结构。迁移序号实施时取仓库最新可用编号，禁止占用并发开发已存在编号；本文用 `<next>` 表示必须分配的序号。
- 每个任务完成最小相关验证后再扩大。实现阶段提交仅包含本任务文件，不混入用户其他改动；本文复选框不是已完成声明。

## Review Focus

1. 共享资金是否还可能落入现有可丢任务队列、无 request_id 回退，或在另一个事务单独给供应者加钱？这些路径一律禁止。
2. 每个入口是否带 actor 与 owner 校验？OAuth 刷新、原始导出、影子账号、缓存快照是否会泄漏或恢复明文凭据？
3. 是否真的覆盖所有余额写入／冻结路径？有无把赠送和历史余额变成可提现现金，或普通消费花掉共享冻结余额？
4. 故障切换后是否把收入给实际 billable attempt 所有者？部分输出、取消、崩溃、未知 usage 是否重复收费或虚构收益？
5. 共享模式是否错误地变成排他账号占用？粘性／previous_response 路径是否跳过资格和会话隐私检查？
6. 提现 processing 未知结果是否错误释放、重复转账？退款在已提现后的债务是否可审计？
7. 两模式的路由、导航、钱包、权限是否独立清晰，同时保持现有页面主题和组件？

## 1. 交付阶段与依赖

| 阶段 | 任务 | 阶段产物 | 可否开放真实共享收费 |
| --- | --- | --- | --- |
| A 身份与供应基础 | T01–T04 | 数据结构、所有权、授权、分享生命周期 | 否 |
| B 资金基础 | T05–T06 | 资金来源、旧写入口兼容、迁移对账 | 否 |
| C 计费与调度 | T07–T09 | 报价冻结、原子分账、真实调用收尾 | 否，仍需后续验收 |
| D 收益与结算 | T10–T11 | 成熟、退款、账单、人工提现 | 否 |
| E 双模式门户 | T12–T15 | 模式切换、账号分享、消费提示、财务与管理页面 | 仅内部测试 |
| F 端到端与发布准备 | T16–T17 | 故障注入、UI 验证、迁移和回退演练、运行手册 | 全部门槛通过后才能灰度 |

依赖：T02→T03→T04；T05→T06；T04+T06→T07→T08→T09；T08→T10→T11；T01+T02→T12→T13；T07+T12→T14；T10+T11+T12→T15；全部业务任务→T16→T17。T01 是共用类型和配置起点。

若实施时使用多个 agent，可在 T01 合同固定后并行推进供应、资金、前端壳三条线，但共享 schema、wire 注册、路由、余额仓储必须明确唯一文件负责人。不得通过并行降低资金串联验收要求。沿用用户已指定的子 agent 配置 GPT-6.1 Terra / ultra（环境支持时），不要扩大成独立线上任务。

## 2. 公共代码边界与接口约定

新增 Go 服务沿用 `internal/service` 接口、`internal/repository` 实现、`internal/handler` 入口和 Wire 注入，不另建一套框架。

| 计划模块 | 职责及主要方法（设计名） |
| --- | --- |
| SupplierService | GetCapabilities、Apply、GetProfile；返回能力，不修改现有 role |
| SupplierAccountService | CreateDraft、Authorize、UpdateOwned、Submit、Pause、Resume、Revoke；每个动作带 actorID 与版本 |
| MarketplaceEligibility | ValidateCandidate(buyer, group, account, protocol, model)；覆盖所有调度路径 |
| WalletFundingService | Credit、Reserve、Capture、Release、Refund、Reclassify；来源和 operationID 必填 |
| MarketplaceQuoteService | ResolveSnapshot、ComputeUpperBound、CalculateActual；输出金额 decimal 和版本引用 |
| MarketplaceRequestService | Prepare、ReserveBudget、RecordAttempt、MarkDispatched、RecordUsage、Settle、MarkUncertain |
| MarketplaceBillingRepository | Capture / Reverse 一个数据库事务；协调旧 UsageBillingRepository 同事务步骤，不能内层另开事务 |
| SupplierSettlementService | Mature、CreateStatement、Reconcile；幂等、基于账本 |
| SupplierPayoutService | Request、Approve、Start、Confirm、Reject、Cancel、FailConfirmed；显式状态机 |

共享值对象放在新建 `backend/internal/service/marketplace_types.go`：`Money`（decimal）、`QuoteSnapshot`、`UsageSnapshot`、`MarketplaceReceipt`、`MarketplaceAttempt`、`SupplierCapability`、状态与原因。QuoteSnapshot 至少包含币种、公开／实际模型、协议、价格版本、解析后倍率与阶梯、fee_bps、hold_hours、account/offer/owner、快照时间；不能仅存指向可变配置的 ID。

`MarketplaceCaptureCommand` 至少含 receipt_id、billable_attempt_id、usage、quote_digest、consumer_charge、supplier_net、platform_fee、operation_id；仓储重新验证派生金额、所有权快照和唯一关系，不相信 handler 传来的金额。`CaptureResult` 返回是否首次生效及原流水引用。

前端新建 `types/marketplace.ts`、`api/supplier.ts`、`api/marketplace.ts`、`api/admin/marketplace.ts`，金额用字符串类型。沿用现有 API client、分页和错误格式，不把模式写进认证 token。

## 3. 开发任务

### T01 — 数据合同、政策开关与基础迁移

**改动位置**：修改 `backend/ent/schema/account.go`、`backend/internal/config/config.go`（以当前配置入口为准）、`backend/internal/service/wire.go`；新建 `backend/internal/service/marketplace_types.go`、`marketplace_policy_service.go`、`backend/ent/schema/supplier_profile.go`、`sharing_offer.go`、`marketplace_policy.go`、`marketplace_price_version.go`、`backend/migrations/<next>_marketplace_identity.sql`。后续财务 schema 由相应任务补全。

- [ ] 先写 `backend/internal/service/marketplace_policy_test.go`：默认关闭、simple mode 拒绝、缺费率／等待期／并发上限／最低提现配置不可启用、已发布版本不可原地修改。
- [ ] 在 `backend/internal/repository/supplier_schema_integration_test.go` 写 owner FK、offer account unique、旧账号 owner=NULL 的兼容测试；先运行确认缺实现导致失败。
- [ ] 实现类型、配置和迁移；添加审核状态、运行资格原因、policy version，不新增 tenant／lease 字段。
- [ ] 更新 Ent 与 Wire 生成文件：在 backend 执行 `go generate ./ent` 和 `go generate ./cmd/server`；审查生成 diff 不含无关表改动。
- [ ] 运行 `go test -tags=unit ./internal/service -run TestMarketplacePolicy` 和 schema 集成测试；记录结果并提交本任务。

**完成标准**：老数据可启动；所有开关关闭时无新派发；政策未准备好无法打开市场。覆盖 R12。

### T02 — 同一身份的供应能力与归属权限

**改动位置**：新建 `backend/internal/service/supplier_service.go`、`backend/internal/repository/supplier_repo.go`、`backend/internal/handler/supplier_handler.go`、`backend/internal/server/routes/supplier.go`；修改 `backend/internal/server/routes/user.go`、`backend/internal/handler/wire.go`、`backend/internal/repository/wire.go` 及相应服务注册。

- [ ] 先写 `supplier_service_test.go` 与 `backend/internal/handler/supplier_handler_test.go`：consumer+supplier 同 userID、pending/rejected/suspended 的读写差异、非 owner 统一 404、role 不提升。
- [ ] 运行 `go test -tags=unit ./internal/service ./internal/handler -run TestSupplier`，确认用例失败原因正确。
- [ ] 实现 `/user/capabilities`、供应申请、本人查询和 owner-scoped 仓储，所有 DTO 脱敏；权限由后端能力校验，不接收前端 workMode 作为授权依据。
- [ ] 补 `backend/internal/server/routes/supplier_routes_test.go` 验证 JWT、面板限速、用户门户限制及管理员入口边界。
- [ ] 重跑上述测试与路由测试，提交本任务。覆盖 R01、R02。

### T03 — 供应授权与凭据加密

**改动位置**：新建 `backend/internal/service/supplier_authorization_service.go`、`credential_envelope.go`、`backend/internal/repository/supplier_authorization_cache.go`；修改 `backend/internal/repository/account_repo.go`、`backend/internal/service/openai_oauth_service.go` 的适配复用边界、`backend/internal/handler/admin/account_data.go`；新增供应授权 handler／DTO。逐项核查所有账号凭据刷新写入点，不仅新增接口。

- [ ] 先写 `credential_envelope_test.go`、`supplier_authorization_service_test.go`：错 owner/AAD/key、密文篡改、旧 key 轮换、state 重放／过期／跨实例、回调绑定不一致、token 不返回浏览器。
- [ ] 运行 `go test -tags=unit ./internal/service -run 'TestCredentialEnvelope|TestSupplierAuthorization'`，确认失败，再实现信封、服务端交换、用户绑定会话和 TTL 600 秒。
- [ ] 加入仓储集成测试：并发字段更新不覆盖加密凭据；令牌刷新仍加密；供应账号无明文回退；管理原始导出拒绝共享凭据；缓存投影和日志不含密钥。
- [ ] 使用本地伪 OAuth／固定凭据 fixture 验证，禁止为 CI 授权真实账号。API key 导入是 write-only，平台 URL／代理白名单不能被供应者覆盖。
- [ ] 重跑测试和生成检查，提交本任务。覆盖 R02。

### T04 — 账号分享生命周期与调度资格

**改动位置**：新建 `backend/internal/service/supplier_account_service.go`、`marketplace_eligibility.go`、`backend/internal/repository/sharing_offer_repo.go`、`backend/ent/schema/marketplace_outbox.go`、对应迁移；修改账号 DTO／用户 routes；后续 T09 接调度实际路径。

- [ ] 先写 `supplier_account_service_test.go`、`marketplace_eligibility_test.go`：并发正整数且受平台上限、所有者不可转移、修改模型范围重新审核、暂停／撤销、主／影子去重、自消费排除。
- [ ] 测试 pending/suspended/credentials_invalid 无法派发，降并发不取消在途，version 冲突不能覆盖。
- [ ] 实现批准状态与 enabled 分开、资格查询、事务 outbox 失效通知；供应者不能写任意 group/proxy/priority/rate_multiplier。
- [ ] 自测探测采用适配器健康检查；确需模型请求时固定无敏感 prompt、小输出上限、限频，获取现有账号并发槽位，不产生供应收益。不能开放任意免费聊天入口。
- [ ] 运行 `go test -tags=unit ./internal/service -run 'TestSupplierAccount|TestMarketplaceEligibility'` 和 owner/offer 仓储集成测试，提交。覆盖 R03、R04。

### T05 — 资金来源账、冻结分配与不可变账本

**改动位置**：新建 `backend/ent/schema/wallet_funding_lot.go`、`wallet_funding_allocation.go`、`wallet_funding_state.go`、`ledger_transaction.go`、`ledger_entry.go`、`supplier_wallet.go`、对应 SQL 迁移；新建 `backend/internal/service/wallet_funding_service.go`、`backend/internal/repository/wallet_funding_repo.go`、`marketplace_ledger_repo.go`。

- [ ] 先写 `wallet_funding_service_test.go`，验证资金类型、FIFO、负债抵扣、decimal 精度、现金与赠送可用性、同操作重放。
- [ ] 写 `wallet_funding_repo_integration_test.go`：两次同时冻结不超额、普通消费无法占用共享冻结、借贷平衡、rollback 不留下半笔账、锁顺序下退款／冻结竞争。
- [ ] 实现 transaction-aware 仓储方法，由上层同一个 SQL/Ent 事务协调；禁止各 repo 自行提交造成拆分事务。
- [ ] 统一金额量化与不变量校验；余额、冻结、lot、ledger 同事务更新。非负列与唯一操作 ID 由数据库保护。
- [ ] 运行 `go test -tags=unit ./internal/service -run TestWalletFunding` 和 `go test -tags=integration ./internal/repository -run 'TestWalletFunding|TestMarketplaceLedger'`；提交。覆盖 R05、R06、R08。

### T06 — 所有旧余额入口兼容与迁移

**必须检查／改动的位置**：`backend/internal/repository/user_repo.go`、`usage_billing_repo.go`、`affiliate_repo.go`；`backend/internal/service/admin_user.go`、`auth_oauth_first_bind.go`、`redeem_service.go`、`promo_service.go`、`usage_service.go`、`user_service.go`、`payment_fulfillment.go`、`payment_refund.go`。新建 `backend/internal/service/wallet_funding_migration.go`、`wallet_funding_reconciliation.go`。

- [ ] 先建立余额写入矩阵与回归测试：充值、兑换码、注册／首绑赠送、优惠码、邀请返佣转入、后台 SetBalance、旧调用扣款、退款、旧冻结／解冻，每一入口都对应来源类型和幂等键。
- [ ] 写 `wallet_funding_migration_test.go` 与仓储 integration 测试：remaining=max(B,0)+H、reserved=H、debt=max(-B,0)，B/H 不改变；旧 batch 在途 capture/release 不重复冻结；迁移与并发入账竞争、迁移重跑不重复、无法识别冻结来源时暂停该用户迁移。
- [ ] 先让所有写入口支持 funding 事务，再实现逐用户行锁迁移；普通消费顺序严格按设计，不能对余额直接 float 加减而漏来源。
- [ ] 对账核验 `sum(remaining)-debt=balance+frozen_balance`、`sum(reserved)=frozen_balance`、历史操作引用；验证旧自营透支可形成 debt 且不侵占共享 hold、所有释放／退款先抵债。记录差异用户并阻止其共享消费。
- [ ] 用 `rg -n 'AddBalance|SetBalance|UpdateBalance|balance\s*=|frozen_balance' backend/internal` 二次审计所有写入口；输出矩阵保存到项目运行手册，不打印用户真实余额。
- [ ] 运行相关既有用户／充值／退款／返佣／兑换测试和新资金 integration 测试，提交。覆盖 R06、R09、R12。该任务未完成不得开启共享扣费。

### T07 — 价格快照、预算与持久请求凭据

**改动位置**：新建 `backend/internal/service/marketplace_quote_service.go`、`marketplace_request_service.go`、`backend/internal/repository/marketplace_request_repo.go`；新建 `backend/ent/schema/marketplace_request.go`、`marketplace_attempt.go` 与迁移；复用 `backend/internal/service/openai_gateway_usage.go` 中计量规则。

- [ ] 先写 `marketplace_quote_service_test.go`：缓存输入去重、阶梯价格、优惠倍率、明确零价和缺价格区别、快照不可变、0/10000 bps 与尾数边界。
- [ ] 写 `marketplace_request_service_test.go`：最大输出限制真的传给上游、工具 schema 输入预算、不能保证费用上界的模型拒绝、切换候选预算补足、同 key 不同摘要冲突。
- [ ] 写仓储集成用例 `TestMarketplaceRequestIdempotency`：多实例竞争创建唯一 receipt、可选 key 按 user+apikey 隔离、已完成不再次请求上游。
- [ ] 实现 prepared/reserved/dispatched/uncertain/settled 状态与唯一 billable attempt；只保存摘要和归属／usage，不保存完整用户 prompt。
- [ ] 运行 `go test -tags=unit ./internal/service -run 'TestMarketplaceQuote|TestMarketplaceRequest'` 及对应 integration 测试，提交。覆盖 R05、R07、R08。

### T08 — 原子扣费、分账与可靠事件

**改动位置**：新建 `backend/internal/repository/marketplace_billing_repo.go`、`backend/internal/service/marketplace_billing_service.go`、`marketplace_outbox_worker.go`；修改 `backend/internal/repository/usage_billing_repo.go` 提取同事务复用步骤、`backend/internal/service/usage_billing.go`、`gateway_usage_billing.go` 的共享分支。

- [ ] 先写 `marketplace_billing_repo_integration_test.go`：消费者扣款、Key quota、supplier pending、平台费、receipt、outbox 全成或全回滚。
- [ ] 写重复捕获与指纹冲突测试，验证一笔消费者扣款恰好等于 S+F；同 receipt 不能有两个 billable attempt；使用字符串或 decimal 精确比较，不能用宽松 float delta 掩盖分账误差。
- [ ] 测试实际费用超过预算：能补足时原子补足，不能补足时 needs_review、无可提现收益、无负余额偷过；失败 outbox 投影不重复扣费。
- [ ] 实现同步 capture、持久 outbox 与可重试投影；资金操作不走队列 drop/sample 或无 requestID legacy 路径。旧自营行为保持已有回归。
- [ ] 运行 `go test -tags=integration ./internal/repository -run 'TestMarketplaceBilling|TestUsageBillingRepository'` 和 `go test -tags=unit ./internal/service -run 'TestMarketplaceBilling|TestGatewayUsageBilling'`；提交。覆盖 R05、R08。

### T09 — 网关真实调用、调度和异常收尾

**改动位置**：修改 `backend/internal/handler/openai_gateway_handler.go`、`backend/internal/service/openai_account_scheduler.go`、`openai_gateway_usage.go`、`gateway_usage_billing.go` 及会话引用缓存路径；新建 `backend/internal/service/marketplace_recovery_service.go`；扩展现有 concurrency 测试。

- [ ] 先写 `backend/internal/handler/marketplace_gateway_test.go`：非流式、完整 SSE、工具消息、客户端取消、证实执行前拒绝才换账号、仅 HTTP 429 不足以认定零执行、部分输出／派发后无 usage 不再换号、DB 故障时阻止新派发。
- [ ] 写 `backend/internal/repository/marketplace_concurrency_integration_test.go`，真实 Redis+两个网关实例同时竞争 N=1/N=3；同用户多个请求同样占槽，降并发不打断，暂停后旧 sticky 不能继续派发。
- [ ] 实现 buyer context 贯穿所有调度路径，复用原子槽位；每个 attempt 实际派发前冻结快照。适配器标记 definitely_not_dispatched / rejected_before_execution / possibly_executed，共享路径遇到不确定即停止自动切换；自营原重试不改。previous_response/session 引用添加消费者访问校验，不增加账号独占。
- [ ] 共享分支等待可信 usage 捕获；超时或崩溃扫描标记 review。流结束与客户端断连采用有界后台收尾；上游消费已发生但账未落地时保留冻结和审计，不为恢复账目重发模型。
- [ ] 测试 shutdown、worker queue 溢出、outbox 重放、释放槽位、无符合条件资源，验证无凭据泄漏和重复扣款。
- [ ] 运行 `go test -tags=unit ./internal/handler ./internal/service -run 'TestMarketplaceGateway|TestMarketplaceRecovery|TestMarketplaceScheduler'` 及并发 integration 测试，提交。覆盖 R03、R04、R07、R08。

### T10 — 收益成熟、账单、退款与对账

**改动位置**：新建 `backend/internal/service/supplier_settlement_service.go`、`marketplace_refund_service.go`、`marketplace_reconciliation_service.go`、`backend/internal/repository/supplier_settlement_repo.go`、`backend/ent/schema/supplier_statement.go`；修改 `backend/internal/service/payment_refund.go` 对接来源账。

- [ ] 先写 `supplier_settlement_service_test.go`：到期与未到期、政策快照、供应冻结、未来收入优先抵债、月边界 UTC、重复成熟不会加两次钱。
- [ ] 写 `marketplace_refund_repo_integration_test.go`：三次部分退款累计不超原值、最终尾差归零、退款保留原来源类型、已提现后形成债务、原流水不可变。
- [ ] 主动充值退款仅退未用未冻 lot；模拟外部拒付，验证产生买方债务／平台损失而非自动侵占正常供应者收益。
- [ ] 实现版次账单与 CSV 脱敏导出（防 CSV 公式注入），不导出买方标识／正文；对账比较流水、钱包、余额、receipt、付款状态。
- [ ] 运行上述单元和集成测试，检查成熟 worker 多实例幂等，提交。覆盖 R09。

### T11 — 收款方式、人工提现与强制二次验证

**改动位置**：新建 `backend/ent/schema/supplier_payout.go`、`supplier_payout_destination.go`、对应迁移；新建 `backend/internal/service/supplier_payout_service.go`、`backend/internal/repository/supplier_payout_repo.go`、供应者／管理员 payout handlers；修改 `backend/internal/server/middleware/step_up.go` 增加强制复用入口及 routes 注册。

- [ ] 先写 `supplier_payout_service_test.go` 与 `supplier_payout_repo_integration_test.go`：并发提现不超 available、强制 TOTP、全局开关关闭也不能绕过、机器 Key 不可替代真人 step-up、自我审批拒绝。
- [ ] 测试 requested→approved→processing→paid；processing 结果未知不退回资金；failed_confirmed 有证据才释放且先抵债；相同外部付款号不可支付两次。退款产生债务时自动取消未 processing 的申请；approve/start 重新检查债务与冻结；退款和 start 竞争由同一 wallet 行锁确定分支。
- [ ] 实现加密收款方式及版本快照、USD 两位提现金额／八位收益余数、操作 ID 幂等、手动凭证登记和脱敏审计。
- [ ] HTTP 超时重试同一幂等键返回原结果，不因前端刷新重提提现。拒绝通用状态 PATCH 跳步。
- [ ] 运行单元／集成与现有 `step_up_test.go`，提交。覆盖 R02、R10。

### T12 — 前端工作区切换与公共壳

**改动位置**：新建 `frontend/src/stores/workMode.ts`、`frontend/src/types/marketplace.ts`、`frontend/src/components/layout/WorkModeSwitcher.vue`、`frontend/src/api/supplier.ts`、`frontend/src/api/marketplace.ts`；修改 `frontend/src/router/index.ts`、`stores/auth.ts`、`components/layout/AppSidebar.vue`、`AppHeader.vue`；新建并注册 `i18n/locales/zh/marketplace.ts` 与 `en/marketplace.ts`。

- [ ] 先写 `frontend/src/stores/__tests__/workMode.spec.ts`：route 优先于偏好、按 userId 分离、两个标签页不相互跳转、退出清理、不改变账号/API Key 状态。
- [ ] 写 `frontend/src/components/layout/__tests__/WorkModeSwitcher.spec.ts` 与路由 guard 用例，覆盖未开通进入 onboarding、suspended 可查历史、admin 个人工作区和后台上下文。
- [ ] 实现 sidebar 品牌下切换、折叠态与移动抽屉；页面模式按路由推导，顶部钱包按模式显示，公共安全／语言／主题复用。
- [ ] 加未保存表单离开 guard；不在 localStorage 保存供应凭据或收益详情。所有明确消费者深链接保持可访问。
- [ ] 执行 `pnpm --dir frontend exec vitest run src/stores/__tests__/workMode.spec.ts src/components/layout/__tests__/WorkModeSwitcher.spec.ts src/router/__tests__/guards.spec.ts`，补现有侧栏回归并提交。覆盖 R01、R11。

### T13 — 供应申请与账号分享页面

**改动位置**：新建 `frontend/src/views/supplier/OnboardingView.vue`、`OverviewView.vue`、`AccountsView.vue`、`AccountDetailView.vue`；新建 `frontend/src/components/supplier/AccountShareWizard.vue`、`AccountConcurrencyForm.vue`、对应测试；扩展 API/i18n 和 routes。

- [ ] 先写 `frontend/src/views/supplier/__tests__/AccountsView.spec.ts`：审核／健康分开显示、并发 1 文案无独占承诺、编辑版本冲突、pause 明确不终止当前请求。
- [ ] 写 `frontend/src/components/supplier/__tests__/AccountShareWizard.spec.ts`，覆盖授权失败、后退保存非敏感字段、state 回调失败、服务端完成后不显示原始 token。
- [ ] 按设计复用 AppLayout、TablePageLayout、DataTable、StatCard、StatusBadge、BaseDialog；不复制管理员账号表单全部权限字段。
- [ ] 实现草稿／审核／暂停／撤销、非收益探测、概览聚合与更新时间；loading/error/empty/read-only 状态齐全。
- [ ] 运行以上 Vitest、typecheck、i18n 检查；记录移动端初步截图到项目内指定目录，提交。覆盖 R02、R03、R04、R11。

### T14 — 消费者共享模型和资金提示

**改动位置**：修改 `frontend/src/views/user/KeysView.vue` 及实际可用渠道页面；新建 `frontend/src/components/marketplace/EligibleBalanceSummary.vue`、`SharedGroupPriceDetails.vue`，扩展市场 API；后端补 catalog/balance handler。

- [ ] 先写 `frontend/src/components/marketplace/__tests__/SharedGroupPriceDetails.spec.ts`：缓存读写价、分档、零价和缺价区别，金额字符串无 Number 计算。
- [ ] 写 `EligibleBalanceSummary.spec.ts`：赠送／历史余额不可用解释、冻结资金单独展示、刷新和错误状态，无误导的“总余额足够”承诺。
- [ ] 实现共享分组标签、价格详情、余额资格说明；沿用用户选分组流程，不增加账号独占／选择具体供应者 UI。
- [ ] 后端测试只返回当前用户可见分组和自己的资金，不披露供应者私有账号信息；保留旧 Key 创建和用量页回归。
- [ ] 运行相关前后端测试并提交。覆盖 R05、R06、R11。

### T15 — 收益、提现与平台管理页面

**改动位置**：新建 `frontend/src/views/supplier/EarningsView.vue`、`StatementsView.vue`、`PayoutsView.vue`、`PayoutDestinationsView.vue`；新建 `frontend/src/views/admin/marketplace/{SuppliersView,OffersView,PoliciesView,PayoutsView,ReconciliationView}.vue`、`frontend/src/api/admin/marketplace.ts`；新建 `frontend/src/utils/money.ts`（若现有工具不能保证字符串精度）。

- [ ] 先写 `frontend/src/views/supplier/__tests__/PayoutsView.spec.ts`：pending/available/reserved 分开、最小额／两位输入、step-up 重试保留操作键、网络未知先查询、不可重复提交。
- [ ] 写 `frontend/src/views/admin/marketplace/__tests__/PayoutsView.spec.ts`：processing 未知无直接释放按钮、自身提现审批禁用并说明、金额币种凭证一致后才确认。
- [ ] 实现收益／账单详情与冲正关联，不把不可提现 pending 写成可用余额；政策发布显示版本、生效范围、必填生产值。
- [ ] 明确需人工处理的 receipt、资金差额、付款未知状态的操作入口与审计理由；后端权限仍独立验证。
- [ ] 运行相关 Vitest、金额格式测试、typecheck 和 i18n，提交。覆盖 R09、R10、R11。

### T16 — 全链路、竞争和故障注入

**改动位置**：新建 `backend/internal/integration/e2e_marketplace_test.go` 与测试 fixture；复用 `e2e_helpers_test.go`、真实隔离 PostgreSQL/Redis、伪模型上游；补关键前端 Vitest。

- [ ] 普通链路：同用户既供又买（调用其他供应者资源），上架→Key 选组→流式／非流式→缓存用量计费→S/F 分账→成熟→提现登记→账单。
- [ ] 竞争链路：10 用户、10 账号、并发 1 与 N；多实例请求不能超账号槽位，不引入一用户一账号分配；变更／暂停／粘性／故障切换全部经过资格校验。
- [ ] 故障链路：pre-dispatch DB 失败、派发后进程退出、usage 缺失、捕获提交结果未知、队列溢出、重复事件、客户端取消、Redis 故障；验证无重复收入／无凭空解冻／无不确定执行后的再次派发；伪“零执行”事后纠正仅走平台损失处理，不二次扣消费者。
- [ ] 财务链路：赠送／旧余额、价格发布中请求、实际费用超预算、三次部分退款、已支付后退款债务、外部拒付、提现未知、退款与提现竞争。
- [ ] 关闭开关、simple mode、自营原路由及旧普通计费完整回归；检查共享所有者在 DTO／日志／导出中的隐私边界。
- [ ] 按 R01–R12 记录测试名称、命令、结果与证据；提交测试和必要修复。

### T17 — UI 一致性、迁移演练与发布准备

**改动位置**：新建 `deploy/MARKETPLACE_RUNBOOK.md`、`deploy/MARKETPLACE_ACCEPTANCE.md`；仅在实际需要时修改现有环境变量示例／Compose 环境注入／CI 测试步骤，保留原构建部署链。

- [ ] 在 360、768、1440px ×明暗主题检查模式菜单、概览、账号向导、收益、提现与管理审核；检查键盘、焦点返回、未保存表单、长模型名／小数、表格空态与错误态。截图和结果只写项目内 `deploy/marketplace-evidence/`，不能外写临时目录。
- [ ] 对脱敏数据演练：兼容写入部署→资金迁移→旧冻结映射→全量对账→小范围准入→关闭新派发→继续收尾／退款／账单。记录不允许回滚到旧余额写入版本的边界。
- [ ] 运行手册写清必配价格、费率、等待期、并发上限、最低提现、密钥备份／轮换、人工审核、24h 初审／72h 处理目标、processing 未知付款处理。
- [ ] 执行最终验证命令（下一节），只在实际失败处修复；区分代码通过与运营启用门槛，不能声称真实付款／真实上游已验证。
- [ ] 默认总开关关闭交付；生产灰度是另一个明确授权的实施动作。提交运行手册与验证证据，完成任务。

## 4. 验证命令与测试环境

以下在实施阶段执行，本次文档编写不运行产品测试。新测试文件命名为计划名，实施时保持与命令一致，不能因为 `-run` 没匹配用例而误报成功；保存测试数量和实际测试名。

```sh
# backend 目录：生成代码与核心单元测试
go generate ./ent
go generate ./cmd/server
go test -tags=unit ./internal/service ./internal/repository ./internal/handler/... ./internal/server/... -run 'TestSupplier|TestMarketplace|TestWalletFunding|TestCredentialEnvelope'

# backend 目录：使用项目既有集成测试的隔离数据库/Redis fixture
go test -tags=integration ./internal/repository -run 'TestSupplier|TestMarketplace|TestWalletFunding'
go test -tags=e2e -v -timeout=300s ./internal/integration -run TestE2EMarketplace

# 项目根：完整已有后端检查与前端检查
make test-backend
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
pnpm --dir frontend run check:i18n
pnpm --dir frontend run test:run
pnpm --dir frontend run build
make test-frontend-critical
git diff --check
```

集成测试复用仓库 `testEntClient` 等 fixture，执行前确认其连接指向测试实例。不得将开发已有余额或线上账号当测试数据。没有可用隔离数据库／Redis时报告未验证，不能把纯 mock 通过说成原子性／并发验证完成。缓存／临时输出若需指定路径，放项目内工作目录，不改用户 HOME 或全局工具配置。

## 5. 完成定义与范围控制

交付必须包含：数据库迁移和兼容策略、后端权限／授权／调度／账务／结算、双模式完整 UI、R01–R12 证据、运行手册、默认关闭的发布配置。可开网页、成功调用一次模型或截图好看都不代表已完成资金链。

暂不包含：租户、账号租约、用户独占、供应者自行任意定价、套餐收益分摊、多币种兑换、自动银行付款、收益自动转消费余额、供应者本机节点、未经准入的协议。这些范围不妨碍消费者和供应者双身份并行使用。

费率和运营门槛是上线配置，不是当前文档的阻塞项。实现阶段可依任务顺序推进，但任何收费或提现启用都必须在对应资金与验收任务完成后进行。
