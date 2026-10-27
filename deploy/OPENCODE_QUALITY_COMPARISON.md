# OpenCode 完整、极简与平衡模板质量对照

日期：2026-10-27，Asia/Shanghai。本轮由多个 `gpt-6.1-terra / ultra` 子 agent 分别执行实验方法审计、配对请求、候选构造和独立 SSE 重解析。全部模型请求在同一本机 Bun 1.4.2 环境执行，没有 SSH、没有改动线上服务，也没有切换网络配置、代理或出口来扩大额度。

## 结论

**不再推荐仅 65 字节 system 的 v1 作为首选。** 本轮在保留原始 SSE 的三个样本中复现其标识内部误抄；这些错误已经存在于上游 `choice 0` 的内容事件，不是本地拼接器生成的。请求 `temperature: 0` 也曾出现一次误抄，因此仅设置该参数不足以修复。

推荐的低开销实验候选改为 **289 字节 system 的 B / v2 平衡模板**：恢复公开 OpenCode 身份介绍，加一条忠实遵循请求、按要求精确复制标识的通用指令，保留原有 `<env>`。工具仍为 48 字节的禁用执行 read 声明，两个显式头和 Bun 版本不变。它在 14 次研究请求中严格匹配 14 次，其中 8 次是在选定候选后预先登记的新样本；发布资产的独立复验另有 1 次严格成功。

这支持将 v2 作为后续离线集成和受控预生产验证的候选，**不等于证明生产稳定性**。任务、采样条件和顺序并非独立同分布；上游隐藏模型、缓存、会话及时间仍有混杂。证据无法区分模型生成与上游服务端后处理，也不能把裁剪认定为唯一根因。

## 对照如何进行

首轮预先登记六对 F/M 请求，覆盖英文精确复制、中文额外 system、多轮历史，各含两种标识形式；每对使用相同标识、相同 session、相同调用方消息、headers、运行时与生成参数，交错 F/M 顺序。F 保留完整 system 和 12 个工具，M 使用极简 system 和 read。传输共同采用 Console Chat、两个显式头、`keepalive: false`、`tool_choice: none`、SSE 和真实 usage；因此 F 是**统一条件下的完整 body 对照**，不是对官方原始请求所有字节的逐字重放。

首轮两臂均 6/6 严格成功。之后事先登记旧引号/句点提示、默认采样和原失败标识回归；第 11 对复现 M 的内部误抄。由此触发已记录的自适应分支：仅替换 M 的 system 为 B，在相同或新 session、请求 0 或省略 temperature 下比较。B 的探索样本全部严格成功后，再登记八个单次确认样本；没有修改失败样本后重试到成功。

全部请求单次执行，共用本机文件锁串行发送、间隔至少 3.2 秒；任何 429、异常协议、tool call 或非正常结束都会停止全组，不自动重试或换出口。本轮 44 次研究请求均 HTTP 200、正常 stop 与独立 `[DONE]`；随后发布脚本复验 1 次，共 45 次真实请求。没有调用线上 OmniRoute 或其 serve。

固定的是请求中的模型名 `big-pickle`，不能保证其隐藏供应商或模型版本固定。所有响应都未提供可据以确认实际模型版本的 fingerprint。请求 temperature 为 0 也不证明上游真的确定性执行或保证逐字输出。

### 模板与结果

| 模板 | system / tools 数组 | 研究请求 | 严格匹配 | 仅末尾句点 | 标识内部损坏 |
| --- | --- | --- | --- | --- | --- |
| F 完整 body | 约 12 KB / 13 KB | 13 | 12 | 1 | 0 |
| M 极简 v1 | 65 B / 48 B | 17 | 13 | 1 | 3 |
| B 平衡 v2 system | 289 B / 48 B | 14 | 14 | 0 | 0 |

上表是描述性计数，不能把不同任务的分母直接当作随机试验成功率。B 中请求 temperature 0 的为 12/12、省略 temperature 的为 2/2；v2 首期选用前者，因为它覆盖了更多确认样本，不是因为已证明 0 是必要条件。确认集有新 session，但部分已知标识此前出现过，不能称全部输入在研究中独立新鲜。AB/BA 顺序和首轮标识格式也存在绑定。

## 关键回归证据

| 对照 | 实际上游内容 | 能说明什么 |
| --- | --- | --- |
| 11：相同 caller system、标识及 session，默认采样，F/M | F：`NONCE_CAPTURE_1380`；M：`NONCE_C新时代文明实践中心_1380` | 复现内部误抄的类别；不能把它当成单纯尾点或解析器问题。 |
| 13：相同消息和 session，默认采样，只改 M/B 的 system | M：`NONCE_Cpackage_1380`；B：`NONCE_CAPTURE_1380` | 在该对条件下，恢复少量 system 与正确结果相伴；仍有采样和时间混杂。 |
| 14：相同消息和 session，请求 temperature 0，只改 M/B system | B：`NONCE_CAPTURE_1380`；M：`NONCE_C# 380` | 请求 0 未充分修复 M；不是“默认采样导致全部错误”的证据。 |
| 15：F/B 默认采样 | 两者均精确，prompt 3202 / 124 | B 在此样本保留精度，且输入更少。 |
| 17：相同失败标识、新 session，请求 0，B/M | 两者均精确 | M 不是每次必然失败，不能忽略 session、缓存和隐藏后端条件。 |
| 12：旧字面引号/句点提示，F/M 默认采样 | 两者均为 `NONCE_CAPTURE_1859.` | 相对不含句点的 expected 字段不严格匹配；但提示本身包含句点，不能直接认定违令。 |

