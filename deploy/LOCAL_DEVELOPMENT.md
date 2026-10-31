# 本地开发环境

日常开发使用本机 Vite 和 Go/Air，PostgreSQL、Redis 放在独立 Docker Compose 项目中。现有完整 Docker Compose 部署和 GHCR CI/CD 继续保留。

当前依赖基线是 PostgreSQL 18、Redis 8。Go 以 `backend/go.mod` 为准（当前 1.27.3）；完整镜像使用 Node.js 24 和 pnpm 9。`DEV_GUIDE.md` 中的 PostgreSQL 16/Windows 服务配置是历史记录。

三套环境的配置、网络差异、代理编号和核验记录见 [环境与网络对照](ENVIRONMENT_REFERENCE.md)。

## 启动与停止

本机需要 Python 3.9+、Go 1.21+（用于下载项目要求的工具链）、Node.js 20+（建议 24）、Corepack 和正在运行的 Docker Desktop/Engine。脚本不会安装或替换全局工具。

在项目根目录执行：

```bash
make dev
```

首次启动会生成私有配置、下载 `backend/go.mod` 声明的 Go 工具链、安装 Air v1.67.4 与 pnpm 9.15.9、安装前端依赖并启动两个依赖容器。工具、缓存、临时文件都在项目 `.cache/dev` 中。后续配置和依赖未变时跳过安装。

```bash
make dev-setup   # 只准备工具和前端依赖，不启动服务
make dev-status  # 查看本地进程及两个依赖容器
make dev-stop    # 停止本地进程和开发依赖容器，保留数据
```

`make dev` 会留在前台。按 Ctrl+C 停止 Air/Go 和 Vite，数据库与 Redis 可以继续运行。`make dev-stop` 可以从另一终端停止整个开发环境，不操作其他 Compose 项目。

## 地址与账号

| 组件 | 默认地址 |
| --- | --- |
| 前端 | http://localhost:3000 |
| 后端 | http://127.0.0.1:18080 |
| PostgreSQL | 127.0.0.1:15433 |
| Redis | 127.0.0.1:16380 |

前端请求通过 Vite 代理到后端；保存 Vue、TypeScript、CSS 文件后热更新。后端使用常规 `go build` 的增量缓存，保存 Go、SQL migration 或相关配置源文件后由 Air 编译并重启；测试文件不会触发服务重启。编译失败时 Air 会重新运行上次成功构建的二进制，期间可能短暂中断连接；修复代码后自动编译并应用新版本。

开发管理员邮箱默认为 `admin@mysubapi-dev.local`，随机密码在 `.dev/.env` 的 `DEV_ADMIN_PASSWORD` 中。该账号、数据与完整部署的账号和 `deploy/.env` 完全独立。首次进入后台的使用须知仍需管理员本人确认。

用于普通用户和 API 联调的 `ai@hostmails.de` 已在开发、完整 Docker 测试环境分别配置，限定使用 `deepseek-v4.1-flash-free` 分组，各有 1,000 USD 测试余额和独立消费 Key。三套环境的地址、账号 ID、验证结果及私有凭据文档位置见 [三环境联调账号](INTEGRATION_ACCOUNTS.md)。

端口、数据库用户名/库名、密码和工具下载代理可在 `.dev/.env` 修改，然后重新启动开发环境。配置文件是普通 `KEY=VALUE`，不执行 shell 命令。已有数据库中的管理员密码不会因为修改环境文件而改变。

## 数据、日志与网络

- `.dev/backend`：自动 setup 配置、安装标记、日志及本地定价数据。
- `.dev/postgres_data`、`.dev/redis_data`：两个开发容器的持久化数据。
- `.dev/logs/backend.log`、`.dev/logs/frontend.log`：开发进程日志。
- `.cache/dev`：Go、Air、Corepack、pnpm 及编译缓存；日常无需清理，清理会使下次构建变慢。

这些目录已被 Git 和生产 Docker 构建上下文忽略；私有环境文件权限为 `600`。开发服务与数据库端口只绑定本机回环地址。不要把开发配置或数据复制到生产部署目录。

`.dev/backend/development.yaml` 是独立的开发覆盖配置，默认使用仓库自带的定价数据并关闭远程定价同步，避免每次热重启等待外部网络；自动初始化仍会正常生成同目录下的 `config.yaml`。其他运行配置由启动器导出的环境变量提供。需要测试真实定价同步时可修改这个开发覆盖文件并重启。

