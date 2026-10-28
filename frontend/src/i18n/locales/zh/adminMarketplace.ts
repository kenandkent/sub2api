export default {
  maturity_failures: '收益结算失败数', maturity_oldest_failure_age_seconds: '最早收益结算失败（秒）',

  retryPayloadMismatch: '输入内容与原操作不一致，请重新输入相同内容后重试。',

  identityReviewed: '我已审核此账号的身份及所有权证据。',

  lookupReceipt: '查询收据', capturedAmount: '已扣款', refundedAmount: '已退款', refundableAmount: '可退金额', buyer_ledger_differences: '买方账本差异', cache_backlog: '缓存待处理', cache_oldest_age_seconds: '最早缓存事件（秒）', maturity_backlog: '收益结算待处理', maturity_oldest_age_seconds: '最早待结算项（秒）',

  incidentReceipt: '请求记录编号（UUID）',
  executionIncident: "执行异常登记",
  incidentRecordOnly: "这里只登记异常与证据，补偿金额由人工核定。不会再次扣除消费者余额，也不会自动计入供应者收益。",
  incidentAttempt: "上游尝试编号",
  incidentRelease: "原解冻操作编号",
  incidentEvidence: "核验证据或受控凭证引用",
  incidentAssociation: "关联信息由管理员报告，登记本身不代表已核实模型用量。请勿粘贴账号密钥、收款明文或完整对话。",
  incidentUnknown: "登记结果尚未确认。查询记录或重试原操作将使用同一操作编号和证据。",
  incidentRecorded: "异常已登记，待人工核定补偿；资金与收益未自动变更。",
  incidentRecords: "查询异常记录",
  incidentEmpty: "该请求暂无异常记录。",
  incidentNew: "新建登记",
  incidentRegister: "登记异常",

  freezePayouts: '冻结提现', unfreezePayouts: '解除提现冻结', freezeHint: '请填写原因。解除冻结要求供应者没有未偿债务。',

  title: '共享市场', kind: '类型', difference: '差额', statusFilter: '状态筛选', filter: '筛选', receiptStatuses: '请求状态', unbalanced_transactions: '不平衡交易', wallet_differences: '钱包差异', funding_differences: '资金来源差异', aged_outbox: '滞留发件箱', uncertain_payouts: '结果未知的提现', migrateConfirm: '确认迁移并核对该用户钱包？请检查操作后的余额。', eligible_available: '共享可用余额', reserved: '冻结余额', excluded_available: '不可用于共享余额', debt: '债务', migrated: '已迁移', reconciled: '已对账',
  suppliers: '供应者申请', offers: '上架审核', policies: '共享市场政策', payouts: '提现管理', reconciliation: '资金对账',
  reviewHint: '查看申请详情并为每项决定记录理由。', id: '编号', status: '状态', account: '账号', supplier: '供应者', details: '详情', actions: '操作', review: '审核', agreement: '协议版本', frozen: '提现已冻结', notFrozen: '提现未冻结', enabled: '已启用', disabled: '已停用', version: '版本', decision: '决定', reason: '理由', active: '启用', approved: '通过', rejected: '拒绝', suspended: '暂停', confirmReview: '确认审核', reloadLatest: '重新加载最新版本', versionConflict: '记录已变化。输入已保留；请刷新并重新核对最新版本。', loadError: '加载失败。', actionError: '操作未能完成。重试前请核查当前状态。',
  policyScope: '发布的版本仅适用于后续请求，已有财务记录保留原版本。', commercialTerms: '商业条款', fee_bps: '平台费率（基点）', hold_hours: '等待期（小时）', max_supplier_concurrency: '供应者最大并发', minimum_payout_usd: '最低提现金额（美元）', priceBook: '价格表', group: '分组', chooseGroup: '选择分组', addGroup: '添加分组', noGroups: '价格表尚无分组。请添加分组并配置模型和价格。', protocol: '协议', remove: '移除', public_model: '公开模型', actual_model: '实际模型', context_limit: '上下文上限', max_output_tokens: '最大输出 token', rate_multiplier: '费率倍率', from_tokens: '起始 token', input_per_million: '输入 / 百万', output_per_million: '输出 / 百万', cache_read_per_million: '缓存读取 / 百万', cache_write_per_million: '缓存写入 / 百万', addTier: '添加阶梯', addModel: '添加模型', removeModel: '移除模型', publish: '发布新版本', publishConfirm: '确认发布这些条款和价格供后续请求使用？已有交易保留记录的版本。', invalidPolicy: '请完整填写条款、模型上限、倍率和每个阶梯价格。价格最多八位小数，允许明确填写零。',
  payoutHint: '操作前请核对提现状态和付款凭证。', payoutDetails: '提现详情', supplier_id: '供应者编号', amount: '金额', currency: '币种', created_at: '创建时间', paid_at: '付款时间', evidence: '凭证', external_reference: '外部流水号', approve: '通过', reject: '拒绝', start: '开始付款', confirm: '确认已付款', fail: '确认付款失败', processingWarning: '付款处理中。外部结果未知时应继续冻结资金并调查；仅在有凭证时确认付款成功或失败，不要重复转账。', selfApproval: '不能审批或确认自己的提现。', actionConfirm: '请核对当前状态和凭证后确认操作。', retrySame: '结果可能未知。重试前请刷新记录；重试将使用原请求和操作键。', retry: '重试', checkStatus: '查询当前状态', retryAction: '重试相同操作', confirmAction: '确认操作', destinations: '收款方式', masked_label: '脱敏收款信息', verified: '已验证', verified_by: '验证人', verified_at: '验证时间', verification_evidence: '验证凭证', reveal: '查看凭据', verify: '验证收款方式', revealHint: '请输入审计理由。凭据最多显示一分钟，关闭对话框后立即清除。', verifyHint: '记录证明此收款方式属于供应者的凭证。',
  reconciliationHint: '查看请求状态、投影差额和滞留时间，仅凭证据处理不确定的请求。', projection_diff: '投影差额', age_seconds: '滞留秒数', reconcile: '处理请求', refund: '消费退款', receipt: '请求凭据', receiptChanged: '该请求已不在当前页或其状态已变化。继续操作前请核对当前记录。', receiptWarning: '不得虚构未知用量或盲目重新派发。仅在证实未执行时释放，或在已知用量时重试结算。', release_confirmed_unexecuted: '释放：已证实未执行', retry_known_usage: '重试：用量已知', migrate: '迁移钱包', statement: '生成账单', userId: '用户 / 供应者编号', month: '账单月份', completed: '已完成。'
}
