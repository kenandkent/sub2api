# OmniRoute OpenCode 免费模型 Bun 运行手册

**生产状态：已切换并完成旧 serve 清理。** 最终核验时间为 **2026-10-27 16:55:40 UTC**（中国时间 2026-10-28 00:55:40）：生产 app 和 worker 健康，两个目标模型的生产工具形态调用通过，旧 `14937` 监听已关闭。下面的结果是该时点的证据，不保证之后上游持续可用。历史 [v2 设计文档](OMNIROUTE_FREE_MODELS_DESIGN.md) 保留当时提议；实现和运行状态以本手册及私有部署证据为准。

机器可读的脱敏摘要见 [本次发布验收记录](opencode/omniroute-release-20261028.json)。

## 当前部署与保留资产

- 主机项目根目录为 `/home/workspace/OmniRoute`。生产 app 容器为 `omniroute`，worker 为 `omniroute-opencode-free-bun`。生产应用本地镜像 tag `omniroute:free-bun-v2-20261027`，**本地镜像 ID** `sha256:c14feb156da25b368938c89a76820838dce943d573a947ff6c75c26bfc72566c`；worker tag `omniroute-opencode-free-bun:20261027`，**本地镜像 ID** `sha256:9457db450f167a2383348f2d5aeabd5d5665a08caca6d93f7b701d61c0c26ea5`，运行 Bun 1.4.2。这些是本地镜像 ID，不是 registry digest；本次未推送镜像。
- 生产公开端口 `20128`、`20129`、`20132` 保持原配置，数据挂载 `/home/workspace/OmniRoute/data:/app/data`。切换时核对了原有非旧网关环境变量逐项一致；旧 `OPENCODE_GATEWAY_` 配置从新部署中移除，并加入 Bun 配置。本地开发机通过服务器公网地址访问应用健康端点返回 200，服务器本机健康检查也返回 200；没有对所有 provider 发起真实调用。
- worker 与当前 app **共享 network namespace**，仅在 `127.0.0.1:19090` 监听，不发布 worker 端口。稳定的私有 Compose 文件为 `/home/workspace/OmniRoute/.config/opencode-free-bun/compose.json`；同目录 `app.env`、`worker.env` 的权限为 `600`，配置目录为 `700`。Compose 必须支持 `env_file.format: raw`，以保留 env 字面值；已用 Docker Compose **5.0.2** 验证。配置文件和认证值不进入 Git、镜像或公开报告。
- 私有运行和回滚目录 `OUT=/home/workspace/OmniRoute/_cache/opencode-free-bun-20261027`，目录权限 `700`；私有 env、数据库和旧服务配置备份权限 `600`。不要向终端或报告输出 env、key、代理凭据、消息内容或会话标识。
- 原应用已保留为停止状态的完整容器 `omniroute-prev-bun-20261027`，原镜像 tag `omniroute:gw-nonstream-failfast-20260926`，本地镜像 ID `sha256:6afda3f2647b18eda69ed1c59e6bd5a4ffa000a9f7bb89750a287eae703d8a95`。切换前 SQLite 一致性备份为 `OUT/rollback/storage-pre-cutover.sqlite`；备份供恢复使用，正常回滚容器时不要未经确认覆盖当前生产数据库。
- 两个旧 unit 的原始文件和 `serve.env` 已归档到 `OUT/retired-units/`，旧 OpenCode 二进制 `/root/.opencode/bin/opencode` 与密码配置 `/root/.config/opencode/service.json` 已保留。`service.json` 是旧 `--service` 模式的实际密码源，不得删除或在不核实恢复依据时覆盖。
- 专用源码分支 `codex/opencode-free-bun-20261027` 和 worktree `/home/workspace/OmniRoute/.claude/worktrees/codex-opencode-free-bun-20261027` 保留未提交，不 push。`OUT/source-release.tar.gz`、`OUT/release.patch`、`OUT/source-sha256.json` 保存本次 34 个源码资产以及父 HEAD `788848706` 和已有部署基线的追溯证据。最终核验确认共享 checkout 仍为 `release/v3.8.50`，其对应 34 个路径的原始 hash 未改变；不要切换、重置、stash 或覆盖共享 checkout 中的其他工作。
- 隔离 canary 的两个容器已下线；克隆数据库和验收证据留存。`20138` 是原 canary 本机测试端口，不是生产公开端口。

## 请求路径和能力边界

应用使用 `OPENCODE_FREE_TRANSPORT=bun` 与 `OPENCODE_FREE_BUN_ENDPOINT=http://127.0.0.1:19090`，app 和 worker 从各自私有 env 取得同一内部认证值。当前生产配置使用既有**第一个**已配置账户对应的 SOCKS5 代理，Bun 通过本地受限 CONNECT 桥连接上游并保持 TLS 校验；不自动换账号、代理或标识。已分发的 Bun 请求不会在同一逻辑请求中自动重放到其他账户、模型或后备 provider。非免费、付费 Zen、Go 和其他 provider 保持既有分发与计价路径；本次没有宣称已对真实付费 provider 发起线上请求。