首次工具安装默认沿用项目的 Go 下载代理 `https://goproxy.cn,direct`，可通过 `DEV_GOPROXY` 修改；无需改全局 `go env`。前端依赖使用冻结的锁文件。`make dev-stop` 不删除任何数据；如遇残留会话锁或孤立进程，先查看 `.dev/session.json` 和日志，启动器不会按端口或进程名批量杀进程。

## 与其他运行方式并行

当前完整部署可以继续使用 `http://localhost:8080`。日常开发用 `3000/18080` 和独立的 `15433/16380`，两套环境可以同时运行。原有 `docker-compose.dev.yml` 仍是完整镜像构建入口，本次没有把它改成依赖专用配置。

## 完整 Compose 验收与运行版本

日常 HMR 与完整镜像是不同运行进程。以下命令从项目根目录执行，固定检查 `deploy` Compose 项目的 `docker-compose.yml` + `docker-compose.opencode-free.yml` 配对环境，使用现有命名卷及显式 `deploy/.env`：

```bash
make local-runtime-report   # 只读：源码身份、应用/worker 镜像及运行状态
make local-runtime-check    # 只读：身份、健康或共享网络不一致则非零退出
make local-runtime-rebuild  # 构建同一源码的应用和 worker，并替换这两个容器
# 机器可读报告
python3 -B deploy/local_runtime.py report --json
```

重建会短暂中断本机完整镜像服务；它保留数据库/Redis 命名卷和私有配置，不管理原生 Air/Vite 或独立 Bun worker。重建前会核验应用、PostgreSQL、Redis 的项目归属和数据挂载；其他项目或 bind mount 配置不能使用这个入口。运行前准备已有 `deploy/.env` 和依赖服务；需要延长构建后的健康等待时使用 `python3 -B deploy/local_runtime.py rebuild --timeout 600`。远程 Docker daemon 不能用于此重建入口。

报告同时显示当前提交、源码是否有未提交修改、源码指纹、运行容器的镜像 ID/仓库摘要与构建标签。未提交源码构建的版本会带 `-dirty.<指纹前12位>`，不能当作干净提交发布。应用和 worker 必须来自同一源码身份；只有 `/health` 返回成功不能证明运行的是当前代码。私有配置、数据和缓存不属于源码指纹；原生开发进程只报告存在状态，不宣称具备发布身份。

完整镜像定义统一在根 `Dockerfile`；旧 `deploy/Dockerfile` 已移除。从项目根目录手工构建用 `docker build -f Dockerfile -t sub2api:mysubapi .`。后端独立开发镜像用 `docker build -f backend/Dockerfile backend`，不包含前端或完整运行时工具。

## 本地测试与 CI 边界

根 `make test` 的前端部分是 lint、typecheck 和关键 Vitest 集，后端部分是默认 Go 测试与 lint；它没有覆盖 CI 的所有带标签测试或全量前端测试。要执行 CI 对应的主要测试，从根目录分别运行：

```bash
make -C backend test-unit
make -C backend test-integration
(cd backend && go test -tags='unit integration' -v ./internal/handler -run '^TestMarketplaceGatewayRealInfrastructure$' -count=1 -timeout=5m)
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
pnpm --dir frontend run test:run
python3 -B -m unittest discover -s deploy/tests -p 'test_*.py'
```

后端集成测试需要可用 Docker/Testcontainers 和测试镜像，测试会建立临时数据库。`TestMarketplaceGatewayRealInfrastructure` 使用真实 PostgreSQL、Redis 与进程内 HTTP handler，但模型上游使用替身；不会验证真实供应商模型或生产网络。CI 还包含 Compose/shell 配置检查、候选镜像构建和隔离 worker smoke，具体列表以工作流为准。当前没有独立常驻 staging；本机完整 Compose 负责集成验收，CI 临时容器负责自动回归，生产部署检查负责运行身份和基础就绪。这些证据不等于真实登录、计费或模型调用验收。

`make -C backend test-e2e` 和兼容别名 `test-e2e-local` 直接运行仓库中 `internal/integration` 的 `e2e` 标签测试，不再引用不存在的脚本。它们要求已启动的**可丢弃测试服务**（`BASE_URL` 默认为 `http://localhost:8080`），用户流程会注册用户、尝试登录并创建 API Key；通过 `ADMIN_EMAIL`/`ADMIN_PASSWORD` 可提供测试管理员。部分场景会跳过或只记录响应，必须审阅 skip/log，不能只看最终 PASS。

