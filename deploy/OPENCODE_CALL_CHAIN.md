# OpenCode 模型调用链与 `big-pickle` 实测记录

记录时间：2026-10-27（Asia/Shanghai）。本文区分**可读源码**、**本地模拟捕获**和**真实上游重放**。请求内容均为合成 nonce，不使用真实用户消息或真实 API 密钥。

本文保留源码链及早期基线。后续差异化消减已得到 61–63 prompt tokens 的实验组合和明确的输出精度限制，完整方法、控制失效、最小 profile 与两份接入设计见 [缩减实验报告](OPENCODE_MINIMIZATION.md)。不能将本文的“完整请求成功”解释为之后每个字段都必需。

最新 [质量对照报告](OPENCODE_QUALITY_COMPARISON.md) 对完整/极简/平衡模板保留原始 SSE 并独立重解析：本轮三次内部错字已定位到上游 content，平衡 v2 为当前实验首选；不能借此追溯缺少 raw 的旧记录或断言隐藏模型/服务端根因。

## 结论和证据边界

- 真实服务器上的独立 Bun `fetch` 重放官方 2.0.10 客户端实际生成的完整请求，得到 HTTP 200、所要求的合成 nonce、SSE `[DONE]` 和 usage 中的 `billing_source: free`。将目标从 `/zen/v1/chat/completions` 换为 `/inference/openai/v1/chat/completions`，也观察到成功。后一路径的结果记录在 [replay-controls.jsonl](../.cache/client-detection-evidence/replay-controls.jsonl)。随后替换本机路径、session/project/trace 和 nonce 的独立诊断脚本再次得到真实 200。这是特定时间、出口和请求样本下的观测，不保证以后或其他环境仍然成功。
- 保持完整捕获的请求头而使用最小 user-only body，观察到 HTTP 429 `FreeUsageLimitError`；保留 system 和 user 但删除 tools，或保留 tools 和 user 但删除 system，也各观察到 429。后两项记录在 [replay-body-controls.jsonl](../.cache/client-detection-evidence/replay-body-controls.jsonl)。这些对照没有确定线上服务端的完整判定式，也不能排除并发、时间、状态等因素。
- 这些观测反驳了两个过强的结论：**“只伪装 UA/session 就足够”**，以及 **“每个这种 429 都能直接证明出口 IP 的真实免费额度已经用尽”**。它们没有证明系统提示或某个工具是必要条件，也没有排除 TLS 指纹或其他检查。完整请求成功只证明该次请求在当时被接受。
- 本地 mock 捕获和真实重放是两件事：mock 是官方客户端向本机代理发请求，由代理返回合成 SSE，证明构造及消费链路；真实重放才是独立 Bun 向真实服务发送捕获请求并得到 200。两者不能混称。

## 版本与证据

| 对象 | 版本或提交 | 能说明什么 |
| --- | --- | --- |
| 本地只读客户端源码 | 2.1.4，`dae4077a96243257ea0a2086068504b791629f10` | 下面的 prompt、runner、协议、错误映射按这个固定提交分析；不是 2.0.10 wire 的逐字说明。 |
| 真实客户端抓包 | npm 官方 `@opencode/cli-darwin-arm64@2.0.10` 原生程序 | 实测 CLI pipeline 生成的 URL、头和 body。npm tarball SHA-512 与发布元数据匹配，见 [integrity.json](../.cache/opencode-wire-capture/package/integrity.json)；未运行安装脚本。 |
| 公开旧服务端参考 | GitHub tag `v2.0.10` 指向 `b8cedc1a7a5e2916bbb65dc1d4b620729c261638` | 只能说明该公开旧提交的实现；不能把它当作当前线上私有后端的精确代码或配置。 |

固定提交的 GitHub 客户端链接均指向 `anomalyco/opencode` 的 `dae4077a96243257ea0a2086068504b791629f10`。以下源码引用均使用该固定提交的公开链接。

## 2.1.4 客户端：从 prompt 到 HTTP

