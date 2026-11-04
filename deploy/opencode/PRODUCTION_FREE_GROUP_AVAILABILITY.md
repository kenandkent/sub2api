# 生产 Mimo / DeepSeek 42 实例对话测试

本轮时间：**2026-11-04 12:26:43 至 2026-11-04 12:29:56（北京时间）**。普通用户入口补充验证及状态采集完成于 2026-11-04 12:30:20。

生产地址：`https://sub2api.vseeu.de`。普通账号：`ai@hostmails.de`（user ID 3）。测试时 app / worker 均运行提交 `5bfc628550578bbb5a8442c212e984507b8170c8`，四项生产服务健康。

## 结果概览

| 模型分组 | 分组 / Key ID | 总数 | 成功 | 免费额度 429 | 其他失败 | 成功实例 |
| --- | --- | ---: | ---: | ---: | ---: | --- |
| `mimo-v2.6-flash-free` | 5 / 6 | 21 | 3 | 16 | 2 | 04、15、16 |
| `deepseek-v4.1-flash-free` | 3 / 5 | 21 | 3 | 16 | 2 | 04、15、16 |

每个固定实例均单独新会话、沿用绑定代理、发起一次真实文本调用，不人工重试、不换实例兜底。成功必须同时满足：诊断流完成、`test_complete.success=true`、实际可见回答为 `5`、没有 error 事件。后台外层 HTTP 200 不作为模型调用成功依据。

提示词：`What is 2 + 3? Reply with only the number 5.`

## 逐实例结果

同编号的两个模型实例绑定同一代理。代理列为生产配置中的节点名称，未另测出口公网 IP。耗时为本次 HTTP 请求读取完整诊断 SSE 的时间。

| 实例 | 代理 ID / 节点 | Mimo 账号 | Mimo 结果 / 耗时 | DeepSeek 账号 | DeepSeek 结果 / 耗时 |
| --- | --- | ---: | --- | ---: | --- |
| 01 | 1 / HTTP23 / 01 / US23-Hy2-out4 | 6 | 429，免费额度限制 / 1.442s | 2 | 429，免费额度限制 / 1.409s |
| 02 | 2 / HTTP23 / 02 / US23-Hy2-out6 | 7 | 429，免费额度限制 / 0.842s | 3 | 429，免费额度限制 / 0.788s |
| 03 | 3 / HTTP23 / 03 / US-RackNerd-out4 | 48 | 429，免费额度限制 / 0.835s | 10 | 429，免费额度限制 / 0.782s |
| 04 | 4 / HTTP23 / 04 / KR-Seoul-out4 | 49 | 成功，回复 `5` / 3.117s | 11 | 成功，回复 `5` / 2.242s |
| 05 | 5 / HTTP23 / 05 / KR-Seoul-out6 | 50 | 429，免费额度限制 / 1.378s | 12 | 429，免费额度限制 / 1.411s |
| 06 | 6 / HTTP23 / 06 / NL-Amsterdam-out4 | 51 | 429，免费额度限制 / 1.246s | 13 | 429，免费额度限制 / 1.757s |
| 07 | 7 / HTTP23 / 07 / DE-nc1-out4 | 52 | 429，免费额度限制 / 1.267s | 14 | 429，免费额度限制 / 1.283s |
| 08 | 8 / HTTP23 / 08 / DE-nc1-out6 | 53 | 429，免费额度限制 / 1.595s | 15 | 429，免费额度限制 / 1.263s |
| 09 | 9 / HTTP23 / 09 / DE-nc2-out4 | 54 | 429，免费额度限制 / 1.517s | 16 | 429，免费额度限制 / 1.204s |
| 10 | 10 / HTTP23 / 10 / DE-nc2-out6 | 55 | 429，免费额度限制 / 1.635s | 17 | 429，免费额度限制 / 1.276s |
| 11 | 11 / HTTP23 / 11 / DE-nc3-out4 | 56 | 429，免费额度限制 / 1.492s | 18 | 429，免费额度限制 / 1.266s |
| 12 | 12 / HTTP23 / 12 / DE-nc3-out6 | 57 | 429，免费额度限制 / 1.342s | 19 | 429，免费额度限制 / 1.574s |
| 13 | 13 / HTTP23 / 13 / DE-nc4-out4 | 58 | 429，免费额度限制 / 1.308s | 20 | 429，免费额度限制 / 1.298s |
| 14 | 14 / HTTP23 / 14 / DE-nc4-out6 | 59 | 429，免费额度限制 / 1.904s | 21 | 429，免费额度限制 / 2.170s |
| 15 | 15 / HTTP23 / 15 / UK-London1-out4 | 60 | 成功，回复 `5` / 2.551s | 22 | 成功，回复 `5` / 2.124s |
| 16 | 16 / HTTP23 / 16 / UK-London2-out4 | 61 | 成功，回复 `5` / 3.691s | 23 | 成功，回复 `5` / 7.242s |
| 17 | 17 / HTTP23 / 17 / HK-out4 | 62 | worker 502，socket 连接异常 / 5.440s | 24 | worker 502，socket 连接异常 / 5.495s |
| 18 | 18 / HTTP23 / 18 / HK-out6 | 63 | worker 502，socket 连接异常 / 5.441s | 25 | worker 502，socket 连接异常 / 5.483s |
| 19 | 19 / HTTP23 / 19 / CN-Aliyun-out4 | 64 | 429，免费额度限制 / 1.842s | 26 | 429，免费额度限制 / 1.937s |
| 20 | 20 / HTTP23 / 20 / CN-Tencent1-out4 | 65 | 429，免费额度限制 / 1.797s | 27 | 429，免费额度限制 / 2.423s |
| 21 | 21 / HTTP23 / 21 / CN-Tencent2-out4 | 66 | 429，免费额度限制 / 5.085s | 28 | 429，免费额度限制 / 2.107s |

