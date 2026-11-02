# Claude Code 接入生产 Mimo

这组配置供 Claude Code 通过本机回环代理调用本项目的
`mimo-v2.6-flash-free` 分组。生产 Bun 免费传输目前只接受原生
OpenAI Chat Completions；直接向该分组发送 Anthropic Messages 会被拒绝。
这里使用标准 LiteLLM Proxy 转换协议，生产账号、Key 和用量归属仍由
Sub2API 管理。

## 已验证版本

2026-11-02，本机 macOS 和生产服务器均已验证：

| 组件 | 版本 |
| --- | --- |
| OpenClaw | 2026.10.9 |
| OpenClaw 专用 Node | 24.22.1 |
| Claude Code | 2.1.316 |
| OpenCode | 2.2.5 |
| LiteLLM | 1.94.0 |

升级前备份客户端配置、状态和服务定义。OpenClaw 的 CLI 与实际
Gateway 都必须使用满足新版要求的 Node；只更新命令行包不能证明服务
已升级。OpenCode 2 的插件和服务器 API 与 1.x 有差异，应保留原状态。

## 安装与配置

在每台机器的 Claude Code 自有目录，例如 `~/.claude/mimo-bridge`，
创建独立 Python venv 并安装 `litellm[proxy]==1.94.0`。Python 3.10、3.13
已验证；运行 `venv/bin/python -m pip check` 确认依赖完整。将本目录的
`config.yaml`、`runner.py` 复制到该应用目录。

创建权限为 `0600` 的 `runtime.env`，使用原始 `KEY=value` 格式，不加
shell 引号；应用目录权限设为 `0700`：

```dotenv
SUB2API_PRODUCTION_MIMO_API_KEY=YOUR_PRODUCTION_MIMO_API_KEY
SUB2API_MIMO_UPSTREAM_BASE=https://sub2api.vseeu.de/v1
SUB2API_MIMO_BRIDGE_PORT=19088
```

运行前台 supervisor，或把同一命令交给 launchd / systemd 用户服务：

```sh
/absolute/path/to/mimo-bridge/venv/bin/python \
  /absolute/path/to/mimo-bridge/runner.py \
  --env-file /absolute/path/to/mimo-bridge/runtime.env
```

服务应在用户登录/服务器启动后自动启动，仅绑定 `127.0.0.1:19088`。
服务定义只保存路径，不保存 Key。runner 将密钥放入子进程环境，缓存
和临时文件留在应用目录，输出先脱敏再写 `proxy.redacted.log`。

在 Claude Code 用户 `settings.json` 中保留原有权限、插件等配置，并设置：

```json
{
  "model": "mimo-v2.6-flash-free",
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:19088",
    "ANTHROPIC_AUTH_TOKEN": "YOUR_PRODUCTION_MIMO_API_KEY",
    "ANTHROPIC_MODEL": "mimo-v2.6-flash-free",
    "ANTHROPIC_CUSTOM_MODEL_OPTION": "mimo-v2.6-flash-free",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "mimo-v2.6-flash-free",
    "ANTHROPIC_SMALL_FAST_MODEL": "mimo-v2.6-flash-free",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "mimo-v2.6-flash-free",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "mimo-v2.6-flash-free",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "mimo-v2.6-flash-free",
    "CLAUDE_CODE_SUBAGENT_MODEL": "mimo-v2.6-flash-free",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "CLAUDE_CODE_DISABLE_THINKING": "1",
    "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING": "1",
    "CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS": "1",
    "DISABLE_INTERLEAVED_THINKING": "1",
    "MAX_THINKING_TOKENS": "0",
    "CLAUDE_CODE_MAX_OUTPUT_TOKENS": "4096"
  }
}
```

不要同时保留其他 `ANTHROPIC_API_KEY`、`apiKeyHelper` 或
Bedrock/Vertex/Foundry 的强制路由配置。AUTH_TOKEN、桥的 master key 和
上游 API key 在该配置中使用同一个生产分组 Key。完整 settings 也应为
`0600`，不要提交到 Git。生产 Key 的来源与保管方式见
[账号接入说明](../../INTEGRATION_ACCOUNTS.md)。

OpenClaw 可以直接使用 `https://sub2api.vseeu.de/v1`，默认模型设为
`sub2api-production-mimo/mimo-v2.6-flash-free`，通过该版本支持的
protected store 或 file SecretRef 读取同一 Key，不经过此协议桥。

## 验证与边界

检查 `/health/liveliness` 及携带指定 Key 的 `/v1/models`，再分别用运行中的
OpenClaw Gateway 和原生 Claude Code 发起最小文本对话。不要用测试时的
model override 代替默认配置验证。用生产 usage 的用户、Key、分组、模型、
时间窗和 token 数确认最终归属。

2026-11-02 的四个原生客户端调用均成功，生产 Usage ID 为：本机
OpenClaw `59`、服务器 OpenClaw `58`、本机 Claude Code `62`、服务器
Claude Code `61`。上游均为 Mimo，本站实际记录费用为零。完整原始配置、
备份和验证证据保留在受限、Git 忽略的本机目录中。

`config.yaml` 使用两个 LiteLLM 官方开关固定 Chat Completions，避免
Messages 自动改走当前生产分组不支持的 Responses。它删除本接口不支持的
采样、元数据和思考控制字段；普通消息、工具定义与调用历史经过往返验证。
当前不支持主动调节思考档位或多模态。客户端显示 thinking off 也不意味着
上游一定没有推理 token。Claude Code 对自定义模型的成本估算不代表本站扣款。

这是一台机器上供本地客户端使用的单 Key 回环桥，不需要数据库。LiteLLM
1.94.0 无 DB 模式的无/错 Key 检查可能返回 `500`/`400`，均不授予访问；
指定 Key 正常返回模型列表。首次非流式生产探测曾遇到 Cloudflare `502`，
其后流式及两端原生客户端对话成功，不应将该错误标成免费额度耗尽。

参考：[LiteLLM Claude Code 集成](https://docs.litellm.ai/docs/proxy/client_setup/claude_code)、
[参数丢弃配置](https://docs.litellm.ai/docs/completion/drop_params)、
[Claude Code 网关说明](https://code.claude.com/docs/en/llm-gateway)。
