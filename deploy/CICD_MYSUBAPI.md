# mysubapi 分支的部署 CI/CD

目标：`root@167.114.210.84:22`，部署根目录 **`/home/workspace/subapi`**，镜像仓库 `ghcr.io/kenandkent/sub2api`。配置入口为 `.github/workflows/mysubapi-ci-cd.yml`。

## 流水线行为

1. 向 `mysubapi` 推送、向该分支提 PR，或手动触发时，并行执行部署回归、后端单元测试、后端集成及真实基础设施网关测试、前端 lint/typecheck/全量 Vitest。网关回归使用临时 PostgreSQL/Redis 和替身模型上游，不调用真实供应商。CI 不再先跑一次关键测试集；本地的关键测试入口继续保留。
2. 仅原仓库 `kenandkent/sub2api` 的 `mysubapi` 分支能发布镜像；PR 不发布和部署。
3. 镜像构建与上述检查同时开始，从同一提交构建应用和 Bun worker 的 `linux/amd64` 镜像，适配目标服务器的 `x86_64` 架构。构建阶段只发布 `sha-<commit>` 候选标签；worker 镜像还会在无外网的隔离容器中验证启动、共享网络、正确/错误鉴权及“未派发上游”的结果。根 Dockerfile 仍支持手动构建其他架构。
4. 默认部署应用和 Bun worker，部署必须等待所有检查、镜像构建及 worker smoke 成功；任一检查失败或取消都不会部署。部署前检查本次 SHA 是否仍是分支最新提交，跳过已被后续提交取代的部署。
5. SSH 上传发布文件到唯一的 `incoming/<SHA>-<run>-<attempt>`，再切换为 `releases/<id>`；镜像始终按 `@sha256:...` 部署，不使用可变标签定位版本。
6. SSH 部署成功后再次核对分支 HEAD；仍为当前提交时，才把应用和 worker 的 `mysubapi` 标签提升到本次部署使用的镜像摘要。已知过期提交跳过标签提升。GitHub 分支检查和 GHCR 标签写入没有跨系统原子事务；两镜像的标签更新也非原子操作，生产版本以服务器 `current-deployment` 中的摘要为准。标签更新失败会使 job 报错，已完成的服务器部署不会因此自动回滚。

各 CI job 使用独立并发组，新推送可以取消同一分支/PR 的旧检查或候选构建。生产部署使用独立并发锁并保持 `cancel-in-progress: false`，不会因为新一轮 CI 取消旧检查而中断正在执行的 SSH 部署。

GitHub 的手动运行入口要求工作流也已存在于默认分支；仅把文件放在 `mysubapi` 不保证出现 Run workflow 按钮。向 `mysubapi` 推送仍是本流程的正常触发方式。

## GitHub 需要配置什么

你已在 Repository secrets 添加 **`SSH_PRIVATE_KEY`**，工作流直接使用该名称，也兼容旧的 `DEPLOY_SSH_KEY`。私钥须可无交互使用，并获准登录目标服务器。无需把私钥提交到代码或再创建同内容 Secret。

服务器 ED25519 公钥已从本机此前信任的 `known_hosts` 提取到 `deploy/ssh/mysubapi_known_hosts`。它是公开身份信息，不含 SSH 私钥。SSH 开启严格主机校验，并禁用其他用户/系统配置作为额外信任源。换主机、端口或确认过主机密钥轮换时，在 Environment / Repository secret **`DEPLOY_KNOWN_HOSTS`** 设置经独立核验的新条目；覆盖主机/端口却未提供对应 pin 时失败关闭，不执行 `ssh-keyscan` 即信任。

在仓库的 Actions → Variables 设置：

| Variable | 值或默认值 | 作用 |
| --- | --- | --- |
| `DEPLOY_HOST` | `167.114.210.84` | 可省略 |
| `DEPLOY_PORT` | `22` | 可省略 |
| `DEPLOY_USER` | `root` | 可省略 |
| `DEPLOY_PATH` | `/home/workspace/subapi` | 可省略；禁止 `.` / `..` 路径段 |

