# OpenCode 接入与免费模型验证

核查日期：2026-10-27。官方源码基准：`anomalyco/opencode` 提交 `dae4077a96243257ea0a2086068504b791629f10`。

后续已完成 [最小组合实验](OPENCODE_MINIMIZATION.md) 和 [完整/极简/平衡模板质量对照](OPENCODE_QUALITY_COMPARISON.md)。65 字节 system 的 v1 虽能低 token 调用，但有上游原始流中的误抄，不再作为首选。当前推荐的实验候选是 289 字节 system 的 B/v2 + 请求 temperature 0：14 次研究请求严格匹配，另有一次发布脚本复验成功；小样本不等于生产稳定性。两份 [本项目设计](OPENCODE_BUN_UPSTREAM_DESIGN.md) 与 [OmniRoute 设计](OMNIROUTE_FREE_MODELS_DESIGN.md) 均已更新，仍未实施或部署。

## 已确认的能力与边界

本项目内置 OpenCode 平台，账号模式仍为 `zen` 和 `go`。Zen 新建账号默认使用 Console 推理接口；显式保存的旧 Zen 地址、自定义地址及 Go 模式保留原有语义。

| 用途 | 新版 Zen / Console 地址 |
| --- | --- |
| Chat Completions | `https://opencode.ai/inference/openai/v1/chat/completions` |
| Responses | `https://opencode.ai/inference/openai/v1/responses` |
| Anthropic Messages | `https://opencode.ai/inference/anthropic/v1/messages` |
| 模型目录 | `https://opencode.ai/inference/v1/models` |

按模型选择协议，不根据 `free` 后缀统一选择 Chat Completions。Google 和 SystemOne 专有模型不属于当前 OpenCode 三协议适配器的支持范围。模型同步返回的名称也不构成该模型可在当前账号上调用的保证。

升级不会改写账号已经保存的自定义 `protocol_rules`。旧账号若保存了旧版默认规则，要使用后来新增的 Responses 模型（例如 `muse-wonder-1.0`），需在账号的上游协议路由中添加该模型的精确 Responses 规则，或明确选择恢复新的默认规则。不要把新建账号的默认规则更新理解为历史账号已自动迁移。

官方客户端未配置账号凭据时会使用公开的 `public` 值，并限制可选模型。本项目保留 API Key 必填校验；如需复现公开客户端请求，可在专用 Zen 账号中显式填写 `public`。它不是已认证账号的替代凭据，也不会获得付费模型或额外免费额度。

公开客户端的 UA 格式为 `opencode/<channel>/<version>/<client>`。会话还携带 project、session、affinity 等元信息。适配器按公开格式补齐元信息、保留显式账号请求头覆写与调用方的稳定会话标识；这些字段不构成上游授权证明。会话标识不承担增加免费额度的功能。

## 本次真实验证与修正后的结论

参考资料包括用户提供的本机 OmniRoute 状态文档和抓包脚本；这些文件仅作读取，没有修改。线上检查使用用户授权的 `root@167.114.210.84` 免密 SSH，未修改配置、重启或部署服务。测试凭据仅在进程内使用，未写入本文件。

