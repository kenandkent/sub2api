# OpenCode 免费 Chat 请求缩减实验

日期：2026-10-27（Asia/Shanghai）。模型为 `big-pickle`，Console Chat URL 为 `https://opencode.ai/inference/openai/v1/chat/completions`，运行时固定 Bun 1.4.2。本轮只做有限诊断、保存实验资产和编写设计，没有接入产品代码或修改线上服务。

后续 [配对质量对照](OPENCODE_QUALITY_COMPARISON.md) 已发现该极简 v1 的误抄风险，首选实验候选改为 289 字节 system 的平衡 v2。本文件保留最小化过程和原始失败，不将最低 token 当作质量通过。

## 结果

本轮搜索中最小的已接受联合组合如下。这里的“最小”限定于已测候选，不是对上游全部判定条件或全局最小值的证明。

| 部分 | 已验证配置 | 大小与边界 |
| --- | --- | --- |
| 显式 headers | `User-Agent: opencode/latest/2.0.10/cli`、合法 `ses_…` 的 `X-Session-Id` | Bun/HTTP 栈仍会自动生成 Host、Content-Type 等；不是线上只有两个头。 |
| system | 下方 `<env>` 工作目录块 | 65 UTF-8 字节；合成逻辑上下文，不代表真实文件访问能力。 |
| tools | `[{"type":"function","function":{"name":"read"}}]` | JSON 数组 48 字节；不提供实际 read 功能。 |
| 禁止工具执行 | `tool_choice: "none"` | 诊断程序本身也没有工具执行器。 |
| 流与用量 | `stream: true`、`stream_options.include_usage: true` | 检查真实 SSE、独立 `[DONE]`、错误、工具片段和 usage。 |

system 的原样内容（含末尾换行）为：

```text
<env>
  Working directory: /workspace/opencode-diagnostic
</env>
```

最终联合旧 nonce 提示样本的完整请求体为 337 字节，上游报告 **63 prompt tokens**；本轮完整原生基线约 6,382–6,389。相同测试口径下总输入下降约 **99.0%**。发布的复现脚本将测试文字改为较短的 `Reply only: …`，请求体 325 字节，实测 **61 prompt tokens**、33 completion tokens、94 total tokens，HTTP 200、精确 nonce、`[DONE]`、`billing_source: free`，耗时 2.954 秒。61/63 包含用户测试文字和协议开销，不能把它们全部称为固定注入成本，也不能据此预报任意长对话的节省比例。

发布资产为 [minimal-text-profile.v1.json](opencode/minimal-text-profile.v1.json)，ID `opencode-big-pickle-text-compat-20261027-v1`，文件字节 SHA-256：

```text
10260cbd65728a32762c18e9bc2d36b76c93e2e1b583067e80f06eea87d7cdfc
```

## 执行与判定方法

三个独立代理分别消减 headers、system、tools，使用同一个项目内 runner 文件锁串行发送，单次请求后至少间隔 3.2 秒；每次仅请求一次，没有自动重试、改变网络配置、轮换代理或伪造源 IP。不同方向的候选先独立测试，再实际组合，不能由独立成功推断组合也成功。

本轮共有 85 次有序 runner 请求，加上独立完整基线和发布脚本复验共 87 次真实请求。包含被拒的显式变体，不是 87 次成功模型生成。每个变体保存动态 nonce、时间、运行时、请求摘要、输入大小、显式头名、HTTP/错误、输出、SSE 结束与真实 usage。索引见 [CSV](opencode/minimization-results-20261027.csv)，项目本机原始 case 和 JSONL 在 `.cache/opencode-minimization-20261027/`；缓存目录未作为部署依赖。

判定分三层：

1. `accepted`：HTTP 200、独立 `[DONE]`、无错误和工具调用片段。它证明请求被接受并返回正常协议响应。
2. `matches`：模型文字去除首尾空白后与随机标识完全一致。
3. `nonce_with_optional_period`：只额外接受一个末尾句点；该诊断辅助值不篡改原始严格匹配结果。

模型多加句点、输出额外文字或误抄标识都须记录，不把 HTTP 200 自动写成精确语义通过。CSV 保留了早期 runner 尚未新增 `accepted` 字段时依据原始状态计算的协议接受值。

## 环境与控制实验

服务器为用户授权的 `root@167.114.210.84`，仅通过 stdin 执行独立 Bun 程序，不写服务器文件。第一阶段在该服务器固定出口进行；后续第 53 次精简控制和第 54 次完整控制也返回 429，因此停止该出口实验，不能将随后服务器组内的失败都解释为字段要求。

本机独立 Bun 的完整基线同一阶段返回 HTTP 200。后续缩减及最终组合固定在本机环境，并穿插同环境控制；这是另一个明确记录的验证环境，不把两边结果混为同一次因果对照。最终最小组合尚未在生产服务器出口恢复正常后复验。429 的错误和 Retry-After 不足以确定唯一拒绝原因；本轮没有声称知道精确日额度或当前私有服务端判定式。

### 主要样本

