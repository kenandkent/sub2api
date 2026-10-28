# 共享账号市场运行手册

本手册用于实施后的准备与演练，不构成生产启用授权。功能默认关闭；真实上游调用、银行转账和生产灰度须另行批准并记录证据。一个用户可同时是消费者和供应者，但不能消费自己的共享账号；没有租户或账号租约。首期仅支持 OpenAI 共享文本 Chat Completions / Responses 的原生 HTTP，包括 function tools；WebSocket、图片等未支持能力应拒绝，不能绕过保护走普通计费。

## 启用准备

1. 部署包含全部资金兼容写入、数据库迁移及市场依赖注入的同一版本，备份数据库并演练恢复。保持 `marketplace.enabled`、`onboarding_enabled`、`dispatch_enabled`、`payouts_enabled` 均为 false；四项分别控制总入口、供应申请、派发和提现。simple mode 不支持启用市场。关闭新业务后仍须运行后台 outbox、缓存投影、收益成熟及月账单任务，直至在途工作完成；核对积压和告警，不要直接停掉处理进程。
2. 由运营明确设置 `fee_bps`（0–10000）、`hold_hours`（0–87600）、正整数 `max_supplier_concurrency`、非负且最多两位小数的 `minimum_payout_usd`；留空不代表已批准的商业条款。发布实际模型价格及缓存价格、适用策略、供应协议版本和正文或有效 URL，确认所需申请字段均非秘密。现有 `deploy/config.example.yaml` 为注释示例，其数值不构成运营决策。
3. `credential_key_id` 指向 `credential_keys_json` 中的密钥 ID，JSON 的每个值是稳定的随机 32 字节密钥的 Base64 编码；通过受控密钥管理分发并备份，勿写日志或证据。多实例必须使用一致密钥集。轮换前保留旧 ID/密钥以读取既有密文，添加新密钥并切换当前 ID，验证重启与解密；不要在确认旧密文已安全处理前删除旧密钥。丢失密钥可能使凭据及收款信息无法读取。`oauth_proxy_url` 仅由可信运营配置。
4. 在配置文件的 `marketplace.providers` 配置结构化白名单：平台、标签、可信 base URL、允许的账号类型和实际支持的模型，必要时指定运营管理的 proxy ID；每平台一条。该结构只通过 YAML 配置，不依赖 Compose 环境变量。四个常用 Compose 文件已传递 `MARKETPLACE_*` 开关、商业条款、协议与密钥变量；在使用其他部署方式前检查实际配置注入，勿把密钥明文提交到仓库。
5. 先逐用户调用管理员 `POST /api/v1/admin/marketplace/wallets/:id/migrate` 并全量对账，再允许其共享消费。`id` 是用户钱包所属用户 ID。旧正余额和冻结额为 `legacy_unverified`，不能自动认作现金；旧 batch 冻结必须关联原业务，无法映射时迁移失败并暂停该用户共享消费。按证据核验来源，禁止重复入账。新验证充值按订单拆分现金与该订单促销赠额（迁移 269）；赠额不能当现金。入账和释放优先抵债。检查 `sum(lot.remaining) - debt = balance + frozen_balance` 与 `sum(lot.reserved) = frozen_balance`。
6. 269 只约束之后的入账；若曾运行过早期原型，审计既有充值和赠额来源并制定修正方案，不能假定迁移追溯修复。当前功能尚未部署时也要将此检查列入演练记录。
7. 完成 [验收清单](MARKETPLACE_ACCEPTANCE.md) 与审核人员、对账、告警、密钥恢复演练后，按总开关、准入、派发、提现分阶段由获授权人员启用，每一步先核验配置、日志及账目；生产启用另需明确授权。

## 日常审核和资金处理

