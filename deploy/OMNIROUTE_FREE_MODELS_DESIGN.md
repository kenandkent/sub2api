# OmniRoute 免费模型 Bun 传输方案设计（历史 v2）

> **历史设计说明（2026-10-27）：** 本文保留当时的 v2 方案、假设和证据，正文中的“本轮不部署”、text-only、Unix socket 等描述不代表当前实现或部署状态。当前实现、canary 门禁、切换与回滚状态以 [OmniRoute Free Bun 运行手册](OMNIROUTE_FREE_BUN_RUNBOOK.md) 和最终部署证据为准；截至运行手册草稿，生产切换仍待验证。

核查日期：2026-10-27。以下为当时的设计与实施建议，当时的范围为**不修改、构建或部署线上 OmniRoute**。本项目和线上 OmniRoute 是不同代码库；以下未来文件路径除特别注明外均相对于线上 OmniRoute 源码根目录。线上源码行号基于本次只读检查的容器 `omniroute:gw-nonstream-failfast-20260926`，实施前必须重新核对目标版本。

## 结论与证据边界

推荐为现有 OpenCode **keyless 免费模型路径**增加默认关闭的 Bun HTTP sidecar，按可验证的模型、协议和版本化请求 profile 严格准入；不改变其他 provider、付费 Zen 或 Go。Node 执行器调用本机 sidecar，sidecar 用固定版本 Bun 的原生 `fetch` 发起上游请求。首期服务普通 OpenAI Chat 文本客户端：connection 显式选用 `text_compat_v2`、以专用公开模型 alias 或受控灰度启用，无需客户端自定义 header。该 profile 为请求增加少量明确声明的兼容内容，是可见、主动选择的有限转换，并非语义透明。保留官方 `opencode serve` 作为兼容路径和回滚手段；付费、Go 和其他 provider 保持原路。

已有对照显示：独立 Bun 1.4.2 配合官方 2.0.10 的完整请求结构和头，在特定时间、出口、`big-pickle` 样本上取得 200、nonce、`[DONE]` 与真实 free usage；独立本机 Bun 也成功。缩短 system 或精简头的样本出现 429；精确同输入、同源 IPv6 和远端的 Node/curl 也出现 429。结果不足以确定 TLS 指纹、某个头、system 或工具 schema 的因果作用，也不保证所有模型、网络和后续版本成功。`FreeUsageLimitError` 不足以单独证明 IP 额度已用尽。详见 [OPENCODE_CALL_CHAIN.md](OPENCODE_CALL_CHAIN.md) 和 [OPENCODE.md](OPENCODE.md)。

**质量对照与候选**：对 44 次请求做了归一化核查：full 13 次中 12 次严格匹配、1 次末尾多句点；65 字节极简 M 共 17 次中 13 次严格匹配、1 次多句点、3 次标识符内部错字；289 字节 system 的平衡候选 B 14 次均严格匹配，其中 `temperature: 0` 12/12、默认温度 2/2。这些样本并非独立同分布，比例不能作为 SLO，也不能推断未来稳定性。旧测试中末尾句点的原始提示本身带有字面引号和句点，不能直接判定为违令。

配对 11 中 full 正确而 M 输出 `NONCE_C新时代文明实践中心_1380`；配对 13、14 中 M 分别输出 `NONCE_Cpackage_1380`、`NONCE_C# 380`，B 正确；配对 17 换新 session 后二者均精确。raw SSE 的单一 choice 0 均以 stop 结束，三次内部错字已经出现在上游响应；旧/新/Python 聚合对 44 条一致，故可以定位本次输出而不能区分模型生成与服务端后处理。缓存、时间、顺序及不透明模型等因素未排除，response ID 前缀不是模型身份；这些现象不能证明缩减是唯一因果。详情见 [质量对照报告](OPENCODE_QUALITY_COMPARISON.md)。

