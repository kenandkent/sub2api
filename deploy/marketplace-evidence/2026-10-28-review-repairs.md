# 未推送分支审查修复记录（2026-10-28）

本轮修复基于 `bddb7cbcef6867d64b756d6ffe76b41242715bea`，对应此前 30 项审查发现。本轮 30 项修复及交叉复审追加问题已完成本地验证；测试范围和最后增量检查分别列明。使用五个 GPT-6.1 Terra / ultra 子 agent 和独立 worktree，主工作区集成并交叉复审；不修改线上服务，不调用真实模型或银行。

## 修复映射

| 项目 | 修复内容 | 回归依据 |
| --- | --- | --- |
| F01 | 分享和更换密钥统一使用后端 `apikey` 枚举 | 真实 options DTO 的向导、凭据和详情 Vitest |
| F02 | 账号槽位跟随上游执行，期限小于 Redis 槽位 TTL；慢读取写期限；清除连接期限；日志 writer 按代际安全转发 | PG/Redis 取消竞争、真实慢读取 socket、同连接后续请求、race lease 测试 |
| F03 | 按经核验的上游总计费输入硬上限冻结；缺少可信上限、超预算目录及无法界定输入预先拒绝 | 配置、网关服务和 handler 输入边界测试 |
| F04 | 旧退款基线纳入退款与拒付累计上限，不重复计算现代冲销 | 历史退款/现代拒付、混合退款和并发 PG 测试 |
| F05 | 拒付同步买方可用/债务账本，兼容修正原型旧分录，加入独立买方账本对账 | chargeback repair 与 finance regression PG 测试 |
| F06 | 每次余额变化持久化失效任务；已迁移用户准入读取权威余额 | balance admission/invalidation PG 测试 |
| F07 | 实现带审计、幂等、订单及唯一兑换证明绑定的旧来源重分类；当前余额来源须人工核验 | funding admin PG 测试含退款、拒付、凭证复用、时序、管理员自处理及交叉处理竞争 |
| F08 | 以 receipt UUID 查找已结算请求、显示退款额度并提供退款入口 | admin receipt PG/handler 测试与 reconciliation Vitest |
| F09 | 分享提交绑定真实政策版本；发布政策事务内生成站内通知 | 目录/账号 PG 测试、向导条款竞态 Vitest |
| F10 | 供应申请审核带 expected_version 和审核快照 | supplier security HTTP/PG 与审核 Vitest |
| F11 | 关闭总开关后保留供应者财务历史读取和模式入口 | workspace/workMode、账号/账单页面 Vitest |
| F12 | 供应暂停与独立提现冻结分离 | payout capability PG 与服务测试 |
| F13 | 管理员强二次验证分批重加密账号及收款凭据；保持业务元数据；审计屏蔽正文 | vault rotation PG/handler、审计和 OAuth 刷新竞争测试 |
| F14 | 收款信息创建/核验要求合格供应者身份 | payout capability PG/handler 测试 |
| F15 | 完成请求解析和构造后、紧邻真实 HTTP 派发前记录 attempt | malformed/local-error 网关 PG/Redis 测试 |
| F16 | 原始请求字节摘要；按主体/Key/组查终态；额度耗尽可受限重放，不能新派发 | replay middleware/repository/handler 与真实进程冷缓存 E2E |
| F17 | 超交付缓冲明确错误；失败终态可信 usage 可结算；不确定流式结果发送错误事件与 receipt | 大响应、失败终态及流式错误 PG/Redis 测试 |
| F18 | 账号创建、OAuth、配置重试保存无敏感明文的恢复标识，避免结果未知后换键重做 | 账号向导/详情 Vitest |
| F19 | 收款信息创建使用稳定操作键恢复 | payout destinations Vitest |
| F20 | 二次验证取消且未派发时解除本次操作状态 | supplier/admin payout 与操作弹窗 Vitest |
| F21 | 账单请求按用户与请求代际丢弃旧响应 | statements Vitest |
| F22 | 接受协议相对路径，与配置契约一致 | onboarding/wizard Vitest |
| F23 | 月账单期初/期末债务和抵债收益按封存账本截止点计算并导出 | statement debt PG/服务契约与 CSV/UI 测试 |
| F24 | 管理员查看和按版本解除买方风险；未偿债不可解除 | funding admin 风险 PG/handler 测试 |
| F25 | 成熟任务连续有界批处理；同连接持有协调锁，支持连接池 1 | maturity worker PG 竞争和单连接测试 |
| F26 | 未完成队列索引和独立工作循环；坏记录/行锁超时记录失败并延迟重试，不阻塞后续供应者 | 成熟 poison/locked-wallet PG 与 worker drain 测试 |
| F27 | 250/265 先准备后并发索引/约束验证；仅兼容明确已知原型校验和；恢复中断索引 | fresh/prototype/retry/concurrent migration PG 测试 |
| F28 | 分支 CI 增加前端全量 Vitest，保留原固定关键集 | 工作流与本轮前端全量命令 |
| F29 | 对账显示买方账本差额、缓存/成熟积压、成熟失败数量及龄期 | reconciliation PG、服务契约和 UI 测试 |
| F30 | 指纹字段改为事实语义，审核要求人工确认上游身份 | identity policy/HTTP、审核 Vitest |