网关 E2E 读取 `CLAUDE_API_KEY`、`GEMINI_API_KEY`，以及可选 `ENDPOINT_PREFIX=/antigravity`。这些是目标网关可用的 Key；配置后测试会向所选模型发出真实请求，可能派发上游并计费。当前 `E2E_MOCK` 仅在未被测试调用的辅助函数中定义，没有 mock server 或 HTTP 拦截器，设置 `E2E_MOCK=true` 不能隔离网络或保证免费。自动 CI 没有运行这套 live-server E2E，日常默认验证应使用上述替身上游回归。

## OpenCode 分组与本地代理入口

2026-10-31 已将生产环境的以下配置分别导入完整 Docker 环境和原生开发环境：

- `deepseek-v4.1-flash-free`、`glm-5.3-flash-free`、`mimo-v2.6-flash-free`、`big-pickle` 四个分组，各自只开放对应模型。
- 每组 `instance-01` 至 `instance-21`，每个实例并发数为 `1`，使用 Bun 免费模型接入。
- 每组按相同编号绑定 21 条带认证 HTTP 代理；四组复用同一套固定出口。

两套本地数据库中的分组、账号、代理 ID 各自独立，按名称和节点编号对应。同步保留已有本地账号，不复制生产用户、余额、API Key 或消费记录。代理凭据保存在各自的数据库及 Git 忽略的私有快照中，不写入本文件。

当前 Mac 到代理 IPv4 入口的请求会被重置，而 IPv6 入口可用。因此，原生开发环境直接使用该网关的 IPv6 入口；Docker 使用 `host.docker.internal:23801` 至 `23821`，经 Mac 回环地址上的 TCP 转发连接同一网关的 IPv6 入口。它们仍然对应原来的固定出口 IP。

完整 Docker 环境使用这些代理前，在项目根目录启动本地转发：

```bash
python3 -B deploy/opencode/proxy_relay.py up
python3 -B deploy/opencode/proxy_relay.py status
```

配置位于 `.dev/opencode-proxy-relay.json`，包含 21 个本地端口和上游入口，不含账号密码；可从 [示例配置](opencode/proxy-relay.example.json) 恢复。当前 Mac 还配置了 `upstream_interface: en0`，仅将转发程序的 IPv6 出站 socket 绑定到该接口，不修改全局网络设置；换用其他网络接口时应相应调整。

转发只监听 `127.0.0.1`，原样传输 TCP 数据，不解析请求内容。状态和日志保存在 `.dev/proxy-relay.*` 与 `.dev/logs/proxy-relay.log`。修改配置后先停止再启动：

```bash
python3 -B deploy/opencode/proxy_relay.py stop
python3 -B deploy/opencode/proxy_relay.py up
```

原生开发环境的 Bun worker 仍使用独立启动命令 `python3 -B deploy/opencode/local.py worker`，另一个终端运行 `make dev`。`make dev-stop` 管理 Air、Vite 和开发依赖；上述独立 worker 和代理转发需要分别停止。完整 Docker 环境中的 worker 由 Compose 管理。

后台账号测试现已按账号的 Bun 标记选择与正式网关共用的请求规范化、代理传递和流式响应校验逻辑。测试不创建消费者计费请求。代理连通成功不代表上游允许模型调用；上游限流或访问拒绝仍会使测试失败。代理请求收到的非 200 响应由 worker 按来源证据分类：带 `Retry-After`/`x-request-id` 或 JSON error envelope 的透传真实状态，否则保守转成 502 并输出结构化错误日志。

完成功能后，仍可运行完整 Compose 构建验收，再推送到 `mysubapi` 执行 CI/CD；生产部署不会执行 Air 或 Vite 开发服务器。

本机的一次小改动验证结果：前端模块热更新约 **477ms**，页面未整页刷新；新增 Go 文件到后端重新健康约 **8.7 秒**，后续修改约 **5.7 秒**。这不是所有修改的固定耗时，首次编译、依赖变化或修改较大的 Go 包会更慢。已验证编译错误后回用上一份成功二进制、修复后自动应用，以及异常 Air 退出时清理其后端和前端进程。

## 配置与进程回归检查

```bash
python3 -B -m unittest discover -s deploy/tests -p 'test_dev_environment.py'
sh deploy/tests/docker-compose-dependencies-test.sh
```
