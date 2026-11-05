# 清冷却 / 异常状态后重测：Mimo / DeepSeek 合并报告

**最新结论对应清状态后的 32 次固定实例重测**：2026-11-05 16:20:10 完成首批清除回读；重测结束于 2026-11-05 16:22:18，最终状态采集于 2026-11-05 16:22:21（北京时间）。

| 模型 | 清状态后重测 | 成功 | 再次免费额度 429 | 其他失败 | 重测成功实例 |
| --- | ---: | ---: | ---: | ---: | --- |
| `mimo-v2.6-flash-free` | 16 | 0 | 16 | 0 | 无 |
| `deepseek-v4.1-flash-free` | 16 | 0 | 16 | 0 | 无 |

**32 个实例在生产环境清除冷却 / 异常状态并确认干净后，重测仍全部收到真实上游免费额度 429。** 这次失败不能仅用本地遗留冷却来解释：本轮实际请求已再次到达上游并被其免费策略拒绝。该结论不表示永久不可用。

本轮用户明确授权把刚才确认免费额度限制的 32 个实例清除冷却 / 异常状态后重测。执行的是 `recover-state` 运行状态恢复及文件中记录的必要残留状态清除；这次确实包含人工恢复动作。它不是 `reset-quota`，没有用此动作重置 usage 计数或手动切换 schedulable。

32 个实例首批清除后均回读为 active、schedulable=true 且运行时阻断字段为空；每次重测前也分别回读验证干净。执行器记录人工恢复 / 清理请求 **32** 次，其中重测前因运行状态重新出现而再次 recover-state **0** 次。每个实例只进行一次固定账号模型重测；没有在重测后为达成‘清状态’再次清除。

首次已经成功的 10 个实例没有被人工恢复，也没有在第二轮重测。下文保留其首次结果与时间，不能把跨两个时间窗的最近观察拼成一次新的 42 实例全量健康测试。本轮没有新增消费者入口或费用测试。

本站运行状态清除不能重置上游免费限制。若重测再次遇到已证实免费额度 429，程序会自动重新进入冷却，规则为默认及上限 12 小时（最短 1 分钟，合法较短 Retry-After 按实现保留）；最终状态按真实调用后快照保留。

## 21 组：首次结果与清状态后重测对比

首次 42 次诊断：2026-11-05 16:00:59 至 2026-11-05 16:04:09；第二轮只覆盖首次失败的 32 个实例。提示词均为 `What is 2 + 3? Reply with only the number 5.`。

耗时为读取完整诊断响应所需时间；账号 / 代理绑定来自生产快照。代理名称没有被另行转换为出口公网 IP。

