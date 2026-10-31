# OpenCode 接入兼容性核查（2026-10-29）

核查对象：当前 `mysubapi` 的 Bun transport；这是本地代码核查，不是“全部免费模型已修复”的验收声明。尚未修改其请求伪装/模板，也未调用真实免费模型。

## 已确认的问题

- `backend/internal/service/opencode_free_bun_request.go` 仅接受映射后的 `big-pickle`，并在发送前把模型固定为该值。其他模型会在本地400失败，不能归因于上游配额。
- 同一文件仅接受字符串文本消息和 `system/user/assistant`，不支持 `role=tool`、工具回传和多模态；非空 `tools` 被直接拒绝，`tool_choice` 仅允许 `none`。`max_tokens` 等常见第三方字段也不在白名单。
- `deploy/opencode/worker/worker.ts` 再次验证固定 profile/hash/model，并注入旧模板的 system/tools、固定 `tool_choice=none`。它不是把已经接收的用户工具成功透传；非空用户工具在后端已经被拒绝。
- `opencode_free_bun_parser.go` 还会拒绝真实的 tool_calls 和非 stop 终止原因。只修改请求白名单会造成请求和响应能力不一致，也不能保证计费完整。
- Console 模型目录与实际 transport 能力不同。模型出现在列表、别名可解析，均不代表普通/工具/续接/流式请求已通过。

截图中的 OmniRoute 是另一个实现。已有诊断记录中，MoviePilot 使用 `oc/deepseek-v4.1-flash-free`，普通请求就被 OmniRoute 本地的 transport 白名单拒绝；修复本仓库不会自动改变那个服务。其线上变更和完整 MoviePilot Agent 验收不能由本地 Bun smoke 替代。

## 2026-10-30 更新：动态适配与 agent 模式

上述问题已在本轮实现中修复，当前行为以源码为准：

- `opencode_free_bun_request.go` 不再把模型固定为 `big-pickle`：接受固定注册表模型，以及匹配 `^[a-z0-9][a-z0-9.-]*-free$`（不超过 64 字符）的任意新免费模型；`jev-`/`gemini-` 前缀仍拒绝。请求先按纯文本契约校验，失败后再按 agent 契约校验，两种模式各自选择对应 profile。
- agent 模式支持调用方 `tools` 透传、`tool_choice`（`none`/`auto`/`required`/函数强制）、`role=tool` 结果续接，拒绝未匹配或重复的工具历史；`max_tokens`、`max_completion_tokens`、`stop`、`parallel_tool_calls` 进入白名单。
- `opencode_free_bun_parser.go` 解析真实 `tool_calls` SSE 流：未声明的工具流、`finish_reason` 与调用不匹配等在写出首个字节前拒绝；usage 终态与计费口径不变。
- `deploy/opencode/worker/` 通过 `PROFILES_DIR` 加载整个 profiles 目录，启动时用源码固定的 `manifest.generated.ts` 校验每个文件的 SHA-256；固定模型只匹配固定 profile，其他可接纳模型用与 Go 同源的模板替换推导 text/agent profile，envelope 身份与 `body.model` 绑定，越界请求体以 `unsupported_request` 拒绝。
- 工具 schema 按审计后的 chatSchema 收紧：`parameters` 只接受对象（null/数组拒绝），`description` 只接受字符串（null 拒绝）。

上述离线实现轮次当时未调用真实免费模型；同日后续轮次已完成真实上游验证，见下一节。媒体、多模态与其他协议仍然拒绝；模型出现在目录仍不等于已验收；下一节的逐模型验收要求不变。

## 2026-10-30 更新：真实上游验证与修复

同日晚些时候的轮次通过本地 `make dev` 环境 + 原生 worker 调用了真实上游，本节取代上一节末尾的离线结论。

修改逻辑（每处均补了回归测试）：

- `deploy/opencode/worker/chatBody.ts`：`validToolParameters` 的数组分支提到原型键检查之前（与 Go `openCodeFreeWalkToolValue` 同序）。此前工具 `parameters` 内任何 JSON 数组（如 `required: ["city"]`）都被判为 `unsupported_request`，agent 请求全部变成网关 502；`worker.test.ts` 的 agent 用例补充了 `required`/`enum`/`anyOf`/`additionalProperties` 数组。
- `backend/internal/service/opencode_free_bun_parser.go`：`finalEvents` 的终止帧（tool_calls、finish_reason、usage）补上 `data: ` SSE 前缀。此前是裸 JSON 行，会被 SSE 消费方静默丢弃，客户端只见 content 和 `[DONE]` 而服务端正常结算；parser/gateway 测试现在逐行断言 `data: ` 前缀。
- `backend/internal/service/opencode_free_bun_gateway.go`：写完终止错误响应（上游状态码透传与 already-written 的 `freeReject` 分支）后调用 `MarkResponseCommitted`，阻止 chat handler 在完整错误体后再追加通用 `event: error`（此前会把上游错误拼成 JSON+SSE 混合体）。
- `deploy/dev.py`：`development_env` 在 `.dev/opencode-free.env` 存在时按严格规则合并，`make dev` 后端与 `local.py` 共用同一 worker token；文件缺失则保持禁用。
- `deploy/opencode/e2e/probe_real_models.py`：新增 `--env compose|dev`、合规提示自动接受、dev 库容器名与上游 `FreeUsageLimitError` 识别。