## 错误原因与证据

**免费额度限制：32 个。** 均以相同请求 ID 关联 worker 日志，确认真实上游 HTTP 429、`classified=upstream`，且错误 JSON 包含 `FreeUsageLimitError` / `Free usage limit reached`。这些是上游免费层限额拒绝，不能归因于本项目用户余额不足。

该结论不等于已经查询到某个上游注册账号的剩余额度数值为 0：这些实例使用绑定代理访问免费入口，上游具体按 IP、身份还是其他维度限额，本轮没有额度查询证据。

**其他失败：4 个。** 下表保留与各次诊断请求 ID 精确关联的 worker 错误证据；不把它们归为免费额度耗尽。

| 测试项 | 账号 / 代理 | 诊断错误 | worker 证据 | 请求 ID |
| --- | --- | --- | --- | --- |
| mimo-17 | 62 / 17 | worker HTTP 502 | `{"event":"dispatch_failed","error_name":"TypeError","error_message":"The socket connection was closed unexpectedly. For more information, pass 'verbose: true' in the second argument to fetch()","dispatched":true}` | `ocfree_0cf799e52ec0ee2fe8b693aad7ac0200c5bb76b634e8c685` |
| mimo-18 | 63 / 18 | worker HTTP 502 | `{"event":"dispatch_failed","error_name":"TypeError","error_message":"The socket connection was closed unexpectedly. For more information, pass 'verbose: true' in the second argument to fetch()","dispatched":true}` | `ocfree_7b4bbea2d14c75188cd2f89c3a91a2680493f2dc8218553e` |
| deepseek-17 | 24 / 17 | worker HTTP 502 | `{"event":"dispatch_failed","error_name":"TypeError","error_message":"The socket connection was closed unexpectedly. For more information, pass 'verbose: true' in the second argument to fetch()","dispatched":true}` | `ocfree_572a059a4304cc2c0f933c94733bd00ba7f4ed2aa200bf57` |
| deepseek-18 | 25 / 18 | worker HTTP 502 | `{"event":"dispatch_failed","error_name":"TypeError","error_message":"The socket connection was closed unexpectedly. For more information, pass 'verbose: true' in the second argument to fetch()","dispatched":true}` | `ocfree_f7e971b52b507f4ab5cf5cf83185651d109244a93c59dfae` |

17 / 18 四次错误的共同日志为 `dispatch_failed`、`dispatched=true`、`TypeError`：`The socket connection was closed unexpectedly.`。worker 已开始 fetch，但没有取得上游 HTTP 响应；这属于连接传输故障，无法据此断言断开方一定是代理节点或一定是模型上游。四个实例也没有被记为免费额度 429。

失败归属校验：可比对的 request / account / proxy / model 字段不匹配 **0**，请求 ID 归属冲突 **0**。32 个额度错误的 worker 记录具有完整账号、代理、模型字段；4 个 socket 错误的 worker 记录未提供账号 / 代理字段，以固定账号诊断目标及相同内部请求 ID 建立关联。成功项不依靠 worker 成功日志（当前 worker 主要记录失败）；以固定账号路径、绑定配置和诊断成功流为证据。

