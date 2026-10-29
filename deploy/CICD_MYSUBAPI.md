# mysubapi 分支的部署 CI/CD

目标：`root@167.114.210.84:22`，部署根目录 **`/home/workspace/subapi`**，镜像仓库 `ghcr.io/kenandkent/sub2api`。配置入口为 `.github/workflows/mysubapi-ci-cd.yml`。本轮只准备代码和本地验证，不推送、不连接服务器、不启用线上部署。

## 流水线行为

1. 向 `mysubapi` 推送、向该分支提 PR，或手动触发时，运行部署回归、后端 unit/integration、真实基础设施网关测试、前端 lint/typecheck/关键集和全量 Vitest。
2. 仅原仓库 `kenandkent/sub2api` 的 `mysubapi` 分支能发布镜像；PR 不发布和部署。
3. 检查成功后，从同一提交构建应用和 Bun worker 的 `linux/amd64`、`linux/arm64` 镜像，发布 SHA 标签及 `mysubapi` 标签。worker 镜像还会在无外网的隔离容器中验证启动、共享网络、正确/错误鉴权及“未派发上游”的结果。
4. 仅 `MYSUBAPI_DEPLOY_ENABLED=true` 时部署。先检查本次 SHA 是否仍是分支最新提交，跳过已被后续提交取代的部署。发布和部署均串行，不中断在途部署。
5. SSH 上传发布文件到唯一的 `incoming/<SHA>-<run>-<attempt>`，再切换为 `releases/<id>`；镜像始终按 `@sha256:...` 部署，不使用可变标签定位版本。

GitHub 的手动运行入口要求工作流也已存在于默认分支；仅把文件放在 `mysubapi` 不保证出现 Run workflow 按钮。向 `mysubapi` 推送仍是本流程的正常触发方式。

## GitHub 需要配置什么

你已在 Repository secrets 添加 **`SSH_PRIVATE_KEY`**，工作流直接使用该名称，也兼容旧的 `DEPLOY_SSH_KEY`。私钥须可无交互使用，并获准登录目标服务器。无需把私钥提交到代码或再创建同内容 Secret。

服务器 ED25519 公钥已从本机此前信任的 `known_hosts` 提取到 `deploy/ssh/mysubapi_known_hosts`。它是公开身份信息，不含 SSH 私钥。SSH 开启严格主机校验，并禁用其他用户/系统配置作为额外信任源。换主机、端口或确认过主机密钥轮换时，在 Environment / Repository secret **`DEPLOY_KNOWN_HOSTS`** 设置经独立核验的新条目；覆盖主机/端口却未提供对应 pin 时失败关闭，不执行 `ssh-keyscan` 即信任。

在仓库的 Actions → Variables 设置：

| Variable | 值或默认值 | 作用 |
| --- | --- | --- |
| `MYSUBAPI_DEPLOY_ENABLED` | 初始不设置；准备好后设 `true` | 总部署开关，必须是 Repository variable，因为 job 条件先于 Environment 加载 |
| `DEPLOY_HOST` | `167.114.210.84` | 可省略 |
| `DEPLOY_PORT` | `22` | 可省略 |
| `DEPLOY_USER` | `root` | 可省略 |
| `DEPLOY_PATH` | `/home/workspace/subapi` | 可省略；禁止 `.` / `..` 路径段 |
| `MYSUBAPI_OPENCODE_FREE_ENABLED` | 默认关闭 | 可选 worker 部署开关，可放 Repository 或 `mysubapi-production` Environment |

部署 job 使用 `mysubapi-production` Environment，可在 GitHub 中配置其分支限制和人工审批。Repository secret 会直接供该 job 使用，不必复制到 Environment。普通 push 默认仍会在测试后发布镜像；不设置总开关时不会 SSH 部署。

应用镜像由 `GITHUB_TOKEN` 的 `packages:write` 发布，部署使用短期 `packages:read` token，经 SSH stdin 登录 GHCR。无需永久 GHCR 密码；若包已存在，分别检查应用包和 `sub2api-opencode-free-worker` 包的 Actions 仓库访问权限，避免 pull 返回 403。

## 首次启用前的服务器准备

服务器需 Docker Engine、Docker Compose **>=2.24.4**、Bash >=4、`timeout`、awk 和标准 Linux 工具。使用本机默认 Docker context；不要将部署指向远程 Docker daemon。系统级及标准用户目录 `cli-plugins` 中的 Compose 插件均可使用；临时认证目录通过只读链接保留用户插件发现路径，不复制原 Docker 登录信息。

在目标目录保存私有 `/home/workspace/subapi/.env`，权限设 `600`，优先以 `.env.mysubapi.production.example` 为模板（完整选项参考 `.env.example`），至少配置：

- `POSTGRES_PASSWORD`、`REDIS_PASSWORD`；
- 首次管理员 `ADMIN_EMAIL`、`ADMIN_PASSWORD`；
- 稳定的 `JWT_SECRET`、`TOTP_ENCRYPTION_KEY`；
- `BIND_HOST`、`SERVER_PORT`。同机反代推荐 `BIND_HOST=127.0.0.1`，对外域名与 HTTPS 由反向代理配置。