| 测试路径 | 模型 / 条件 | 结果 |
| --- | --- | --- |
| 线上官方 serve `/api/info` | 持久服务配置提供的认证 | 200，版本 2.0.10 |
| 线上 OmniRoute 非流式 | `oc/mimo-v2.6-flash-free` | 200，`PONG`，3.14 秒 |
| 线上 OmniRoute 非流式 | `oc/big-pickle` | 200，`PONG`，3.13 秒 |
| 线上 OmniRoute 流式 | `oc/big-pickle`，全新随机回复标识 | 200，精确返回随机标识，2 个 JSON chunk 及 `[DONE]`，3.75 秒 |
| 服务器直接请求旧 Zen | 抓包 UA + UUID session + `Bearer public` | 429，`FreeUsageLimitError` |
| 服务器直接请求旧 Zen | 抓包 UA + `ses_` + 12 hex + 14 base62 session + `Bearer public` | 同上 |
| 服务器直接请求新 Console | 同上合法形态 session | 同上 |
| 服务器直接请求旧 Zen | 完全按参考脚本，不发送 Authorization | 同上，`Retry-After: 78203` 秒 |
| 独立 Bun 1.4.2，官方 2.0.10 完整请求 | `big-pickle`，完整 system、12 个工具及请求头 | 200，指定 nonce、真实 usage、`[DONE]`；Zen 和 Console 两个 URL 均成功 |
| 独立 Bun，原始请求仅替换 nonce | 保留原始 user 引号、system、工具及 headers | 200，精确返回 `NONCE_CAPTURE_8542` |
| 独立 Bun，完整 headers、缩短 system | 保留 12 个工具，system 改为官方第一句话 | 429 `FreeUsageLimitError` |
| 独立 Bun，完整 body、精简 headers | 只保留 public 认证、Content-Type、官方 UA | 429 `FreeUsageLimitError` |
| 独立 Node 22.23.1，完全相同原始 URL/headers/body | 固定到与 Bun 相同的 IPv6 出口和远端 IP，证书验证正常 | 429 `FreeUsageLimitError` |
| curl，完全相同原始 URL/headers/body | 与 Bun 相同 IPv6 出口和远端；实际协商 HTTP/1.1 | 429 `FreeUsageLimitError` |
| 本项目脱敏复现脚本，独立 Bun 1.4.2 | 新 session、project、trace、nonce；替换本机路径 | **200，精确返回 `NONCE_CAPTURE_6368`，1.981 秒，usage 标记 `free`，`[DONE]`** |
| 本机独立安装的 Bun 1.4.2，运行同一脚本 | 无 OpenCode 可执行文件、无 serve、无远程脚本 | **200，精确返回 `NONCE_CAPTURE_8648`，1.733 秒，usage 标记 `free`，`[DONE]`** |

后续对照是显式授权的差异化诊断，没有自动重试、代理轮换或来源 IP 伪造。上述结果说明：**独立 HTTP 脚本可以成功调用当前免费模型；只复制 UA/session 无法复现完整客户端行为；同样的 `FreeUsageLimitError` 不能一律解释成该 IP 的全部真实免费额度已耗尽。**

初轮验证成功的是 **Bun 1.4.2 + 官方完整请求结构和头**，后续实验又得到了上述小组合。Node、curl 的失败以及裁剪变体的失败，尚不能确定具体的 TLS 指纹、头顺序、哪段 system 或哪个工具 schema 是服务端的必要检查条件。公开旧版服务端的 `checkHeaders` 检查甚至被注释、`headersExist=true`；它不是当前线上私有后端的完整判定式。完整源码链、版本区别、实验差异和原始证据见 [调用链报告](OPENCODE_CALL_CHAIN.md)。

本项目现有 Go 网关的 headers/模型地址兼容更新，并未因此被证明能直接转发任意客户端的免费请求。成功脚本使用了官方 agent 的完整 system 和工具定义；将它们自动加到用户请求会改变提示词、工具语义及输入 token，因此本轮没有把这份诊断模板注入网关的生产请求。

## 可复现的独立请求

诊断资产位于 `deploy/opencode/`：

- `probe-free-chat.mjs`：构造请求、发送一次、校验真实 SSE nonce 和 usage；默认只 dry run。
- `native-chat-template.v2.0.10.json`：从校验过完整性的官方客户端捕获，替换个人路径、session 和 nonce 的诊断模板。
- `NOTICE`：来源、npm 完整性值和 OpenCode MIT 授权声明。
- `minimal-text-profile.v1.json`：极简失败回归，通过 `--minimal` 选择；保留为对照，不再首选。
- `balanced-text-profile.v2.json`：本轮较稳的低开销实验候选，通过 `--balanced` 选择，锁定 Console Chat、Bun 1.4.2、temperature 0，禁用工具执行和连接复用。
- `quality-capture.mjs` 与测试：保留原始 SSE、仅解析 choice 0、单独记录 reasoning/usage/结束原因的诊断捕获器。

本地有 Bun 1.4.2 时，在仓库根目录执行：

