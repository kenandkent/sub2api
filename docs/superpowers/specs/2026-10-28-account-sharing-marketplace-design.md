# 账号共享市场：消费者／供应者双模式设计

日期：2026-10-28。代码基线：`mysubapi`，`dc8cdf597`。状态：待实施设计；本文中的新增表、接口、页面均不是已有功能。

配套：[开发计划](../plans/2026-10-28-account-sharing-marketplace.md)。本文作为功能、资金和权限语义的唯一基准；计划负责实施顺序与验证。

## 1. 目标与已确认边界

在现有 Sub2API 网关上增加账号供应能力：同一个平台注册用户既能消费模型，也能分享获准提供服务的上游账号。平台按实际调用用量向消费者计费，按事先确定的比例形成平台收入与供应者收益，再完成供应者结算。

已确认的硬约束：

1. 复用一个 `users.id` 和现有登录体系。消费者、供应者拥有不同的管理界面，可以切换模式，不需要两套注册或登录。
2. 不引入租户、`tenant_id`、账号租约、独占用户、占用续期或空闲保留机制。
3. 复用 `accounts.concurrency` 和现有用户请求并发限制。并发为 1 表示同一时刻最多一个在途请求，不保证固定服务一个用户。一个用户的多个同时请求也分别占用并发。
4. 切换界面不暂停供应账号，不停用消费者 API Key，不迁移资金，不改变在途请求。
5. 保留现有完整 Docker Compose、开发环境和 CI/CD；本次文档任务不实现功能、不部署、不修改线上 OmniRoute。
6. 前端使用本项目现有布局、主题、组件与交互方式。

“P2P”在这里指用户供应、用户消费的业务关系。第一阶段仍由平台网关调度和转发，不建设浏览器直连或用户电脑上的节点网络。

以下是为使方案可实施而选定的工程默认值，可在实现前调整：平台统一定价、专用共享分组、余额预付费、人工付款结算、排除供应者消费自己的共享账号、首期仅支持已验证的 OpenAI 兼容文本协议。费率、供应并发上限、收益等待期、最低提现金额必须在启用前配置，不把示例金额当生产配置。

## 2. 现有实现与接入位置

| 现有能力 | 源码依据（仓库相对路径） | 本次处理 |
| --- | --- | --- |
| 用户、余额与用户并发 | `backend/ent/schema/user.go`、`backend/internal/repository/user_repo.go` | 保留身份与余额，补资金来源账、供应者能力 |
| 上游账号、请求并发、主／影子账号 | `backend/ent/schema/account.go` | 增加所有者；复用 concurrency；共享池禁止影子重复供给 |
| Redis 原子并发槽位 | `backend/internal/repository/concurrency_cache.go`、`backend/internal/service/concurrency_service.go` | 保留请求级槽位，不新增用户占用账号机制 |
| OpenAI 调度、粘性和故障切换 | `backend/internal/service/openai_account_scheduler.go`、`backend/internal/handler/openai_gateway_handler.go` | 各候选路径统一追加共享资格和所有权校验 |
| Token、缓存 Token、倍率计费 | `backend/internal/service/openai_gateway_usage.go`、`gateway_usage_billing.go` | 复用计算规则，新增不可变报价快照与共享资金路径 |
| 扣费事务与请求去重 | `backend/internal/repository/usage_billing_repo.go`、`backend/internal/service/usage_billing.go` | 共享扣费扩展为原子分账，不走无去重回退 |
| 管理员账号和 OAuth | `backend/internal/server/routes/admin.go`、`backend/internal/service/openai_oauth_service.go` | 新建用户绑定的授权流程，不直接开放管理员接口 |
| 充值、退款、邀请返佣 | `payment_fulfillment.go`、`payment_refund.go`、`affiliate_repo.go` | 参考幂等事务，接入资金来源；供应收益独立于邀请返佣 |
| 用户页面与后台 | `frontend/src/router/index.ts`、`frontend/src/stores/auth.ts` | 保留原路由，增加能力和供应工作区 |
| 布局与表格 | `AppLayout.vue`、`AppSidebar.vue`、`AppHeader.vue`、`TablePageLayout.vue`（均在 `frontend/src/components/layout/`） | 复用布局，仅按路由模式调整导航和指标 |

当前代码不能直接以“加一个 owner 字段”实现可结算市场，原因包括：

- 账号仓储现有凭据写入的是 JSONB，审计路径未见应用层加密；现有管理员原始导出也不能对供应者开放。
- OAuth 会话目前没有绑定供应者身份；管理员获取 tokenInfo 的接口不能直接搬到用户页面。
- `ActualCost` 才是消费者计费结果；`TotalCost`、账号统计成本和 `account.rate_multiplier` 不是供应者应收款，也不等于真实上游账单。
- 用量日志在扣费之外尽力写入，普通文本用量任务在特定队列溢出配置下可能丢弃。不能扫描 usage_logs 作为唯一结算依据。
- 现有余额包含充值、兑换码、赠送、返佣、管理员调整等，且调用后扣费可能产生负余额。必须区分可形成现金收益的资金来源。

## 3. 范围、角色和权限

### 3.1 第一阶段范围

支持平台明确准入的供应商与账号类型，以及通过验证的文本 Chat Completions、Responses 协议，包含流式、非流式及客户端工具调用消息。工具定义和结果作为文本用量正常计费；不在平台执行供应者工具。