四个 profile 的 SHA-256 已根据本地 source 资产计算并与源码注册值核对：

| 模型 | 类型及 profile | SHA-256 |
|---|---|---|
| `oc/big-pickle` | text：`big-pickle-text-balanced.v2.json` | `da80fdbfd8112be5433856044945bbf09f926d06c1a4dc59b70624ab8bd9a0a9` |
| `oc/big-pickle` | agent：`big-pickle-agent-balanced.v1.json` | `23927e109355a85b84bc0e671feda37f7f492dc297427737f2f0f51b89b8f966` |
| `oc/mimo-v2.6-flash-free` | text：`mimo-v2.6-flash-free-text-balanced.v1.json` | `5d8c67ddedd624f1837dd47503948069de6b03a29ff5b5252c0f81fd7b58cc52` |
| `oc/mimo-v2.6-flash-free` | agent：`mimo-v2.6-flash-free-agent-balanced.v1.json` | `86278247b87dc05c0a213a4789b24ec3b9d7d4f8cdd810139e37356f27f4c99f` |

Agent 模式保留调用者声明的工具、文本数组和工具调用/结果历史；worker 不执行这些工具。兼容处理仅在严格条件下为缺少 index 的完整 terminal tool snapshot 补 index；MiMo 的 details 只接受与 reasoning 相同的已知 plain 重复形态，带 usage 的重复 finish 仅在没有新增语义时接受。其他不完整或含新语义的情况按校验失败处理。

未知免费模型、未支持的协议或多模态内容在上游分发前明确拒绝，不能因名称带 `-free` 就推断准入。包含潜在 Bun 目标或未解析动态 `auto` 目标的真实并行 combo 在分发前拒绝，可能保守覆盖带 key 的同名免费模型；单目标和普通 provider 按既有规则执行。非零 temperature 等未支持参数以实际 shared schema 为准，不会悄悄丢弃。HTTP 代理非 200 结果保守映射为 502；受控 SOCKS 隧道上可确认的上游状态和 `Retry-After` 应保留。

## 验收记录

- 离线验证：focused Node **74**、Bun **26**、旧 OpenCode 回归 **31**、相关回归 **85**；类型门禁在既有 **11 条冻结基线诊断**范围内通过。这些不等于真实付费 provider 线上调用验证。
- 隔离 canary **8/8 API 验收通过**：两个模型各完成文本 nonstream、七工具 `auto` / text 数组 stream、强制工具 stream、工具结果历史 nonstream；均为 200、精确输出并带真实 free usage。**8/8 `usage_history` 五分项**与 API 用量对账通过。canary 重启后健康检查和共享 network namespace 验证通过。
- 生产三次验证请求的 `usage_history` 输入、输出、缓存读取、缓存创建及 reasoning 五项全部匹配；34 个发布源码资产与 manifest 一致，运行容器内七个关键源码文件的 hash 与发布源码一致。
- 实际断连时 worker 活动 socket 从基线 **1** 增至 **6**，随后降回 **1**，后续三次采样保持基线。工具只作合成声明和合成结果，不执行真实工具。
- 生产切换后，两个模型各完成七工具 `auto` / text 数组 streaming 实际调用并返回 200：Big Pickle **3264 ms**，MiMo **1924 ms**。旧服务清理后 MiMo nonstream 再次返回 200，**2504 ms**，真实 free usage 为 prompt **151**、output **29**、cache read **64**。这些是实际样本，不构成上游可用性保证。
- 最终核验时 app 与 worker 健康，三个公开端口和原有非旧网关 env 项一致；服务器本机及本地开发机经公网访问应用健康端点均返回 200。没有进行真实付费 provider 请求，相关保障限于离线回归和原环境配置核对。
- 两个旧单位 `omniroute-gw-serve.service` 与 `omniroute-gateway-serve.service` 已停止、禁用并归档，`14937` 已关闭；`/etc/omniroute-gw/serve.env` 已移至私有归档。旧二进制和 `service.json` 保留。

## 日常维护：不调用模型

在服务器使用稳定 Compose 路径查看容器状态：

```sh
docker compose -f /home/workspace/OmniRoute/.config/opencode-free-bun/compose.json ps
```

在 app 网络命名空间检查 worker 的本地健康端点，并从本机检查应用健康端点：

```sh
docker exec omniroute node -e 'Promise.all(["live","ready"].map(async p => { const r = await fetch("http://127.0.0.1:19090/health/" + p); if (!r.ok) throw new Error("worker health failed"); })).catch(() => process.exit(1))'
curl --noproxy '*' -fsS -o /dev/null -w 'HTTP %{http_code}\n' http://127.0.0.1:20128/api/monitoring/health
```

