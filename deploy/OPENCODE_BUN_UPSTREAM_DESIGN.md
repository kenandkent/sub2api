# OpenCode 免费模型 Bun 上游接入设计

状态：v2 已在 `mysubapi` 实现，并于 2026-10-27 完成本地完整 Docker Compose、离线端到端及两次真实上游验收；2026-10-30 新增动态适配与 agent 模式（任意可接纳 `-free` 模型、调用方工具透传、profiles 目录 + `manifest.generated.ts` 校验），已通过全量 Go 单测与 24 个 Bun worker 测试；同日晚间完成真实上游逐模型验证（5 个抽样模型的文本/流式与计费对账全部通过，agent 请求带 tools 到达上游但被上游 `FreeUsageLimitError` 免费配额 429 拦截、待配额重置后复测），验证过程中修复 worker `chatBody.ts` 数组遍历、parser `finalEvents` 缺失 `data: ` 前缀、上游错误响应被 handler 追加第二帧三处缺陷。本轮未部署线上服务器，未修改线上 OmniRoute。本文所说的实验均为特定时间和环境的观测，生产准入矩阵仍需单独验收。相关证据见 [本地接入说明](opencode/LOCAL_FREE.md)、[兼容性核查](opencode/COMPATIBILITY.md)、[OpenCode 接入记录](OPENCODE.md)、[调用链报告](OPENCODE_CALL_CHAIN.md) 和 [诊断脚本](opencode/probe-free-chat.mjs)。

## 已实现的接口与本地验证

下文保留设计论证；实际文件和字段以本节及源码为准。全局配置为 `opencode_free_bun`，环境变量前缀为 `OPENCODE_FREE_BUN_`，默认关闭。账号凭据使用 `opencode_free_transport: bun` 和 `opencode_free_profile: opencode-big-pickle-text-balanced-20261027-v2`，仅允许 Zen、API Key `public`、官方 Console Chat 地址及 `adaptive`/`chat_completions` 协议。普通付费 Zen/Go、ChatGPT 及其他平台不会进入此路径。

- Go 实现位于 `backend/internal/service/opencode_free_bun_{request,parser,gateway,account_validation,dynamic}.go`；只有明确选择免费 Bun 接入的账号使用新分支。2026-10-30 起支持动态适配：接受固定注册表模型与任意 `^[a-z0-9][a-z0-9.-]*-free$`（≤64 字符，`jev-`/`gemini-` 除外）模型，按纯文本/agent 契约选择模式与 profile，agent 模式支持调用方工具透传、`tool_choice` 与工具结果续接；媒体、其他协议、非零 temperature 和白名单外参数仍在发送前拒绝。非固定模型的 profile 由嵌入的 `big-pickle` text/agent 模板按 `gen_profiles.py` 同源字节替换推导，固定 profile（`big-pickle`、`mimo-v2.6-flash-free`）优先且不可被推导覆盖。
- `deploy/opencode/worker/` 运行固定 Bun 1.4.2，通过 `PROFILES_DIR` 加载 `deploy/opencode/profiles/` 全部 profile，并用源码固定的 `manifest.generated.ts` 在启动时校验各文件 SHA-256；固定模型只匹配固定 profile，其余可接纳模型按同源规则推导并校验 hash，envelope 身份绑定 `body.model`。可选 Compose overlay 增加一个 worker，与应用共用网络命名空间且只监听 loopback，不公开 worker 端口。普通 Compose 仍为三个服务。
- 迁移 249 仅新增免费请求记录和会话表。`opencode_free_usage.go` 的专用仓储扩展复用现有价格和扣费 SQL，将冻结费用、幂等扣款、usage log 与 settled 状态纳入同一事务；恢复不重发模型请求。缺失或不完整 usage、取消、明确写入失败记为 unknown，不估算扣费。
- 真实上游 usage 源为 `usage.opencode.billing_source` 和 `usage.opencode.tokens`。缓存读取从 prompt 总量中拆分计价；reasoning 已包含在 completion 总量中，不另加一次。

本地 Compose 离线验收覆盖正常流/非流、缓存计价、余额对账、取消和缺 usage；独立 PostgreSQL 集成测试覆盖重复结算、事务回滚、冻结价格及恢复队列。真实上游两次请求分别耗时 3.607/6.904 秒，prompt 178/181、completion 140/87，各含缓存读取 69，均精确返回要求的文字并完成非零测试价格的账务核对。该样本只证明本次本地出口可用，不保证其他出口或未来调用始终成功。

首版明确限制：HTTP 代理已覆盖 CONNECT、TLS、凭据隔离与并发测试，但为避免把代理拒绝响应误认为上游，配置代理时任何非 200 均保守归为 502 worker error，可能丢失真实 429 的 `Retry-After`；HTTPS/SOCKS 代理拒绝。数据库核心结算有事务与幂等保护，缓存、平台配额及通知在崩溃恢复时不保证恰好执行一次。未验证生产出口、生产容量或完整生产准入矩阵。

## 目标与证据边界