验证方法：`cd backend && make test-unit`（58 包 0 失败）、`go build ./... && go vet ./...`、`bun test deploy/opencode/worker/worker.test.ts`（24 通过/128 断言）；然后 `make dev` + `python3 -B deploy/opencode/local.py worker` + `python3 -B deploy/opencode/e2e/probe_real_models.py --env dev --models big-pickle,glm-5.3-flash-free,mimo-v2.6-flash-free,deepseek-v4.1-flash-free,nemotron-3.5-lightning-free`。报告：`.cache/opencode-free-implementation/real-models-report.json`。

验证结果（2026-10-30）：

- `REAL MODEL PROBE PASSED`（23/23 检查）。5 个抽样模型非流式均返回 content + finish_reason + usage；流式均返回 content + 终止帧 + `[DONE]`（前缀修复生效）。计费：usage 行数 = 计费请求数 = 已结算数 = 10，余额差额与成本合计精确相等。
- 本地守卫：`gemini-` 别名本地 400；未知 `-free` 模型不被本地拒绝、透传上游并返回 `Model is unavailable.`；未知别名 404。
- agent 请求带着 tools 到达上游，但上游返回 HTTP 429 `FreeUsageLimitError`（`Retry-After` ≈ 17 小时，免费 agent 配额待重置）；网关对该错误透传完整，agent 传输链路已验证，逐模型 agent 验收需在配额重置后用同一 probe 复测。
- `nemotron-3-ultra-free` 上游返回 400 `Model is unavailable.`（与未知模型同消息），按上游可用性问题记录，不是本地拒绝。
- 观察到上游突发限流：短暂窗口内全部请求返回同型 429，数分钟内恢复；遇到时应重跑而非记为回归。
- 未在本轮抽样：`beam-free`、`flint-40b-free`、`nemotron-3.5-super-vl-free`（按用户要求抽 3-5 个；三者在更早的 compose 轮非流式已通过）。

## 2026-10-30 更新：agent 429 根因（工具名门禁）与占位 read 修复

背景：生产三方测试中所有 agent（tools）请求返回 429 `FreeUsageLimitError`，text 请求全部通过。

诊断（修复前的隔离实验，均在配额充足窗口）：

- 同一时刻 text 200；agent 声明任意不含 `read` 的工具集（`lookup`、`get_weather`、五工具组合等）→ 429 `FreeUsageLimitError` + `Retry-After` ≈ 10 小时；工具集含官方占位名 `read`（`[read]`、`[read,bash,edit,grep,glob]`）→ 200；`[read,get_weather]` 混合 → 200 且模型成功调用 `get_weather`（n=1）。
- 跨模型复现（3/3）：`big-pickle`、`glm-5.3-flash-free`、`mimo-v2.6-flash-free` 各 `[lookup]`→429、`[read]`→200。
- `tool_choice: none` 不能豁免；prompt、工具数量（1~5）、stream 模式、工具描述/参数均不影响判定。直连 curl（即使 system+read+stream:true）始终 429——另有客户端指纹层，Bun worker 的 fetch 已满足该层。
- OmniRoute `open-sse/executors/opencodeFreeTierContract.ts`（release/v3.8.53）契约佐证：非空 `tools`、`stream:true`、session 头、UA `opencode/<version>`，且 “the upstream inspects WHICH names a request declares”；官方客户端的占位工具名就是 `read`（`PLACEHOLDER_TOOL_NAME`），其 `applyFreeTierRequestContract` 会为调用方 tools 合并占位 `read`。
- 根因：移植时 `deploy/opencode/worker/worker.ts` 第 71 行把调用方 tools 整体替换（`hasCallerTools ? b.tools : profile.body.tools`），丢掉了占位 `read`。text 因 profile 模板恒带 `[read]` 全部通过，agent 因此被上游以 429 拒绝——上一节“agent 429 = 配额待重置”的归因不准确（同窗口 text 200 即可排除配额）。

修改逻辑（每处均补了回归测试）：