推荐 [balanced-text-profile.v2.json](opencode/balanced-text-profile.v2.json)，ID `opencode-big-pickle-text-balanced-20261027-v2`，SHA-256 `da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9`。它锁定 Bun 1.4.2 和 `big-pickle`，使用 289 字节 system（官方开头两句、通用忠实遵循/精确复制指令、合成 `<env>`），48 字节 `read` 工具数组、`tool_choice: none`、UA 与 `x-session-id`、stream/include_usage；请求 `temperature: 0`。工作目录是合成的逻辑兼容上下文，不代表真实文件系统或提供文件访问。配对故障场景 M/B 分别为 83/124 prompt tokens，full/B 为 3202/124，后者减少约 96.1%；缓存读取另计，不能当作费用降幅。8 个预注册 held-out `B0` 均严格匹配，覆盖英文、中文、额外 system、多轮、算术、Unicode 和明确要求句点，输入 171–195 tokens；其他探索性 B 输入为 124–206 tokens。

65 字节 v1 [minimal-text-profile.v1.json](opencode/minimal-text-profile.v1.json) 有已观察到的内部错字，不再推荐作为首选，保留为回归对照。289 字节 v2 是**有限样本通过的离线集成和受控预生产验证候选**，不构成全面稳定或上线证明；默认仍走 serve，Bun 默认关闭。服务器 seq53/54 的精简和完整控制均为 429，已停止该出口测试；联合与质量对照结果来自本机，生产出口恢复后仍须受控复验，长流、代理、并发和取消等也须逐项验收。本轮不修改线上。不得悄悄注入完整官方 agent fixture 或改写历史；不能静默改语义或在上游拒绝后改写重试。

发布 v2 资产后的独立脚本复验另有一次成功：同 hash、HTTP 200、精确 NONCE_CAPTURE_1254、stop 和 [DONE]、181 prompt / 35 completion tokens、2.911 秒、真实 free usage。该单次复验不并入 44 条研究统计，也没有运行线上 OmniRoute。

## 现状核查

线上容器工作目录 `/app`，只读核实 `/app/open-sse/executors/opencode.ts` 与 `opencodeServeGateway.ts` 存在；`opencode serve` 为独立服务，既有版本 2.0.10，端口 14937。只读检查不包含密钥、真实 prompt 或模型调用。容器内 `package.json` 版本为 3.8.50；具体 Git 提交与生产构建来源尚须实施前确认。

| 现状 | 代码证据与影响 |
| --- | --- |
| 路由限定 | `opencode.ts:80-87,143-151,242-250,359-396`：名单或 `-free` 被视作免费，keyless、非 Go、目标格式 OpenAI 且配置 gateway 才走 serve。名单含已下架模型，后缀并不证明可用或协议能力。 |
| 请求语义 | `opencodeServeGateway.ts:89-137` 将 system、历史折成一个文本 prompt，只提取文本；tool role 被标注成文字，工具调用 ID、工具 schema、结构化内容、图片和原生角色语义无法等价保留。`opencode.ts:269-271` 使用它。 |
| 非流 | `opencodeServeGateway.ts:270-280,519-548` 同步等待 prompt，再轮询消息；尾随 `idle/outcome=failed` 已快速报错（174-195）。`opencode.ts:283-295` 以字符数除 4 估算 usage，`total_tokens=0`；不是上游真实计数。 |
| 流 | `opencode.ts:315-337` 先打开全局事件，再同步等待 `enqueuePrompt`，完成后才构造下游流，因此不能承诺真实首 token 延迟；释放后可能输出缓冲事件。转换器只生成文本 delta 和 `[DONE]`，不输出真实 usage 或原生工具调用（`opencodeServeGateway.ts:366-402`）。 |
| 隔离与清理 | `/api/event` 是全 session 广播（`opencodeServeGateway.ts:311-317`）；`471` 行仅在事件携带且不匹配 `sessionID` 时跳过，缺 session ID 的 terminal 仍可能结束当前流，不能断言严格隔离。终态、取消、异常路径已有幂等释放上游（404-424、443-445、496-515）；setup 失败时也会取消（`opencode.ts:321-329`）。流 setup 的 create/enqueue 未传 `input.signal`（318-320），取消传播仍需覆盖。 |
| 原有直连 | `opencode.ts:399-525` 有 account/proxy 与 429/402 轮换；新免费路径须禁止已 dispatch 请求的隐式重发；已有合法节点冷却和后续新请求调度策略可以保留，不能影响付费路径现有行为。 |