保留本项目现有账号选择、并发控制、用户认证、协议处理和计费；为明确启用的 OpenCode Zen 免费模型提供可关闭的 Bun 传输路径。首期交付普通 OpenAI Chat Completions 文本请求接入 `big-pickle`：管理员为账号勾选“免费模型（Bun v2）”，通过专用分组及公开模型别名（例如 `opencode-big-pickle-compat`）选择该能力，无需客户端自定义 header。生产发布前需通过下文矩阵；其他模型、协议、媒体和工具能力不得因这次实验自动获得支持。默认现有 Zen/Go HTTP 行为不变。

已观察到：Bun 1.4.2 使用官方 2.0.10 捕获的完整 system、12 个工具及 headers，在 Zen 和 Console 的 Chat URL 得到真实 200、nonce、SSE `[DONE]` 和 `usage.billing_source=free`；本机独立 Bun 也成功，无需运行官方程序。相同原始输入、相同源 IPv6 和远端的 Node/curl 对照为 429；Bun 缩短 system 或精简 headers 的对照也为 429。控制实验与证据范围详见上述记录。它们不识别服务端判定条件，不证明 TLS 指纹、某个字段必需，更不保证请求今后、不同出口或其他模型可用。`FreeUsageLimitError`/429 也不能单凭错误名称推断真实日配额耗尽。

**诊断成功不等于任意下游请求等价。** 捕获模板是带 agent 身份、指令及工具 schema 的请求；把它拼进用户请求会改变指令优先级、行为、上下文及 token 成本。实验里工具没有执行器。不能把注入约 5k system 的路径宣传为透明代理，也不能声称仅修改 Go headers 已复现成功。配对实验表明 v1 会出现标识符内部误抄；v2 在本轮有限任务集表现更好且开销较低，具备离线集成和受控预生产验证基础，默认仍关闭，未通过生产验收。

### v1 最小候选与历史观测

已固定的实验资产是 [minimal-text-profile.v1.json](opencode/minimal-text-profile.v1.json)，ID 为 `opencode-big-pickle-text-compat-20261027-v1`，文件原始字节 SHA-256 为 `10260cbd65728a32762c18e9bc2d36b76c93e2e1b583067e80f06eea87d7cdfc`。Bun 1.4.2 联合请求显式头仅为 `user-agent: opencode/latest/2.0.10/cli` 和 `x-session-id`；不显式发送 authorization 或 content-type，Bun 自动生成的头另算。65 字节 system 为：

```text
<env>
  Working directory: /workspace/opencode-diagnostic
</env>
```

最后一行后也有换行。注入的工具数组为 `[{"type":"function","function":{"name":"read"}}]`（48 字节），配合 `tool_choice: none`、`stream: true`、`stream_options.include_usage: true`。该逻辑路径为合成上下文，不提供文件访问。本地联合 seq80 为 HTTP 200、`[DONE]`、nonce 后仅多句点、prompt 63 tokens、body 337 字节；seq85 使用相同组合及 `keepalive: false`，HTTP 200、`[DONE]`、精确输出 `NONCE_CAPTURE_8311`、prompt 63 tokens。当前本机成功支持所选传输可行，**不证明响应普遍符合指令**。生产若显式加 `application/json` 或认证等头，须在该最终组合下单独复验；先前三头 nonce 成功不是对所有后续组合的证明。

本轮完整基线 prompt 为 6382–6389 tokens，63 tokens 相对减少约 99.0%；先前 `shortenv` 712 字符 system、277 字节工具、6 headers 的 527 tokens 样本约减少 91.7%。两者均为具体 nonce 样本上的观测，不承诺用户历史或其他输入保持该用量。

| 本机候选语义实验 | 观察 |
| --- | --- |
| seq81 中文 | HTTP 200，精确 nonce，prompt 72 tokens |
| seq82 自定义 system | HTTP 200，但输出 `NONCE_C##_1380`，未匹配要求 |
| seq83 多轮 | HTTP 200，精确 nonce，prompt 89 tokens |
| seq84 普通 nonce 重复 | HTTP 200，但输出 `NONCE_CResearch_1859.`，未匹配要求 |

seq82、84 的不匹配说明 HTTP 200 与协议接入成功并不等于严格指令遵循。旧的句点样本提示本身在字面引号内含句点，不能直接认定添加句点违反了指令；后续质量对照把它单列。此前另一个 131 字节前缀中 seq67 自定义 system 和 seq68 多轮精确成功，而 seq66 中文额外输出字符；不能把质量变化归因于某个字段，也不能将该前缀当作已经稳定的保守方案。**63-token v1 不再作为首选，保留为失败回归，默认关闭**。上述本机结果不代表服务器已验证：服务器 seq53/54 控制请求返回 429 后已停测，服务器恢复后需要另行验收。

详细材料见 [缩减实验报告](OPENCODE_MINIMIZATION.md)；脱敏原始证据为仓库内 `.cache/opencode-minimization-20261027/final-joint-results.jsonl` 和 `.cache/opencode-minimization-20261027/final-semantic-results.jsonl`。所有实验均不执行工具；观测受时间、出口和上游状态约束。