1. **接收并入队。** 服务端 session handler 调用 `session.prompt`（[本机源码](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/server/src/handlers/session.ts#L290)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/server/src/handlers/session.ts#L290)）。`SessionPrompt.prepare` 处理文字、文件和 skill 等内容并构造 user inbox item，session 服务再 admit 并唤醒 execution（[prompt.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/prompt.ts#L34)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/prompt.ts#L34)；[session.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/session.ts#L155)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/session.ts#L155)）。
2. **Runner 准备上下文。** execution 通过 runner drain 驱动执行（[execution.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/execution.ts#L84)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/execution.ts#L84)）。runner 推进入队消息、首轮异步生成标题、加载上下文，然后执行模型步骤；必要时处理 compaction（[runner/llm.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/runner/llm.ts#L161)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/runner/llm.ts#L161)）。上下文按 agent、权限和配置组合工具、MCP、skills、指令及历史，实际内容随会话和配置变化（[context.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/context.ts#L126)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/context.ts#L126)）。基准 transcript 使用 agent 自定义 system 或系统提示及历史消息（[model-request.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/model-request.ts#L92)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/model-request.ts#L92)）。
3. **解析模型与免费凭据。** runner 优先解析明确选择的 session model，否则根据 agent 偏好或默认值选择（[runner/model.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/runner/model.ts#L77)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/runner/model.ts#L77)）。resolver 合并 provider/model 设置和凭据，选择对应 provider package 并创建 LanguageModel（[model-resolver.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/model-resolver.ts#L380)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/model-resolver.ts#L380)）。OpenCode provider 在没有配置真实 key 或连接、且未禁用免费模型时注入 `public`，并对无真实 key 的模型列表作免费模型过滤（[opencode.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/plugin/provider/opencode.ts#L331)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/plugin/provider/opencode.ts#L331)）。`public` 是公开占位凭据，不是私有 API key。
4. **选择 API 与地址。** 2.1.4 内置模型目录中 `opencode/big-pickle` 的 chat-completions base URL 是 `https://opencode.ai/inference/openai/v1`，SDK 是 `@opencode/ai/providers/openai-compatible`，模型为零价、上下文 200,000、输出上限 32,000（[本地 snapshot](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/models-dev/snapshot.txt#L1)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/models-dev/snapshot.txt#L1)）。配置、模型目录或 hook 可以改变实际值。2.0.10 的本次真实抓包地址则是 `/zen/v1/chat/completions`，不要把两个版本的默认地址混同。
5. **构造请求和头。** `SessionModelRequest.prepare` 组合 system、messages、工具定义、生成选项、缓存和 affinity，并设置 `x-opencode-session-id`、`x-session-affinity`、`X-Session-Id`、`User-Agent`、`x-opencode-project`、`x-opencode-session`、`x-opencode-client`，子会话还可能带 parent 头（[model-request.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/model-request.ts#L230)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/model-request.ts#L230)）。UA 格式由 app 信息生成（[app.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/app.ts#L24)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/app.ts#L24)）。2.1.4 的 `model.request`、`http.request` 和 `http.response` hooks 可以改写 URL、头或 HTTP 请求/响应，因而源码默认值不等于任何安装的最终 wire（[model-request.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/model-request.ts#L303)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/model-request.ts#L303)）。
6. **协议编码与发送。** OpenAI-compatible provider 设置 bearer auth 和 endpoint，chat 路由追加 `/chat/completions`（[openai-compatible.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/providers/openai-compatible.ts#L28)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/providers/openai-compatible.ts#L28)；[协议路由](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/protocols/openai-compatible-chat.ts#L14)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/protocols/openai-compatible-chat.ts#L14)；[endpoint 拼接](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/endpoint.ts#L54)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/endpoint.ts#L54)；[bearer auth](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/auth.ts#L43)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/auth.ts#L43)）。OpenAI chat 编码器将模型、消息和非空 tools 编成 JSON，流式调用包含 `stream: true`，默认请求 usage（[openai-chat.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/protocols/openai-chat.ts#L796)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/protocols/openai-chat.ts#L796)）。transport 构造 JSON body、合并 HTTP 配置并执行请求（[transport/http.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/transport/http.ts#L35)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/transport/http.ts#L35)；[executor.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/executor.ts#L229)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/executor.ts#L229)）。

## 2.1.4 客户端：SSE、错误和工具

- 传输层把 UTF-8 bytes 分帧为 SSE；`[DONE]` 是终止标记（[framing.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/framing.ts#L38)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/framing.ts#L38)）。chat 协议解析 choices、content delta、finish reason、usage 和 tool calls（[openai-chat.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/protocols/openai-chat.ts#L1034)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/protocols/openai-chat.ts#L1034)）。
- HTTP 状态不小于 400 时读取响应体、状态、头和 retry-after 并分类（[executor.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/executor.ts#L81)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/route/executor.ts#L81)；[provider-error.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/provider-error.ts#L125)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/ai/src/provider-error.ts#L125)）。当错误分类为 `QuotaExceeded` 且 body 包含 `FreeUsageLimitError`，session 映射成 `provider.free-limit`；`RateLimit` 则映射为 `provider.rate-limit`（[to-session-error.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/to-session-error.ts#L28)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/to-session-error.ts#L28)）。这只是客户端对响应的分类，不证明后端具体为何拒绝。
- 正常 runner 遇到非 provider 执行的 tool call，会调用已准备好的工具执行器（[runner/step.ts](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/runner/step.ts#L103)，[固定提交](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/runner/step.ts#L103)）。完整诊断 fixture 含 shell、write、edit、execute、subagent 等工具 schema；重放诊断应只读取并验证 SSE，拒绝执行所有返回的 `tool_calls`，不能把该 fixture 直接接到自动执行工具的 agent。

## 官方 2.0.10 原生客户端的模拟抓包

隔离环境运行官方客户端的 session/model pipeline，测试消息是合成的 `Reply exactly with NONCE_CAPTURE_7319.`；客户端实际编码的 user content 带一层字面双引号。独立本地 HTTPS CONNECT 代理仅接受 `opencode.ai:443`，记录请求，返回 `CAPTURED (mock response)`、`finish_reason: stop` 和 `[DONE]`，不转发任何推理请求。CLI 收到模拟文本并以退出码 0 结束。进程以空继承环境和项目内 `OPENCODE_TEST_HOME`、XDG、TMPDIR 运行；sandbox 限制写入该项目内诊断目录并将出站网络限制在 localhost，关闭项目配置与模型目录网络更新。

- 第一个请求是标题生成：[capture-1.json](../.cache/opencode-wire-capture/capture-1.json)，原始 body 2,393 字节。
- 第二个是主模型请求：[capture-2.json](../.cache/opencode-wire-capture/capture-2.json)，[原始 body](../.cache/opencode-wire-capture/capture-2.body.json) 25,872 字节。顶层键为 `model`、`messages`、`tools`、`stream`、`stream_options`；`model` 是 `big-pickle`，`stream` 为 true，`stream_options.include_usage` 为 true。system 约 12,174 字符，含通用 harness 指令、隔离路径、时间和通用工具目录；user 为合成输入；12 项工具定义分别为 `edit`、`glob`、`grep`、`question`、`read`、`shell`、`skill`、`subagent`、`webfetch`、`websearch`、`write`、`execute`。
- 捕获头包括 `authorization: Bearer public`、`content-type: application/json`、`user-agent: opencode/latest/2.0.10/cli`、`b3`、`traceparent`、`x-opencode-client`、`x-opencode-project`、`x-opencode-session`、`x-session-affinity`、`x-session-id`，以及连接、accept、host、编码和长度头；完整值见捕获文件。trace、session 和 project 值是此次生成的动态值，不意味着服务端必需验证它们。抓包中没有 2.1.4 源码新增构造的 `x-opencode-session-id`。
- 按本次样本检查，认证是公开值，system 和工具定义不含项目源文件内容；捕获仍含本机隔离路径、临时标识与时间。对外发布前应审阅并替换；任何替换都会改变测试样本，不能假定行为不变。

离线复现 mock（在三个终端内依次运行；它不会转发真实上游）：

```sh
node ../.cache/opencode-wire-capture/proxy.js
../.cache/opencode-wire-capture/run.sh serve --hostname 127.0.0.1 --port 32149
../.cache/opencode-wire-capture/run.sh run --server http://127.0.0.1:32149 --model opencode/big-pickle --format json 'Reply exactly with NONCE_CAPTURE_7319.'
```

`run.sh` 和 [sandbox.sb](../.cache/opencode-wire-capture/sandbox.sb) 是本机诊断脚本，依赖 macOS 的 `sandbox-exec`、该目录下已校验的 Darwin arm64 二进制、证书和本地端口；它不是跨平台部署方案。`OPENCODE_SERVER_PASSWORD` 是只用于本地 CLI 与本地 serve 的合成值，不用于上游认证。代理本地 CA 私钥只用于这个 mock，应留在诊断目录。

## 真实服务重放与控制实验

| 请求 | 观察 | 边界 |
| --- | --- | --- |
| 完整 2.0.10 capture-2 body 和 headers，Zen URL，服务器上独立 Bun | HTTP 200，合成 nonce 正确，`[DONE]`，`billing_source: free` | 补充复测记录见 [bun-exact-sockets.jsonl](../.cache/client-detection-evidence/bun-exact-sockets.jsonl)，包含进程实际 socket。 |
| 同一完整请求，URL 换成 `/inference/openai/v1/chat/completions` | HTTP 200，合成 nonce、`[DONE]`，`billing_source: free` | 可查看上述 `replay-controls.jsonl`；不能推断两个路由永远等价。 |
| 捕获头不变，body 换成最小 user-only | 429 `FreeUsageLimitError` | 这个样本不能建立某一个缺失字段的因果关系。 |
| 完整其他条件下删除 tools，保留 system/user | 429 `FreeUsageLimitError` | 观察值，不证明 tools 必为认证条件。 |
| 完整其他条件下删除 system，保留 tools/user | 429 `FreeUsageLimitError` | 观察值，不证明 system 必为认证条件。 |

对照请求与成功请求不是完全同时发生：当前没有线上服务端版本、流量规则、配额状态和其全部判定输入的直接证据。包括仅有 HTTP 错误文案、单次请求耗时或 `Retry-After`，都不足以确定唯一根因。相同 headers 的 body 对照反例足以否定“单靠复制 UA/session 必然成功”，但不能排除请求头在别的情形起作用。Bun 重放成功同样不能证明不存在 TLS/客户端指纹或其他校验。

### 独立脚本补充对照

多个独立脚本分别构造 body、headers 和运行时对照，调用错开，记录状态、错误、用量与连接信息。没有自动重试或轮换出口，也未修改服务器文件、服务和配置。

| 对照 | 结果 | 证据 |
| --- | --- | --- |
| Bun 1.4.2，原始完整 body 只替换 nonce 后四位，保留 user 字面引号 | 200，精确 `NONCE_CAPTURE_8542`，`[DONE]`，`billing_source: free` | [body 对照](../.cache/client-detection-evidence/body/compare_system_results.jsonl) |
| 同一 Bun/full headers/12 tools，system 缩为官方首句 | 429 `FreeUsageLimitError`，`Retry-After: 74005` | 同上 |
| Bun/full body，headers 仅认证、Content-Type 和官方 UA | 429 `FreeUsageLimitError` | [headers 对照](../.cache/client-detection-evidence/headers/bun-minimal-headers.jsonl) |
| 同上，把 UA 换成诊断 UA | 429 `FreeUsageLimitError` | 同上；不能据这两个失败判断 UA 是否有作用。 |
| Node 22.23.1，原始 URL、全部 headers、原始 body 逐字不变 | 429 `FreeUsageLimitError`，166 ms | [精确输入对照](../.cache/client-detection-evidence/headers/node-exact-wire.json) |
| Node `https.request`，同一原始输入，固定与 Bun 相同远端 IPv6 `2606:4700:78::90:0:143` | 429，同一源 IPv6 `2607:5300:60:7954::1`，HTTP/1.1，TLS 证书验证成功 | [固定远端对照](../.cache/client-detection-evidence/headers/node-https-curl-http2.jsonl) |
| curl 7.81，相同 body/headers 和固定远端，提供 HTTP/2 ALPN | 429；服务端实际选择 HTTP/1.1 | 同上；没有建立 HTTP/2，不能当成 HTTP/2 成败实验。 |
| 新诊断模板，替换本机路径，并新建 session、project、trace 和 nonce，由独立 Bun 1.4.2 发送 Console URL | 200，`NONCE_CAPTURE_6368` 精确匹配，1.981 秒，`[DONE]`；input prompt 5300、completion 39，免费计费 | [脚本复现结果](../.cache/client-detection-evidence/sanitized-probe-result.json) |
| 同一项目脚本在本机独立安装的 Bun 1.4.2 下运行；不使用 OpenCode 可执行文件或 serve | 200，`NONCE_CAPTURE_8648` 精确匹配，1.733 秒，`[DONE]`；prompt 6385、completion 23，免费计费 | [本机独立运行结果](../.cache/client-detection-evidence/local-standalone-bun-result.json) |

Node 最初的两条变体同时去掉了 user 的字面引号，未作为严格运行时对照采用；后续逐字原体和固定远端实验才用于记录 Node 与 Bun 的观察差异。原始 Bun 的进程 socket 快照确认使用 IPv6，并非只凭 DNS 推断。由此不能用这次样本的源 IP 地址族或远端地址不同解释全部结果。

**可确认的充分组合是本次 Bun 1.4.2 加完整原生请求形态；未确认的是线上内部究竟检查 TLS、HTTP 编码、头顺序、某个头、某段 system 或工具列表中的哪部分。** 所有被裁减的因素中可能有冗余，不应把完整保留当成已证明每项必要。也没有证据证明当前服务端只做某两项校验。公开旧服务端代码不包含当前线上全部配置和逻辑，因此不能拿旧源码补足这项未知。

可执行方案和命令见 [接入说明](../deploy/OPENCODE.md)，实现为 [probe-free-chat.mjs](../deploy/opencode/probe-free-chat.mjs)。它只发送独立 HTTP 请求，不运行官方 agent、serve 或返回的工具调用。本机调用使用 `bun`，进一步证明成功无需 OpenCode 可执行文件；服务器试验为节省安装操作复用了二进制内嵌的同版本 Bun。相同模板两次观察到约 5.3k/6.4k prompt tokens，不能当成保持任意下游请求语义的透明转发器；本轮未把它注入 Go 网关。

## 旧版公开服务端参考：不等于当前线上实现

在公开的 `b8cedc1a7a5e2916bbb65dc1d4b620729c261638` 中，Zen handler 从 `x-real-ip` 取得 IP（对 IPv6 作截取）、将 `public` key 视为无私钥，读取 session、client、UA 等用于 metrics，并根据模型的 `allowAnonymous` 选择 IP 或 key limiter（[固定旧版 handler](https://github.com/anomalyco/opencode/blob/b8cedc1a7a5e2916bbb65dc1d4b620729c261638/packages/console/app/src/routes/zen/util/handler.ts#L98-L130)）。这段读取 metrics 的代码本身不证明头决定当前线上请求资格。

该旧版 IP limiter 中，原本的 `checkHeaders` 校验代码被注释，`headersExist` 被固定为 true；之后依据限制配置和 Redis 计数判断是否抛出 `FreeUsageLimitError`，并跟踪请求（[固定旧版 ipRateLimiter.ts](https://github.com/anomalyco/opencode/blob/b8cedc1a7a5e2916bbb65dc1d4b620729c261638/packages/console/app/src/routes/zen/util/ipRateLimiter.ts#L8-L47)）。相应错误在旧版 handler 映射为 HTTP 429，并可包含 `retry-after`（[固定旧版 handler 错误分支](https://github.com/anomalyco/opencode/blob/b8cedc1a7a5e2916bbb65dc1d4b620729c261638/packages/console/app/src/routes/zen/util/handler.ts#L505-L530)）。旧版模型的 `allowAnonymous`、`rateLimit` 等属于运行时模型数据，模型配置来自 `Resource.ZEN_MODELS1` 至 `ZEN_MODELS30`（[旧版 model.ts](https://github.com/anomalyco/opencode/blob/b8cedc1a7a5e2916bbb65dc1d4b620729c261638/packages/console/core/src/model.ts#L22-L33)、[资源加载](https://github.com/anomalyco/opencode/blob/b8cedc1a7a5e2916bbb65dc1d4b620729c261638/packages/console/core/src/model.ts#L78-L111)）；免费限制来自 `Resource.ZEN_LIMITS`（[旧版 subscription.ts](https://github.com/anomalyco/opencode/blob/b8cedc1a7a5e2916bbb65dc1d4b620729c261638/packages/console/core/src/subscription.ts#L44-L50)）。这些运行时值及当前线上代码并未由公开源码确定，故不能据此断言当前服务端的 IP 配额、客户端检查或路由内部逻辑。