- 供应者使用 API key 或 OpenAI 官方服务端 PKCE OAuth 授权，审核供应申请、协议和账号状态后上架。不要索取或记录密码、token、提示词正文。现有身份判断不保证识别与平台自营账号或影子账号的重复凭据；不同 API key 也可能属于同一上游账号，无法确认时人工审核，勿承诺完全去重。
- 可信实际 usage 才可结算：缓存命中使用缓存价，避免重复计费；不得以估算量生成收益。消费者实际扣款 C、费率 b（bps），供应者 S = round_half_up(C × (10000 − b) / 10000, 8)，平台 F = C − S。保留价格和费率快照，按流水对账。
- 后台每分钟扫描一次，最多锁定 100 个超过两小时未更新的 prepared / budget_reserved / dispatched / usage_pending receipt 并转为 needs_review；跳过正被事务锁定的行。两小时只是滞留判据，不证明上游未执行；扫描不解冻、不改 usage、不重发模型。已记录的可信 usage 仍可正常捕获。无任何 attempt/capture 的滞留 receipt 也必须由强二次验证的管理员附证据人工释放；有历史 attempt 的空引用不得套用此释放路径。关注 receipt 状态及最长滞留、冻结龄期、usage 缺失、账务差额、outbox 与缓存投影积压、成熟延迟及提现龄期。对 `usage_pending` / `needs_review` 的不确定请求保留冻结，目标 24 小时内初审、72 小时内处理或说明延期；核对上游及原始证据，通过强二次验证的管理员人工处理并留审计。没有可信证据不能编造 usage 或收益，也不能为补账自动重发上游请求。实际费用超预算且无法补足时进入审核，不可把未收款变为可提现收益。
- 消费退款通过 receipt 冲正，累计不超过原扣款，多次部分退款按累计结果计算并在全额时收齐舍入尾差；不足部分形成供应者债务并冻结提现。充值退款使用对应订单未消费、未冻结来源，并按退款比例处理该订单赠额，不能借用其他订单赠额。退款和拒付合计不得超过原订单上限；核对部分及全额退款、赠额与舍入尾差。
- 外部拒付须先人工核验支付方事件和证据，通过 `POST /api/v1/admin/marketplace/funding-chargebacks` 登记；必须使用活动管理员 JWT 和强制 TOTP 二次验证。请求字段为 `order_id`、入账冲销金额字符串 `credited_amount`、稳定的 `provider_event_id` 与必填 `evidence`，订单所有者从数据库读取。相同事件只能重放相同意图；不得为重试换事件号。`credited_amount` 是财务按原订单和拒付证据核定的应冲销入账余额（USD，含适用赠额），与支付方原始扣款可能不同。接口不做支付金额、手续费、赠额或汇率的自动换算；映射未经确认时先暂停，不能直接抄支付方金额。当前没有自动支付方拒付 webhook 集成。不可把退款恢复接口误当拒付入口。涉及已消费资金时核对买方债务和平台损失，不自动撤走正常履约供应者收益。
- 提现是人工银行转账，系统不连接银行。先核验收款账户，管理员查看完整信息须提供原因；界面显示 60 秒，避免复制到日志或截图。审核通过后标记 processing，再由授权人员核对收款人与金额并转账一次，记录唯一外部交易引用和付款证据后确认。不得审批或确认自己的提现。若外部结果未知，保持 processing 和冻结，查证后才确认付款或确认失败，禁止盲目重付。操作超时先按原幂等键查询状态。
- 退款、拒付或其他风险可产生债务及提现冻结；释放资金先抵债。仅债务为零且完成审计核验时由管理员解冻，记录原因与操作人。

## 关闭与恢复

关闭 dispatch / onboarding / payouts 以停止相应新增动作，必要时关闭总开关；保留数据库、密钥、在途收尾、退款、对账和账单能力，监测处理完成。若出现异常，暂停新增派发并保留不确定冻结与付款证据，定位后再恢复。资金迁移后禁止回退到会绕过 funding ledger 写余额的旧版本；回退须使用兼容写入版本并先验证迁移、未完成请求及余额恒等式。不要删除密钥或账务数据作为回退手段。