本轮三次内部错字都在原始 SSE 的 content 事件中。旧逐行聚合器、新 JS 捕获器和独立 Python 重解析在全部 44 份记录上的文本一致；各 chunk 只出现 choice 0，没有 tool call、JSON/event 解析错误或替换字符，结束原因均正常。请求摘要和响应标识自洽，未发现文件串写证据。这些检查不等于证明所有并发/网络情况绝无错误。

保存的是经严格 UTF-8 校验的完整响应文本与事件，没有单独保存原始网络字节。旧轮第 82/84 条只有聚合文字、没有 raw，不能用本轮数据替它们恢复根因。三次本轮错误与非 `mai-api-` 形式的响应 ID 同现；ID 形式不是模型身份或可信路由标签，不得据此宣称确定了供应商、自动过滤或重放请求。

## Token 取舍

在相同故障输入的 M/B 对照中，总 prompt 为 **83 / 124 tokens**：增加约 41 tokens 后，该回归样本从错误变为精确。对应 F/B 的 **3202 / 124** 总输入相差约 **96.1%**。这是选择 B 而非继续追求 61-token 极简模板的依据。

B 的探索样本输入为 124–206 tokens，八个确认样本为 171–195；这些数值包含用户文字、会话和上游开销，不是全部固定注入成本。不同样本不能与旧 61-token 短测试直接作成本百分比比较。总 prompt 包含缓存读取；例如完整对照存在大量缓存命中，因此输入 token 降幅不等于金额降幅。

八项事先登记的 B + 请求 temperature 0 确认样本全部严格匹配：英文标识、中文标识、混合大小写与连字符、额外 caller system、多轮回忆、算术 `42`、中文答案 `北京`、Unicode 标识，以及明确要求保留的末尾句点。混合大小写样本同时包含额外 system，因此这些能力类别重叠，实际请求数是八次。每种只有一个样本，不能据此承诺通用可靠率。

## 发布的候选资产

[balanced-text-profile.v2.json](opencode/balanced-text-profile.v2.json)：

- ID：`opencode-big-pickle-text-balanced-20261027-v2`。
- SHA-256：`da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9`。
- Bun 1.4.2、Console Chat、`big-pickle`、两个显式 headers、289 字节 system、48 字节 read、`tool_choice: none`、`keepalive: false`。
- 请求 temperature 默认 0；调用方显式 0 保留，首期其他值明确拒绝，不能静默覆盖调用方设置。B 在默认采样也曾成功，但尚未做相同规模验证。

289 字节 system 恢复了官方公开身份介绍，并增加自拟通用指令 `Follow the user's instructions faithfully. Copy identifiers exactly when requested.`。它不是只针对某个 nonce 的硬编码，也不会强制所有普通回答都不解释。它仍会改变模型上下文，不能称为语义透明。合成工作目录不表示实际文件访问，read 标记从不执行。

发布脚本复验使用该文件 hash 与明确测试输入，得到 HTTP 200、精确 `NONCE_CAPTURE_1254`、stop、`[DONE]`、181 prompt / 35 completion tokens、上游 free usage，耗时 2.911 秒。此单次复验独立列示，不并入 44 条研究统计。

```sh
BUN_RUNTIME_TRANSPILER_CACHE_PATH=0 bun deploy/opencode/probe-free-chat.mjs --balanced
BUN_RUNTIME_TRANSPILER_CACHE_PATH=0 bun deploy/opencode/probe-free-chat.mjs --balanced --send
```

第一条仅 dry run，第二条发送一次。`--minimal` 保留失败回归配置，默认原生大模板保留作控制；没有覆盖 v1 文件或删除旧失败记录。探针现在按完整 SSE 事件处理、仅聚合 choice 0、检查正常结束与严格原文匹配；不会清洗输出把失败变成功。生产 worker 仍需增量解析、取消和背压验证，不能直接使用缓冲完整响应的诊断器。

## 记录与两份设计

- [44 条研究索引](opencode/quality-results-20261027.csv)：任务、参数、严格结果、输入量和本机原始文件位置。
- [失败回归事件子集](opencode/quality-regressions-20261027.json)：仅保留 content/finish，明确不是完整网络抓包；完整记录仍在本机 `.cache/opencode-quality-20261027/raw/`。
- [捕获器](opencode/quality-capture.mjs)、[离线测试](opencode/quality-capture.test.mjs)及 [探针回归测试](opencode/probe-free-chat.test.mjs)。
- [本项目 Bun 上游设计](OPENCODE_BUN_UPSTREAM_DESIGN.md)与 [OmniRoute 免费模型设计](OMNIROUTE_FREE_MODELS_DESIGN.md)：将 v2 作为有有限质量证据的低开销候选；v1 留作回归。

设计仍默认关闭新路径。实际服务器出口、持续长流、真实代理与跨租户隔离、取消、计费/缺 usage、其他模型和协议均未因本轮文本测试而获得验收。下一阶段可据此做离线集成和受控预生产验证；本轮未修改 Go/Vue 上游运行逻辑、部署文件或线上 OmniRoute。

最终离线验证：探针三种模式共 31 项 Node 回归测试通过；独立捕获器 9 项 Bun 测试、43 断言通过。将 44 份已保存原始响应通过加固后的探针再次离线回放，文本、结束原因、usage 和严格结果均与原记录一致：39 条精确、5 条不匹配全部保留，没有通过改写输出抹掉失败。文件摘要、289/48 字节组件、研究索引、发布证明、文档链接和 diff 检查通过。

诊断只聚合 choice 0 不意味着生产可忽略其他 choice：两份设计均要求先检查所有 choices 的工具事件与旧式 function_call，首期只支持 n 缺省或 1；无法判定、混合工具帧或意外 choice 必须在写出前明确拒绝。允许调用方 tools 缺省或空数组并规范化，非空工具保持未支持。