发布脚本随后以同一资产 hash、`keepalive: false` 和较短测试文字独立复验成功：HTTP 200、精确 `NONCE_CAPTURE_2934`、61 prompt tokens、真实 free usage、耗时 2.954 秒。该成功补充不消除上表已有的格式/输出错误，也不能升级为全部语义通过。详细记录见 [缩减报告](OPENCODE_MINIMIZATION.md)。

### v2 配对质量对照与建议

新候选资产为 [balanced-text-profile.v2.json](opencode/balanced-text-profile.v2.json)，ID `opencode-big-pickle-text-balanced-20261027-v2`，文件字节 SHA-256 `da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9`。B 的 system 为 289 字节：官方前两句、通用指令 `Follow the user's instructions faithfully. Copy identifiers exactly when requested.`，以及 v1 的 65 字节合成 env；其 `read` 数组 48 字节、`tool_choice: none`、两项显式 headers 与 Bun 1.4.2 传输保持一致。候选 B 加请求 `temperature: 0` 作为首期推荐的实验配置，不能宣称 temperature 0 保证确定性。发布后的同 hash 资产也已独立复验：HTTP 200、精确 NONCE_CAPTURE_1254、stop 与 [DONE]、181 prompt / 35 completion tokens、耗时 2.911 秒、free usage；这次另计，不并入 44 条研究统计。

本轮 44 次真实请求均保留 raw；旧算法、新捕获器及独立 Python 对 choice 0 重解析结果一致。归一化 full（完整 system + 12 工具，传输和 `tool_choice: none` 等与 M 统一）13 次为 12 次严格匹配、1 次句点；M（65 字节 system）17 次为 13 次严格匹配、1 次句点、3 次内部误抄；B 14 次均严格匹配，其中请求 temperature 0 的 12/12、默认 temperature 的 2/2。这些任务并非独立同分布，不能把比例解释为生产可靠率或 SLO。

配对 11 的 full 正确而 M 输出 `NONCE_C新时代文明实践中心_1380`；配对 13 中 M 输出 `NONCE_Cpackage_1380` 而 B 正确；配对 14 相同请求 temperature 0 时 M 输出 `NONCE_C# 380` 而 B 正确；配对 17 fresh session 的 B 与 M 均正确。配对 12 的提示引号内字面包含句点，两个版本均加句点，不能直接判为违令。三次内部错误都出现在原始上游数据中，本轮没有证据显示由本地聚合或转码混入；仍不能区分模型生成和上游后处理，也不能证明单独裁剪字段导致差异。`system_fingerprint` 缺失，响应 ID 的 `mai-api` 前缀相关性不能证明模型身份；缓存、顺序与时间均可能混杂。

同一故障样本 M 为 83、B 为 124 输入 tokens；匹配的 full 为 3202、B 为 124，输入下降约 96.1%。这包括缓存相关 token，不能直接当成费用下降比例。预注册 held-out 的 B + temperature 0 共 8/8 严格匹配，涵盖英文、中文、用户 system、多轮、算术、Unicode、明确句点；fresh session 不保证上游标识全新，输入为 171–195 tokens（其他探索样本 124–206）。有限样本表现为后续验证提供依据，不保证一般请求或生产结果。详见 [配对质量报告](OPENCODE_QUALITY_COMPARISON.md)；今后验证使用 `deploy/opencode/quality-capture.mjs` 保存 raw、choice 0 和结束原因，并保留 v1 误抄回归。

## 代码现状

- `backend/internal/service/opencode_go.go` 区分 Zen 和 Go，按模型规则路由 Chat、Responses、Anthropic；历史自定义规则不能悄悄覆盖。Google 和 SystemOne 专用协议当前不支持。`account.go` 保留旧 Zen 基址兼容。
- `openai_opencode_session.go` 已从下游头和请求体解析 session，在本次请求中可生成 UUID，并为官方主机补客户端元数据；生成值的生命周期仅覆盖当前请求，不能推断跨请求稳定。`openai_gateway_forward.go` 构建请求并使用账号 `Proxy.URL()`；Chat、Messages 及其他路径也通过 `doOpenAIUpstream`。该函数在 `openai_plugin_transport.go` 先处理已启用的 OpenAI OAuth 插件，否则调用 `HTTPUpstream.Do`；现有插件不是 OpenCode API Key 的 Bun 实现。
- `openai_gateway_handler.go` 使用 session hash、账号选择与并发槽位，处理可重试错误和 failover；不能旁路这些控制。`openai_gateway_response_handling.go` 处理 SSE、结束事件和 usage，读取输入、输出及缓存字段；`openai_gateway_usage.go` 的 `RecordUsage` 负责现有计费。现有 `OpenAIUsage` 不含 `billing_source`，需要明确扩展或单独持久化，不能假定已记录。成功但缺 usage 的路径会记录缺失，不能因此当成已知零使用量。
- `probe-free-chat.mjs` 默认 dry run；发送时只调用一次、消费完整响应、核对 nonce、`[DONE]`、错误和 tool calls，不支持代理切换或工具执行。这是诊断资产，不是可直接用于生产的网关。