源文件开头关于独立 Bun 不可复现的旧注释与本次证据不一致；实施时应更新注释，保留实验条件和未确定性，不能将其改写为对线上检测机制的确定结论。

## 方案比较

| 方案 | 优点 | 限制与决定 |
| --- | --- | --- |
| A. 继续官方 serve 桥接 | 已上线、易回滚、无需新的传输服务；官方运行时负责上下文 | 历史压平、工具语义损失、同步等待和估算 usage；适合明确接受这些限制的旧调用。 |
| B. Bun 原生 HTTP sidecar + 受控 profile，保留 A | 保留文本历史的角色和顺序、支持真实 SSE 和 usage，传输运行时隔离；通过显式 profile 服务普通 Chat 客户端 | 兼容内容会影响语义；v2 在有限样本严格匹配，生产出口和长流等未验证。**推荐离线集成和受控预生产验证**，默认关闭，不保证任意请求成功。 |
| C. 继续 Node 直连并复制头或批量注入完整 fixture | 结构简单 | 实测相同请求的 Node 失败；注入 fixture 改变语义和 token，且可能诱导不可执行的工具调用。**不采用**。 |

选择 B 是待验证的工程判断，并非宣称 Bun 是唯一必要因素或绕过限额的方法。禁止伪造来源、无限自动重试探测或通过更换标识规避配额；合法节点健康调度用于后续新请求，不能重放已 dispatch 请求。

## 请求路由与接口