## 旧余额写入口核查矩阵

迁移 253 的数据库兼容触发器覆盖已迁移用户的 `users.balance` / `frozen_balance` 写入；269 替换其充值来源分类。资金仓储使用事务内标记避免重复过桥。下表记录代码路径与来源规则，不能用它代替生产数据逐用户对账。

| 写入路径 | 来源 / 处理 | 主要核验 |
| --- | --- | --- |
| 验证充值、充值关联兑换 | 按原订单现金与该订单 payment_bonus 拆分；先偿债 | `TestLegacyPaymentBonusKeepsCashAndGiftSeparate`、`TestPaymentBonusRefundReservesBothOrderSources` |
| 普通兑换码、注册及首绑赠额、优惠码、邀请返佣转入、后台调增余额 | 无经核验订单引用时按 promotional 兼容来源，不能自动转成可结算现金 | `TestWalletFundingLegacyMigrationAndBalanceWrites`，已有兑换/返佣/用户测试 |
| 后台 SetBalance / 旧自营扣款 | 对应增减来源；旧自营透支形成 debt，不消费共享已冻结资金 | `TestWalletFundingLegacyHoldAndOverdraftDoNotStealReservedMoney`、既有 UserRepoSuite |
| 旧 batch 冻结、捕获及释放 | 原业务 hold 只映射一次；不明冻结拒绝迁移；释放先抵债 | `TestWalletFundingLegacyMapsExistingBatchHoldExactlyOnce`、`TestWalletFundingLegacyUnmappedHoldFailsClosed` |
| 共享预冻结、实际捕获、消费退款 | 直接使用同 SQL 事务的来源分配及不可变账本；退款回原来源、先偿债 | `TestMarketplaceBillingCaptureAtomicReplayAndConservation`、`TestMarketplaceRefundRepaysBuyerDebtBeforeRestoringLot` |
| 充值退款、拒付 | 仅原订单来源，现金/赠额分开；退款加拒付累计上限；外部结果未知保留冻结 | `TestPaymentFundingRefundRejectsRestoredCashAfterChargeback`、`TestPaymentRefundAndChargebackSerializeAgainstOrderCap` |

生产数据库备份恢复、历史异常来源人工认定、多实例密钥轮换恢复属于启用前演练；本次隔离集成测试没有替代这些操作。CI 显式执行 `unit integration` 组合标签的真实基础设施网关测试，单独运行两个标签不会选中该文件。

## 误判未执行后的异常登记

若人工解冻后发现真实执行证据，在管理后台“资金对账 → 执行异常登记”填写 receipt UUID、attempt 编号、原解冻审计操作编号和受控凭证引用。也可调用 `POST /api/v1/admin/marketplace/receipts/:id/execution-incidents`，用 `Idempotency-Key` 固定登记操作，正文为 `attempt_id`、`release_operation_id`、`evidence`；同路径 GET 查询已登记记录。写入和查询均要求活动管理员及强制二次验证。未知响应先查询或使用原操作键和原证据重试。

登记只保存不可变异常和报告的关联，不证明 provider usage 可信，不做消费者二次扣款、不自动生成供应收益，也不生成手续费。补偿金额由人工核定；本版本没有自动补偿、银行付款或补偿退款流程。证据不要包含账号密钥、收款明文或完整对话。历史解冻审计未保存 attempt 编号时须人工核对关联，不可将登记当作自动结算依据。

## 升级注意

调度缓存的内部元数据必须保留 `OwnerUserID` 供共享账号资格判断；插件和公开 DTO 仍不返回该字段。升级前停止旧版本实例写缓存，等待新版本 scheduler 完整重建快照。旧缓存没有归属时会失败关闭并可能短暂返回 503；不要把去除归属校验作为绕过手段。迁移 270 为滞留扫描建立仅覆盖未完成请求的索引，避免每分钟扫描完整历史。