## 方案比较与选择

| 方案 | 优点 | 代价与边界 |
| --- | --- | --- |
| A：可选 Bun transport worker + Go 现有网关 | Bun 与 Go 的职责分明；可继承选号、限流、usage 和响应处理；正常 Zen/Go 不受影响 | 要验证 Bun 代理及连接隔离；原生 body 透传只覆盖调用者本就提供兼容 body 的情况；转换会改变语义 |
| B：官方 `opencode serve` 桥接 | 由官方 agent 构造模型请求 | 是会话/agent 语义，不是完整 API 透传；历史、参数、工具、实时事件及真实 usage 需要另行证明，运行与隔离复杂 |
| C：继续 Go HTTP 并模仿 headers | 无额外服务 | 目前没有成功证据；即使成功也不能证明任意请求兼容 |

推荐 A，首期实现**明确标识的文本兼容转换** `text_compat_v2`，而非仅交付原生 body 诊断。管理员为 Zen 账号开启该 transport/profile，并配置专用分组、公开模型别名，调用方以正常 Chat 请求选择该别名即表示选择此受限能力；不要求额外 header。模型别名、管理界面和文档必须公开提示该能力会注入 system 和工具 schema、影响回答和 token，不能宣传为透明转发。系统不存在相应权限或定价配置时不得放量。

原生 body 透传仅作为附属诊断路径，调用方本身提供兼容 body 才可能复现基线；它不代替普通文本交付，也不自动开放工具。仅有 nonce 对照不足以证明任意用户内容可用，真实中文、多轮和自定义 system 均须通过受控验收后才能开放。


首期只允许管理端明确启用的专用 Zen 免费账号，现有 API Key 必填时填写公开值 `public`；最小 profile 可以不显式发送 Authorization。配置了真实上游私钥的账号保持普通 Zen/Go 路径；若强行选择此匿名 Bun profile，应校验失败，不能把真实付费身份静默降为匿名。内部 worker 的服务间认证与上游匿名身份分开。

## Profile 与请求契约

Profile 是不可变、受版本控制的文件，不接受下游传入任意模板。首选实验候选为 `deploy/opencode/balanced-text-profile.v2.json`，其 ID 和文件字节 SHA-256 见上文；v1 最小资产保留作回归，不能覆盖；启动时对实际文件字节校验 hash，运行时引用 `id@sha256`。它包含实验请求示例，必须由受控转换器用用户消息替换占位示例，不能直接将 nonce 模板用于生产。记录来源与许可、OpenCode 客户端版本、Bun 版本、适用 endpoint/model/protocol、headers、转换规则、能力和实验环境；更改任何字节生成新 ID/hash，不覆盖旧版。v2 标记 `experimental`，本轮有限集通过但生产出口、长流、代理与账务尚未验收，默认不开启。原有 `native-chat-template.v2.0.10.json` 仍为诊断基线，不得携带个人路径、用户内容或凭据。未来可将已发布 profile 放入 `deploy/opencode/profiles/`，迁移需保持原始字节及摘要。

首期仅允许服务端配置的 Console 或旧 Zen 两个确切 HTTPS Chat 路径和 `big-pickle`，不能使用任意自定义 URL、重定向或外部模型来自动启用。完整基线曾在两个路径成功；候选观测基于 Console 路径，旧 Zen 必须另行通过对应 profile 的验收才可启用。下游请求必须通过现有鉴权、模型允许列表和账号路由；Go 在确定请求实际为受支持的 Chat 形状之后选择 worker，Responses/Anthropic 转换后的请求只有经过独立等价验证才能加入。调用方每次须提交完整文本历史；缺少可信稳定 session ID 时，每次生成独立隔离 session，仍接受该历史，但不承诺跨请求 affinity。

建议内部协议：Go 调用仅私网/Unix socket 的 `POST /v1/transport/chat`，带版本化 JSON envelope：`request_id`、`profile_id`、`profile_sha256`、允许的目标枚举、`account_id`、代理配置引用及代次、隔离后的 session、绝对 deadline、受控上游 headers、已编码 body。凭据由 Go 在已认证连接中作为临时敏感字段发送，绝不进入 profile、日志或 URL；worker 不持久化。代理引用只能由受控配置注册表解析；注册表向 worker 安全下发代理地址及认证信息，更新附带代次，已在途请求维持原配置、后续请求使用新代次；下游不可注册代理。实际返回上游 status、白名单响应头与原始 body 流；内部响应有受信的来源标记，Go 先剥离任何同名上游头并区分 worker 错误与上游 HTTP 错误，不能将内部错误伪装成上游响应。worker 对请求体大小、并发、响应头大小与错误体长度设限；它不自动重试、不转换模型响应、不执行工具。使用 `GET /health/live` 检查进程、`GET /health/ready` 检查版本/profile/hash 和内部资源，不向模型发探针；生产运行时与发布测试锁定 Bun 1.4.2 和镜像 digest。