| 实例 | Mimo 账号 / 代理 / 节点 | Mimo 首次 → 重测（耗时） | DeepSeek 账号 / 代理 / 节点 | DeepSeek 首次 → 重测（耗时） |
| --- | --- | --- | --- | --- |
| 01 | 6 / 1 / HTTP23 / 01 / US23-Hy2-out4 | 上游 429，免费额度限制 / 0.855s → 上游 429，免费额度限制 / 1.491s | 2 / 1 / HTTP23 / 01 / US23-Hy2-out4 | 上游 429，免费额度限制 / 0.864s → 上游 429，免费额度限制 / 0.844s |
| 02 | 7 / 2 / HTTP23 / 02 / US23-Hy2-out6 | 上游 429，免费额度限制 / 1.996s → 上游 429，免费额度限制 / 0.863s | 3 / 2 / HTTP23 / 02 / US23-Hy2-out6 | 上游 429，免费额度限制 / 0.814s → 上游 429，免费额度限制 / 1.350s |
| 03 | 48 / 3 / HTTP23 / 03 / US-RackNerd-out4 | 上游 429，免费额度限制 / 0.911s → 上游 429，免费额度限制 / 0.831s | 10 / 3 / HTTP23 / 03 / US-RackNerd-out4 | 上游 429，免费额度限制 / 1.043s → 上游 429，免费额度限制 / 0.886s |
| 04 | 49 / 4 / HTTP23 / 04 / KR-Seoul-out4 | 成功，回复 5 / 4.001s → 未恢复、未重测（保留首次观察） | 11 / 4 / HTTP23 / 04 / KR-Seoul-out4 | 成功，回复 5 / 3.111s → 未恢复、未重测（保留首次观察） |
| 05 | 50 / 5 / HTTP23 / 05 / KR-Seoul-out6 | 上游 429，免费额度限制 / 1.444s → 上游 429，免费额度限制 / 2.012s | 12 / 5 / HTTP23 / 05 / KR-Seoul-out6 | 上游 429，免费额度限制 / 1.393s → 上游 429，免费额度限制 / 1.318s |
| 06 | 51 / 6 / HTTP23 / 06 / NL-Amsterdam-out4 | 上游 429，免费额度限制 / 1.685s → 上游 429，免费额度限制 / 1.551s | 13 / 6 / HTTP23 / 06 / NL-Amsterdam-out4 | 上游 429，免费额度限制 / 1.543s → 上游 429，免费额度限制 / 1.242s |
| 07 | 52 / 7 / HTTP23 / 07 / DE-nc1-out4 | 上游 429，免费额度限制 / 1.911s → 上游 429，免费额度限制 / 1.288s | 14 / 7 / HTTP23 / 07 / DE-nc1-out4 | 上游 429，免费额度限制 / 1.627s → 上游 429，免费额度限制 / 1.808s |
| 08 | 53 / 8 / HTTP23 / 08 / DE-nc1-out6 | 上游 429，免费额度限制 / 1.420s → 上游 429，免费额度限制 / 2.226s | 15 / 8 / HTTP23 / 08 / DE-nc1-out6 | 上游 429，免费额度限制 / 1.349s → 上游 429，免费额度限制 / 1.263s |
| 09 | 54 / 9 / HTTP23 / 09 / DE-nc2-out4 | 上游 429，免费额度限制 / 1.676s → 上游 429，免费额度限制 / 1.264s | 16 / 9 / HTTP23 / 09 / DE-nc2-out4 | 上游 429，免费额度限制 / 1.355s → 上游 429，免费额度限制 / 1.434s |
| 10 | 55 / 10 / HTTP23 / 10 / DE-nc2-out6 | 上游 429，免费额度限制 / 1.399s → 上游 429，免费额度限制 / 1.339s | 17 / 10 / HTTP23 / 10 / DE-nc2-out6 | 上游 429，免费额度限制 / 1.391s → 上游 429，免费额度限制 / 1.241s |
| 11 | 56 / 11 / HTTP23 / 11 / DE-nc3-out4 | 上游 429，免费额度限制 / 1.357s → 上游 429，免费额度限制 / 1.248s | 18 / 11 / HTTP23 / 11 / DE-nc3-out4 | 上游 429，免费额度限制 / 1.368s → 上游 429，免费额度限制 / 1.464s |
| 12 | 57 / 12 / HTTP23 / 12 / DE-nc3-out6 | 上游 429，免费额度限制 / 1.496s → 上游 429，免费额度限制 / 1.279s | 19 / 12 / HTTP23 / 12 / DE-nc3-out6 | 上游 429，免费额度限制 / 1.590s → 上游 429，免费额度限制 / 1.418s |
| 13 | 58 / 13 / HTTP23 / 13 / DE-nc4-out4 | 上游 429，免费额度限制 / 1.323s → 上游 429，免费额度限制 / 1.374s | 20 / 13 / HTTP23 / 13 / DE-nc4-out4 | 上游 429，免费额度限制 / 1.388s → 上游 429，免费额度限制 / 1.390s |
| 14 | 59 / 14 / HTTP23 / 14 / DE-nc4-out6 | 上游 429，免费额度限制 / 1.302s → 上游 429，免费额度限制 / 1.287s | 21 / 14 / HTTP23 / 14 / DE-nc4-out6 | 上游 429，免费额度限制 / 1.356s → 上游 429，免费额度限制 / 1.333s |
| 15 | 60 / 15 / HTTP23 / 15 / UK-London1-out4 | 成功，回复 5 / 2.902s → 未恢复、未重测（保留首次观察） | 22 / 15 / HTTP23 / 15 / UK-London1-out4 | 成功，回复 5 / 1.740s → 未恢复、未重测（保留首次观察） |
| 16 | 61 / 16 / HTTP23 / 16 / UK-London2-out4 | 成功，回复 5 / 2.571s → 未恢复、未重测（保留首次观察） | 23 / 16 / HTTP23 / 16 / UK-London2-out4 | 成功，回复 5 / 1.826s → 未恢复、未重测（保留首次观察） |
| 17 | 62 / 17 / HTTP23 / 17 / HK-out4 | 成功，回复 5 / 4.650s → 未恢复、未重测（保留首次观察） | 24 / 17 / HTTP23 / 17 / HK-out4 | 成功，回复 5 / 2.971s → 未恢复、未重测（保留首次观察） |
| 18 | 63 / 18 / HTTP23 / 18 / HK-out6 | 成功，回复 5 / 15.304s → 未恢复、未重测（保留首次观察） | 25 / 18 / HTTP23 / 18 / HK-out6 | 成功，回复 5 / 3.174s → 未恢复、未重测（保留首次观察） |
| 19 | 64 / 19 / HTTP23 / 19 / CN-Aliyun-out4 | 上游 429，免费额度限制 / 1.884s → 上游 429，免费额度限制 / 2.105s | 26 / 19 / HTTP23 / 19 / CN-Aliyun-out4 | 上游 429，免费额度限制 / 1.938s → 上游 429，免费额度限制 / 3.193s |
| 20 | 65 / 20 / HTTP23 / 20 / CN-Tencent1-out4 | 上游 429，免费额度限制 / 1.870s → 上游 429，免费额度限制 / 3.439s | 27 / 20 / HTTP23 / 20 / CN-Tencent1-out4 | 上游 429，免费额度限制 / 3.562s → 上游 429，免费额度限制 / 1.836s |
| 21 | 66 / 21 / HTTP23 / 21 / CN-Tencent2-out4 | 上游 429，免费额度限制 / 2.006s → 上游 429，免费额度限制 / 1.768s | 28 / 21 / HTTP23 / 21 / CN-Tencent2-out4 | 上游 429，免费额度限制 / 2.039s → 上游 429，免费额度限制 / 1.684s |

## 授权恢复动作与清除回读

只对首次已确认额度 429 的账号执行恢复。表中保留实际请求方法、路径、HTTP 和 request ID；恢复动作属于管理 API 调用，不计作模型上游派发。清除后状态与模型重测后状态是不同时间点。

