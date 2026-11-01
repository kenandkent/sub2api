# 三环境联调账号

核验时间：2026-11-01 11:15:14（Asia/Shanghai）。以下是专用普通用户的配置快照；余额和上游可用性会随使用变化。

## 账号与接入地址

三套环境均使用邮箱 `ai@hostmails.de`，角色为 `user`，授权分组为 `deepseek-v4.1-flash-free` 和 `mimo-v2.6-flash-free`。`restrict_public_groups` 保持 `true`，仅开放这两个明确授权的分组；三套账号均无活动订阅。

| 环境 | 网页入口 | OpenAI 兼容 Base URL | 用户 ID | 核验时余额（USD） |
| --- | --- | --- | ---: | ---: |
| 本地开发（Air / Vite） | `http://localhost:3000` | `http://127.0.0.1:18080/v1` | 29 | 999.99505861 |
| 本地测试（完整 Docker Compose） | `http://localhost:8080` | `http://127.0.0.1:8080/v1` | 4 | 1000 |
| 生产 | `https://sub2api.vseeu.de` | `https://sub2api.vseeu.de/v1` | 3 | 999.99048689 |

登录密码和六把完整 API Key 保存在本机私有文档 [`.dev/INTEGRATION_ACCOUNTS.md`](../.dev/INTEGRATION_ACCOUNTS.md)，文件权限为 `600`，由现有 `.dev/` 规则排除 Git 跟踪。该文件仅在已完成账号配置的本机存在，新克隆仓库需另行取得凭据。

## 按模型选择 API Key

原 `OpenClaw-local-deepseek` Key 保持原值、ID 和分组绑定。本次在每个环境各新增一把 `OpenClaw-local-mimo` Key，绑定该环境已有的 Mimo 分组；六把 Key 各自独立。同一环境的两把 Key 共用该普通用户的余额。

| 环境 | 模型 / 分组 | Key 名称 | 分组 ID | Key ID | 状态 |
| --- | --- | --- | ---: | ---: | --- |
| 本地开发（Air / Vite） | `deepseek-v4.1-flash-free` | `OpenClaw-local-deepseek` | 29 | 28 | active |
| 本地开发（Air / Vite） | `mimo-v2.6-flash-free` | `OpenClaw-local-mimo` | 31 | 29 | active |
| 本地测试（完整 Docker Compose） | `deepseek-v4.1-flash-free` | `OpenClaw-local-deepseek` | 4 | 3 | active |
| 本地测试（完整 Docker Compose） | `mimo-v2.6-flash-free` | `OpenClaw-local-mimo` | 6 | 4 | active |
| 生产 | `deepseek-v4.1-flash-free` | `OpenClaw-local-deepseek` | 3 | 5 | active |
| 生产 | `mimo-v2.6-flash-free` | `OpenClaw-local-mimo` | 5 | 6 | active |

## 使用与启动

API 客户端填写对应环境的 Base URL，按模型选择该环境的 Key：

- `OpenClaw-local-deepseek`：模型填写 `deepseek-v4.1-flash-free`。
- `OpenClaw-local-mimo`：模型填写 `mimo-v2.6-flash-free`。

Base URL 已包含 `/v1`；Chat Completions 相对路径为 `/chat/completions`，完整 HTTP 路径为 `/v1/chat/completions`。开发前端 `localhost:3000` 经 Vite 代理到 Go 后端；直接 API 调试使用 `127.0.0.1:18080/v1`。

启动方式见 [本地开发环境](LOCAL_DEVELOPMENT.md) 和 [环境与网络参考](ENVIRONMENT_REFERENCE.md)。原生开发需同时运行 Air/Vite 和独立 Bun worker；完整 Docker 测试需运行 Compose worker 及 Mac TCP 中继。

三套数据库、账号余额和消费 Key 互相独立；它们的模型实例使用相同固定上游出口，上游免费模型的限流可能跨环境相互影响。本次没有修改本机 OpenClaw 配置。

## 本轮验证结果

- 三环境普通用户均登录成功，角色为 user；允许分组由原 DeepSeek 组追加为 DeepSeek、Mimo 两组，`restrict_public_groups=true`，无活动订阅。
- 六把 Key 均为 active。三把原 DeepSeek Key 的完整值、ID、名称和分组绑定核验一致。
- 六次 `GET /v1/models` 均返回 HTTP 200。DeepSeek Key 各只显示 `deepseek-v4.1-flash-free`；Mimo Key 各只显示 `mimo-v2.6-flash-free`。
- 本轮没有发起真实模型推理请求，也没有追加充值；以上结果验证账号权限、Key 绑定和模型可见性。

脱敏核验记录保存在本机 `.cache/mimo-integration-accounts-20261101/verification.json`，包含修改前后的用户分组、Key 元数据、模型列表及核验时余额。

## 历史记录

2026-10-31 初始配置时，开发、测试分别通过官方充值码兑换路径充值 1,000 USD 测试余额，充值记录 ID 均为各自数据库的 `1`、状态 `used`。固定操作码保存在私有文档。生产沿用此前的 1,000 USD 充值记录；消费后余额会变化。旧配置核验记录为 `.cache/local-integration-accounts-20261031/verification.json`。

2026-11-01 08:57～08:58（Asia/Shanghai）对本地开发 DeepSeek 分组的 21 个实例各执行一次短文本对话：12 个成功、9 个返回上游 429，21 条代理出口全部核对通过。测试前没有额度冷却状态，无需清理；失败实例由服务重新设置冷却。完整实例、代理与结果对照见 [本地开发 DeepSeek 实例可用性](opencode/DEV_DEEPSEEK_AVAILABILITY.md)。该历史测试未测试生产或完整 Docker 环境，也未验证 Mimo 或完整 Agent 工具调用。

## 不触发推理的连通性检查

使用对应模型的开发环境 Key 读取模型列表：

```bash
curl --noproxy '*' --fail-with-body \
  -H 'Authorization: Bearer <开发环境对应模型的 API Key>' \
  http://127.0.0.1:18080/v1/models
```