当调用方提供可信稳定会话 ID 时，使用服务端密钥 HMAC（带版本及长度分隔编码）派生已认证租户/下游 API key、上游账号和该 ID 的隔离存储键，以此查询下述生成的上游 session 值；同一隔离域跨轮稳定，不同租户或账号不碰撞。将该 session 值用于 profile 指定的 session/affinity 字段，不改变 scheduler 的粘性绑定语义。多实例共享密钥；密钥轮换明确标记新版本，旧会话按配置窗口保留映射或重新开始，不能无声声称稳定。没有 ID 无法判断请求是否属于同一会话：每次新建隔离 session，依靠客户端提交的完整历史维持文本上下文，不保证缓存或跨请求 affinity；不得由消息内容推测身份或用 session 绕过配额。


稳定 ID 的存储键可以使用带版本 HMAC，但**上游 `X-Session-Id` 的值不能直接使用任意 HMAC hex**。首次请求应按已验证的 OpenCode 形态分配 `ses_` + 12 位时间 hex + 14 位 base62 随机值，并以 HMAC 隔离键原子保存；后续复用该值。多实例用共享 Redis/持久化存储的原子创建避免竞态；建议空闲 TTL 24 小时，过期或密钥版本切换视为新会话并记录。没有可信 ID 时每个请求生成新合法值，仍接受自包含文本历史。该映射不会提供额度或权限。

## 语义、工具及多协议边界

`text_compat_v2` 将版本化平衡 profile system 插为首个 system 消息，合成的工作目录仅是逻辑兼容上下文，并不表示 worker 可访问真实文件；禁止暴露部署目录或将其用于文件操作。该模式随后原样按顺序保留调用者的 system 与 user/assistant 文本消息和完整历史，不压平；这明确改变指令上下文，不能保证 system 优先级或回答语义等价。只注入该 profile 的版本化最小 `read` 兼容 schema，绝不执行工具。v2 候选的 schema、system、headers 和文件 hash 已固定；有限配对与 held-out 结果支持继续受控验证，但不能作为已完成生产验收的 profile。

首期接受纯文本 Chat，`tools` 缺省或空数组均允许并规范化为无调用方工具；非空 `tools`、非 `none` 的 `tool_choice`、历史 tool 消息或 tool calls、多模态、Responses/Anthropic、未知或未验证参数均在发送前明确拒绝；不能静默丢字段。首期向上游发送 `tool_choice: none`；当前最终组合在本机完成联合实测，但尚须通过语义、部署环境及安全等完整准入矩阵。即使设置了 none，仍必须检测和拦截所有意外 tool-call delta。不能把注入工具回传为用户声明的工具，亦不做隐式覆盖/改名。原生诊断模式若未来支持工具，需独立验证工具名称、schema、增量参数、ID、结果与历史关联，禁止自动执行；未经验证保持关闭。

2026-10-30 补充（agent 模式与工具名门禁）：agent 模式现已支持调用方 `tools` 透传与 tool 结果续接，经真实上游隔离实验证实上游免费层会**检查请求声明的工具名集合**：非空 `tools` 中缺少官方占位名 `read` 即返回 429 `FreeUsageLimitError`（`tool_choice: none` 不豁免），含 `read` 则 200；3/3 模型复现。因此 worker 对非空调用方工具集合**合并占位 `read`**（描述声明禁止调用，schema 为 `{filePath}`），网关侧 `openCodeFreeInjectedTools` 与之保持同规则；注入调用在累积期允许（含前缀分片），finish 时从结果剔除并重排剩余调用，全部为注入调用时 `tool_calls` 降级为 `stop`，`none`/forced/required 语义不变，注入名绝不回传给客户端。`InjectedTools` 未配置时行为与原实现完全一致。详见 `deploy/opencode/COMPATIBILITY.md` 对应小节。

2026-10-31 补充（失败日志、代理证据分类与配额 429 处置）：首版“代理任意非 200 归 502”的限制已按证据细化——worker 有界读取错误体，带 `Retry-After`/`x-request-id` 或 JSON error envelope 的判定为上游响应并透传真实状态（网关因此能收到真实 429 与 `Retry-After`），无证据/读取失败/体超限仍保守 502；worker 与网关的全部失败路径输出结构化 WARN 日志（request_id/account/model/状态/脱敏片段）。上游 429 命中 `FreeUsageLimitError`/`Free usage limit reached` 时按 `Retry-After`（夹紧 1 分钟~24 小时，缺省 1 小时）写 `RateLimitResetAt` 冷却该实例并即时通知调度器；客户端响应未写出则返回 failover 错误由 handler 切换同分组其他实例，已写出（keepalive）则维持原 SSE 透传。管理端账号测试与网关共用该路径。详见 `deploy/opencode/COMPATIBILITY.md` 的 2026-10-31 小节。

首期 `n` 缺省或为 1，其他值发送前拒绝；旧式 `functions` / `function_call` 请求字段明确列为未支持，不静默转换。上游意外返回非零 choice index 属协议不匹配，必须检查所有 choices 后拒绝，不能只选择 choice 0 而漏过其他 choice 的工具事件。诊断器只取 choice 0 是研究聚合规则，不代表生产验收可以跳过其他 choice。