| 模型 / 实例 | 账号 | 动作 | 方法 / 路径 | HTTP | 管理 request ID | 动作完成（北京时间） | 清除后回读 |
| --- | ---: | --- | --- | ---: | --- | --- | --- |
| Mimo / 01 | 6 | `recover-state` | `POST /api/v1/admin/accounts/6/recover-state` | 200 | `e0c54736-ef47-4138-ae82-944144676c43` | 2026-11-05 16:18:05 | 通过 |
| Mimo / 02 | 7 | `recover-state` | `POST /api/v1/admin/accounts/7/recover-state` | 200 | `f5337431-865b-41cb-8e5c-0add28840f71` | 2026-11-05 16:18:09 | 通过 |
| Mimo / 03 | 48 | `recover-state` | `POST /api/v1/admin/accounts/48/recover-state` | 200 | `a892f9f5-52c7-41f3-a212-2038aec0eceb` | 2026-11-05 16:18:12 | 通过 |
| Mimo / 05 | 50 | `recover-state` | `POST /api/v1/admin/accounts/50/recover-state` | 200 | `1b9e8a0a-da17-431c-aae5-315d09c9399f` | 2026-11-05 16:18:15 | 通过 |
| Mimo / 06 | 51 | `recover-state` | `POST /api/v1/admin/accounts/51/recover-state` | 200 | `320f1d15-22bc-4363-8f49-2f7c8af87643` | 2026-11-05 16:18:19 | 通过 |
| Mimo / 07 | 52 | `recover-state` | `POST /api/v1/admin/accounts/52/recover-state` | 200 | `bae4ef03-fd80-4b24-bcf7-bc5a3a14745d` | 2026-11-05 16:18:22 | 通过 |
| Mimo / 08 | 53 | `recover-state` | `POST /api/v1/admin/accounts/53/recover-state` | 200 | `d050b353-3062-4aa4-be0d-17e5c62083d9` | 2026-11-05 16:18:26 | 通过 |
| Mimo / 09 | 54 | `recover-state` | `POST /api/v1/admin/accounts/54/recover-state` | 200 | `931de88f-16a9-463a-8b08-971bdb995ca5` | 2026-11-05 16:18:31 | 通过 |
| Mimo / 10 | 55 | `recover-state` | `POST /api/v1/admin/accounts/55/recover-state` | 200 | `95d3ff17-48e9-426b-9c16-a04c68575ced` | 2026-11-05 16:18:34 | 通过 |
| Mimo / 11 | 56 | `recover-state` | `POST /api/v1/admin/accounts/56/recover-state` | 200 | `614e5824-05fb-4693-b902-2b9de7cb1f3b` | 2026-11-05 16:18:40 | 通过 |
| Mimo / 12 | 57 | `recover-state` | `POST /api/v1/admin/accounts/57/recover-state` | 200 | `db14cef0-0690-46e0-b0b5-3759c8bfb0fe` | 2026-11-05 16:18:43 | 通过 |
| Mimo / 13 | 58 | `recover-state` | `POST /api/v1/admin/accounts/58/recover-state` | 200 | `79c3de28-548d-46f8-98a5-3a65d5a81900` | 2026-11-05 16:18:47 | 通过 |
| Mimo / 14 | 59 | `recover-state` | `POST /api/v1/admin/accounts/59/recover-state` | 200 | `e308572d-b866-49e0-ae3c-6a2defdcb1fc` | 2026-11-05 16:18:52 | 通过 |
| Mimo / 19 | 64 | `recover-state` | `POST /api/v1/admin/accounts/64/recover-state` | 200 | `35d671d6-cd29-4a27-8af0-95f0fe0da753` | 2026-11-05 16:18:57 | 通过 |
| Mimo / 20 | 65 | `recover-state` | `POST /api/v1/admin/accounts/65/recover-state` | 200 | `2c9119e2-02ef-4b1c-a0ea-d8160afa9340` | 2026-11-05 16:19:00 | 通过 |
| Mimo / 21 | 66 | `recover-state` | `POST /api/v1/admin/accounts/66/recover-state` | 200 | `f561ead2-e13b-4254-8947-1e127f8235db` | 2026-11-05 16:19:03 | 通过 |
| DeepSeek / 01 | 2 | `recover-state` | `POST /api/v1/admin/accounts/2/recover-state` | 200 | `6ed1a6df-b900-4f6e-8e2d-9f64ae2a95d5` | 2026-11-05 16:19:08 | 通过 |
| DeepSeek / 02 | 3 | `recover-state` | `POST /api/v1/admin/accounts/3/recover-state` | 200 | `03e964a9-f350-420d-b44b-5974bd7cf4ba` | 2026-11-05 16:19:12 | 通过 |
| DeepSeek / 03 | 10 | `recover-state` | `POST /api/v1/admin/accounts/10/recover-state` | 200 | `cd7133db-6d29-4b0c-8146-18459d5fd14f` | 2026-11-05 16:19:16 | 通过 |
| DeepSeek / 05 | 12 | `recover-state` | `POST /api/v1/admin/accounts/12/recover-state` | 200 | `8a2f2072-e52f-442b-8df9-b2922ddc21ed` | 2026-11-05 16:19:20 | 通过 |
| DeepSeek / 06 | 13 | `recover-state` | `POST /api/v1/admin/accounts/13/recover-state` | 200 | `9603864b-4460-4695-b714-59bb90dfd6e3` | 2026-11-05 16:19:28 | 通过 |
| DeepSeek / 07 | 14 | `recover-state` | `POST /api/v1/admin/accounts/14/recover-state` | 200 | `a545925d-c1a8-4112-bf9b-3ad2e21881c5` | 2026-11-05 16:19:34 | 通过 |
| DeepSeek / 08 | 15 | `recover-state` | `POST /api/v1/admin/accounts/15/recover-state` | 200 | `a52f0877-b102-43ff-aaa3-76628fe4c043` | 2026-11-05 16:19:37 | 通过 |
| DeepSeek / 09 | 16 | `recover-state` | `POST /api/v1/admin/accounts/16/recover-state` | 200 | `f1eb58bc-eb97-4224-8444-81263d370c4c` | 2026-11-05 16:19:39 | 通过 |
| DeepSeek / 10 | 17 | `recover-state` | `POST /api/v1/admin/accounts/17/recover-state` | 200 | `f8e37f01-e1d5-4328-8d3b-dba4b026e216` | 2026-11-05 16:19:43 | 通过 |
| DeepSeek / 11 | 18 | `recover-state` | `POST /api/v1/admin/accounts/18/recover-state` | 200 | `9c5ddccd-aa2d-4736-b1c7-8dbd7ab6a10a` | 2026-11-05 16:19:47 | 通过 |
| DeepSeek / 12 | 19 | `recover-state` | `POST /api/v1/admin/accounts/19/recover-state` | 200 | `338cafb7-6937-4d05-8333-2cfa26e283e4` | 2026-11-05 16:19:50 | 通过 |
| DeepSeek / 13 | 20 | `recover-state` | `POST /api/v1/admin/accounts/20/recover-state` | 200 | `2dba6b46-6925-4dae-b872-9cfdbf5a6933` | 2026-11-05 16:19:54 | 通过 |
| DeepSeek / 14 | 21 | `recover-state` | `POST /api/v1/admin/accounts/21/recover-state` | 200 | `1148723e-14d3-4ea8-bc17-559c3fe54d60` | 2026-11-05 16:19:57 | 通过 |
| DeepSeek / 19 | 26 | `recover-state` | `POST /api/v1/admin/accounts/26/recover-state` | 200 | `663cf763-10cd-465b-b3e4-cf5b80c6f1d6` | 2026-11-05 16:20:01 | 通过 |
| DeepSeek / 20 | 27 | `recover-state` | `POST /api/v1/admin/accounts/27/recover-state` | 200 | `be14afa1-19d4-4d6c-bcde-c8caf32c2414` | 2026-11-05 16:20:04 | 通过 |
| DeepSeek / 21 | 28 | `recover-state` | `POST /api/v1/admin/accounts/28/recover-state` | 200 | `d69958bc-dc69-4f6e-87b8-8eff01a75e7e` | 2026-11-05 16:20:09 | 通过 |