部署 job 使用 `mysubapi-production` Environment，可在 GitHub 中配置其分支限制和人工审批。Repository secret 会直接供该 job 使用，不必复制到 Environment。符合仓库和分支限制的 push 在检查和镜像发布成功后会进入部署；Environment 配置的审批和分支限制仍然生效。

应用和 worker 候选镜像由镜像 job 的 `GITHUB_TOKEN` 以 `packages:write` 发布。部署 job 同样需要 `packages:write`，用于成功部署后的标签提升；服务器拉取使用同一短期 token 经 SSH stdin 登录 GHCR。无需永久 GHCR 密码；若包已存在，分别检查应用包和 `sub2api-opencode-free-worker` 包的 Actions 仓库访问权限，避免 pull 返回 403。标签提升的 Docker 认证保存在 job 工作区的独立目录。

## 构建缓存与耗时

`actions/setup-go` 继续缓存测试 job 的 Go 依赖和编译结果；集成及真实网关验证使用 `-count=1`，每轮实际执行。镜像保留独立的 GHA 层缓存 scope；Dockerfile 将版本、提交、时间和目标架构参数放到最终编译前，避免这些变化影响稳定的依赖步骤。

Docker 的 GHA 层缓存默认不跨运行保存 `RUN --mount=type=cache` 的内容。因此，这次调整不代表 Go 编译缓存或 pnpm store 已实现跨 runner 持久化。后续应根据恢复、保存和编译的实测时间决定是否增加缓存挂载持久化。

优化前的 run `41028524086`：后端约11分58秒、随后镜像约8分35秒，实际部署被跳过。新流程的耗时需要以实际 Actions 运行测量，不能把旧时间直接当作优化后的验收结果。

## 首次启用前的服务器准备

服务器需 Docker Engine、Docker Compose **>=2.24.4**、Bash >=4、Python 3、`timeout`、awk 和标准 Linux 工具。使用本机默认 Docker context；不要将部署指向远程 Docker daemon。系统级及标准用户目录 `cli-plugins` 中的 Compose 插件均可使用；临时认证目录通过只读链接保留用户插件发现路径，不复制原 Docker 登录信息。

生产 Compose override 将 PostgreSQL 18.6 Alpine 和 Redis 8.10.3 Alpine 固定为经核验的 `@sha256:...` 引用；本机常规 Compose 仍使用 `postgres:18-alpine`、`redis:8-alpine`。应用内 PostgreSQL 客户端也固定到同一生产基线。摘要以 `docker-compose.production.yml` 与根 `Dockerfile` 为准；更新时应核对运行版本及架构兼容性，不能仅修改文档中的版本号。

在目标目录保存私有 `/home/workspace/subapi/.env`，权限设 `600`，优先以 `.env.mysubapi.production.example` 为模板（完整选项参考 `.env.example`），至少配置：

- `POSTGRES_PASSWORD`、`REDIS_PASSWORD`；
- 首次管理员 `ADMIN_EMAIL`、`ADMIN_PASSWORD`；
- 稳定的 `JWT_SECRET`、`TOTP_ENCRYPTION_KEY`；
- 稳定且独立的 `OPENCODE_FREE_BUN_TOKEN`，至少 32 个不含空格的 ASCII 可见字符，例如使用 `openssl rand -hex 32` 生成；
- `BIND_HOST`、`SERVER_PORT`。同机反代推荐 `BIND_HOST=127.0.0.1`，对外域名与 HTTPS 由反向代理配置。

这些配置由服务器维护，CI 不上传或覆盖开发机 `.env`。只有 Compose 明确透传的环境变量会进入容器；不要假定模板中每项都自动生效。新安装先使用与实际 PostgreSQL 容量匹配的应用连接池，例如 `DATABASE_MAX_OPEN_CONNS=50`、`DATABASE_MAX_IDLE_CONNS=10`；模板的高并发示例不是对容器实际连接上限的保证。