```sh
bun deploy/opencode/probe-free-chat.mjs
bun deploy/opencode/probe-free-chat.mjs --send
# 首选实验候选；省略 --send 时只 dry run
bun deploy/opencode/probe-free-chat.mjs --balanced --send
# 极简失败回归对照
bun deploy/opencode/probe-free-chat.mjs --minimal --send
```

第一条只检查构造，不发模型请求；第二条恰好发送一次。`--zen` 可切换至本次同样验证成功的旧 Zen URL。当前固定测试模型为 `big-pickle`，没有声称其他免费模型或其他协议也已验证。

本次在已授权服务器上的实际复现命令如下。它只通过 stdin 运行独立 Bun 脚本，不创建服务器文件、不启动 OpenCode agent、不调用 serve API：

```sh
set -o pipefail
node deploy/opencode/probe-free-chat.mjs --emit-script | \
  ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o UpdateHostKeys=no \
      -o ControlMaster=no -o ControlPath=none -o ConnectTimeout=10 \
      root@167.114.210.84 \
      'cd / && BUN_BE_BUN=1 BUN_RUNTIME_TRANSPILER_CACHE_PATH=0 /root/.opencode/bin/opencode run -'
```

`node` 在本机只生成代码，真正的 HTTP 请求由服务器的 Bun 1.4.2 发出。`BUN_BE_BUN=1` 使用编译程序内嵌的 Bun CLI；不能把它与运行官方 agent 或 serve 混同。该路径依赖服务器现有可执行文件，不是新部署操作。

脚本遇到失败即退出；仅在状态成功、回复精确匹配本次随机 nonce、收到独立 `[DONE]` 事件且没有错误或 tool calls 时标记成功。模板虽然声明工具，脚本没有工具执行器。服务器结果留在本机 `.cache/client-detection-evidence/sanitized-probe-result.json`，本机独立 Bun 结果在 `.cache/client-detection-evidence/local-standalone-bun-result.json`；均包含公开计费字段和合成输入，不含真实密钥。

## OmniRoute 方案实际做了什么

已核对线上源码：keyless 免费模型先交给真实运行的 `opencode serve`，由官方客户端访问上游。桥接流程为：创建会话、提交 prompt、读取会话消息或订阅 SSE，再翻译为 OpenAI 响应。它与本项目直接构建第三方 HTTP 请求是不同的传输路径。

线上参考实现有以下边界，不应原样移植后声称完整兼容：

- 历史消息被渲染成单个文本 prompt；工具调用、多模态内容和部分生成参数没有等价保留。
- SSE 源是所有会话的全局事件流，必须严格按 session ID 隔离；无会话 ID 的失败事件不能结束其他用户请求。
- 现有流式路径先等待同步 prompt 完成，短请求成功不能证明长回复可实时逐 token 输出。
- 结束、失败、客户端断开和超时都需释放 SSE reader/body；关闭事件流还不等于取消服务端模型执行。
- 现有非流 token 数是字符数估算，观察到 `total_tokens=0`；不可直接把它当作准确的用户计费依据。

## 后续优化方向

已验证的两条路径分别是官方 serve，以及上述独立 Bun 原生请求结构复现。前者由官方维护 agent 请求构造，后者目前是固定版本、固定模型的诊断实现。若要把后者引入本项目，应先定义 system、工具及历史消息的转换约定，验证流式/工具调用和真实用量；不能将当前 nonce 成功测试视为任意 OpenAI 请求已经兼容。

若选择可选的官方 serve 适配器，应与正常 Zen/Go HTTP 传输分离，明确文本能力边界，提供会话隔离、异步事件消费、失败快速结束、取消和资源回收验证，并明确真实用量与估算用量的计费策略。

serve 应通过受认证的本机或私网地址连接；复用既有生产 serve 与为本项目独立部署是不同的资源隔离选择。本轮未把本项目绑定到现有生产 serve，也未新增或部署该传输适配器。

官方资料：

- [推理 API 与免费层访问条件](https://opencode.ai/v2/docs/console/api/inference/)
- [官方客户端公开凭据初始化](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/plugin/provider/opencode.ts)
- [官方会话请求元信息](https://github.com/anomalyco/opencode/blob/dae4077a96243257ea0a2086068504b791629f10/packages/core/src/session/model-request.ts)