温度参数采用明确契约：调用方未提供时发送 `temperature: 0`，显式 0 原样保留；任何非 0 值在发起上游请求前返回 `unsupported_parameter`，绝不静默覆盖。0 仅为本轮观察有效的请求条件，不保证确定性。

首期同时支持下游流式及非流式文本：上游统一使用经过验证的 SSE 模式并请求 usage；非流式在 Go 中有界聚合，完整保留文本、finish reason、错误和真实 usage，工具事件同样先拦截。超过上限或异常结束返回明确错误，不能输出部分结果伪装成功。普通文本下游与上游 stream 参数、usage 尾帧、错误的准确转换都须纳入验收。

## 账号、代理和安全

worker 只由网关访问：优先 Unix socket 文件权限，跨容器时私网加双向认证及 TLS；部署中不发布公网端口。仅 Go 的受控账号配置能选择 profile、目标及代理，客户端 headers 不得控制目标、代理、认证或 Host。目标以枚举硬映射到 `opencode.ai` 两个固定 HTTPS 路径，禁止 redirects；限制 DNS/连接目标、证书校验及出站网络，防 SSRF/DNS 重绑定。清除 hop-by-hop 和未授权头，校验长度与值；不记录 API key、代理凭据、提示词、工具参数、原始用户内容及完整 session。

账号绑定代理必须保持现有语义：不因 worker 而直连、换代理或共享错误出口。**首期固定 Bun 1.4.2 每个上游 `fetch` 均设置 `keepalive: false`，不复用连接**；账号凭据和代理参数均作为不可变的逐请求配置传入，不通过切换全局 `process.env` 配置代理。启动环境必须清除未受控代理环境变量；直连及代理选择在 1.4.2 上实测确认，不能被进程环境隐式改写。代理身份/代次、目标及 profile 在请求建立时固定，配置变更只影响后续请求，绝不让跨账号并发互相覆盖。需确认实际使用的 1.4.2 代理配置接口在并发时有此语义，不能依赖当前版 API 尚未在 1.4.2 证实的特性；若不能逐请求安全指定某个代理类型，则明确拒绝该类型，禁止静默直连。每次新建连接增加 TCP/TLS 建连成本，需计入并发和延迟容量。

本机离线 HTTP mock 在 Bun 1.4.2 下使用 `keepalive: false` 观察两次请求源端口不同，支持上述实现选择，但不能由此推断所有 TLS、代理类型或连接状态均已验证。上线前须对直接和每种受支持代理运行并发 mock、真实出口/凭证隔离验收，本机 v1 的 seq85 已在真实上游验证其联合候选的该选项，实际部署出口仍须经授权重验；失败则保持关闭。Bun 代理类型、认证、DNS、IPv4/IPv6 均需单独验证，不宣称 1.4.2 支持全部 proxy schemes。

