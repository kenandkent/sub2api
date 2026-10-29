# OpenCode 接入兼容性核查（2026-10-29）

核查对象：当前 `mysubapi` 的 Bun transport；这是本地代码核查，不是“全部免费模型已修复”的验收声明。尚未修改其请求伪装/模板，也未调用真实免费模型。

## 已确认的问题

- `backend/internal/service/opencode_free_bun_request.go` 仅接受映射后的 `big-pickle`，并在发送前把模型固定为该值。其他模型会在本地400失败，不能归因于上游配额。
- 同一文件仅接受字符串文本消息和 `system/user/assistant`，不支持 `role=tool`、工具回传和多模态；非空 `tools` 被直接拒绝，`tool_choice` 仅允许 `none`。`max_tokens` 等常见第三方字段也不在白名单。
- `deploy/opencode/worker/worker.ts` 再次验证固定 profile/hash/model，并注入旧模板的 system/tools、固定 `tool_choice=none`。它不是把已经接收的用户工具成功透传；非空用户工具在后端已经被拒绝。
- `opencode_free_bun_parser.go` 还会拒绝真实的 tool_calls 和非 stop 终止原因。只修改请求白名单会造成请求和响应能力不一致，也不能保证计费完整。
- Console 模型目录与实际 transport 能力不同。模型出现在列表、别名可解析，均不代表普通/工具/续接/流式请求已通过。

截图中的 OmniRoute 是另一个实现。已有诊断记录中，MoviePilot 使用 `oc/deepseek-v4.1-flash-free`，普通请求就被 OmniRoute 本地的 transport 白名单拒绝；修复本仓库不会自动改变那个服务。其线上变更和完整 MoviePilot Agent 验收不能由本地 Bun smoke 替代。

## 官方公开能力与授权边界

[官方模型文档](https://dev.opencode.ai/v2/docs/console/models) 当前列出九个免费聊天模型：`big-pickle`、`glm-5.3-flash-free`、`deepseek-v4.1-flash-free`、`beam-free`、`flint-40b-free`、`mimo-v2.6-flash-free`、`nemotron-3-ultra-free`、`nemotron-3.5-lightning-free`、`nemotron-3.5-super-vl-free`。同页明确要求免费聊天模型通过 OpenCode 使用，其他客户端采用付费 Console 或 Go。

另一免费模型 `jev-1.13-free` 使用 System One 结构化接口，官方展示了 Console API key 接入，不能当成第十个通用聊天模型。项目现有 TypeSafe/System One 路由也不等于已支持这一 Console 模型。

本轮不会扩展伪装客户端以绕过免费聊天访问限制。对于经正式授权开放给第三方的模型，可以独立做标准API模型发现、能力声明、请求工具透传、工具结果续接、流式解析与真实usage计费兼容；不能以支付模型替换免费模型却仍声称满足原需求。

## 后续完整验收应覆盖

每个明确获准接入的模型分别测试普通对话、function tools、tool结果续接、流式及非流式、length/tool_calls终态、缓存/推理usage与计费。模型目录更新不等于验收成功。

应保留错误来源、HTTP状态、Retry-After，区分本地未支持、协议错误、上游鉴权/客户端限制、上游真实429。当前代理路径的非200可能来自代理 CONNECT 拒绝而非真实上游，因此保守归类为502 transport错误；没有来源证明时也不能直接改成“上游额度耗尽”。没有对照证据时不能把工具字段认定为429原因，也不应自动重发可能已执行的工具请求。

MoviePilot 的验收必须从实际客户端完整 Agent 对话发起，确认工具参数和调用ID传递正确；不执行真实有副作用的工具操作。用户当前明确要求继续使用 OmniRoute 的 OpenCode free provider；本轮没有将其切换到付费模型或其他提供方，也未修改该线上配置。