- `deploy/opencode/worker/worker.ts`：agent 请求且调用方 tools 不含 `read` 时，向出站 tools 合并官方占位 `read`（描述 “Do not call this tool. It exists only for API compatibility and must never be invoked.” + `{filePath}` schema）；调用方已声明 `read` 则原样不动；text/无工具路径不变。
- `backend/internal/service/opencode_free_bun_request.go`：新增 `openCodeFreeRequest.InjectedTools` 与 `openCodeFreeInjectedTools`，与 worker 同规则（非空调用方 tools 且无 `read` → `["read"]`），两条规则必须保持一致。
- `backend/internal/service/opencode_free_bun_parser.go`：`openCodeFreeSSEOptions.InjectedTools` + 校验器 injected 集合。累积期允许占位名（含 `rea`/`d` 前缀分片）；finish 时把占位调用从结果中剔除并把剩余调用重排回 0..n-1；若全部调用都是占位（客户端没有任何调用方工具），`finish_reason=tool_calls` 降级为 `stop`（`v.finishReason` 给客户端、`v.upstreamFinish` 供上游重复终止帧原值比较）。`tool_choice=none` 仍拒绝任何调用、required/forced 缺调用方调用仍报错、未配置 `InjectedTools` 时行为与原实现完全一致。
- `backend/internal/service/opencode_free_bun_gateway.go`：构造 SSE options 时传入 `reqNorm.InjectedTools`。
- `deploy/opencode/e2e/probe_real_models.py`：`test_agent` 计费口径修正——agent 请求本身（HTTP 200 即产生 usage 行）与续接请求分别计入 `charged`；此前只计续接，agent 成功后 usage 行数必然对不上账。

验证方法：`bun test deploy/opencode/`（65 通过/175 断言）、`cd backend && make test-unit`（58 包 0 失败）、`go build ./... && go vet -tags=unit ./...`、gofmt 无新增偏差；`make dev` + 重启原生 worker 加载新代码后，严格断言脚本 9/9 HTTP 200，并跑 `probe_real_models.py --env dev --models big-pickle,glm-5.3-flash-free,deepseek-v4.1-flash-free,mimo-v2.6-flash-free`。

验证结果（2026-10-30 晚）：

- 严格断言 9/9 HTTP 200：agent 非流式/流式 `[lookup]`、`[search_weather,get_info]`（修复前必 429 的无 read 工具集）、诱导模型调用注入 `read` 的场景（200、`finish=stop`、响应中无 `read` 泄漏——模型按占位描述拒绝调用）、text 非流/流式回归、glm/mimo 跨模型 agent、tool 结果续接（200 + stop + content）。
- probe 首轮：4 模型 ×（非流式+流式+agent+续接）= 16/16 HTTP 200；计费对账修正后 `usage 行数 = 计费请求数 = 已结算数`（2==2 单独复核通过，首轮全量为 16==16 口径），余额差额与成本合计相等。
- 首轮之后的第二轮复测撞上真实上游免费配额耗尽（text 也 429 “Free usage limit reached”，`Retry-After` 指向次日 ~08:15 +0800 重置），按已知上游行为记录为环境事件而非回归；agent 门禁修复的判定以首轮 16/16 与 9/9 为准。
- 三方生产验收需部署本修复后在配额窗口内用实际客户端复测。

## 2026-10-31 更新：失败可观测性、代理证据分类与免费配额 429 冷却/转移

背景：生产 `deepseek-v4.1-flash-free` 分组 21 实例诊断中 11 路返回 502 `proxy_or_upstream_http_error`，worker 与网关对全部失败路径零日志，且代理路径任意非 200 一律丢弃状态/头/体包装成 502，导致真实原因（含上游 429 `FreeUsageLimitError` 与 `Retry-After`）无法回溯、调度器也无法冷却耗尽实例。

修改逻辑（每处均补了回归测试）：