## 普通用户生产 Key 入口

为精确测试 42 个实例，主测试走管理员固定账号诊断接口，该接口不携带普通用户 Key，也不创建消费者计费记录。已另外用 user 3 的 Mimo Key 6 / group 5 和 DeepSeek Key 5 / group 3 各发起一次 `/v1/chat/completions`，结果另计。两把 Key 的 `/v1/models` 权限及模型可见性检查均通过（该请求会更新 Key 最后使用时间，不能证明全池健康）。

| 模型 | HTTP | 真实可见回答 | 完整流 | 结果 | 耗时 |
| --- | ---: | --- | --- | --- | ---: |
| `mimo-v2.6-flash-free` | 200 | `5` | 是 | 成功 | 14.674s |
| `deepseek-v4.1-flash-free` | 200 | `5` | 是 | 成功 | 2.880s |

| 模型 | attempt 轨迹（实例 / 账号 / 状态） | 唯一结算 usage | 本站余额扣费 USD |
| --- | --- | ---: | ---: |
| `mimo-v2.6-flash-free` | 17 / 62 / unknown → 17 / 62 / unknown → 04 / 49 / settled | 232 | 0 |
| `deepseek-v4.1-flash-free` | 15 / 22 / settled | 233 | 0.00001725 |

消费者归属依据：精确请求 payload SHA-256、user / Key、时间窗、唯一 settled usage 及原始 / 存储 token 数同时吻合；不把公网 request ID 冒称为内部 `ocfree_` ID 的直接链接。Mimo 前两条账本状态保留为 `unknown`，没有 usage 结算记录；另通过这些内部 request ID 对应的 worker 日志核对传输错误。

本次 Mimo 实际走通了“17 原实例首次 socket 502 → 17 原实例重试一次仍 socket 502 → 换 04 成功”的路径。两个内部失败 request ID 均通过上述账本匹配与 worker 日志精确核对；成功结算落在 04 / account 49 / proxy 4。DeepSeek 直接在 15 / account 22 / proxy 15 成功。

余额由 999.99044399 USD 变为 999.99042674 USD；两条已结算记录合计 **0.00001725 USD**，与本窗口余额差额一致。上游 free billing 不代表本站 DeepSeek 分组免除用户费用。

## 冷却与配置状态

本轮没有手动暂停、清冷却、重置 usage 或修改代理 / 分组 / Key。成功诊断会通过现有逻辑恢复运行状态；识别到免费额度 429 会自动进入冷却并安排后台探针。429 的本地冷却默认及上限为 12 小时，合法的较短 Retry-After 按实现保留。

新的后台复验流程先立即验证一次，后续按间隔 20 / 40 / 80 / 160 / 320 分钟复验；已有流程会按每账号并发保护合并处理。只有完整有效的免费 SSE 和可见答案才允许清除对应冷却。探针与消费者请求的自动重试均不计入 42 次固定实例主测试。冷却截止表示本地恢复候选资格的时间，不保证上游届时恢复。

| 模型 | 实例 | 账号 ID | 本次 429 后观察到的冷却截止（北京时间） |
| --- | --- | ---: | --- |
| Mimo | 01 | 6 | 2026-11-05 00:26:45 |
| Mimo | 02 | 7 | 2026-11-05 00:26:48 |
| Mimo | 03 | 48 | 2026-11-05 00:26:51 |
| Mimo | 05 | 50 | 2026-11-05 00:27:00 |
| Mimo | 06 | 51 | 2026-11-05 00:27:03 |
| Mimo | 07 | 52 | 2026-11-05 00:27:07 |
| Mimo | 08 | 53 | 2026-11-05 00:27:12 |
| Mimo | 09 | 54 | 2026-11-05 00:27:16 |
| Mimo | 10 | 55 | 2026-11-05 00:27:19 |
| Mimo | 11 | 56 | 2026-11-05 00:27:24 |
| Mimo | 12 | 57 | 2026-11-05 00:27:27 |
| Mimo | 13 | 58 | 2026-11-05 00:27:31 |
| Mimo | 14 | 59 | 2026-11-05 00:27:35 |
| Mimo | 19 | 64 | 2026-11-05 00:28:08 |
| Mimo | 20 | 65 | 2026-11-05 00:28:13 |
| Mimo | 21 | 66 | 2026-11-05 00:28:20 |
| DeepSeek | 01 | 2 | 2026-11-05 00:28:23 |
| DeepSeek | 02 | 3 | 2026-11-05 00:28:26 |
| DeepSeek | 03 | 10 | 2026-11-05 00:28:29 |
| DeepSeek | 05 | 12 | 2026-11-05 00:28:37 |
| DeepSeek | 06 | 13 | 2026-11-05 00:28:43 |
| DeepSeek | 07 | 14 | 2026-11-05 00:28:47 |
| DeepSeek | 08 | 15 | 2026-11-05 00:28:50 |
| DeepSeek | 09 | 16 | 2026-11-05 00:28:54 |
| DeepSeek | 10 | 17 | 2026-11-05 00:28:57 |
| DeepSeek | 11 | 18 | 2026-11-05 00:29:01 |
| DeepSeek | 12 | 19 | 2026-11-05 00:29:05 |
| DeepSeek | 13 | 20 | 2026-11-05 00:29:08 |
| DeepSeek | 14 | 21 | 2026-11-05 00:29:11 |
| DeepSeek | 19 | 26 | 2026-11-05 00:29:47 |
| DeepSeek | 20 | 27 | 2026-11-05 00:29:51 |
| DeepSeek | 21 | 28 | 2026-11-05 00:29:56 |