## 交叉复审中补充修复

- 历史资金重分类防止复用一张兑换凭证对应多笔订单；显式记录“当前未消费旧余额仍归属该订单”的人工确认，不以付款凭证推定剩余资金。
- 管理员同时是买方，以及两名管理员互相处理余额的行锁按统一顺序获取，真实 PostgreSQL 死锁复现由失败变为通过。
- Vault 密文重写与在途 OAuth 刷新竞争时，仅对完整解密内容未变的记录做有限 CAS 重试；刷新采用所有者标识和原子解锁，总工作期限短于 Redis 锁 TTL。旧刷新不能覆盖重新授权或删除的账号。
- 成熟任务坏记录与被锁钱包有独立退避和告警，保持该笔收益待处理，允许后续供应者推进。
- 管理工具的错误协议区分“已确认无变更”“输入尚未检查既有操作”“幂等冲突”和“结果未知”；结果未知必须保留操作键。跨标签页协调与持久恢复只存标识和加盐摘要，正文需重新输入；按实际 JSON 编码长度校验请求。
- 真实主程序集成补齐 Wire 的重放 resolver 和管理入口。现有池化日志 writer 以持有代际的方式转发写期限，不暴露可被旧请求复用的底层 writer。集成测试容器在 TestMain 返回退出前清理。

## 明确边界

本轮不引入租户、账号租约或用户独占；继续使用请求并发数。运营仍须核实模型硬上限、价格/倍率与现金覆盖。历史未记录来源的消费不能由软件反推；时间证据异常保持拒绝。任意数据库/提供方故障下的一次性 OAuth token 恢复不在保证范围。密钥轮换和 Redis 刷新锁升级要求先排空旧实例；250/265 拆分迁移的回退须使用兼容二进制或恢复备份。生产等量数据的锁时长、真实资金/上游和完整视觉巡检仍是上线前演练项。

## 最终验证

本轮全部下列命令退出码为 0，详细范围及日志 SHA-256 见 [本轮验证清单](review-repair-manifest.json)。日志保留项目内 `.superpowers/sdd/2026-10-28-marketplace-review-fixes/`；不提交原始测试全文。

- 后端 `go test ./...` 完整默认套件；`-tags=unit` 的 service、handler、server、config、cmd/server 套件通过。
- repository 完整 PostgreSQL/Redis 集成套件通过；最后重分类幂等/管理员锁顺序、成熟退避、OAuth 所有者锁与密钥轮换竞争增量检查通过。
- `unit integration` 组合标签的真实 handler 网关测试及响应写期限回归通过。子 agent 的池化 writer 测试也通过 race 检查。
- 最终二进制在仅回环网络的 macOS 沙箱中，用临时 PG/Redis 和模拟上游执行 Chat/Responses 普通、流式、function tools 六类请求；六次真实 usage 计费、C=S+F、无重复 capture、无遗留冻结/槽位。工具优先及普通请求优先两种顺序均通过。额度耗尽且余额归零后重启应用清除内存鉴权缓存，原操作仍返回 settled receipt，未新增上游调用。
- 前端 **399 文件、3,066 测试**全量通过；随后一处多余分号修正后的资金管理/对账/i18n **31 测试**通过。TypeScript、生产构建和完整 ESLint 通过。
- 后端完整 `golangci-lint` 为 **0 issues**；Wire 生成、路由权限与审计敏感正文回归通过；`git diff --check` 通过。
- 部署脚本 8 个、开发环境 11 个 Python 测试，以及 CI 中 Compose 依赖/生产/安全/环境变量/资源与 Caddy 检查通过。新增超时变量在四种 Compose 中按 300 秒默认值注入，市场开关仍默认 false。

生产启用前事项继续见 [验收清单](../MARKETPLACE_ACCEPTANCE.md)。本轮结果不代表生产数据迁移、真实上游或银行转账已验证；没有部署线上服务。