1. 在 `open-sse/executors/opencode.ts` 的 gateway 判断附近引入无共享可变状态的路由决策：读取 provider、是否 keyless、规范化后的 model、明确的目标协议及请求能力。既有 `_requestFormat` 是实例字段，任何改动须避免并发请求共享状态；付费、Go 与其他 provider 原路不动。
2. `FreeTransport = serve | bun`，默认 `serve`；先对指定 connection/model 的 `text_compat_v2` 和专用模型 alias 或确定性灰度桶启用 `bun`，固定一个请求的决策；客户端用标准 Chat 请求，无需额外 header。只允许 registry 中明确标记已验证免费、已验证具体协议和 profile 的模型；`-free` 和目录同步只供候选发现，不能自动放行。初始候选是 `big-pickle` OpenAI Chat；其他免费模型、Responses、Anthropic 或多模态要分别验证后登记，不根据后缀推断协议或能力。
3. 追加 `open-sse/executors/opencodeFreeTransport.ts`，定义 `execute(request, context) -> { response, metadata }`：请求包含 provider/model/协议、经过现有协议转换后的完整 body、允许的头、稳定会话标识、deadline 和 abort；结果是状态、过滤后响应头、可背压读取的字节流及可观察元数据。不要把 credentials、原始 prompt 或请求体写入日志。
4. 新增 Bun sidecar 源文件（例如 `services/opencode-free-bun/server.ts`），在同机 Unix socket 上接收有界请求，校验本地访问权限、路径、body 上限和协议版本；socket 目录归服务用户、权限 0700，socket 0600，禁止公网暴露。若部署环境不能共享 socket，使用仅 loopback 的内部端口并加服务间认证，不信任外部转发头。sidecar 本身绝不执行工具或模型返回的命令；必须识别并拦截意外工具调用，再明确终止请求。
5. profile 为有版本的配置与实现，记录 Bun 版本、协议、header 规则、body 转换、准确 system/tool 字节和 hash、验证样本/日期及能力标志。`text_compat_v2` 只在明确选用后生效：将 v2 实验资产中的 289 字节兼容 system 作为首条 system，保留 caller 的全部原 system 和 user/assistant 历史角色、顺序及内容，不压平；添加实验资产中 46 字节 `read` 对象（48 字节数组）的兼容标记工具并设置 `tool_choice: none`；候选 hash 已固定，但只有输出质量、生产出口和长流等验收通过后才准予真实灰度。不会运行该工具；新增 system 可能改变指令优先级、模型行为及 token 成本，需在 alias/connection 配置界面和文档中告知。首期仅拟接受输出质量和生产出口验收通过、实证覆盖的文本 Chat：`tools` 缺省或为空、`tool_choice` 缺省或为 `none`；非空 tools、非 `none` tool_choice、已有 tool 历史、多模态和非 Chat 请求在发送前明确拒绝。`temperature` 缺省时明确默认 0，显式 0 保留；非 0 发送前返回 `unsupported_parameter`，不能静默覆盖。温度 0 不承诺输出确定性。其他 generation 参数逐项列入 allowlist，未知或不能忠实表达的参数拒绝，不静默丢弃。profile 不匹配返回 `unsupported_request_profile`；用户可另选已告知限制的 serve，不能上游拒绝后自动改写重试。
6. 固定已批准的 HTTPS 上游 URL 和证书校验，拒绝调用方自定义目的地、hop-by-hop 头与外部注入的 Host、Content-Length、伪造源 IP 头；按需生成经验证的头。首期锁定 Bun 1.4.2，每个 fetch 显式设置 `keepalive: false`，以禁用连接复用并隔离不同账户、出口及代理的连接；代理配置在每个请求内固定，不通过全局 `process.env` 切换。可配置固定代理用于网络连通性，不得通过轮换扩大额度；代理密钥只由部署注入，不进入日志。各代理类型与认证方式须分别通过 mock 和出口证明、证书及连接隔离验证后才开放，不假设支持所有 scheme。离线 HTTP mock 中 Bun 1.4.2 的两次 `keepalive: false` 请求使用不同源端口，支持不复用的观察；seq85 对 v1 联合候选使用 `keepalive: false` 获一次真实上游 200、精确 nonce 和真实 usage；这只证明该本机样本成功，仍须重复验证并在生产出口恢复后验收。

### 运行时版本与连接池