这些配置由服务器维护，CI 不上传或覆盖开发机 `.env`。只有 Compose 明确透传的环境变量会进入容器；不要假定模板中每项都自动生效。新安装先使用与实际 PostgreSQL 容量匹配的应用连接池，例如 `DATABASE_MAX_OPEN_CONNS=50`、`DATABASE_MAX_IDLE_CONNS=10`；模板的高并发示例不是对容器实际连接上限的保证。

默认是应用、PostgreSQL、Redis **三个容器**。Compose 项目名为 `mysubapi`；固定容器名沿用 `sub2api`、`sub2api-postgres`、`sub2api-redis`。首次接管已有服务前核实容器归属、数据库版本和数据路径；不要创建冲突项目或覆盖其他目录。

## 可选 Bun worker

默认关闭。需要部署已有的 Bun transport 时，将 `MYSUBAPI_OPENCODE_FREE_ENABLED=true`，并在服务器 `.env` 配置 `OPENCODE_FREE_BUN_TOKEN`：至少32个不含空格的 ASCII 可见字符，推荐 `openssl rand -hex 32` 生成。该开关只部署现有 transport，**不扩大已有模型/工具能力，也不代表官方已允许第三方调用免费聊天模型**；当前兼容范围另见 `deploy/opencode/COMPATIBILITY.md`。

启用后增加第四个 `opencode-free-worker` 容器。生产 overlay 只拉取同一提交的 worker 摘要，不在服务器编译；它共享应用网络命名空间、仅监听 `127.0.0.1:19090`、不发布端口，不读取 PostgreSQL/JWT 等其他秘密，使用只读文件系统和临时 `/tmp`。应用每次重建后 worker 强制重建，避免留在旧网络命名空间。

应用和 worker 从同一私有变量取令牌；部署还从实际运行中的应用容器通过匿名管道读取令牌，向 worker 发送无效空请求，必须得到已鉴权的400及 `x-sub2api-dispatched:false`。探针不调用任何真实模型，令牌不放命令参数、发布状态或日志。

启用和停用均支持回滚。准备停用时，仍保留合法令牌，直到不再需要回滚旧的 enabled 版本；若旧版本必需配置已缺失，部署会在替换应用前拒绝。应用成功切到关闭模式后，旧 worker 才停止并移除。

## 数据、验证与失败恢复

持久目录固定在 `/home/workspace/subapi/data`、`postgres_data`、`redis_data`，通过 `--project-directory` 避免落到各个 release 内。发布目录无需源码、Go、Node 或 Bun 宿主安装。

部署锁覆盖拉取、配置预检、依赖就绪、备份、应用/worker替换和恢复。镜像拉取最多三次，每次默认180秒；先验证当前及可回滚配置，再拉取全部所需镜像。依赖 `up` 可能启动或重建 PostgreSQL/Redis，不能把“应用尚未替换”理解为依赖绝不改变。

SQL查询通过后生成非空、私有权限的 `pg_dump -Fc` 备份，再更新应用。容器健康、初始化状态、公开设置，以及启用时的 worker 鉴权均通过后才原子更新 `current-deployment`。新状态是 `RELEASE_ID APP_DIGEST WORKER_DIGEST_OR_-`，兼容读取旧的两字段状态。

替换失败会尝试用旧发布的应用和 worker 摘要恢复，仍以失败退出；不会把回滚成功当本次发布成功。**数据库迁移不自动逆转**，旧二进制可能不兼容新 schema。保留备份，依照 `MARKETPLACE_RUNBOOK.md` 的迁移边界选择兼容版本，不能盲目恢复旧数据库或镜像。发布、备份和旧镜像不会自动删除，也不执行全局 Docker prune。

## 本地验证

```bash
python3 -B -m unittest discover -s deploy/tests -p 'test_mysubapi_*.py'
sh deploy/tests/docker-compose-production-test.sh
bash -n deploy/deploy-mysubapi.sh
# 构建本机架构的现有 worker；测试禁止外网且不调用真实模型
docker build -f deploy/opencode/worker/Dockerfile -t sub2api-opencode-free-worker:ci-verify .
WORKER_SMOKE_IMAGE=sub2api-opencode-free-worker:ci-verify python3 -B deploy/tests/opencode_worker_smoke.py
```

脚本回归注入拉取、旧配置、备份、启动、鉴权及回滚失败；SSH步骤通过本地替身验证 pin、路径、两镜像参数、Secret不输出和失败即停止。真实 worker smoke 是隔离网络下的运行时验证；它不能替代线上 GHCR 权限、SSH 私钥可用性或生产完整 Compose 验收。

## 2026-10-29 本地验证结果

- 18项部署 helper 测试和4项真实工作流 SSH 步骤替身测试，共22项通过；另11项开发环境回归通过。
- 生产 Compose 渲染、默认三容器/启用四容器、必需 token/digest、目录定位、现有市场/网关/安全/运行资源检查通过。
- actionlint 1.7.12、Bash语法和 diff whitespace 检查通过。
- 从当前源码构建本机架构的真实 worker 镜像，并以无外网、无宿主挂载的两个临时容器验证共享 namespace、ready、正确鉴权400/错误鉴权401及未派发；测试容器已清除。双架构远端发布仍由实际 GitHub run 验证。
- 未读取 GitHub Secret 内容、未上传镜像、未推送、未SSH到生产或修改 MoviePilot。OpenCode 的现有纯文本路径单元测试通过，不代表所有免费模型及工具调用已被支持。