该分支生产部署默认运行应用、PostgreSQL、Redis 和 Bun worker **四个容器**。Compose 项目名为 `mysubapi`；固定容器名沿用 `sub2api`、`sub2api-postgres`、`sub2api-redis`。首次接管已有服务前核实容器归属、数据库版本和数据路径；不要创建冲突项目或覆盖其他目录。

## Bun worker

分支生产部署包含现有 Bun transport；它**不扩大已有模型/工具能力，也不代表官方已允许第三方调用免费聊天模型**。当前兼容范围另见 `deploy/opencode/COMPATIBILITY.md`。

生产部署必须提供同一提交构建的 worker 镜像摘要，缺少摘要时不能部署。生产 overlay 只拉取该摘要，不在服务器编译；它共享应用网络命名空间、仅监听 `127.0.0.1:19090`、不发布端口，不读取 PostgreSQL/JWT 等其他秘密，使用只读文件系统和临时 `/tmp`。应用每次重建后 worker 强制重建，避免留在旧网络命名空间。

应用和 worker 从同一私有变量取令牌；部署还从实际运行中的应用容器通过匿名管道读取令牌，向 worker 发送无效空请求，必须得到已鉴权的400及 `x-sub2api-dispatched:false`。探针不调用任何真实模型，令牌不放命令参数、发布状态或日志。

通用 Compose 和手动部署 helper 仍支持不提供 worker 摘要的三容器配置，以兼容既有用法及旧版本回滚。回滚需要的旧配置必须保留；若缺失，部署会在替换应用前拒绝。

## 数据、验证与失败恢复

持久目录固定在 `/home/workspace/subapi/data`、`postgres_data`、`redis_data`，通过 `--project-directory` 避免落到各个 release 内。发布目录无需源码、Go、Node 或 Bun 宿主安装。

部署锁覆盖拉取、配置预检、依赖就绪、备份、应用/worker替换和恢复。镜像拉取最多三次，每次默认180秒；先验证当前及可回滚配置，再拉取全部所需镜像。依赖 `up` 可能启动或重建 PostgreSQL/Redis，不能把“应用尚未替换”理解为依赖绝不改变。

SQL查询通过后生成非空、私有权限的 `pg_dump -Fc` 备份，再更新应用。容器健康、`/setup/status` 初始化状态、`/api/v1/settings/public` 站点名称、worker 鉴权空请求，以及实际运行身份均通过后才原子更新 `current-deployment`。worker 必须返回400且 `x-sub2api-dispatched:false`。这些探针不会登录或调用真实模型；`/health` 也不持续检查数据库/Redis 连接。新状态是 `RELEASE_ID APP_DIGEST WORKER_DIGEST_OR_-`，兼容读取旧的两字段状态。

在服务器上可随时执行只读身份核验：

```bash
python3 -B /home/workspace/subapi/releases/<release-id>/verify-deployment.py /home/workspace/subapi
```

核验器读取 `current-deployment`，检查 Compose 容器归属、运行镜像 ID 与指定 digest 的本地镜像 ID、OCI revision 与 release 提交，以及应用/worker 的配对源码标签；标记为 dirty 的镜像会失败。兼容旧镜像的 OCI 标签时明确提示 `legacy source labels absent`，不声称具有新版源码指纹。命令不会拉取镜像、重启容器或读取私有环境内容，最终成功行是 `Deployment identity verified: <release>`；不匹配时非零退出。部署和失败后的恢复都执行同样的身份核验。

替换失败会尝试用旧发布的应用和 worker 摘要恢复，仍以失败退出；不会把回滚成功当本次发布成功。**数据库迁移不自动逆转**，旧二进制可能不兼容新 schema。保留备份，依照 `MARKETPLACE_RUNBOOK.md` 的迁移边界选择兼容版本，不能盲目恢复旧数据库或镜像。发布、备份和旧镜像不会自动删除，也不执行全局 Docker prune。