## 重测后的真实冷却状态

所有时间均为北京时间。首批清除后回读及每次重测前的干净状态从 report 重新核对；若自动复验或同期生产请求随后影响运行时字段，最终快照保留实际观察，不按预期覆盖。冷却截止仅表示本地恢复候选资格，不保证上游届时恢复。

| 模型 / 实例 | 账号 | 恢复前冷却截止 | 首批清除回读 | 重测前干净 | 重测后冷却截止 | 最终冷却截止 |
| --- | ---: | --- | --- | --- | --- | --- |
| Mimo / 01 | 6 | 2026-11-06 04:01:00 | 通过 | 通过 | 2026-11-06 04:20:13 | 2026-11-06 04:20:13 |
| Mimo / 02 | 7 | 2026-11-06 04:01:04 | 通过 | 通过 | 2026-11-06 04:20:18 | 2026-11-06 04:20:18 |
| Mimo / 03 | 48 | 2026-11-06 04:01:08 | 通过 | 通过 | 2026-11-06 04:20:20 | 2026-11-06 04:20:20 |
| Mimo / 05 | 50 | 2026-11-06 04:01:17 | 通过 | 通过 | 2026-11-06 04:20:23 | 2026-11-06 04:20:23 |
| Mimo / 06 | 51 | 2026-11-06 04:01:21 | 通过 | 通过 | 2026-11-06 04:20:28 | 2026-11-06 04:20:28 |
| Mimo / 07 | 52 | 2026-11-06 04:01:25 | 通过 | 通过 | 2026-11-06 04:20:32 | 2026-11-06 04:20:32 |
| Mimo / 08 | 53 | 2026-11-06 04:01:30 | 通过 | 通过 | 2026-11-06 04:20:36 | 2026-11-06 04:20:36 |
| Mimo / 09 | 54 | 2026-11-06 04:01:33 | 通过 | 通过 | 2026-11-06 04:20:41 | 2026-11-06 04:20:41 |
| Mimo / 10 | 55 | 2026-11-06 04:01:38 | 通过 | 通过 | 2026-11-06 04:20:44 | 2026-11-06 04:20:44 |
| Mimo / 11 | 56 | 2026-11-06 04:01:41 | 通过 | 通过 | 2026-11-06 04:20:48 | 2026-11-06 04:20:48 |
| Mimo / 12 | 57 | 2026-11-06 04:01:45 | 通过 | 通过 | 2026-11-06 04:20:52 | 2026-11-06 04:20:52 |
| Mimo / 13 | 58 | 2026-11-06 04:01:49 | 通过 | 通过 | 2026-11-06 04:20:56 | 2026-11-06 04:20:56 |
| Mimo / 14 | 59 | 2026-11-06 04:01:52 | 通过 | 通过 | 2026-11-06 04:21:00 | 2026-11-06 04:21:00 |
| Mimo / 19 | 64 | 2026-11-06 04:02:32 | 通过 | 通过 | 2026-11-06 04:21:04 | 2026-11-06 04:21:04 |
| Mimo / 20 | 65 | 2026-11-06 04:02:36 | 通过 | 通过 | 2026-11-06 04:21:10 | 2026-11-06 04:21:10 |
| Mimo / 21 | 66 | 2026-11-06 04:02:40 | 通过 | 通过 | 2026-11-06 04:21:15 | 2026-11-06 04:21:15 |
| DeepSeek / 01 | 2 | 2026-11-06 04:02:43 | 通过 | 通过 | 2026-11-06 04:21:18 | 2026-11-06 04:21:18 |
| DeepSeek / 02 | 3 | 2026-11-06 04:02:46 | 通过 | 通过 | 2026-11-06 04:21:20 | 2026-11-06 04:21:20 |
| DeepSeek / 03 | 10 | 2026-11-06 04:02:49 | 通过 | 通过 | 2026-11-06 04:21:24 | 2026-11-06 04:21:24 |
| DeepSeek / 05 | 12 | 2026-11-06 04:02:59 | 通过 | 通过 | 2026-11-06 04:21:27 | 2026-11-06 04:21:27 |
| DeepSeek / 06 | 13 | 2026-11-06 04:03:03 | 通过 | 通过 | 2026-11-06 04:21:31 | 2026-11-06 04:21:31 |
| DeepSeek / 07 | 14 | 2026-11-06 04:03:07 | 通过 | 通过 | 2026-11-06 04:21:34 | 2026-11-06 04:21:34 |
| DeepSeek / 08 | 15 | 2026-11-06 04:03:12 | 通过 | 通过 | 2026-11-06 04:21:39 | 2026-11-06 04:21:39 |
| DeepSeek / 09 | 16 | 2026-11-06 04:03:16 | 通过 | 通过 | 2026-11-06 04:21:43 | 2026-11-06 04:21:43 |
| DeepSeek / 10 | 17 | 2026-11-06 04:03:19 | 通过 | 通过 | 2026-11-06 04:21:47 | 2026-11-06 04:21:47 |
| DeepSeek / 11 | 18 | 2026-11-06 04:03:23 | 通过 | 通过 | 2026-11-06 04:21:52 | 2026-11-06 04:21:52 |
| DeepSeek / 12 | 19 | 2026-11-06 04:03:26 | 通过 | 通过 | 2026-11-06 04:21:56 | 2026-11-06 04:21:56 |
| DeepSeek / 13 | 20 | 2026-11-06 04:03:30 | 通过 | 通过 | 2026-11-06 04:22:00 | 2026-11-06 04:22:00 |
| DeepSeek / 14 | 21 | 2026-11-06 04:03:34 | 通过 | 通过 | 2026-11-06 04:22:04 | 2026-11-06 04:22:04 |
| DeepSeek / 19 | 26 | 2026-11-06 04:03:58 | 通过 | 通过 | 2026-11-06 04:22:08 | 2026-11-06 04:22:08 |
| DeepSeek / 20 | 27 | 2026-11-06 04:04:04 | 通过 | 通过 | 2026-11-06 04:22:13 | 2026-11-06 04:22:13 |
| DeepSeek / 21 | 28 | 2026-11-06 04:04:08 | 通过 | 通过 | 2026-11-06 04:22:17 | 2026-11-06 04:22:17 |

