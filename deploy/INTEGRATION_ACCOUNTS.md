# 三环境联调账号

核验时间：2026-10-31 20:06（Asia/Shanghai）。以下是专用普通用户的配置快照；余额和上游可用性会随使用变化。

## 账号与接入地址

三套环境使用邮箱 `ai@hostmails.de`，角色为 `user`，唯一授权分组和模型均为 `deepseek-v4.1-flash-free`。API Key 名称均为 `OpenClaw-local-deepseek`，各环境的 Key 独立。三套账号均无活动订阅。

| 环境 | 网页入口 | OpenAI 兼容 Base URL | 用户 ID | 分组 ID | Key ID | 核验时余额（USD） |
| --- | --- | --- | ---: | ---: | ---: | ---: |
| 本地开发（Air / Vite） | `http://localhost:3000` | `http://127.0.0.1:18080/v1` | 29 | 29 | 28 | 1,000 |
| 本地测试（完整 Docker Compose） | `http://localhost:8080` | `http://127.0.0.1:8080/v1` | 4 | 4 | 3 | 1,000 |
| 生产 | `https://sub2api.vseeu.de` | `https://sub2api.vseeu.de/v1` | 3 | 3 | 5 | 999.99998335 |

登录密码和三套完整 API Key 保存在本机私有文档 [`.dev/INTEGRATION_ACCOUNTS.md`](../.dev/INTEGRATION_ACCOUNTS.md)，文件权限为 `600`，由现有 `.dev/` 规则排除 Git 跟踪。该文件仅在已完成账号配置的本机存在，新克隆仓库需另行取得凭据。

开发、测试环境分别通过官方充值码兑换路径充值 1,000 USD 测试余额，充值记录 ID 均为各自数据库的 `1`、状态 `used`。固定操作码可避免重复入账，具体值保存在私有文档。生产沿用此前的充值与 Key；本轮仅读取核验生产账号。

## 使用与启动

API 客户端填写对应环境的 Base URL、该环境 Key，以及模型 `deepseek-v4.1-flash-free`。调用协议为 Chat Completions，路径为 `/v1/chat/completions`。开发前端 `localhost:3000` 经 Vite 代理到 Go 后端；直接 API 调试使用 `127.0.0.1:18080/v1`。

启动方式见 [本地开发环境](LOCAL_DEVELOPMENT.md) 和 [环境与网络参考](ENVIRONMENT_REFERENCE.md)。原生开发需同时运行 Air/Vite 和独立 Bun worker；完整 Docker 测试需运行 Compose worker 及 Mac TCP 中继。

三套数据库、账号余额和消费 Key 互相独立；它们的模型实例使用相同固定上游出口，上游免费模型的限流可能跨环境相互影响。本次没有更改本机 OpenClaw 默认模型，仍保留此前恢复的 Mimo。

## 本次验证结果

- 开发、测试：普通用户登录成功，余额各为 1,000 USD，仅授权对应 DeepSeek 分组；Key 为 active 且绑定该分组，无活动订阅。`GET /v1/models` 返回 HTTP 200，模型列表仅含 `deepseek-v4.1-flash-free`。
- 生产：普通用户登录、分组和 Key 绑定核验通过；`GET /v1/models` 返回 HTTP 200，但列表当前为空。此前两轮 Agent 请求已使 21 个实例收到上游 429 并进入冷却，日志中的恢复时间约为 2026-11-01 08:00（Asia/Shanghai）。
- 本轮没有发起真实模型推理请求；以上验证证明账号与接入配置，不能代替上游推理成功验收。模型列表可见也不保证上游允许真实调用。

脱敏核验记录保存在本机 `.cache/local-integration-accounts-20261031/verification.json`，包含环境、账号/分组/Key ID、余额、模型列表及充值状态。

不触发推理的开发环境连通性检查：

```bash
curl --noproxy '*' --fail-with-body \
  -H 'Authorization: Bearer <开发环境 API Key>' \
  http://127.0.0.1:18080/v1/models
```