共享分组暂不支持套餐扣费、图片／音视频计价、Realtime／WebSocket、平台执行的付费搜索或其他不能可靠归属成本的扩展。必须在共享入口明确拒绝，不静默使用错误价格。平台自营分组原有能力保持不变。后续按协议分别补计量和验收，再开放共享。

供应者授权证明其控制账号，并不自动证明其拥有转售权限。平台应维护 provider × account_type × protocol 的准入清单。个人 ChatGPT 订阅不默认视为可转售资源；商业开放前核验具体上游授权。参考 [OpenAI 使用条款](https://openai.com/policies/terms-of-use/)、[账号共享说明](https://help.openai.com/en/articles/10471989-openai-account-sharing-policy)、[服务协议](https://openai.com/policies/services-agreement/)（2026-10-28 查阅）。技术实现不以绕过上游限制为准入条件。

### 3.2 同一身份的能力

| 能力 | 消费者 | 供应者 | 平台管理员 |
| --- | --- | --- | --- |
| 自己的 Key、消费、余额、订单 | 是 | 切到消费者模式使用 | 按现有个人权限 |
| 申请供应、查看申请状态 | 是 | 是 | 审核 |
| 自己的授权账号、并发、上架状态 | 否 | 是，受供应状态约束 | 管理与审核 |
| 自己的收益、账单、提现 | 否 | 是 | 审核、对账 |
| 别人的凭据、提示词、响应内容 | 否 | 否 | 仅现有被授权运维权限，不扩大 |
| 市场价格、抽成政策、准入与共享分组 | 否 | 否 | 是 |

供应者身份通过 `supplier_profiles` 表示，不把普通用户升级为 `admin`，也不替换现有 `role=user/admin`。UI 模式不是服务端权限凭证。后台所有操作仍验证真实用户、能力、对象所有权和动作资格。

供应者状态：未申请 → pending → active / rejected；active → suspended；整改审核后 suspended → active。pending/rejected 可补资料并重提；suspended 保留自身历史收益、账单和状态读取，禁止新上架。供应冻结不自动禁止消费；提现冻结需要独立状态与原因。全局用户禁用仍遵循现有规则。

## 4. 前端双模式与交互设计

### 4.1 布局与路由

保留所有现有消费者 URL：`/dashboard`、`/keys`、`/usage`、`/redeem`、`/affiliate`、`/available-channels`、`/profile`、`/subscriptions`、`/purchase`、`/orders`。新建供应者 `/supplier/*`，管理员功能放在 `/admin/marketplace/*`。`/home` 继续是现有公共入口。

模式入口位于 `AppSidebar` 品牌区下方、导航菜单上方：显示“消费者模式”或“供应者模式”，点击出现两个选项与当前选中态。未开通供应能力时仍可选择“供应者模式”，进入申请／审核状态页；不制造第二次登录。侧栏收起时显示带可访问名称的模式图标和菜单；移动端放在现有抽屉内。

`workMode` 从当前路由得出。`localStorage[work-mode:<userId>]` 只记住无目标登录后的默认工作区；明确打开某个 URL 时尊重该 URL。两个标签页可分别处于两种模式，不因 storage 事件互相强制跳转。切换模式回到该模式最近合法页面，目标不存在时使用 `/dashboard` 或 `/supplier/overview`；供应申请未完成时使用 `/supplier/onboarding`。

管理员菜单根据 `/admin/*` 路由上下文显示，个人工作区按消费者／供应者模式显示；保留管理员进入后台的明确入口。不要仅凭 `isAdmin` 强制始终展示后台菜单。共用个人资料、安全设置、通知、语言和主题；退出登录取消未完成请求并清除内存中的供应者数据。

切换前若存在未保存表单，使用 `ConfirmDialog` 提示“放弃未保存的修改并切换？”；未发出的授权表单不保存密钥至浏览器缓存。已提交提现即便切换页面也继续处理，返回后按服务端状态展示。

### 4.2 页面清单

| 页面 | 内容与主要动作 | 状态与限制 |
| --- | --- | --- |
| 供应申请 `/supplier/onboarding` | 准入说明、协议版本确认、资料提交、审核结果 | pending 展示进度；rejected 展示原因和重提 |
| 概览 `/supplier/overview` | 在线账号、当前请求并发、待结算／可提现收益、趋势、待办 | 显示统计更新时间；并发是即时近似读数，不承诺额外容量 |
| 账号 `/supplier/accounts` | 平台、名称、可供模型、并发上限／当前值、健康状态、上架状态；新增、编辑、暂停 | 业务审核与运行健康分别展示；分页、过滤、空态 |
| 账号详情 `/supplier/accounts/:id` | 授权有效性、配置、自己账号的聚合用量、审核记录 | 不显示凭据、消费者标识、请求正文 |
| 收益 `/supplier/earnings` | 按时间／账号／模型筛选的收入、抽成、净收益和冲正 | 金额明细对应不可变流水；不把估算展示成已入账 |
| 账单 `/supplier/statements` | 月账单、期初期末、净入账、冲正、提现、可下载脱敏明细 | UTC 月边界，页面可本地化时间并标注结算时区 |
| 提现 `/supplier/payouts` | 可提现、提现中、已支付；创建申请与历史详情 | 服务端确认后才显示成功；不自动承诺到账时间 |
| 收款方式 `/supplier/payout-destinations` | 脱敏收款信息、验证状态、更新 | 二次验证；已用于支付的快照不可覆盖 |
| 管理后台 `/admin/marketplace/*` | 供应申请、上架审核、准入、价格抽成、资金对账、提现审核 | 禁止管理员审批／确认支付自己的提现 |

账号分享交互：选择获准平台／类型 → 服务端发起授权或一次性密钥录入 → 配置名称、允许模型子集、请求并发 → 展示当前价格和抽成规则并确认 → 提交审核。API Key 输入只写入一次，后续不能取回明文。OAuth 回调遵循已有供应商合法授权方式，不凭空假设可注册的新回调地址。

并发字段文案：“最多同时处理的请求数”。辅助文字：“设为 1 时，请求逐个执行，可能来自不同用户。”输入正整数且不大于平台上限。减小并发不会中断已有请求；恢复到上限以下后才接收新请求。暂停确认框明确“停止分配新请求，当前请求会继续完成”。撤销授权需单独确认，显示可能影响在途请求。

消费者沿用 Key 和可用渠道页面。在共享分组增加“共享资源池”标签、模型单价、缓存价格、资金可用范围提示。用户选择分组，平台调度账号；第一阶段不提供挑选具体供应者或独占账号入口。消费者顶部仍展示消费余额及冻结额；共享用途展示“可用于共享模型的余额”，解释赠送额度可能不可用。

### 4.3 与项目一致的视觉规范

- 使用 `AppLayout`，64px 顶栏、256px／72px 侧栏、主区 `p-4 md:p-6 lg:p-8`；保留已有 mesh 背景、glass 顶栏和明暗主题。
- 使用 Tailwind 已有 `primary` 色：500 `#14b8a6`、600 `#0d9488`，dark 色阶与现有系统字体。不新增品牌色、字体或组件库。
- 列表复用 `TablePageLayout`、`DataTable`、`Pagination`、`SearchInput`、`Select`；状态使用 `StatusBadge`；金额概览参考现有 Affiliate 页面卡片层次，但收益业务独立。
- 表单、确认和详情复用 `BaseDialog`、`ConfirmDialog`、`Input`、`Toggle`、`HelpTooltip`；现有 `.btn-primary`／secondary／danger 与 `.card` 样式保持一致。
- 供应模式顶栏突出“可提现收益”，消费者顶栏突出“消费余额”，不得混合成一个总资产数字。收益页同时展示待结算、可提现、提现中和已支付，分别标注。
- 加载用 Skeleton；无账号、无流水、无提现分别给明确空态和可执行下一步。错误留在原页并给重试；金融写入不做乐观成功，按钮防重复，超时后先查询同一幂等操作状态。
- 配置更新用版本号防覆盖，409 时保留未提交字段、提示配置已变化并重新确认；账号授权失败不丢掉非敏感配置。
- 360／768／1440px、light／dark 都检查；移动端表格遵循现有滚动／卡片模式，金额和动作不截断。支持键盘、焦点恢复、Esc、明确标签；状态不只靠颜色。所有新增文字提供中英文 i18n。

## 5. 账号归属、授权与分享生命周期

### 5.1 所有权和凭据

`accounts.owner_user_id` 可空：空值表示现有平台自营账号；非空表示该供应者拥有的账号。发生财务记录后禁止直接改归属，转移需求另行设计。供应者仓储操作必须使用 `id AND owner_user_id`，查询他人对象与不存在对象统一 404；不能先按 ID 读取再由前端隐藏。

供应者可改字段白名单：展示名称、允许的模型子集、受限请求并发、暂停／恢复申请。价格、结算费率、管理优先级、组归属、平台代理、统计倍率由平台控制。第一阶段不允许供应者任意提交转发 URL、内网地址或代理配置。

新增共享凭据采用 AEAD 加密信封：`version/key_id/nonce/ciphertext`，AAD 包含 provider、owner_user_id、account_id。主密钥由服务端环境或受控 secret 注入，不入库、不进日志；启动时验证当前 key_id 可用。数据库先生成账号 ID，再在事务中保存加密信封。既有账号继续原有读取方式；有 owner 的账号禁止明文回落。

创建、刷新令牌、批量更新、并发更新和调度快照均必须保留加密封装。仅上游调用和刷新适配器在内存解密；供应者 DTO、事件、缓存投影、日志和导出不得包含凭据。管理员的通用原始导出对共享账号也默认拒绝；需要恢复时通过受审计的备份恢复渠道。密钥轮换按 key_id 逐批重加密；解密失败立即停止新请求并告警。

供应授权会话绑定 authenticated user_id、purpose、account_id（重新授权时）、provider、固定 redirect/proxy 配置、PKCE/state，TTL 600 秒，单次消费。使用跨实例共享的安全状态存储；令牌交换在服务端完成。浏览器只能得到成功／失败与账号引用。重放、他人 state、过期会话、回调绑定不一致全部拒绝。

### 5.2 分享状态

`sharing_offers` 一账号一条主记录：draft → pending_review → approved；审核拒绝回到 rejected，可修改重提。approved 下另有供应者开关 `enabled`；paused、credentials_invalid、rate_limited、supplier_suspended 等为有效可服务状态的原因，不混为一枚持久枚举。撤销为 revoked；恢复授权必须重新通过适用审核。

实际可调度条件是：供应者 active、offer approved 且 enabled、账号健康可调度、协议／模型准入、共享分组成员有效、未全局关停。修改凭据／平台／模型范围扩大会重新审核；缩小模型范围、调整受限并发、暂停无需重新审核。通过事务 outbox 使现有调度缓存失效；缓存命中后派发前再次核验资格。

首期共享池只收主账号；影子账号不重复上架、不作为共享候选。授权创建时检查已有主账号／影子关系与可获得的上游稳定身份指纹，重复上架拒绝，不能可靠辨识时转人工审核。这里不保证控制供应者在平台外使用账号的并发。

## 6. 调度与请求并发

继续沿用现有分组、模型过滤、优先级、负载、粘性、限流与故障切换。共享资源使用独立分组，第一阶段不与平台自营账号混池，避免同一消费者请求出现不同资金语义。

| 配置 | 含义 |
| --- | --- |
| 用户并发 5 | 用户所有请求继续受现有用户并发控制 |
| 账号并发 1 | 任意用户合计最多 1 个在途请求；结束即释放 |
| 账号并发 3 | 任意用户合计最多 3 个请求，同一用户也可占多个 |
| 粘性命中 | 优先尝试历史账号，但仍经过资格和并发检查，不独占 |
| 满载或限流 | 沿用现有等待／其他可用账号／明确错误策略；不建立租约 |

在普通选择、粘性命中、previous_response_id 路径和故障切换中执行同样的资格过滤，传递 buyer_user_id，排除 owner_user_id 等于买方的共享账号。若全部候选都是自己的账号，返回无可用共享资源，不扣费。供应者自测使用单独非收益探测，不绕过账号并发。

账号并发继续使用现有 Redis 原子槽位和释放／失效机制，多个后端实例共享限制。无需新增按用户持有账号的表或锁。分享暂停后，新请求不能因旧粘性缓存继续进入；已派发请求按现有超时完成。降低并发到当前在途数以下不强杀请求，只阻止继续增加。

会话私密性另设访问控制：服务端保存的上游 response/session 引用只能被原消费者身份及获准会话上下文使用，不能因多个用户共用上游账号而读取彼此上下文。这是请求数据访问控制，与账号租约无关。

## 7. 消费计费、资金来源和平台抽成

### 7.1 定价与用量

共享分组由平台发布版本化价格，包含公开模型名到实际模型映射、协议、输入／输出／缓存读／缓存写价格、上下文分档和有关倍率。供应者可接受已公布政策或暂停供应，首期不自行改消费者单价。每次实际上游尝试保存解析后的价格、消费者倍率、供应者、账号、费率版本；价格修改仅影响之后的新尝试。

复用现有用量解释器与 Billing calculator：缓存命中按缓存价格，不能既计普通输入又重复计缓存；reasoning 若已含在输出中，不二次收费。优惠、零价、长上下文阶梯均以快照中的解析规则计算。缺价格必须拒绝；明确发布的零价可以调用，但不产生可提现收益，除非另有平台出资的补贴账。

共享首期仅扣余额；订阅／套餐不作为供应者现金收益来源。货币统一 USD，新增资金字段 `NUMERIC(20,8)`、Go decimal；新 API 金额用字符串，禁止浏览器或新资金代码用浮点运算。旧 API 为兼容可保留格式，进入新资金事务前统一量化到 8 位。价格计算保持足够中间精度，最终消费者扣款一次量化。

设实际完成扣款 C，平台费率 b（0..10000 个基点）：

```
供应者净收益 S = round_half_up(C × (10000 - b) / 10000, 8)
平台收入     F = C - S
必须满足     C = S + F，C/S/F 均 >= 0
```

示例仅用于理解：实际扣款 $0.01234567，费率 2000 bps，供应者 $0.00987654，平台 $0.00246913。账号统计成本和供应者真实上游支出不参与此分账公式。

### 7.2 可结算资金与旧余额兼容

消费余额与供应收益是两个钱包。消费者的 `users.balance` 继续作为既有余额投影；引入 `wallet_funding_lots` 记录余额来源：cash_paid（已确认充值）、promotional（赠送／一般兑换码／邀请返佣）、legacy_unverified（迁移旧余额）、platform_subsidy（明确预算出资）。只有 cash_paid 和明确允许的 platform_subsidy 可用于共享消费。

不能根据历史正余额推定现金：迁移时以现有可用余额 B 和冻结余额 H 为输入，建立 `max(B,0)+H` 的 legacy_unverified lot，其中 H 关联原冻结业务，债务为 `max(-B,0)`。核验到真实历史充值后可做有证据的来源重分类，必须避免重复计入。既有免费赠送／邀请码余额仍可用于原业务，页面解释共享可用额度差异。

所有余额写入统一经过 funding service／同事务仓储：充值、兑换、注册／OAuth 首绑赠送、优惠码、邀请返佣转入、管理员调账、旧网关扣费、退款、冻结／解冻。普通自营消费默认先耗 promotional，再 legacy_unverified，再 subsidy，再 cash_paid，各类 FIFO；共享消费只选择允许的来源。新入账先抵扣既有债务，余量才形成可用 lot。管理员加钱默认 promotional；标记现金必须关联已验证入款证据。

保留项目现有余额表示：`users.balance` 是可用余额（允许旧业务透支），`users.frozen_balance` 是另外存放的冻结余额；冻结发生时 balance 减少、frozen 增加。新恒等式为 `sum(lot.remaining) - wallet.debt = users.balance + users.frozen_balance`，并且 `sum(lot.reserved) = users.frozen_balance`。remaining 包含 reserved；不改变旧 API／缓存里可用余额的含义。

示例：原余额 10、冻结 3 后数据库 B=7、H=3；迁移建立 remaining=10、reserved=3，B/H 不改。hold 3 实际消费 2 后，remaining=8、reserved=0、B=8、H=0；若完全释放则 B=10。旧 batch image 等尚未结束的任务按原业务唯一键映射 allocation，继续 capture/release 同一笔冻结，不重新 reserve。映射不清的用户暂停资金迁移并排查，禁止创建新共享冻结；迁移必须先具备所有旧 hold 路径的兼容处理能力。

共享消费只允许足额冻结，不允许形成新的透支。自营旧请求保留后付实际费用、允许透支的语义：先消耗未冻结 lot，不足部分记 debt 并使 balance 为负，绝不挪用共享或旧批量任务的 reserved。债务可与有效冻结共存，既有已派发共享请求仍可从专属 hold 结算；禁止该用户发起新的共享请求。自营余额检查／缓存失效仍按原规则执行。

任意释放冻结或消费退款所恢复的未冻结资金先抵扣现有 debt，再形成可用 lot；balance 按旧可用余额语义增加，防止账上有债却新增可提现来源。新的资金余额显示使用上述投影，主动充值退款只能使用未冻结可用额。共享和旧 hold 均使用统一冻结服务。

必须先部署所有资金写入兼容代码，再以用户行锁幂等迁移，跑一致性检查，最后开放共享收费。禁止迁移后旧版本继续绕过资金账写余额。人工结算、提款手续费不应混同模型抽成；首期外部转账手续费由平台承担，不额外减少已确认供应者申请金额。

### 7.3 资金与请求链路

1. 鉴权、用户并发、共享分组／协议／资金资格检查。生成服务端 receipt_id，并在数据库持久化请求摘要，不存提示词正文。
2. 预估本次允许的最大费用，冻结足够的合格 lot。文本上下文和工具 schema 纳入输入预算；输出必须有可执行上限。不能可靠计算上界的模型／协议不开共享。最大输出缺省取已公布的模型策略上限，并传给上游，不能仅在本地估价。
3. 按既有调度选账号，核验所有权和资格，获取现有账号槽位，创建 attempt，冻结归属和价格快照，再派发上游。不能在数据库记录失败时仍发请求。
4. 复用既有调度与故障切换框架，但共享资金路径增加更严格的重试门槛：仅明确未派发，或适配器确认上游在执行前拒绝且零用量的失败，才允许选择下一账号。不能只凭无 usage 或 HTTP 状态码推定零成本。部分输出、上游已受理但连接断开、处理结果不明都停止自动切换，进入收尾／人工核验。每个 receipt 最多一个实际可能收费的 attempt，因此唯一 billable attempt 与一份预算一致；自营旧路径保持原重试语义。
5. 得到可信实际 usage 后，同步等待原子捕获事务：核销实际资金、释放多余冻结、更新消费者余额／额度、登记消费者扣款和 S/F、增加供应者 pending、保存 receipt 和 outbox。共享资金操作不提交到可丢弃的普通用量队列，不使用 legacy 无去重扣款回退。
6. usage_logs 和统计图可由 outbox 重试投影；失败不影响已完成的资金账，不作为二次扣费理由。非流式最终响应、流式终止事件在已知用量持久化完成后结束；客户端提前断开也执行有界的服务端收尾。
7. 释放请求并发槽位；收益等待期到达且无风控冻结后，独立任务把 pending 转 available。

报价预算必须覆盖允许的故障切换候选，或在派发下一次尝试前补足冻结；供应者切换后归属以实际 billable attempt 为准，不能沿用首个候选所有者。已证实零成本的失败尝试冻结可复用，不多次冻结同一笔预算。适配器必须提供保守的 `definitely_not_dispatched / rejected_before_execution / possibly_executed` 分类；无法证明属于前两者一律视为 possibly_executed。如果之后发现“零成本”证据错误，禁止补扣第二个 attempt，记录计量事故并由平台单独出资／损失账处理，先下线有问题的适配器。

### 7.4 幂等与不确定结果

新共享入口使用服务端 receipt_id；客户端 Idempotency-Key 可选，作用域为 buyer_user_id + api_key_id + key，并校验请求摘要一致。相同 key 在处理中返回明确 409；已完成返回已处理状态与 receipt 引用，不再次调用上游；不保证原始模型响应重放。不同内容同 key 拒绝。缺省时每个 HTTP 请求独立，不根据提示词相同而合并。

资金捕获以 receipt + billable attempt 唯一键去重，并包含价格／归属指纹；重复提交返回原结果，指纹变化报冲突。原有 `(request_id, api_key_id)` 去重关联服务器生成的 request_id，不能相信下游随意传入的 X-Request-ID 就代表同一财务操作。

状态：prepared → budget_reserved → dispatched → settled；确定未调用可 voided；缺失 usage 或派发后崩溃进 usage_pending／needs_review。后台扫描超时 receipt，不为“补账”重发模型请求。不确定请求的冻结不能按普通请求超时直接释放，避免先花钱再形成供应收益；展示原因，告警并进入人工核验队列。运营按 24 小时内初审、72 小时内提供处理结果或明确延期原因执行；没有可信 usage 时不得生成可提现收益。允许有审计的释放或平台承担损失决定，但禁止伪造 token。

实际费用大于冻结时，先原子补足合格余额；不足则记录完整实际 usage 和差额，进入 needs_review，不把未收到的钱变成供应者 available。只能在消费者补足或平台出资补贴后完成唯一捕获；禁止悄悄扣成负数然后照常提现。

## 8. 收益、退款与结算

### 8.1 流水与账单

新建不可变 `ledger_transactions/ledger_entries`，用资金科目记录消费者可用／冻结、供应者 pending／available／payout_reserved、平台抽成和补贴／损失。每个 transaction 同币种借贷相等；业务表余额是投影而非独立事实。既有充值入口接入后，消费者余额变化与资金来源变化同事务。

收益等待期从 settled_at 计算，使用该笔政策快照，不因政策更改重新计算旧收益。到期任务按唯一流水操作键入账，可以重试。风控冻结账户不能转可提现；已有 pending 收益保留，不删除。月账单由流水汇总生成版本化快照，退款跨月在新月份体现并关联原交易，不重写已出账单。

### 8.2 两类退款分别处理

**模型消费退款**：管理员基于 receipt 执行追加冲正，累计退款不得超过原 C。按原费率和累计退款额计算累计应冲正 S，再减已冲正 S；最后全额退款精确冲完原 S/F，避免多次舍入多退。退款恢复原 funding kind／来源约束。若供应者 pending/available 不足，形成供应者 debt，冻结提现并用未来收益先抵债；已经实际支付不能撤销历史流水。

**充值退款／拒付**：主动充值退款仅允许对应资金 lot 中未消费、未冻结部分，复用现有支付退款状态并加入来源核验。银行外部拒付可能发生于钱已消费后：冲销剩余来源、记录买方债务／平台损失并冻结风险行为，不能自动把正常履约供应者收益全额撤走。确认供应者参与欺诈或服务退款时，由独立审核决定关联消费冲正。

### 8.3 提现与人工付款

MVP 使用人工外部转账，平台系统负责申请、冻结、审批、记录付款证据与对账，不在本阶段自动调用银行转账 API。供应者收益钱包与消费余额分开；收益转消费余额暂不提供。

收款方式需验证并加密保存，页面仅显示脱敏信息；能保存支付服务商 beneficiary reference 时优先不用原始银行资料。新增／变更收款方式、提现申请、管理员审批／确认付款均必须通过现有 TOTP step-up 能力的强制检查，不能因全局可选 step-up 开关关闭而跳过。未启用 TOTP 时进入现有安全设置完成开通。

状态与资金处理：

| 状态 | 动作 | 钱的位置 |
| --- | --- | --- |
| requested | 核验可用收益、最低金额、收款快照；幂等创建 | available → payout_reserved |
| approved | 管理员复核通过 | 继续 reserved |
| processing | 操作员开始外部付款，记录操作标识 | 继续 reserved；未知结果不能直接退回 |
| paid | 核实外部流水号、币种、金额、时间及凭证 | reserved → 已支付科目 |
| rejected / cancelled | 未开始外部付款时拒绝／取消 | reserved → available |
| failed_confirmed | 有证据证明未支付成功 | reserved → available |

processing 且外部结果未知时保持冻结，查证后才 paid 或 failed_confirmed，禁止盲目重复转账。外部交易引用唯一；相同后台操作重试返回原结果。请求 USD 金额最多两位小数，收益记八位，小于一美分部分保留余额。首期不支持自动换汇。供应者可取消 requested，其他取消由管理员在确证未发款时执行。管理员不得审批或确认支付自己的提现。

退款与提现竞争采用同一 supplier wallet 行锁串行处理。消费冲正先用对应 pending 与 available，不足形成 debt 并冻结提现。该事务取消尚未进入 processing 的 requested／approved 申请，释放的 reserved 先抵 debt，剩余回 available；记录 risk_cancelled 原因。approve/start 每次重新检查 debt=0、无冻结和金额充分，不能依赖早先审批结果。processing 已可能在外部执行，不能自动取消或侵占 reserved：确认成功仍记 paid，保留 debt 并抵扣未来收入；确认未支付则释放 reserved，先偿债后恢复 available。cancel/reject/failed_confirmed 的所有释放路径也必须先偿债。若 start 先拿锁进入 processing，退款遵循已派发分支；若退款先拿锁，start 被拒绝。

## 9. 数据模型与一致性约束

新增表名为设计名称，实施时遵循项目 Ent 命名规则。所有可变配置带 version；所有财务记录含 created_at、operation_id／外部事件唯一引用。资金历史不得级联删除；账号解绑保留 tombstone 与必要脱敏引用。

| 表／扩展 | 核心字段及约束 |
| --- | --- |
| accounts 扩展 | owner_user_id nullable FK；共享账号凭据为加密信封；现有 concurrency 保留 |
| supplier_profiles | user_id unique、status、审核原因、协议版本／确认时间、payout_frozen、风险原因 |
| sharing_offers | account_id unique、owner_user_id、review_status、enabled、allowed_models、version、撤销时间；所有者与账号一致 |
| marketplace_policies / price_versions | 不可变版本、准入／模型协议、分组／价格／倍率规则、fee_bps、hold_hours、发布人／时间 |
| wallet_funding_lots | user_id、kind、origin_type/id unique、original/remaining/reserved、currency；remaining >= reserved >= 0 |
| wallet_funding_allocations | lot_id、receipt/order/operation 引用、reserved/captured/released/refunded、唯一操作键 |
| wallet_funding_states | user_id unique、debt、migration_version、reconciled_at；投影校验状态 |
| marketplace_requests | UUID receipt、buyer/api_key/group、摘要、可选幂等 key、状态、预算／实际费、唯一 billable_attempt_id |
| marketplace_attempts | receipt_id + ordinal unique、account/offer/owner、immutable quote、调度／派发状态、可信 usage 和来源 |
| supplier_wallets | user_id + currency unique；pending/available/payout_reserved/debt（非负独立列）与 version |
| ledger_transactions / ledger_entries | operation_id unique、原交易关联、币种、科目、主体、金额；事务平衡；不可 UPDATE 金额 |
| supplier_statements | supplier + UTC月份 + version unique、流水截止点、各项合计、状态 |
| supplier_payout_destinations | supplier、加密收款信息、masked_label、验证状态、版本 |
| supplier_payouts | supplier、金额、状态、收款方式不可变快照、唯一申请幂等键、唯一外部流水引用、审批证据 |
| marketplace_outbox | 业务事务内产生、event_id unique、类型、对象引用、重试状态；payload 无凭据／正文 |

唯一索引负责竞争下去重；资金事务以 buyer wallet → funding lots（ID 升序）→ supplier wallet → payout 的固定顺序加锁。只涉及单方的操作遵循同样相对顺序，避免退款与提现死锁。使用短事务；事务中不调用上游、银行或其他网络服务。钱包非负约束、原交易累计退款上限和重复操作测试必须在真实 PostgreSQL 下验证。

## 10. API 合约

沿用现有 JWT、BackendModeUserGuard、面板限速和审计。分页格式、错误 envelope 使用已有约定；金额字段为字符串、时间为 ISO 8601 UTC。共享资金入口仅在 standard 计费模式且用户门户可用时启用；simple mode 不支持共享市场，不能绕过扣款。

| 接口（均 `/api/v1` 下） | 含义 |
| --- | --- |
| `GET /user/capabilities` | consumer、supplier_status、marketplace_enabled、allowed_workspaces；不改变 role |
| `GET /marketplace/catalog` | 可用共享分组、价格版本、模型及资金约束，不披露供应者凭据 |
| `GET /marketplace/balance` | 自己的 eligible_available / reserved / excluded，币种与排除说明 |
| `GET/POST /supplier/profile` | 自己的申请状态／提交或重提 |
| `GET /supplier/overview` | 自己的聚合指标、更新时间 |
| `GET/POST /supplier/accounts` | 自己账号列表／创建受限草稿或密钥导入 |
| `POST /supplier/authorizations` | 创建绑定用户的授权会话，返回合法授权入口与不含密钥的 session 引用 |
| `POST /supplier/authorizations/:id/complete` | 当前用户完成既定回调交换；幂等、单次使用 |
| `GET/PATCH /supplier/accounts/:id` | owner-scoped 详情／白名单字段，PATCH 含 expected_version |
| `POST /supplier/accounts/:id/submit` | 提交审核 |
| `POST /supplier/accounts/:id/pause`、`resume`、`revoke` | 明确动作、版本和理由；资格检查 |
| `POST /supplier/accounts/:id/probe` | 限频固定探测，非收益，不接受任意用户 prompt |
| `GET /supplier/earnings`、`statements`、`statements/:id` | 自己的脱敏流水／账单 |
| `GET/POST /supplier/payout-destinations` | 查看／提交收款方式；更新采用新版本 |
| `GET/POST /supplier/payouts` | 提现列表／申请，POST 必填 Idempotency-Key |
| `GET /supplier/payouts/:id`、`POST .../:id/cancel` | 查询状态／取消允许状态的申请 |
| `/admin/marketplace/suppliers`、`offers`、`policies` | 审核、状态管理、发布价格政策，变更写审计 |
| `/admin/marketplace/payouts/:id/{approve,reject,start,confirm,fail}` | 显式状态动作，不允许通用 PATCH 跳过状态机 |
| `/admin/marketplace/receipts/:id/refund`、`reconcile` | 幂等消费冲正／人工处理不确定 receipt，记录证据 |

网关消费者接口继续原有 `/v1/chat/completions`、`/v1/responses`，共享行为由分组决定。错误代码需区分 supplier_not_active、marketplace_disabled、insufficient_eligible_balance、unsupported_shared_protocol、price_unavailable、idempotency_conflict、operation_in_progress、version_conflict；通过现有错误 envelope 映射。金融写接口成功返回 operation_id 和已确认状态，前端超时可查询，不能重新生成操作键重试扣钱。

## 11. 上线配置、可观测性和回退

默认关闭总开关 `marketplace.enabled`，另设 onboarding、dispatch、payouts 开关。总开关只阻止新增业务，不停止既有 receipt 收尾、退款、对账和财务历史读取。启用门槛：准入与价格完备、加密密钥可用、余额来源迁移对账通过、费率／等待期／最大供应并发／最低提现配置、人工审核与付款流程可用、验收完成。

策略默认值不偷替商业决定：fee_bps 0..10000，hold_hours 非负有界整数，max_supplier_concurrency 正整数，minimum_payout_usd 非负两位小数；生产启用前必须显式设置。生产控制台显示政策生效范围，供应者提交时确认当前版本；平台发布新版本需通知，未来请求采用新版本，历史交易不改。

指标：共享调用成功率、真实 usage 缺失率、receipt 各状态与最长滞留、冻结余额龄期、资金账不平差额、outbox 延迟、并发等待、供应收益成熟延迟、提现处理龄期。日志使用 receipt/attempt/operation 引用，禁止凭据与提示词正文；供应者看不到下游消费者身份。

先内部测试用户和受控供应账号，再小范围生产供应、人工对账，然后扩大。发现账不平时关闭新共享派发与新提现并告警，保持收尾与核验可用。关闭市场可回到原消费者体验；财务迁移后不能回滚到仍会绕过 funding ledger 的旧程序，不删除财务表或历史流水。部署沿用现有 Compose/CI，只添加正式所需的密钥和开关配置，不增加 OpenCode Serve 或新的业务服务容器要求。

## 12. 验收清单

| 编号 | 必须通过的验收 |
| --- | --- |
| R01 | 同用户双能力、路由／模式切换、双标签页、退出清理；切换不改变供应或消费运行状态 |
| R02 | 消费者、供应者、管理员权限；跨用户 IDOR、OAuth state 重放／错用户、加密与导出泄漏防护 |
| R03 | 主账号上架／审核／暂停／撤销、影子去重、所有权不可变、缓存粘性路径立即资格复核 |
| R04 | 多后端共享 Redis，账号并发 1／N，单用户多请求、多人请求、降并发、异常释放；无租户／账号租约 |
| R05 | 普通与缓存 token、价格分档、优惠／明确零价／缺价格、倍率快照、舍入守恒 |
| R06 | 所有资金写入口、旧余额／在途 batch 冻结迁移、赠送不可套现、自营透支与共享冻结共存、负债抵扣、普通消费不得花共享冻结额 |
| R07 | 流式／非流式／工具消息实际 usage，客户端断开、仅证实零执行失败切换、部分或未知结果不重试、不确定 usage 不虚构收益 |
| R08 | 同请求重复捕获、并发重试、数据库故障、崩溃恢复、队列溢出、outbox 重放，均不重复扣款分账 |
| R09 | 收益成熟、冻结／解冻、账单；多次部分退款尾差、提现后退款债务、充值退款与拒付分离 |
| R10 | 提现与退款竞争、释放先抵债、强制二次验证、自我审批禁止、外部结果未知不释放不重付、唯一付款引用 |
| R11 | 360／768／1440 明暗主题、键盘、i18n、空／错／加载／未保存状态与现有页面风格一致 |
| R12 | 开关关闭保持旧调用行为；simple mode 拒绝共享；财务开关关闭仍可收尾和对账；真实服务不参与 CI |

完成标准是上述行为和财务不变量有证据通过，不是仅能够展示供应者页面或成功转发一个请求。分阶段实现见配套开发计划。

## 13. 设计审查记录

2026-10-28，独立 builder check 使用 GPT-6.1 Terra / ultra，提出四项会影响实施的关键问题。本版已逐项明确：保留旧 balance/frozen 分离语义并迁移在途冻结；自营透支记债且不得占用共享 hold；共享请求只有证实零执行才能自动重试；退款与提现通过钱包行锁和先抵债释放规则串行处理。该审查是设计可实施性检查，不代表功能已经实现或财务测试已经通过。


## 2026-10-28 实施修订：误判未执行后的异常处理

用户明确选择：管理员误判请求未执行并解冻后，若后续证据表明上游实际执行，先只登记异常和证据，补偿金额由人工核定；当前版本不自动计入供应者收益，也不再次扣除消费者余额。

异常记录须绑定原 receipt、具体 attempt、原解冻审计操作、原报价摘要、登记管理员和证据。历史解冻审计不包含 attempt 时，关联仅为管理员报告，不视为已独立核实的模型用量。登记必须经过管理员 JWT 与强制二次验证，使用不可变记录和幂等键；不修改原冻结、结算、退款、供应钱包或平台手续费。后续如需平台承担损失并自动补偿，应另行明确金额、等待期、冲正和账单规则再实施。此修订替代原 T16 中自动财务纠正的隐含要求。


## 2026-10-28 审查后的实现约束

本轮审查后的预算准入、旧余额证据核验、审核版本、分批密钥轮换、成熟失败退避及幂等恢复细则以 [运行手册](../../../deploy/MARKETPLACE_RUNBOOK.md) 和 [修复验收映射](../../../deploy/marketplace-evidence/2026-10-28-review-repairs.md) 为准。输入预算采用经运营核验且上游强制执行的总计费输入硬上限；未配置或不能证明的能力失败关闭，不使用字符数估计作为资金担保。旧来源重分类只在有当前未消费资金归属证据时由强二次验证管理员执行，且不增加总余额。该补充不改变无租户、无账号租约、按请求并发控制以及误解冻后仅登记异常、人工核定补偿的需求。