## 本地验证

```bash
python3 -B -m unittest discover -s deploy/tests -p 'test_*.py'
sh deploy/tests/docker-compose-production-test.sh
bash -n deploy/deploy-mysubapi.sh
# 构建本机架构的现有 worker；测试禁止外网且不调用真实模型
docker build -f deploy/opencode/worker/Dockerfile -t sub2api-opencode-free-worker:ci-verify .
WORKER_SMOKE_IMAGE=sub2api-opencode-free-worker:ci-verify python3 -B deploy/tests/opencode_worker_smoke.py
```

脚本回归注入拉取、旧配置、备份、启动、鉴权及回滚失败；SSH步骤通过本地替身验证 pin、路径、两镜像参数、Secret不输出和失败即停止。真实 worker smoke 是隔离网络下的运行时验证；它不能替代线上 GHCR 权限、SSH 私钥可用性或生产完整 Compose 验收。

发布前 dump 与每日生产备份独立管理。生产每日 03:30（Asia/Shanghai）的备份、14 天保留、手动拉取至本项目私有目录和隔离恢复检查见 [生产备份](PRODUCTION_BACKUP.md)；现有发布前 dump 不纳入该每日备份的清理范围。

根 `make test` 的前端部分只跑关键集，并不等同于 CI 的全量 Vitest 与带标签后端测试。手工 live-server E2E 不在 CI 中运行，其前置条件和 `E2E_MOCK` 的实际限制见 [本地测试与 CI 边界](LOCAL_DEVELOPMENT.md#本地测试与-ci-边界)。当前没有独立常驻 staging；CI 临时依赖和本机完整 Compose 是现有验收环境，不能替代真实业务流程的专项验收。

## 2026-10-29 本地验证结果

- 18项部署 helper 测试和4项真实工作流 SSH 步骤替身测试，共22项通过；另11项开发环境回归通过。
- 生产 Compose 渲染、默认三容器/启用四容器、必需 token/digest、目录定位、现有市场/网关/安全/运行资源检查通过。
- actionlint 1.7.12、Bash语法和 diff whitespace 检查通过。
- 从当前源码构建本机架构的真实 worker 镜像，并以无外网、无宿主挂载的两个临时容器验证共享 namespace、ready、正确鉴权400/错误鉴权401及未派发；测试容器已清除。当前 CI 的 amd64 远端发布仍由实际 GitHub run 验证，未声明已完成双架构发布验收。
- 未读取 GitHub Secret 内容、未上传镜像、未推送、未SSH到生产或修改 MoviePilot。OpenCode 的现有纯文本路径单元测试通过，不代表所有免费模型及工具调用已被支持。

## 2026-10-30 线上只读核验

目标部署目录当时为空；GitHub Actions run `41028524086` 的 deploy job 为 skipped。这是该日期的历史记录，不代表当前部署状态；当前状态以服务器 `current-deployment` 和只读运行身份核验结果为准。隔离 smoke 验证不代表真实 Agent 已验收。

## 2026-10-30 流程优化的本地验证

- 26项部署及真实工作流 shell 回归、11项开发环境回归通过；包括双镜像摘要提升、非法摘要阻断、首次提升失败即停止和部署后分支 HEAD 复查。
- 完整部署检查列表中的 Compose、网关、市场、安全、资源与 Caddy 检查通过，actionlint、Bash 语法和 diff whitespace 检查通过。
- `docker buildx build --check --platform linux/amd64` 通过。真实 amd64 构建在 Alpine 包索引出现 SSL unexpected EOF 后停止，未完成镜像构建；最终编译、镜像运行与优化后的实际耗时仍由新 Actions 运行验证。
- 该次只读核验时服务器私有 `.env` 仍不存在；这是历史快照。工作流会明确报首次配置缺失，不会跳过该检查或生成临时密钥。
