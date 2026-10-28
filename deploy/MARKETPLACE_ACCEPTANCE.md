# 共享账号市场验收清单

状态：**待执行、待记录证据，未批准生产启用。** 本清单不表示计划 T01–T17 均已完成。记录执行版本、日期、执行人、测试名称和数量、退出码、证据路径；通过、失败、跳过和未执行分别标注。脱敏截图和日志仅放项目内 `deploy/marketplace-evidence/`，不得包含凭据、收款明文或提示词正文。

## 发布门槛

- [ ] 使用隔离数据验证数据库迁移、兼容写入、旧 batch 冻结映射及全量资金恒等式；无法映射时失败关闭。演练关闭新派发后继续收尾、退款和账单；确认不能回退至绕过资金账写余额的版本。
- [ ] 总开关默认关闭，simple mode 拒绝启用；开关独立、旧自营路由及普通计费回归；多实例密钥、备份恢复、价格及商业条款、协议与受信供应白名单经运营确认。
- [ ] 同一用户切换消费者/供应者身份；不能调用自身共享账号；申请、审核、暂停、重启及账号资格检查生效。OAuth 是 OpenAI 官方服务端 PKCE；核验不能保证覆盖自营/影子凭据或不同 API key 指向同一账号的重复情况，人工复核该风险。
- [ ] Chat / Responses 文本流式、非流式、function tool、缓存实际 usage；不支持的能力失败关闭。无 usage、客户端中断、上游结果不确定、捕获未知、队列/Redis 故障和崩溃恢复时不虚构收益、不重复上游派发或解冻；并发多实例不超账号槽位。
- [ ] 核对 C、S、F 八位精度和守恒、重复操作幂等、价格快照及账务平衡。核验迁移 269 之后的订单现金与该订单赠额分离；审计可能存在的历史原型数据。验证充值退款与拒付累计上限、部分/全额退款赠额比例、舍入尾差、跨订单赠额隔离，以及退款与消费/提现竞争。
- [ ] 提现收款账户验证、查看原因与 60 秒展示、强二次验证、自我审批拒绝、唯一外部交易引用；processing 未知时保持冻结且不重复转账。退款后债务、释放先抵债、债务为零后的审计解冻、成熟和月账单均对账。模拟转账不代表银行转账验证。
- [ ] 外部拒付的人工核验证据和服务接口流程已由运营确认；当前无自动支付方拒付 webhook 和管理员拒付路由。模型消费退款与充值退款分别核对，不把退款恢复当作拒付入口。
- [ ] 在 360、768、1440px 的明暗主题检查模式菜单、概览、账号向导、收益、提现和管理审核；覆盖键盘、焦点返回、未保存表单、长模型名/小数、空态和错误态。记录截图、浏览器与结果。
- [ ] 对照设计文档 R01–R12 为每项附测试名及证据，记录遗留问题、负责人和阻断决定；不凭一次页面访问或模型调用认定资金链完成。

## 本地验证命令与证据

在项目内隔离环境执行，不使用开发或生产余额。需要 Docker/Testcontainers 的测试必须确认连接到隔离实例；命令没有匹配测试、跳过或缺少依赖均不能记作通过。正常开发环境可直接使用 `go`；若团队环境使用 Go wrapper，确保其行为与相同版本的 `go` 等价，并记录实际命令。以下命令是待执行清单，最终结果由执行人填写。

```sh
# backend 目录
go test -tags=unit ./internal/service ./internal/repository ./internal/handler/... ./internal/server/... -run 'TestSupplier|TestMarketplace|TestWalletFunding|TestCredentialEnvelope'
go test -tags=integration ./internal/repository -run 'TestSupplier|TestMarketplace|TestWalletFunding|TestPaymentBonus|TestPaymentFundingRefund|TestPaymentRefundAndChargeback'
go test -tags='unit integration' -v -timeout=300s ./internal/handler -run TestMarketplaceGatewayRealInfrastructure

# 项目根目录
make test-backend
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
pnpm --dir frontend run check:i18n
pnpm --dir frontend run test:run
pnpm --dir frontend run build
make test-frontend-critical
git diff --check
```

真实基础设施网关测试使用隔离 PostgreSQL、Redis 和 HTTP handler，但其上游是测试替身，catalog 和 scheduler 使用 fixture/桥接，不能视为完整生产认证、真实上游或银行端到端验证。计划中的 `TestE2EMarketplace` 文件/命令须先确认实际存在且匹配测试，不能用零测试通过替代。前端测试、最终回归和视觉证据尚待本次最终执行结果；不得把先前日志作为当前提交的完成证明。

| 范围 | 版本 / 环境 | 测试名、数量及结果 | 证据路径 / 负责人 |
| --- | --- | --- | --- |
| 资金与回归 | 待填写 | 待填写 | 待填写 |
| HTTP、PostgreSQL、Redis | 待填写 | 待填写 | 待填写 |
| 前端、i18n、构建 | 待填写 | 待填写 | 待填写 |
| 视觉、迁移与回退演练 | 待填写 | 待填写 | 待填写 |
| 运营配置、审核和付款演练 | 待填写 | 待填写 | 待填写 |