版本差异：本机 1.4.2 实测 `typeof Bun.FetchSession === "undefined"`；官方 [Bun 1.4.3 发布说明](https://bun.sh/blog/bun-v1.4.3) 将 `Bun.FetchSession` 列为新增，提供隔离连接池。当前版 [Bun fetch 文档](https://bun.com/docs/runtime/networking/fetch) 描述 `keepalive: false` 与 FetchSession，但它不是 1.4.2 版本文档，不能证明旧版具备后者。后续若需要复用，可选按 egress/代理/profile 隔离独立 worker 进程，或升级到支持 FetchSession 的版本并重新通过完整 profile、代理和真实上游矩阵；均不属于首期默认。Node/curl 与 Bun 对照差异说明不能认为运行时升级不影响结果。

## 流、取消、超时与故障

worker 以流式方式把上游字节按需转发，不能先缓冲完整 SSE；响应头和首事件及时传回 Go，有限缓冲、慢客户端背压传递至上游读取。Go 保持现有 SSE 解析和协议响应职责，验证分片、跨 chunk 的 UTF-8/SSE、心跳、结束事件和 usage 尾帧。下游断连、Go 取消、deadline、worker 停机时关闭 reader 并 abort 上游、释放连接和账号槽位；现有某些转发路径使用 detached context，接入点必须显式桥接下游取消，不能假定现有 context 自动传播；取消只能保证本地停止读取和发出取消信号，不能保证上游未完成或未计费。分别设置连接/响应头、首输出、流空闲和总 deadline，纳入请求总预算。

worker 零重试；Bun 路径一旦 dispatch，Go 对任何结果都不自动重放、不换号，包括首输出前失败或 429；原因是无法可靠排除已经执行和计费。dispatch 前若本地校验或连接建立已确定未发出请求，可返回错误，由调用方自行决定是否重新请求。不得用 profile 失败自动切回普通 HTTP 或静默替换 profile，因为语义和结果可能不同。

`openai_gateway_chat_completions_raw.go` 的 raw Chat 流式逻辑目前会逐行写出并支持首输出前 failover；Bun 不能直接复用未经修改的行为。必须在写出前按完整 SSE 事件增量解析所有 choice，遇任意 `tool_calls`/旧式 `function_call` delta 或工具结束原因先拦截，不泄漏工具名称、ID 或参数片段；一帧同时含文本与工具时整帧拒绝。未提交响应时返回明确兼容能力错误；已输出文本时写明确 SSE 错误并终止，不伪造 `[DONE]`。读取畸形、超限或不能判定内容也先拒绝；无工具事件才转发，缓冲必须有界。不能复用会在发出后重试的 handler failover，针对 dispatch 状态增加不可重放标记并覆盖每条 Chat 入口。

分类记录：内部校验/认证失败、代理/DNS/TLS/连接失败、上游 401/403、429（保留原始安全错误码和 `Retry-After`，标为原因未确定）、5xx、首输出/流空闲超时、上游 SSE 错误、取消、usage 缺失；按现有错误响应与账号调度策略映射，但不要把所有 429 写成日配额耗尽或永久禁用账号。限流处理与冷却只在有明确语义的上游信号或现有通用策略下生效，不能通过轮换账号/代理规避限制。

## 用量与计费

保留原始真实 usage 的输入、输出、缓存读取/写入及上游提供的 `billing_source`；在 Go 的解析、`OpenAIForwardResult`、usage log/存储和显示链中补齐来源及完整性标记。先核对该上游缓存 token 是否包含在 prompt 总量中及现有 `RecordUsage` 的扣减规则，避免重复计入；用真实 fixture 验证缓存读写、显式零值、尾帧及不同格式。注入的 system/tools token 是实际输入的一部分，不可人为扣除；上游 `free` 表示上游报告的来源，不自动决定本项目对用户的价格，项目价格仍按已配置计费政策。

首期固定以下闭环，不建设泛化金融预留系统：

1. dispatch 前在数据库持久化 `usage_attempt`（`request_id` 唯一、账号/API key、profile hash、状态 `pending`）；写入失败不得发送模型请求。ID 为网关生成的稳定内部 ID，与上游 request ID 分开。既有用户/账号并发、RPM、鉴权及配额检查仍执行，不能因 unknown 不扣费就放开限制。
2. 仅当正常结束且真实 usage 完整可信，将完整 usage 和结算所需上下文以事务写入本地幂等 outbox，状态转 `ready`；消费者以同一 ID 调用既有 `RecordUsage`，完成后标记 `settled`。异步入账失败只重试本地 outbox，绝不重发模型。真实 usage 包含输入/输出及实际提供的缓存明细；缺字段与明确零值分开判断。
3. 断流、取消、缺 usage 或不可信/异常结束转 `unknown`；首期不扣用户余额，由平台承担，不估算、不事后未经通知追扣。保留脱敏 ID、状态和审计信息，不能把 unknown 标成真实零使用量。若 outbox 已持久化可靠 usage 即使进程重启也照常幂等结算。
4. 启动恢复该实例遗留或租约过期的 `pending` 为 `unknown`，不得触碰其他存活实例的请求，也不重发上游；恢复 `ready` 时只重试 outbox。终态变更使用事务/CAS，防恢复任务与原请求并发覆盖。

现有 `usage_billing_repo.go` 的 `Apply` 在事务中以 `(request_id, api_key_id)` 去重并核对指纹；实现必须验证 `RecordUsage` 使用的正是本内部 ID、真实入账路径和 outbox 重试具有一致指纹，且 usage log、计费结果的重复写入也满足幂等。新增 `usage_attempt.request_id` 和 outbox 唯一约束及状态/入队事务；若现有去重无法覆盖某一步，则补充数据库唯一约束和事务，不能仅依赖内存锁或口头保证。覆盖提交前崩溃、扣款后标记前崩溃、多实例重复消费及恢复竞争测试。

## 落点、配置与阶段

以下为建议文件，不表示当前存在：

- `backend/internal/service/opencode_bun_transport.go`：限定账号/模型/协议/profile 的选路、内部 envelope、流式响应和取消；在 `openai_gateway_chat_completions_raw.go` 的原生 Chat 路径构造 `text_compat_v2` 请求，使用 `doOpenAIUpstream` 限定选择 Bun transport；拦截 raw SSE 并禁用 dispatch 后 failover，覆盖所有实际命中的 Chat 入口，不能意外截获 OAuth 插件、其他协议或普通账号。先用路由测试锁定实际入口。
- `backend/internal/service/opencode_bun_profile.go` 与测试：profile 校验、文本转换、能力判定、session 隔离；`usage_attempt`/outbox 的迁移、仓储及恢复消费者承担持久化与结算；配置/账号管理遵循现有项目结构并添加验证和脱敏。usage 补充在 `openai_gateway_response_handling.go`、`openai_gateway_service.go`、`openai_gateway_usage.go` 与对应存储/迁移中完成。
- `deploy/opencode/worker/`：Bun worker、锁版本容器构建、profile 读取及单元/集成测试；`deploy/opencode/profiles/` 保存清理后的不可变 profile。部署文件以可选服务加入现有 Compose，不改变默认启动链。

建议配置项：全局 `opencode_bun.enabled=false`、`worker_endpoint`、内部认证密钥引用、`profile_directory`、连接/首输出/空闲/总超时、最大并发/体积；账号显式配置 `transport=default|bun`、`profile=id@sha256` 和 `text_compat_v2`，另限定允许模型/专用别名；原生透传仅诊断，须单独标识。敏感配置只使用现有 secret 注入方式。默认禁用或账号未选择 Bun 时保持现有链；账号已明确选择 Bun 后，模型、协议或 profile 能力不匹配，以及 worker 不可用时均明确失败，不静默回退。字段名称在实现中须与现有配置机制对齐，新增账号字段需支持编辑、持久化和脱敏，不能覆盖旧账号协议规则。

阶段：① 保留 v1 失败回归，v2 配对和预注册 held-out 有限通过，本机发布资产 proof 已完成，目标部署出口与生产验收未完成；② 实施离线 mock、内部传输、认证、隔离和错误/流测试；③ 打通真实 usage、幂等对账及能力拒绝；④ 受控预生产验证目标出口、长流、代理和账务，并在目标部署环境重新验证同 hash profile；⑤ 全矩阵达到预先确定的严格语义与运行指标后才考虑账号/模型灰度。v2 具备离线集成和受控预生产验证基础，默认不开启，不以有限样本替代生产验收。更多协议和工具各自独立评审、验证后启用。

观测使用脱敏 request ID、账号 ID、profile hash、模型、代理 ID、错误分类、首 token 延迟、流时长、取消/截断、usage 完整率及成本；不按用户内容打日志。灰度按明确账号和模型白名单启用，比较成功率、429、延迟、缺失 usage、重复计费和代理泄漏；上线前确定基线、样本量及回滚阈值。出现泄漏、跨租户混流、错误计费或取消失效立即关闭；回滚关闭兼容别名/账号功能，新请求明确返回不可用，普通 Zen/Go 账号继续既有默认路径；已开始流自然结束或依策略取消，保留 unknown/outbox 审计数据。不可将回滚解释为已选 Bun 请求的运行时静默降级。

## 验收矩阵

| 场景 | 可验证通过条件 |
| --- | --- |
| 路由和开关 | 默认关闭；仅明确账号、模型、Chat/profile 命中；Zen/Go 正常链、历史自定义规则、其他协议及 OAuth 插件回归不变 |
| profile | v2 文件字节 hash 固定；hash/版本错误启动或请求失败；旧 profile 不原地修改。v1 失败回归保留；v2 有限集通过但未完成生产验收，默认关闭 |
| 普通文本 | 兼容别名无额外 header；合成及真实中文、单轮/多轮、自定义 system 的重复样本保持消息顺序，验证严格预期输出、200、`[DONE]`、真实 usage；v1 内部误抄为失败回归；v2 的 8 个 held-out 通过但不等于生产验收；句点歧义样本不得直接判违令。记录 profile/hash、时间及出口，不以 nonce 单次成功替代 |
| 语义和工具 | 缺省或显式 temperature 0 发送 0，非 0 明确拒绝；注入 system 在首位且用户消息顺序不变；tools 缺省/空数组放行并规范化；非空 tools、非 none tool_choice、未知字段、媒体和非 Chat 协议拒绝；任意 tool-call delta（包括与文本同帧及分片）写出前拦截，已输出文本后发 SSE 错误且无假 DONE；永不执行工具 |
| session | 稳定 ID 同租户同账号跨轮相同，不同租户/账号不同；无 ID 每次新 session 且完整历史仍可用、不声称 affinity；多实例、过期及 key 轮换可预测 |
| 代理和安全 | 两账号不同代理并发请求到达各自 mock 出口，每次禁复用且无直连/串用；凭证和代理变更不影响在途请求，后续使用新代次；私网鉴权、固定目标、DNS/redirect/SSRF 和日志脱敏测试通过 |
| 流和资源 | 长流首 token 实时到达；分片 UTF-8、SSE、心跳、尾帧、慢客户端背压正确；断连、取消、超时和 worker 重启释放 reader、连接及槽位 |
| 重试与错误 | 429、401/403、5xx、代理故障、首 token 前/后断流、部分 tool call 均分类正确；任何 dispatch 后不重放、不换号，含 raw Chat 首输出前 failover |
| 用量 | 真实 input/output/cache/billing_source 与 fixture 一致，注入 token 全计；先持久化 pending；完整 usage 的 outbox 重试至多结算一次；断流/取消/缺失变 unknown、用户不扣款；并发恢复、崩溃和重复事件不重发模型或双重扣费 |
| 部署与回滚 | Bun 1.4.2 及镜像 digest 锁定，本机 v1 seq85 已验证 `keepalive: false`；实际部署出口的联合 profile 须重验通过；live/ready 不发模型请求；服务关闭、不可达、profile 无效行为符合开关语义；关闭灰度后兼容别名明确不可用、普通路径可用且 unknown/outbox 记录保留 |

此矩阵需区分本地可重复自动化、受控真实上游验收和生产观测；外部上游结果具有时效性，重测必须获得授权并控制请求量。
