# 环境与网络参考

本文记录 **2026-10-31（Asia/Shanghai）** 的实际部署和本机状态，地址、容器名和验证结果是该时点的快照，不代表将来仍然可用。所有命令从项目根目录执行；生产发布和回滚以 [mysubapi CI/CD](CICD_MYSUBAPI.md) 为准，本机开发细节见 [本地开发环境](LOCAL_DEVELOPMENT.md) 和 [OpenCode 本地运行](opencode/LOCAL_FREE.md)。

## 环境对照

| | 生产 Linux | 本机完整 Docker | 本机原生开发 |
| --- | --- | --- | --- |
| 入口 | [sub2api.vseeu.de](https://sub2api.vseeu.de)，经 Cloudflare、源站 Nginx TLS 到应用 `127.0.0.1:8080` | `http://localhost:8080`，宿主机仅绑定 `127.0.0.1` | Vite `http://localhost:3000`，代理至 Go `127.0.0.1:18080` |
| 应用 | Compose 项目 `mysubapi`，应用容器 `sub2api` | Docker Desktop `desktop-linux`，Compose 项目 `deploy`，应用容器 `sub2api` | Air 管理本机 Go，Vite 独立运行 |
| PostgreSQL / Redis | `sub2api-postgres` / `sub2api-redis`，仅内部网络 | `sub2api-postgres` / `sub2api-redis`，仅内部网络 | 独立 Compose 项目 `sub2api-dev`：`sub2api-dev-postgres-1` / `sub2api-dev-redis-1`，本机 `127.0.0.1:15433` / `127.0.0.1:16380` |
| Bun worker | `mysubapi-opencode-free-worker-1` | `deploy-opencode-free-worker-1` | 本机单独启动 |
| worker 地址 | 与应用共享网络命名空间的 `127.0.0.1:19090`，不发布端口 | 同左；属于容器网络命名空间 | 宿主机 `127.0.0.1:19090`，与容器内同名端口不冲突 |
| 配置 | 服务器部署根目录 `.env`，权限 `600` | `deploy/.env`；worker 私有配置 `.dev/opencode-free.env` | `.dev/.env`；worker 私有配置 `.dev/opencode-free.env` |
| 数据 | 服务器 `/home/workspace/subapi/data`、`postgres_data`、`redis_data`；独立于本机 | Docker 命名卷 `deploy_sub2api_data`、`deploy_postgres_data`、`deploy_redis_data` | `.dev/backend`、`.dev/postgres_data`、`.dev/redis_data`；工具和缓存位于 `.cache/dev` |

生产主机为 `root@167.114.210.84`，SSH 端口 22，部署根目录 `/home/workspace/subapi`；服务器在该目录维护私有 `.env`、发布目录及 `current-deployment` 等部署辅助文件。生产绑定挂载分别为 `data` → `/app/data`、`postgres_data` → `/var/lib/postgresql/data`、`redis_data` → `/data`；这些宿主机目录均位于部署根目录。`mysubapi` 分支通过 GitHub Actions 构建 GHCR 应用和 worker 镜像，并由 Compose 按镜像摘要部署。发布状态和备份、回滚约束参见 [CI/CD 文档](CICD_MYSUBAPI.md)。

两套本机数据库互相独立，也不是生产数据库；原有本机账号保留。本机两套环境已从生产**配置**复制四个模型组、84 个实例和 21 条代理记录，本地 ID 重新分配；没有复制生产用户、消费者 API key、余额或用量记录。代理固定出口及认证与生产相同。上述配置是快照，不会自动随生产变更同步。消费者调用需要在本地授权对应分组并创建本地 API key，使用本地测试余额和定价；后台账号测试可以直接按实例诊断，不经过消费者扣费。

## 本机启动与停止

原生开发：

```bash
make dev         # 前台运行 Air/Go 和 Vite，并启动独立的 PostgreSQL/Redis
make dev-status  # 查看开发进程与依赖容器
make dev-stop    # 停止开发进程和依赖容器，保留数据
```

在另一个终端单独运行原生 worker，保持前台运行：

```bash
python3 -B deploy/opencode/local.py worker
```

首次准备 worker 私有配置可使用 `python3 -B deploy/opencode/local.py init`，详见 [OpenCode 本地运行](opencode/LOCAL_FREE.md)。`make dev-stop` 不管理这个 worker，也不管理下面的 TCP 中继；需分别停止。原生 worker 直连 IPv6 代理入口，无需为它启动 Docker 中继。

完整 Docker 使用两个 Compose 文件及私有环境文件：

```bash
docker compose --env-file deploy/.env -f deploy/docker-compose.yml -f deploy/docker-compose.opencode-free.yml up -d --build
docker compose --env-file deploy/.env -f deploy/docker-compose.yml -f deploy/docker-compose.opencode-free.yml ps
```

请求通过本机 Docker 的代理之前先启动下文中继。排查服务名可用 `docker compose ... config --services`；不要把解析后的完整 Compose 配置或私有环境文件输出到共享日志。

## 代理与出口

生产代理入口是需 HTTP 认证的 `23.106.155.32`，21 个端口分别固定到 14 个 IPv4 和 7 个 IPv6 出口。四个组为 `deepseek-v4.1-flash-free`、`glm-5.3-flash-free`、`mimo-v2.6-flash-free`、`big-pickle`；每组各有 `instance-01` 至 `instance-21`，并发、优先级和倍率均为 1，模型白名单各自精确限定该组模型。四组按相同序号复用 21 条代理记录：84 个实例并不意味着 84 个出口。本轮配置同步只建立固定绑定，额度和切换行为以实际部署版本为准。

这些实例是 84 条逻辑账号配置，不会启动 84 个容器。单套环境共用一个 Bun worker，当前活动请求上限为 64；四组配置并发总和为 84，不代表能够同时执行 84 条上游请求，实际还受用户并发、代理和上游限制影响。

同一代理序号在各环境的走法：

```text
生产 worker ── HTTP 认证 ──> 23.106.155.32:网关端口 ──> 固定出口
本机原生 worker ── HTTP 认证 ──> [2607:8700:5500:6bde::2]:网关端口 ──> 同一固定出口
Docker worker ── HTTP 认证 ──> host.docker.internal:本机中继端口
                              ── Mac TCP 中继 ──> [2607:8700:5500:6bde::2]:网关端口 ──> 同一固定出口
```

原生环境的代理 Host 使用不带方括号的 IPv6 地址 `2607:8700:5500:6bde::2` 和原网关端口；方括号用于上图这种带端口的地址表示。Docker 环境的代理 Host 使用 `host.docker.internal` 和下表中继端口。修改的是代理入口，固定出口不变；认证沿用现有私有配置，勿将其写入文档或命令日志。

| 序号 / 实例 | 固定出口名称 | 固定出口 IP | 原网关端口 | Docker 中继端口 |
| --- | --- | --- | ---: | ---: |
| 01 | US23-Hy2-out4 | `23.106.155.32` | 38001 | 23801 |
| 02 | US23-Hy2-out6 | `2607:8700:5500:6bde::2` | 38101 | 23802 |
| 03 | US-RackNerd-out4 | `107.174.82.165` | 38002 | 23803 |
| 04 | KR-Seoul-out4 | `85.155.176.38` | 38003 | 23804 |
| 05 | KR-Seoul-out6 | `2406:ef80:5:7e31::1` | 38102 | 23805 |
| 06 | NL-Amsterdam-out4 | `162.141.92.240` | 38004 | 23806 |
| 07 | DE-nc1-out4 | `91.132.147.54` | 38005 | 23807 |
| 08 | DE-nc1-out6 | `2a03:4000:37:db0:c894:f9ff:fe68:773b` | 38103 | 23808 |
| 09 | DE-nc2-out4 | `193.31.27.181` | 38006 | 23809 |
| 10 | DE-nc2-out6 | `2a03:4000:2b:1ef9:8485:73ff:fe98:bbc8` | 38104 | 23810 |
| 11 | DE-nc3-out4 | `93.177.66.165` | 38007 | 23811 |
| 12 | DE-nc3-out6 | `2a03:4000:38:f99:9865:c2ff:fe34:a8d3` | 38105 | 23812 |
| 13 | DE-nc4-out4 | `94.16.111.245` | 38008 | 23813 |
| 14 | DE-nc4-out6 | `2a03:4000:51:c97:18b4:77ff:fe4d:cad` | 38106 | 23814 |
| 15 | UK-London1-out4 | `185.168.195.212` | 38009 | 23815 |
| 16 | UK-London2-out4 | `185.184.69.42` | 38010 | 23816 |
| 17 | HK-out4 | `2.27.109.223` | 38011 | 23817 |
| 18 | HK-out6 | `2400:c620:22:300::a` | 38107 | 23818 |
| 19 | CN-Aliyun-out4 | `47.116.46.218` | 38012 | 23819 |
| 20 | CN-Tencent1-out4 | `124.223.88.72` | 38013 | 23820 |
| 21 | CN-Tencent2-out4 | `124.221.52.11` | 38014 | 23821 |

### macOS Docker TCP 中继

当前 Mac 的 Bun/Node 可经上述 IPv6 入口访问网关；宿主机和 Docker 到 IPv4 网关连接约 5 秒后出现 `ECONNRESET`。Docker 访问 IPv6 网关出现 `FailedToOpenSocket`、无 IPv6 路由，但可通过 `host.docker.internal` 访问 Mac 回环地址。以上是当前网络观察，尚未确定 IPv4 重置的具体责任组件。

中继在 Mac 的 `127.0.0.1:23801`–`23821` 接收 TCP，原样转发字节流到 IPv6 网关，不检查认证、不改写 HTTP 头、不重试或切换出口。此次 Mac Python 未绑定出站接口时收到 EOF；对 socket 显式设置 Darwin `IPV6_BOUND_IF` 并绑定 `en0` 后，真实 TLS 样本 3/3 成功。当前配置 `upstream_interface` 为 `en0`；更换机器或网络上行接口后先核对并调整，不需要修改全局网络设置。

首次使用时参考 [中继配置示例](opencode/proxy-relay.example.json)，保存为 `.dev/opencode-proxy-relay.json`，按本机上行接口调整 `upstream_interface`。已有配置先核对，避免覆盖。然后：

```bash
python3 -B deploy/opencode/proxy_relay.py up
python3 -B deploy/opencode/proxy_relay.py status
python3 -B deploy/opencode/proxy_relay.py stop
```

默认配置路径为 `.dev/opencode-proxy-relay.json`，也可通过 `--config` 指定；日志在 `.dev/logs/proxy-relay.log`。中继校验接口并管理自己的 PID、就绪标记与锁，运行中的配置冲突会被拒绝。它与 Docker Compose、`make dev-stop` 分开管理。

## 验证快照与排查

- 本机原生 Bun 经 IPv6 入口、以及 Docker 经绑定 `en0` 的中继，分别验证了 21/21 固定出口 IP 与预期一致，且 21/21 OpenCode 首页请求为 HTTP 200。这些网络检查不证明模型调用有可用额度。
- 生产约 2026-10-31 00:09 的 21 路历史诊断中，10 路通过、11 路返回通用 502，不能推断 11 路都是 429。用量合计 prompt 1070、completion 114、total 1184，其中 reasoning 87 已包含在 completion 中；这是直接 worker 诊断，不产生消费者账单。
- 部署 `6c9d78020`（2026-10-31 11:16 CST）后于 11:22 复跑同一直连 worker 的 21 路诊断：21/21 通过，历史失败的 11 路全部恢复，全部 HTTP 200、`billing_source=free`（用量 prompt 2247/completion 188，同样不产生消费者账单）；全程 worker 与应用无失败日志，本轮未出现上游拒绝样本。
- 本机 US23 样本的原始上游响应为 `429 FreeUsageLimitError`，带 `Retry-After`；该样本不是模型调用成功。worker 经代理收到上游非 200 时（2026-10-31 起）按来源证据分类：带 `Retry-After`/`x-request-id` 或 JSON error envelope 的透传真实状态与 `Retry-After`，无证据仍包装为 502 并留结构化日志，因此 502 本身仍不足以判定原始状态。
- 网站或 trace 的 HTTP 200 只证明对应请求可达，不能证明模型额度或模型调用成功。管理员测试接口即使 HTTP 200，也要检查 SSE 事件：`error` 表示测试失败，只有实际收到 `test_complete` 且 `success: true` 才表示测试成功。
- 管理员 Bun 测试修复已随 `6c9d78020` 部署生产，2026-10-31 复测中 instance-01/02 的管理端测试均收到 `test_complete success:true`。该路径在通用 Go 路由前识别 Bun，复用规范化、profile、代理、worker 派发及严格 SSE 免费校验，不使用消费者计费或伪造 API key。

本次清理移除了 27 个无关容器、83 个不再使用的项目测试容器和 20 个关联镜像；六个当前服务（完整 Docker 四个、原生开发依赖两个）健康，未删除卷或绑定数据。后续集成测试可能再次创建测试容器。清理时先核对具体资源及归属，不使用全局 prune 命令。

本轮验收结束时，工作区出现了另外一组 worker／额度处理并行改动（失败结构化日志、代理证据分类、免费配额 429 冷却与同分组转移）；该组改动已随后续提交完成单元测试与门禁验证，并随 `6c9d78020` 部署生产、完成上述部署后复测（含一次消费者网关 200 冒烟与全部状态回滚），行为细节以 `opencode/COMPATIBILITY.md` 的 2026-10-31 小节为准。

### 2026-10-31 三环境 DeepSeek 逐实例复验

测试时间：2026-10-31 11:58:50 至 12:02:36（Asia/Shanghai）。对 `deepseek-v4.1-flash-free` 的 `instance-01` 至 `instance-21`，在生产、本地 Docker、原生开发环境分别执行一次普通文本模型调用，共 63 次；逐个执行，未重试或切换账号。每次同时核对对应代理的真实出口 IP。

| 环境 | 模型调用成功 | 出口核对成功 | 平均模型响应 | prompt tokens | completion tokens | total tokens |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| 生产 Linux | 21/21 | 21/21 | 2.08 秒 | 2247 | 271 | 2518 |
| 本地 Docker | 21/21 | 21/21 | 2.73 秒 | 2247 | 214 | 2461 |
| 原生开发 | 21/21 | 21/21 | 2.75 秒 | 2247 | 199 | 2446 |

全部 63 次均为 HTTP 200、精确回答 `5`、`finish_reason=stop`、包含 `[DONE]`，并返回 `billing_source=free`；基本用量字段及总和校验通过。本轮没有出现 429、502 或超时。响应耗时不含出口探测。

本轮直接固定调用实际 Bun worker 和账号绑定的代理，未经过消费者网关的调度、完整 Go 用量解析或扣费流程，也未触发账号配额冷却；因此上述结果用于验证各实例的普通文本调用及出口，不能代替消费者计费、Agent 工具调用或故障切换验收。测试前后所核对的账号配置快照均一致。

运行版本有差异：生产 worker 包含新的错误透传逻辑，本地 Docker 仍使用较早的 worker 镜像；原生开发采集的是磁盘源码指纹，不能据此确认进程已加载的版本。本轮全部是成功响应，未覆盖不同版本的错误处理分支。

脱敏逐实例记录位于项目缓存 `.cache/deepseek-three-environments-20261031T115652/combined-report.json`，横向对照为同目录的 `instance-matrix.csv`。记录包含实例、账号/代理 ID、实际出口、响应时间和基本 token 用量，不含认证凭据。