Bun 1.4.2 本机实查 `Bun.FetchSession` 为 `undefined`，首期不得依赖该 API。官方 [Bun 1.4.3 发布说明](https://bun.sh/blog/bun-v1.4.3) 才列出 `Bun.FetchSession`，可为会话隔离连接池；[当前 Fetch 文档](https://bun.sh/docs/runtime/networking/fetch) 也说明 `keepalive: false` 与 FetchSession，但它描述的是当前版本，不能据此推定 1.4.2 具备后者。首期使用经本地 mock 检查的逐请求禁复用方案。后续若要复用连接，可以按 egress/proxy/profile 分配专属 worker 进程并验证隔离，或升级到 1.4.3 后重新验证从请求 profile、语义、错误到代理和线上出口的整条链；不得仅为了连接池随意升级已验证运行时。

首期 `n` 缺省或为 1，其他值发送前拒绝；旧式 `functions` / `function_call` 请求字段列为未支持。上游意外非零 choice index 按协议不匹配拒绝，但仍检查全部 choices 的工具事件，不能因诊断器聚合 choice 0 就漏检其他 choice。

### 会话与状态

调用方提供稳定 session ID 时，用服务端密钥对租户、connection 和 ID 做作用域分隔的 HMAC，生成稳定、隔离的 affinity；不把客户端 ID 作为认证。缺少 ID 时接受完整自包含历史，每次生成新的隔离 session，不承诺跨请求粘性。单次请求固定出口；已 dispatch 请求不因 429 更换会话、代理或账户重放。配置替换和部署采用排空旧进程的方式，已有流保留原决定。


稳定 ID 的存储键可以使用带版本 HMAC，但**上游 `X-Session-Id` 的值不能直接使用任意 HMAC hex**。首次请求应按已验证的 OpenCode 形态分配 `ses_` + 12 位时间 hex + 14 位 base62 随机值，并以 HMAC 隔离键原子保存；后续复用该值。多实例用共享 Redis/持久化存储的原子创建避免竞态；建议空闲 TTL 24 小时，过期或密钥版本切换视为新会话并记录。没有可信 ID 时每个请求生成新合法值，仍接受自包含文本历史。该映射不会提供额度或权限。

### 流、非流与真实 usage

上游支持 SSE 时逐帧增量转发，使用完整 SSE 分帧（多行 data、跨字节边界、心跳、终止和 UTF-8），不能以单个 TCP chunk 或单行作为完整 JSON。首期文本 profile 保留 choices、文本 delta、finish_reason 和 `[DONE]`；完整 SSE 事件中的所有 choices 都须检查：任意 `tool_calls`、旧式 `function_call` delta、工具 finish reason 都在写出前拦截；文本与工具同帧时整帧拒绝，畸形、超限或无法判断的帧先拒绝。若尚未提交响应返回明确能力/协议错误，已有文本则发明确 SSE 错误并终止、取消上游；不泄漏工具名称、ID、参数片段，不伪造成功 `[DONE]`。其他协议字段按明确的能力契约处理。使用拉取式读取或有界队列（建议 256 KiB 上限）传递背压；超限或下游断开时取消上游并释放 reader/socket。流式响应只在有真实上游数据后发送；真实首 token 时间及总耗时分别观测。

非流可直接使用上游非流格式；若该 profile 只支持流，则聚合 SSE 文本事件及 finish reason 为完整响应，设置最大大小和 deadline；发现意外工具调用返回错误；流未正常结束时返回错误，不伪造成功。请求 usage 仅在上游支持的协议参数中开启；透传真实数值与 billing 字段，并区分 `usage_source=upstream | unavailable`。缺失 usage 就省略或标记不可用，**不把字符估算伪装成真实 usage**；如计费系统必须估算，应使用单独字段和明确来源，不能与真实值合并。serve 旧路径继续标识为 estimated/unknown，直到有可证实的真实来源。

上游尚未发起时可以安全地按配置选择 serve；一旦 dispatch 开始，网络中断、超时甚至没有收到首 token 都不能证明上游未执行，禁止自动重放或切换。**首个响应字节/事件已向客户端提交后更严格禁止切换、重放或补发**；终端错误以当前协议允许的错误事件或连接中断表示，不发送成功 `[DONE]` 掩盖失败。用户自行重试时需接受可能重复执行/计量。

### 失败分类与取消

| 条件 | 返回与处理 |
| --- | --- |
| 本地 profile 不匹配、能力不支持 | dispatch 前返回明确 4xx `unsupported_request_profile` / `unsupported_capability`；不要改变请求。 |
| 上游 429 `FreeUsageLimitError` | 保留 429、错误代码及有效 `Retry-After`，分类为 `free_limit_or_rejection`；不宣称真正 IP 额度耗尽，已 dispatch 请求不重放。可使用可配置通用冷却/回退策略，冷却不能证明根因；后续新请求可由既有 scheduler 选择合法可用节点。 |
| 其他 429/402、401/403 | 保留可安全透传的状态和错误类型、Retry-After；分别记录 rate/quota/auth 或 policy，禁止把全部归为 IP 耗尽。 |
| 模型不可用/no-route、上游 4xx | 尽快返回对应可用状态和脱敏错误；不无限轮询，不根据目录声称模型可用。 |
| 连接错误、超时、侧车不可用、异常 SSE | 记录 transport/timeout/protocol 分类；已 dispatch 请求不重放，未提交响应时返回 502/504；已提交时终止流。 |
| 下游取消 | 向 sidecar 和 Bun fetch 传播 abort，释放 reader、计时器、连接；不构造成功终态；服务端记录取消而非上游失败。 |

错误消息、指标和日志只记录请求 ID、model、profile 版本、阶段、状态、耗时、计数与脱敏的连接/出口标识；不得记录 credentials、完整 body、prompt、工具参数、session 原值或响应正文。限制错误 body 的读取与输出大小并清洗敏感信息。

## 未来落点与部署设计

这些是拟议落点，不表示当前仓库已有实现：

- OmniRoute `open-sse/executors/opencode.ts`：仅在现有 keyless 免费入口选择 transport；保留默认 serve、付费和 Go 路由。`opencodeServeGateway.ts`：维持回滚路径，独立修正严格 session 隔离与 cancel 传播，补充真实/估算 usage 标记；不得凭无 session ID 事件完成任何会话。
- OmniRoute `open-sse/executors/opencodeFreeTransport.ts`、`services/opencode-free-bun/`：适配器、sidecar、版本化 profile、健康检查。现有 `open-sse/config/providers/registry/opencode/` 与目标格式配置：单独维护已验证免费模型/协议/能力 allowlist，不把动态目录或 `-free` 后缀当能力证明。首期允许 tools 缺省/空数组并规范化，非空 tools、tool 历史和多模态在发送前拒绝；未来扩展前须验证现有转换器可逆。
- OmniRoute 测试目录以目标仓库实际结构为准：侧车协议契约、SSE/取消/背压、executor 路由、并发隔离、usage 与错误回归；构建文件增加锁定的 Bun 1.4.2 发行物及完整性校验，确认镜像架构、许可证和可重现构建，镜像不放凭据或诊断样本。配置在部署编排中增设 `OPENCODE_FREE_TRANSPORT=serve|bun`（默认 serve）、profile/allowlist、socket、deadline、队列上限、固定代理和灰度百分比；名称为提议，实施时与现有配置规范对齐。现有 `OPENCODE_GATEWAY_*` 原样保留。
- 健康检查只查 sidecar 进程、socket、版本/profile 可加载、配置和本地依赖；serve 检查 `/api/info`。不消耗免费模型请求；健康不等于上游免费可用性。监控上游 429、兼容拒绝、TTFT、终止原因、取消/缓冲上限、连接数、usage 来源和跨会话异常，不记录内容。

实施前从生产镜像与真实源码构建来源确定准确 commit、已部署补丁、Dockerfile 和现有测试路径，建立可审查的最小变更、锁定依赖与镜像摘要；在生产 OmniRoute 的源码仓库提交 PR 并通过 CI、代码评审后，按正式发布流程部署，不得直接修改运行中容器。本轮未写入线上。先跑离线 fixture/本地 fake upstream 的契约测试，在隔离部署检查监听权限、代理类型及认证、出口、证书、`keepalive: false` 和停止排空，之后单节点、小比例、指定模型灰度。真实免费调用在受控验证窗口按预定有限次数执行，不把健康探测、重试或轮换当冒烟。达到验收和观察门槛再逐步放量。

部署记录应保存源码 commit、构建参数、镜像 digest、sidecar/Bun/profile 版本、配置版本和上一个已验证镜像；secret 存在现有部署密钥系统，不复制进日志或文档。上线前明确回滚窗口和负责人。回滚将新请求切回 `serve` 并排空或有界终止 Bun 在途流，恢复先前经验证的容器镜像与配置；如 sidecar 独立部署，再停止它。请求一旦发出不重放。回滚后检查 `/api/info`、路由开关、进程/连接和现有 provider 健康，不通过免费模型请求做周期探测；旧 serve 仍有本设计列出的语义限制。具体命令应按届时实际部署编排写入评审后的 runbook，不凭当前只读检查猜测。

## 实施阶段与验收矩阵

1. **证据与准入**：v1 有内部错字，v2 本轮有限样本严格匹配但不是 SLO；离线集成和受控预生产验证继续覆盖文本质量、长流、代理、并发及取消。生产出口恢复后独立受控复验。确认生产源码版本、现有协议与目标部署配置。
2. **离线实现**：先做显式 `text_compat_v2` 的准入与声明、平衡 system/工具转换及 temperature 契约、sidecar、路由及契约测试；不开启灰度。不注入完整诊断 fixture，不开启自动重放。补充 serve 隔离、取消及 usage 标记测试。
3. **有限灰度**：仅在语义质量与生产出口门槛均通过、线上源码 PR 和 CI 完成后，选定已验证模型、单连接、固定出口，小比例真实流量；人工查看错误类别、TTFT、上游真实 usage、连接泄漏、拒绝率，并预设停止阈值。语义不符、异常 429、跨 session 或资源泄漏即暂停并回滚。
4. **扩大范围**：按模型、协议、工具/多模态能力逐项验证和登记，更新 profile 证据；未验证能力保持关闭。只有实际达到阈值并通过评审后才改变默认策略。

| 场景 | 验收标准 |
| --- | --- |
| 未开启、非免费、付费 Zen、Go、其他 provider | 仍走原路径，行为与凭据处理不变。 |
| 联合 profile 与输出质量 | 200、真实 `[DONE]` 和 usage 只是传输证据；nonce、中文、自定义 system 和多轮语义须按预设样本量重复对照保守 profile，达到评审门槛才准入真实灰度。v1 的内部错字和 v2 有限样本结果须分开呈现；不能把非 IID 比例当 SLO。 |
| 不匹配/未知 profile、下架模型 | 发送前明确拒绝或显式选 serve；no-route 及时返回；只注入明确选用的版本化平衡兼容 system/tool，不注入完整诊断 fixture。 |
| 中文及自定义 system 多轮文本 | 经实证支持后准入；兼容首 system 后所有原 system/user/assistant 内容、角色、顺序不变，不压平；无 session ID 时完整历史自包含。 |
| temperature 缺省、0、非 0 | 缺省明确设 0，显式 0 保留，非 0 发出前返回 `unsupported_parameter`；不能保证确定性。 |
| tools 缺省/空数组、非空 tools、tool_choice 与历史 | tools 缺省/空数组放行并规范化；非空 tools、非 none tool_choice、tool/function 历史、多模态、非 Chat 在发送前明确拒绝；绝不执行工具。 |
| 意外工具与非流聚合 | 所有 choices、tool_calls、旧 function_call、工具 finish reason、文本与工具同帧及畸形/无法判定帧均在写出前拒绝；未提交时返回错误，已提交时明确 SSE 错误且无假 DONE；非流也返回错误，绝不泄漏或执行工具。 |
| 多租户并发与全局事件 | 事件严格匹配 session；缺 ID、其他 session 的终态不能结束本请求；不同会话不串内容。 |
| 真实流式、慢消费者、断连 | 首 token 在模型结束前可达；缓冲不超过上限；client abort、超时、终态后连接与 reader 均释放，重复运行资源稳定。 |
| 429、402、认证失败、超时和异常流 | 错误分类与 Retry-After 保留；已 dispatch 请求不重放，后续新请求可按既有合法节点策略调度；不把不完整流伪装为成功。 |
| usage 存在/缺失 | 精确透传真实上游值并标记来源；缺失不会冒充为真实计数；serve 估算显式区分。 |
| 运维 | 本地健康检查零模型请求，固定代理及逐请求禁复用按配置隔离；配置关闭、回滚和在途排空按 runbook 可验证。 |