测试前尚未到期的冷却：Mimo 11 个，DeepSeek 0 个。主测试直接命中固定实例，未先清除此状态。本次 32 条额度错误后观察到的截止时间见上表；17 / 18 的连接错误没有被误写为免费额度冷却。

独立复核确认：42 个账号的配置、`status=active`、`schedulable=true` 保持；21 个完整代理定义、两个分组配置、两个用户 Key 的配置均不变。账号变化仅涉及运行时冷却或使用时间等字段。分组 active / rate-limited 派生计数可随这些运行状态变化，不作为分组配置变更。

44 份完整 SSE 均重新计算 SHA-256 并独立重判，覆盖和结果一致；生产四项服务在测试后仍健康，容器与测试前相同。

日志窗口内另外观察到 21 个独立 `opencode-free-verify-` 后台探针 ID，其 worker 均返回免费额度 429；这只是窗口内可观察的自动复验，不逐条归属于某个人工诊断，也不据此断言不存在其他自动请求。人工部分为 42 个固定实例诊断 + 2 个普通用户生成请求；两次用户请求对应 4 条 attempt，人工主流程共 46 次已验证的上游派发 / attempt，后台探针另计。

32 个主测试额度错误的真实上游 Retry-After 为 70422–73622 秒（均超过 12 小时），本地均按配置上限截为 12 小时。测试后截止范围为 2026-11-05 00:26:45 至 2026-11-05 00:29:56（北京时间）。后台探针成功时仍可按现有机制提前解除冷却。

应用访问日志也逐个核对了 42 个固定账号诊断 POST：无缺失、无重复、无额外实例测试；该日志窗口没有其他修改性 admin 请求。

## 与上一轮的比较

- `mimo-v2.6-flash-free`：11 月 3 日成功实例 04、15、16、17、18；本轮成功 04、15、16。本轮新增成功 无，本轮未再成功 17、18。
- `deepseek-v4.1-flash-free`：11 月 3 日成功实例 04、15、16、17、18；本轮成功 04、15、16。本轮新增成功 无，本轮未再成功 17、18。

## 范围与证据文件

本轮验证最小文本对话，不包含 OpenClaw / DeepSeek Harness 的完整工具往返。诊断请求配置和模型映射已核对；admin parser 没有启用 ExpectedModel，因此不声称另外严格校验了上游响应的模型 echo。

原始证据保存在本项目 `.cache/production-dual-group-20261104/`，不进入 Git；私有目录 0700，报告与原始文件 0600。

- `report.json`、`wire/`：42 个固定实例与两个普通用户入口的原始结果、完整 SSE、SHA-256、耗时及前后状态。
- `instances.csv`、`verified-results.json`：逐实例结果与最终错误分类。
- `method-audit/evidence/correlation.private.json`：失败请求、真实上游错误、worker 日志和 account / proxy / model 归属。
- `review/`：独立覆盖率、SSE、配置、Key 与只读账本归属核查。
- `method-audit/consumer-attempt-log-correlation.private.json`、`method-audit/automatic-verify-summary.json`：消费者内部 attempt 日志关联与后台探针分开计数。
- `before.private.json`、`after.private.json`：含敏感字段的前后快照，仅供本机后续核查。
- `production-runtime.json`：测试时的部署版本和服务健康。

可用性结论仅对应上述测试窗口；免费层状态和生产流量会继续变化。