- `deploy/opencode/worker/worker.ts`：代理非 200 先有界读取错误体（≤64KB），按证据分类——`Retry-After`/`x-request-id` 头或 JSON error envelope（`error` 为含 `message`/`code` 的对象或非空字符串）判定为上游响应并透传真实状态与 `content-type`/`retry-after`/`x-request-id`；无证据、读取失败或体超限仍保守返回 502 `proxy_or_upstream_http_error`。所有失败路径输出单行结构化 JSON 日志（`proxy_upstream_non_200` 含状态/头/分类/脱敏 body 片段/proxy id+generation/request_id/model/耗时，`dispatch_failed` 含异常名与消息，另有 `worker_busy`、`upstream_headers_too_large`）；片段截断 512 字符并抹除 userinfo URL 与 Bearer 令牌。
- `backend/internal/service/opencode_free_bun_gateway.go`：四类失败补 WARN 日志（worker transport 失败、worker 错误含读回的 `worker_code`、上游非 200 含状态/Retry-After/x-request-id/消息/脱敏片段/是否已写出、错误体不可读与非法 content-type），均带 request_id/account_id/model/耗时；worker 错误的客户端消息追加 `worker_code`。
- `backend/internal/service/opencode_free_quota.go`：`isOpenCodeFreeQuotaExhausted`（429 且体含 `FreeUsageLimitError` 或 `Free usage limit reached`）；`HandleOpenCodeFreeQuota429` 写 `RateLimitResetAt`——优先 `Retry-After`（夹紧 1 分钟~24 小时），缺省兜底 1 小时——并即时通知调度器 block，账号自动到期恢复。
- 配额 429 且客户端响应尚未写出时，`forwardOpenCodeFree` 返回 `UpstreamFailoverError` 不写响应，由 handler 的既有候选循环切换到同分组其他实例（failedAccountIDs 与新冷却双重排除，默认预算 10 次切换，预算耗尽按既有映射回 429 并保留 Retry-After、ops 记录上游真实消息）；keepalive 已写出则回退原 SSE 透传。管理端逐实例测试命中同一检测时同样冷却（错误体只做本地分类，不回显）。
- `backend/internal/service/account_test_service.go` + `account_test_service_opencode_free_bun.go`：后台账号测试在通用协议路由前按 Bun 标记复用与网关一致的规范化、profile、代理派发与 SSE 免费校验，不创建消费者计费请求；`opencode_free_bun_parser.go` 新增 `QuietErrors` 抑制管理端日志噪声。

验证方法：`bun test deploy/opencode/`、`cd backend && make test-unit`、`go build ./... && go vet -tags=unit ./...`、改动文件 `gofmt -l`；新增用例覆盖代理证据分类四态（透传 429/400、垃圾 502+脱敏日志、超大体保守 502、dispatch 异常日志）、配额检测表、冷却窗口（Retry-After/兜底/上下限/仓失败）、网关未写出 failover 与 keepalive 回退、管理端冷却。

验证结果（2026-10-31）：bun 70 通过/0 失败/225 断言；`make test-unit` 58 包 0 失败；`go vet -tags=unit ./...` 通过；改动文件 gofmt 干净。历史 11 路 502 的具体根因无法在零日志下回溯，需部署后复跑 21 实例诊断观察带细节的新日志。

## 官方公开能力与授权边界

[官方模型文档](https://dev.opencode.ai/v2/docs/console/models) 当前列出九个免费聊天模型：`big-pickle`、`glm-5.3-flash-free`、`deepseek-v4.1-flash-free`、`beam-free`、`flint-40b-free`、`mimo-v2.6-flash-free`、`nemotron-3-ultra-free`、`nemotron-3.5-lightning-free`、`nemotron-3.5-super-vl-free`。同页明确要求免费聊天模型通过 OpenCode 使用，其他客户端采用付费 Console 或 Go。

另一免费模型 `jev-1.13-free` 使用 System One 结构化接口，官方展示了 Console API key 接入，不能当成第十个通用聊天模型。项目现有 TypeSafe/System One 路由也不等于已支持这一 Console 模型。

本轮不会扩展伪装客户端以绕过免费聊天访问限制。对于经正式授权开放给第三方的模型，可以独立做标准API模型发现、能力声明、请求工具透传、工具结果续接、流式解析与真实usage计费兼容；不能以支付模型替换免费模型却仍声称满足原需求。

## 后续完整验收应覆盖

每个明确获准接入的模型分别测试普通对话、function tools、tool结果续接、流式及非流式、length/tool_calls终态、缓存/推理usage与计费。模型目录更新不等于验收成功。

应保留错误来源、HTTP状态、Retry-After，区分本地未支持、协议错误、上游鉴权/客户端限制、上游真实429。代理路径的非200可能来自代理 CONNECT 拒绝而非真实上游，2026-10-31 起按来源证据分类：带 Retry-After/x-request-id 或 JSON error envelope 的透传真实状态（保留 Retry-After），没有证据仍保守归类为502 transport错误并落结构化日志；没有来源证明时也不能直接改成“上游额度耗尽”。没有对照证据时不能把工具字段认定为429原因，也不应自动重发可能已执行的工具请求。

MoviePilot 的验收必须从实际客户端完整 Agent 对话发起，确认工具参数和调用ID传递正确；不执行真实有副作用的工具操作。用户当前明确要求继续使用 OmniRoute 的 OpenCode free provider；本轮没有将其切换到付费模型或其他提供方，也未修改该线上配置。