| 序号 | 条件 | 结果 |
| --- | --- | --- |
| 1–3 及后续控制 | 完整原生模板、完整头、12 tools | 服务器 200；prompt 约 6.4k。 |
| 27 | 约 712 字符 system、277 字节 read、六个头 | 服务器 200，精确 nonce，527 prompt tokens。 |
| 53–54 | 精简/完整控制 | 服务器 429；最后一组 system 负结果失去可解释性。 |
| 55 | 本机，三头、712 字符 system、95 字节 read、禁用工具 | 200，nonce 句点，233 prompt tokens。 |
| 58、60 | 131 字节 system 及同组控制 | 200，131 字节样本精确 nonce，78 prompt tokens。 |
| 69、73 | read 省略 parameters 及控制 | 200；工具数组由 95 缩到 48 字节，但 prompt 同为 78，没有观察到这一步省 token。 |
| 70、74、76、78 | 两头、删 session、删 UA、两头控制 | 两头两次 200，单头各一次 429；仅说明本次候选中的局部消减结果。 |
| 72、75、77、79 | env-only、仅官方首句、无 system、原 131 字节控制 | env-only 200，63 tokens；两个负变体 429；控制 200。 |
| 80 | 两头＋65 字节 system＋48 字节工具＋none 联合 | 200，nonce 句点，63 prompt tokens。 |
| 85 | 相同联合组合，`keepalive: false` | 200，精确 `NONCE_CAPTURE_8311`，63 prompt tokens。 |

最终字段组合以本机观察为依据；没有把每项在每个版本都必需当成结论。省略 Authorization 在本轮匿名免费请求被接受，不代表付费凭据可以省略或账号权限发生改变。本项目首期应使用专用 `public` 免费账号，不能将真实付费 API key 悄悄替换为匿名身份。

## 普通文本和多轮：已验证与未通过

以下都保留原角色与历史，没有扁平化为单个 prompt。最终 65 字节 system/48 字节工具组合的结果为：

| 序号 | 请求 | HTTP/SSE | 精确结果 |
| --- | --- | --- | --- |
| 81 | 中文单轮指令 | 200、DONE | 精确返回随机标识；72 prompt tokens。 |
| 82 | 保留调用方额外 system | 200、DONE | 输出 `NONCE_C##_1380`，未匹配；83 prompt tokens。 |
| 83 | 完整 user/assistant/user 历史，回忆上文标识 | 200、DONE | 精确匹配；89 prompt tokens。 |
| 84 | 重复单轮 nonce | 200、DONE | 输出 `NONCE_CResearch_1859.`，未匹配；63 prompt tokens。 |
| 发布脚本复验 | 同 profile、较短测试文字、`keepalive: false` | 200、DONE | 精确 `NONCE_CAPTURE_2934`；61 prompt tokens。 |

较早 131 字节 system 候选的额外 system 和多轮样本曾精确成功，中文样本却有多余文字。上述样本数不足以估计可靠成功率，也不足以把错误归因于某个删减项、某个网络特征或模型本身；缺少同环境交错完整模板的质量对照。

**最小组合是协议接受候选，不是已通过全部严格语义验收的生产 profile。** 两份设计因此默认关闭 Bun 兼容路径，要求先做保守模板/小模板的同环境质量对照、更多正常问题和历史样本，再恢复生产出口受控复验。不能仅因 99% 的 token 降幅就上线。长流、客户端取消、真实代理、其他模型、工具调用、多模态、Responses 和 Anthropic 都没有被此次简单文本测试覆盖。

## 独立复现

在本机执行（第一条 dry run，第二条只发一次）：

```sh
BUN_RUNTIME_TRANSPILER_CACHE_PATH=0 bun deploy/opencode/probe-free-chat.mjs --minimal
BUN_RUNTIME_TRANSPILER_CACHE_PATH=0 bun deploy/opencode/probe-free-chat.mjs --minimal --send
```

脚本默认仍为原生大模板，`--minimal` 才选择小 profile。`--minimal --zen` 被拒绝，因为最终小 profile 只验证了 Console Chat 地址。严格匹配失败时脚本退出非零，即使 `accepted=true`；必须同时查看状态、`matches`、真实输出和 usage，不能自行重试到成功为止。

profile 是合成公开元信息和模板，不含真实密钥、部署目录或用户内容。成功复验见 `.cache/opencode-minimization-20261027/published-minimal-probe.json`。诊断不执行返回的工具。`read` 标记不能对外宣称具备文件读取能力，调用方工具在首期转换模式中明确拒绝。

## 两份接入设计

- [本项目 OpenCode Bun 上游设计](OPENCODE_BUN_UPSTREAM_DESIGN.md)：保留 Go 账号调度、并发、协议与计费，引入可选 Bun worker；普通客户端通过专用模型别名选择明确的文本兼容 profile。
- [线上 OmniRoute 免费模型设计](OMNIROUTE_FREE_MODELS_DESIGN.md)：仅改 keyless 免费入口的拟议 Bun 路径，保留 serve，明确真实流、usage、隔离与回滚；尚未提交或部署线上改动。

本机 Bun 1.4.2 未提供 `Bun.FetchSession`，但已用本地 mock 验证 `keepalive: false` 会使用不同连接，并有第 85 次真实上游成功证据。该设计不能直接依赖当前文档中的独立 session 池：它是 [Bun 1.4.3 新增能力](https://bun.sh/blog/bun-v1.4.3)。其他代理、连接和升级行为仍需独立验证；[当前 fetch 文档](https://bun.sh/docs/runtime/networking/fetch)不能代替锁定版本的测试。