这些检查不调用模型，只证明本地进程/服务可达，不能证明上游内容或计费正常。应用容器被替换或其网络命名空间改变后，按实际 Compose 编排重建或重新关联 worker，再检查健康；单纯看到 worker 进程仍在不能证明它连接到新 app。不要通过定期真实模型请求保持或探测服务。

后续重新部署前先核对源码归档、当前容器配置、镜像 ID、私有 env 权限和一致性备份。只有在两个模型的必要文本/工具、流式/非流式、真实 free usage、取消和不重放门禁通过后才进行切换；上游限流或失败时停止，不用换代理、换标识或重复请求寻求成功。控制项目中的 `.cache/omniroute-bun-20261027/api-canary-e2e.py` 是受控真实调用验收脚本，每模型最多四次请求，失败停止；它不是日常健康检查。usage 记录的异步归因有边界，应结合响应与受控运行窗口核验。

## 回滚到旧 serve

回滚前确认私有归档、旧容器和当前生产数据状态；不要输出认证值或把旧数据库备份直接覆盖到生产。回滚步骤只针对本次 OmniRoute 资源：

1. 从 `OUT/retired-units/` 恢复两个归档 unit 的原文件到 `/etc/systemd/system/`；若需恢复旧环境文件，将归档的 `serve.env` 恢复到 `/etc/omniroute-gw/serve.env`，保持原有私有权限。确认旧二进制及 `/root/.config/opencode/service.json` 保持可用、密码未变，执行 `systemctl daemon-reload`。
2. 启用并启动 `omniroute-gw-serve.service`，核对 `14937` 本地监听。`omniroute-gateway-serve.service` 原先因 `ProtectHome=yes` 无法访问二进制而不断失败；**不要启动该故障 unit**。
3. 停止本次新 worker 与新 app，给新 app 一个唯一的故障保留名；将停止保留的 `omniroute-prev-bun-20261027` 重命名回 `omniroute` 并启动，沿用旧容器原配置、端口和数据挂载。保留新故障容器和两套镜像用于排查。
4. 核对旧 app、旧网关、原公开端口和数据挂载；如需验证真实模型，按受控验收方案执行，不做周期性探测。记录原因、时间和结果。若密码配置或归档缺失，先恢复正确资产，不要重建随机密码后继续。

## 已知用量与成本差异

`usage_history.tokens_input` 使用包含 cache 的 prompt 计数，`tokens_output` 使用包含 reasoning 的 completion 计数；cache 和 reasoning 是分项，不应再次累加到请求总量。现有 token 限额逻辑还会把 reasoning 加到 input + output，可能对已包含 reasoning 的 completion 重复计数；这是既有规则，本次迁移未修改。

`usage_history` 没有 USD cost 列；预算成本表仅在估算值大于 0 时写入，不写零成本行。canary 未配置匹配价格，成本计算因缺省价格返回 0；这不是按 `billing_source=free` 强制归零的保证，后续非零价格配置可能改变估算。应分别检查上游 free 标识、客户端用量、history 分项与预算成本，不要把缺少成本记录当成已写入零成本。


## OpenClaw 实际客户端验收（2026-10-28）

通过两端现有 Gateway 创建独立测试会话，未改主配置或默认模型，也未向外部渠道发送消息。普通对话会话强制禁用全部工具；工具测试会话的有效工具集合经 RPC 核对仅为 `read`。MiMo 真实调用 `read` 读取各自已安装 OpenClaw 的 `package.json`，观察同一 toolCallId 的 start/result、正确文件路径及返回版本后，再由模型精确输出版本和测试标识。OpenClaw 正常持久化测试会话、日志和自身运行状态。

| 客户端 | OpenClaw 版本 | 普通对话 | 工具闭环 |
|---|---|---|---|
| 本机 | 2026.9.5 | 通过，6111 ms | 通过，13992 ms |
| 服务器 | 2026.7.1-2 | 通过，7269 ms | 通过，12795 ms |

四项的实际模型均为 `oc/mimo-v2.6-flash-free`。本机 provider 名为 `ominiroute`，服务器为 `omniroute`；它们指向同一套 OmniRoute。按独立合成标识匹配到六次上游 HTTP 请求：每端普通对话一次、工具回合两次，全部为 `opencode/mimo-v2.6-flash-free`、HTTP 200，使用匿名/keyless凭据；生产 Bun 模式和 worker 健康，旧 serve 端口仍关闭，因此未走其他 provider 或旧网关回退。流式日志摘要没有保留 vendor `billing_source` 扩展，这一缺失不用于推断收费状态；路由核对依据为实际模型、凭据类型和当前执行器配置。

机器可读脱敏记录见 [OpenClaw 端到端验收](opencode/openclaw-e2e-20261028.json)。