## 重测失败的精确证据

免费额度类别要求精确 worker request ID 关联、`classified=upstream`、真实上游 HTTP 429 和 `FreeUsageLimitError` / `Free usage limit reached` 标记同时成立。空回答、不完整流、HTTP 5xx 与 socket 故障不会合并成额度错误；额度拒绝也不是上游剩余额度数值为 0 的直接查询。

| 测试项 | 账号 / 代理 | 已核实类别 | worker / 内部 request ID | 诊断错误 | worker 关键证据 |
| --- | --- | --- | --- | --- | --- |
| `mimo-01` | 6 / 1 | 上游 429，免费额度限制 | `ocfree_6a147c2c123983891bc323884de46827a09f43192073af90` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_6a147c2c123983891bc323884de46827a09f43192073af90 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"59117","free_usage_limit_marker":true}]` |
| `mimo-02` | 7 / 2 | 上游 429，免费额度限制 | `ocfree_00a2ef2d341a39f32b4b855361dc1b6674c2addaf26af8fe` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_00a2ef2d341a39f32b4b855361dc1b6674c2addaf26af8fe profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"56718","free_usage_limit_marker":true}]` |
| `mimo-03` | 48 / 3 | 上游 429，免费额度限制 | `ocfree_da1b90180ef976d9ea8ab443cd424e60a1b779a347b149d5` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_da1b90180ef976d9ea8ab443cd424e60a1b779a347b149d5 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"57596","free_usage_limit_marker":true}]` |
| `mimo-05` | 50 / 5 | 上游 429，免费额度限制 | `ocfree_579607eb1c7af76dbf7b01ca9c4061231580ac9f9492f874` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_579607eb1c7af76dbf7b01ca9c4061231580ac9f9492f874 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"59619","free_usage_limit_marker":true}]` |
| `mimo-06` | 51 / 6 | 上游 429，免费额度限制 | `ocfree_fff80d1b1c7b677823b3a7be38ab9c1fdadcc01fd87f794f` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_fff80d1b1c7b677823b3a7be38ab9c1fdadcc01fd87f794f profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"57712","free_usage_limit_marker":true}]` |
| `mimo-07` | 52 / 7 | 上游 429，免费额度限制 | `ocfree_67cb6787d9144bb9266b4b0fcc550e4f4c67b13ffcd7517f` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_67cb6787d9144bb9266b4b0fcc550e4f4c67b13ffcd7517f profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"57181","free_usage_limit_marker":true}]` |
| `mimo-08` | 53 / 8 | 上游 429，免费额度限制 | `ocfree_3c326d4fe358639169b1cf61dfcb19920239289fcb4159eb` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_3c326d4fe358639169b1cf61dfcb19920239289fcb4159eb profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"56684","free_usage_limit_marker":true}]` |
| `mimo-09` | 54 / 9 | 上游 429，免费额度限制 | `ocfree_a3804bfd469361dd5f617e8413bcdc3420841f34929e9ec7` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_a3804bfd469361dd5f617e8413bcdc3420841f34929e9ec7 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"56516","free_usage_limit_marker":true}]` |
| `mimo-10` | 55 / 10 | 上游 429，免费额度限制 | `ocfree_8d097a401c65daefaa521d8ec3e7ea01c9119841235b25b4` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_8d097a401c65daefaa521d8ec3e7ea01c9119841235b25b4 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"58162","free_usage_limit_marker":true}]` |
| `mimo-11` | 56 / 11 | 上游 429，免费额度限制 | `ocfree_7ad44199dfa76823ec8f0be49a20ac2ea7df039220b9dc80` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_7ad44199dfa76823ec8f0be49a20ac2ea7df039220b9dc80 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"56626","free_usage_limit_marker":true}]` |
| `mimo-12` | 57 / 12 | 上游 429，免费额度限制 | `ocfree_ce6823761393af1828af60389a8e3f93465753f928e0eb57` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_ce6823761393af1828af60389a8e3f93465753f928e0eb57 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"57360","free_usage_limit_marker":true}]` |
| `mimo-13` | 58 / 13 | 上游 429，免费额度限制 | `ocfree_92865caaef74f018727ccf194c9c76b07119ad401d66ffd1` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_92865caaef74f018727ccf194c9c76b07119ad401d66ffd1 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"56893","free_usage_limit_marker":true}]` |
| `mimo-14` | 59 / 14 | 上游 429，免费额度限制 | `ocfree_500c64ca388bfd9094275fe0cbd70a842edec68f1f20b44c` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_500c64ca388bfd9094275fe0cbd70a842edec68f1f20b44c profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"58717","free_usage_limit_marker":true}]` |
| `mimo-19` | 64 / 19 | 上游 429，免费额度限制 | `ocfree_0f904e9777bc9d4b99251d4ebbdbc60a3b8ba3a2edf41ca4` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_0f904e9777bc9d4b99251d4ebbdbc60a3b8ba3a2edf41ca4 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"57844","free_usage_limit_marker":true}]` |
| `mimo-20` | 65 / 20 | 上游 429，免费额度限制 | `ocfree_ff42b64a1558f21fca6d1ece7cfabaf7b37dcff62a6f3e09` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_ff42b64a1558f21fca6d1ece7cfabaf7b37dcff62a6f3e09 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"58731","free_usage_limit_marker":true}]` |
| `mimo-21` | 66 / 21 | 上游 429，免费额度限制 | `ocfree_f82f64deb1646b040d1e95aa97a7d2243902d23589dbe191` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_f82f64deb1646b040d1e95aa97a7d2243902d23589dbe191 profile=opencode-mimo-v2.6-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"59484","free_usage_limit_marker":true}]` |
| `deepseek-01` | 2 / 1 | 上游 429，免费额度限制 | `ocfree_670b51b36f012b3464a76e9a487e1f501571c88c0b55d8c3` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_670b51b36f012b3464a76e9a487e1f501571c88c0b55d8c3 profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"59052","free_usage_limit_marker":true}]` |
| `deepseek-02` | 3 / 2 | 上游 429，免费额度限制 | `ocfree_51e437d2bd6cb964d368f9de812dff769c8c856ab2321b71` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_51e437d2bd6cb964d368f9de812dff769c8c856ab2321b71 profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"56656","free_usage_limit_marker":true}]` |
| `deepseek-03` | 10 / 3 | 上游 429，免费额度限制 | `ocfree_f676949aa6d3addb9d8638a11cad965e4e35a2ea02c9477d` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_f676949aa6d3addb9d8638a11cad965e4e35a2ea02c9477d profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"57533","free_usage_limit_marker":true}]` |
| `deepseek-05` | 12 / 5 | 上游 429，免费额度限制 | `ocfree_cdc8e18eaea3289f11798ee2c0d014ecd22248973868e073` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_cdc8e18eaea3289f11798ee2c0d014ecd22248973868e073 profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"59555","free_usage_limit_marker":true}]` |
| `deepseek-06` | 13 / 6 | 上游 429，免费额度限制 | `ocfree_ac9ea0387ee035d59f7605376902791c38ec7841ea75b5c6` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_ac9ea0387ee035d59f7605376902791c38ec7841ea75b5c6 profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"57649","free_usage_limit_marker":true}]` |
| `deepseek-07` | 14 / 7 | 上游 429，免费额度限制 | `ocfree_d69ac780a509851242c8b98d429adbfcecc1c4a2ceacf1be` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_d69ac780a509851242c8b98d429adbfcecc1c4a2ceacf1be profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"57119","free_usage_limit_marker":true}]` |
| `deepseek-08` | 15 / 8 | 上游 429，免费额度限制 | `ocfree_689384910ae181aa0b20fa793646c6491d31860f36ddb10a` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_689384910ae181aa0b20fa793646c6491d31860f36ddb10a profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"56621","free_usage_limit_marker":true}]` |
| `deepseek-09` | 16 / 9 | 上游 429，免费额度限制 | `ocfree_1dc8e174a8dee72f6674b6e1249d211872be8b561fe0e9c1` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_1dc8e174a8dee72f6674b6e1249d211872be8b561fe0e9c1 profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"56453","free_usage_limit_marker":true}]` |
| `deepseek-10` | 17 / 10 | 上游 429，免费额度限制 | `ocfree_dc8f765628a7da8116b4f549ead36bc923df4ca686fd6e9c` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_dc8f765628a7da8116b4f549ead36bc923df4ca686fd6e9c profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"58099","free_usage_limit_marker":true}]` |
| `deepseek-11` | 18 / 11 | 上游 429，免费额度限制 | `ocfree_87ff9e20ff1285c4f2717ff918ad910781bf049e408f8556` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_87ff9e20ff1285c4f2717ff918ad910781bf049e408f8556 profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"56562","free_usage_limit_marker":true}]` |
| `deepseek-12` | 19 / 12 | 上游 429，免费额度限制 | `ocfree_03918a65f3f63bc9bc95e1a7e0a4fc617bb6a33064647270` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_03918a65f3f63bc9bc95e1a7e0a4fc617bb6a33064647270 profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"57296","free_usage_limit_marker":true}]` |
| `deepseek-13` | 20 / 13 | 上游 429，免费额度限制 | `ocfree_d2ea2f99ddd3b9caf9bb2780fac20ed0fb3d7298f97760bf` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_d2ea2f99ddd3b9caf9bb2780fac20ed0fb3d7298f97760bf profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"56829","free_usage_limit_marker":true}]` |
| `deepseek-14` | 21 / 14 | 上游 429，免费额度限制 | `ocfree_9d2f62238328f999103d1b09408aa670b625cc1cadd863d7` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_9d2f62238328f999103d1b09408aa670b625cc1cadd863d7 profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"58653","free_usage_limit_marker":true}]` |
| `deepseek-19` | 26 / 19 | 上游 429，免费额度限制 | `ocfree_9f889055ed213c5ccd2f0f1880866b230c96f92577a263bf` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_9f889055ed213c5ccd2f0f1880866b230c96f92577a263bf profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"57780","free_usage_limit_marker":true}]` |
| `deepseek-20` | 27 / 20 | 上游 429，免费额度限制 | `ocfree_f264ff427f8ce1d12d3ffc7f43e682f058a5868cb6f03175` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_f264ff427f8ce1d12d3ffc7f43e682f058a5868cb6f03175 profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"58667","free_usage_limit_marker":true}]` |
| `deepseek-21` | 28 / 21 | 上游 429，免费额度限制 | `ocfree_735689b0887941d1ac4a1e3f59627ac0544006efdc2d0dd1` | `OpenCode free upstream failed (HTTP 429, request_id=ocfree_735689b0887941d1ac4a1e3f59627ac0544006efdc2d0dd1 profile=opencode-deepseek-v4.1-flash-free-text-balanced-20261027-v1)` | `[{"classified":"upstream","upstream_status":429,"error_type":"FreeUsageLimitError","error_message":"Free usage limit reached. Free models work in OpenCode (https://opencode.ai); subscribe to OpenCode Go to keep going: https://opencode.ai/go","retry_after":"59422","free_usage_limit_marker":true}]` |

归属核验：精确请求 ID 冲突 0，可比对账号 / 代理 / 模型字段不匹配 0。成功按固定账号路径、绑定、有效完整响应合同、`test_complete.success=true`、可见 `5` 和无 error 核验，HTTP 200 本身不足以证明成功。

## 首次 42 次测试和消费者记录（历史）

| 模型 | 首次固定实例 | 成功 | 免费额度 429 | 其他失败 | 首次成功实例 |
| --- | ---: | ---: | ---: | ---: | --- |
| `mimo-v2.6-flash-free` | 21 | 5 | 16 | 0 | 04、15、16、17、18 |
| `deepseek-v4.1-flash-free` | 21 | 5 | 16 | 0 | 04、15、16、17、18 |

首次 42 次诊断阶段没有人工清状态；随后根据用户追加指示执行了本报告前面的授权恢复与重测。因此“首次无手动清除”只描述首次阶段，不能用于整个任务。首次文件保持原样，详细错误与初始 / 最终冷却时间仍可在上一份私有报告追溯。

| 首次消费者模型 | 结果 | 账号 / 代理 / usage | 本站余额扣费 USD |
| --- | --- | --- | ---: |
| `mimo-v2.6-flash-free` | 成功 | 62 / 17 / 234 | 0 |
| `deepseek-v4.1-flash-free` | 成功 | 24 / 17 / 235 | 0.0000477 |

首次已匹配 settled 余额扣费合计 0.0000477 USD；本轮没有新增消费者 Key 请求或费用验证。固定账号 admin 诊断不使用消费者 Key，不产生同类消费者结算记录。余额整段变化不能据此排除其他并发生产流量。

## 配置、请求计数与证据边界

| 项目 | 数量 |
| --- | ---: |
| 首次固定账号诊断 | 42 |
| 首次消费者请求 | 2 |
| 首次已匹配消费者 attempt | 2 |
| 本轮授权恢复 / 清理管理请求 | 32 |
| 本轮固定账号重测 | 32 |
| 本轮人工模型请求 + 恢复管理请求（POST） | 64 |
| 本轮方法审计确认的模型上游派发 | 32 |
| 本轮消费者请求 | 0 |
| 首次窗口可见独立自动复验 worker ID（历史） | 21 |
| 本轮窗口可见独立自动复验 worker ID | 11 |

上述类别分开计数，自动探针不逐一强行归属到人工诊断；恢复请求也不是上游模型调用。后台探针可能延续旧流程或合并新冷却，窗口可见 ID 数不证明没有成功或未记录的其他探针。不把不同轮次可能重叠的日志观察简单相加成生产上游总数，也不排除其他生产流量。

| 独立复核项目 | 结果 |
| --- | --- |
| 目标恰为首次 32 个额度实例 | 通过 |
| 首批恢复与清除回读 | 通过 |
| 恢复 metadata 与访问日志精确匹配 | 通过 |
| 32 次重测前状态独立核对干净 | 通过 |
| 32 份保存响应文件 SHA | 通过 |
| 保存文件字节数 | 通过 |
| 保存文件 UTF-8 无损解码 | 通过 |
| 32 次结果独立重判 | 通过 |
| 32 次真实上游 429 与额度 marker | 通过 |
| 首次成功 10 账号无恢复 / 诊断访问 | 通过 |
| 42 账号四快照静态定义 | 通过 |
| 42 账号 status 保持 | 通过 |
| 42 账号手动 schedulable 保持 | 通过 |
| 21 代理定义保持 | 通过 |
| 分组配置保持 | 通过 |
| 无新增消费者生成或 Key 触碰 | 通过 |
| 已部署方法与核验源码吻合 | 通过 |
| 执行器账号配置不变（含账号 / 代理 / 分组 / 手动 schedulable） | 通过 |

访问日志独立核对：模型诊断 32 次，授权恢复 32 次；预期诊断 ID 缺失 0，预期恢复 ID 缺失 0，未归属修改性访问 0。排除的 10 个首次成功账号没有本轮模型测试或恢复访问。

运行时冷却、异常和使用时间可以因用户授权恢复、真实调用及后台探针变化；静态定义和手动标志复核与这些变化区分。此处不声称恢复 API 重置了模型上游额度。

独立复核全部 28 项检查通过。目标有效冷却数量：恢复前 32 → 首批清除后 0 → 重测后 32 → 复核时 32。变化字段仅记录为 `rate_limit_reset_at、rate_limited_at、updated_at`。

首次 SSE 执行器先 `response.text()` 后以 UTF-8 保存，SHA 校验对象是保存的文本文件字节；本轮重测通过 `response.arrayBuffer()` 保存 HTTP 响应体字节，SHA 针对该保存文件计算。两者均不被描述成 TLS 原始线缆捕获。admin parser 未设置 `ExpectedModel`，不声称严格核验上游模型 echo。

只验证最小文本对话，不覆盖 OpenClaw / DeepSeek Harness 的完整工具往返。所有可用性结论都是相应测试窗口的快照；已成功的历史观察也不保证之后持续可用。

私有目录 0700，脚本和三份衍生输出 0600；原始快照可能包含凭证，只读私有留存。

- 本轮：`report.json`、`wire/`、`before.private.json`、`cleared.private.json`、`after.private.json`。
- 本轮核验：`method-audit/evidence/correlation.private.json`、`method-audit/summary.json`、`review/summary.json`。
- 本轮另行核验：`root-offline-verification.json`；运行状态：`production-runtime-after.json`。
- 本轮衍生输出：`RESULTS.md`、`instances.csv`、`verified-results.json`。
- 首次历史报告：`../production-dual-group-20261105/RESULTS.md`；首次逐实例原始和核验文件均保持只读。

本轮 429 Retry-After 数值范围：56453–59619 秒；超过 12 小时 32 条。实际本地冷却截止见逐账号状态表。

测试后服务核验时间：2026-11-05 16:24:51（北京时间）；四服务全部健康 通过，容器与基线一致 通过；公开 `/health` HTTP 200。
