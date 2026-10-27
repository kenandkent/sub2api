# sub2api 项目开发指南

> 本文档记录项目环境配置、常见坑点和注意事项，供 Claude Code 和团队成员参考。

本分支日常开发统一使用 `make dev`：Vite 热更新、Go/Air 增量编译与自动重启，PostgreSQL/Redis 使用独立 Docker 容器。地址、账号、缓存与停止命令见 [本地开发环境](deploy/LOCAL_DEVELOPMENT.md)。下文历史 Windows 手工环境配置可作为参考，不是当前开发环境的启动入口。

## 一、项目基本信息

| 项目 | 说明 |
|------|------|
| **上游仓库** | Wei-Shaw/sub2api |
| **Fork 仓库** | kenandkent/sub2api |
| **本地开发分支** | mysubapi |
| **技术栈** | Go 后端 (Ent ORM + Gin) + Vue3 前端 (pnpm) |
| **数据库** | PostgreSQL 16 + Redis |
| **包管理** | 后端: go modules, 前端: **pnpm**（不是 npm） |

## 二、本地环境配置

### PostgreSQL 16 (Windows 服务)

| 配置项 | 值 |
|--------|-----|
| 端口 | 5432 |
| psql 路径 | `C:\Program Files\PostgreSQL\16\bin\psql.exe` |
| pg_hba.conf | `C:\Program Files\PostgreSQL\16\data\pg_hba.conf` |
| 数据库凭据 | user=`sub2api`, password=`sub2api`, dbname=`sub2api` |
| 超级用户 | user=`postgres`, password=`postgres` |

### Redis

| 配置项 | 值 |
|--------|-----|
| 端口 | 6379 |
| 密码 | 无 |

### 开发工具

```bash
# golangci-lint（原流水线使用 v2.14.0，新流水线应与本地工具保持一致）
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0

# pnpm (前端包管理)
npm install -g pnpm
```

## 三、CI/CD 流水线

### 当前分支状态

`mysubapi` 是独立开发分支，已移除继承的 `backend-ci.yml`、`security-scan.yml`、`cla.yml` 和 `release.yml`，改用专用的 `.github/workflows/mysubapi-ci-cd.yml`。工作流仅针对本分支执行检查和镜像发布；服务器部署需要显式启用。

本地构建、部署脚本、GoReleaser 配置、发布辅助工具和安全检查工具继续保留，供手动使用和后续编写专用流水线时参考。它们不会自行触发自动化任务。

### 专用 CI/CD 的约定

- 目标环境为 `root@167.114.210.84:22`，部署根目录 `/home/workspace/subapi`；只允许可信 `mysubapi` 分支推送或手动运行进行发布。默认不执行部署，仓库变量 `MYSUBAPI_DEPLOY_ENABLED=true` 后才启用。
- 三份常规 Compose 从当前检出的完整仓库和根 `Dockerfile` 构建本地镜像 `sub2api:mysubapi`，通过 `pull_policy: build` 在启动时重新构建。服务器结合 `docker-compose.production.yml` 覆盖配置后，移除 build 并使用 GHCR 的不可变镜像 digest，无需上传源码。
- Go 版本以 `backend/go.mod` 为准，当前声明为 **1.27.3**；根 `Dockerfile` 已使用相同版本。`deploy/Dockerfile` 和 `backend/Dockerfile` 仍使用 **1.27.2**，若要采用这些入口，需先统一构建工具链。
- 前端使用 pnpm 9 和 `pnpm install --frozen-lockfile`，必须提交 `frontend/pnpm-lock.yaml`；根镜像使用 Node.js 24。
- 流水线应先执行后端单元/集成测试、前端检查和部署脚本检查，再构建镜像。部署时等待健康检查，并验证登录和实际网关请求；`/health` 只返回应用存活状态，不检查运行期间的数据库或 Redis 连接。
- 数据库迁移随服务启动执行，更新前需备份数据库并记录可回退的镜像版本；镜像回退不会自动回退数据库结构。
- 服务器连接、镜像仓库认证和运行密钥通过部署环境或 GitHub Secrets 配置，不写入仓库。
- 首次部署固定 `JWT_SECRET` 和 `TOTP_ENCRYPTION_KEY`，将运行配置、密钥和数据一同备份。JWT 已在数据库中持久化，后续修改环境变量不等于轮换数据库中的现有密钥。
- `deploy/docker-deploy.sh`、二进制安装脚本和后台更新服务仍面向上游仓库；定制版本接入专用 CI/CD 后应统一通过自有镜像升级，避免这些入口覆盖分支修改。

完整配置、首次服务器准备和应用恢复边界见 [mysubapi CI/CD](deploy/CICD_MYSUBAPI.md)。本轮只准备工作流与配置，不连接服务器或执行远程部署。

### 本地测试命令

```bash
# 后端单元测试
cd backend && go test -tags=unit ./...

# 后端集成测试
cd backend && go test -tags=integration ./...

# 代码质量检查
cd backend && golangci-lint run ./...

# 前端依赖安装（必须用 pnpm）
cd frontend && pnpm install
```

## 四、常见坑点 & 解决方案

### 坑 1：pnpm-lock.yaml 必须同步提交

**问题**：`package.json` 新增依赖后，CI 的 `pnpm install --frozen-lockfile` 失败。

**原因**：上游 CI 使用 pnpm，lock 文件不同步会报错。

**解决**：
```bash
cd frontend
pnpm install  # 更新 pnpm-lock.yaml
git add pnpm-lock.yaml
git commit -m "chore: update pnpm-lock.yaml"
```

---

### 坑 2：npm 和 pnpm 的 node_modules 冲突

**问题**：之前用 npm 装过 `node_modules`，pnpm install 报 `EPERM` 错误。

**解决**：
```bash
cd frontend
rm -rf node_modules  # 或 PowerShell: Remove-Item -Recurse -Force node_modules
pnpm install
```

---

### 坑 3：PowerShell 中 bcrypt hash 的 `$` 被转义

**问题**：bcrypt hash 格式如 `$2a$10$xxx...`，PowerShell 把 `$2a` 当变量解析，导致数据丢失。

**解决**：将 SQL 写入文件，用 `psql -f` 执行：
```bash
# 错误示范（PowerShell 会吃掉 $）
psql -c "INSERT INTO users ... VALUES ('$2a$10$...')"

# 正确做法
echo "INSERT INTO users ... VALUES ('\$2a\$10\$...')" > temp.sql
psql -U sub2api -h 127.0.0.1 -d sub2api -f temp.sql
```

---

### 坑 4：psql 不支持中文路径

**问题**：`psql -f "D:\中文路径\file.sql"` 报错找不到文件。

**解决**：复制到纯英文路径再执行：
```bash
cp "D:\中文路径\file.sql" "C:\temp.sql"
psql -f "C:\temp.sql"
```

---

### 坑 5：PostgreSQL 密码重置流程

**场景**：忘记 PostgreSQL 密码。

**步骤**：
1. 修改 `C:\Program Files\PostgreSQL\16\data\pg_hba.conf`
   ```
   # 将 scram-sha-256 改为 trust
   host    all    all    127.0.0.1/32    trust
   ```
2. 重启 PostgreSQL 服务
   ```powershell
   Restart-Service postgresql-x64-16
   ```
3. 无密码登录并重置
   ```bash
   psql -U postgres -h 127.0.0.1
   ALTER USER sub2api WITH PASSWORD 'sub2api';
   ALTER USER postgres WITH PASSWORD 'postgres';
   ```
4. 改回 `scram-sha-256` 并重启

---

### 坑 6：Go interface 新增方法后 test stub 必须补全

**问题**：给 interface 新增方法后，编译报错 `does not implement interface (missing method XXX)`。

**原因**：所有测试文件中实现该 interface 的 stub/mock 都必须补上新方法。

**解决**：
```bash
# 搜索所有实现该 interface 的 struct
cd backend
grep -r "type.*Stub.*struct" internal/
grep -r "type.*Mock.*struct" internal/

# 逐一补全新方法
```

---

### 坑 7：Windows 上 psql 连 localhost 的 IPv6 问题

**问题**：psql 连 `localhost` 先尝试 IPv6 (::1)，可能报错后再回退 IPv4。

**建议**：直接用 `127.0.0.1` 代替 `localhost`。

---

### 坑 8：Windows 没有 make 命令

**问题**：CI 里用 `make test-unit`，本地 Windows 没有 make。

**解决**：直接用 Makefile 里的原始命令：
```bash
# 代替 make test-unit
go test -tags=unit ./...

# 代替 make test-integration
go test -tags=integration ./...
```

---

### 坑 9：Ent Schema 修改后必须重新生成

**问题**：修改 `ent/schema/*.go` 后，代码不生效。

**解决**：
```bash
cd backend
go generate ./ent  # 重新生成 ent 代码（json.RawMessage 字段会生成为同类型的 jsontext.Value，属预期）
git add ent/       # 生成的文件也要提交
```

---

### 坑 10：前端测试看似正常，但后端调用失败（模型映射被批量误改）

**典型现象**：
- 前端按钮点测看起来正常；
- 实际通过 API/客户端调用时返回 `Service temporarily unavailable` 或提示无可用账号；
- 常见于 OpenAI 账号（例如 Codex 模型）在批量修改后突然不可用。

**根因**：
- OpenAI 账号编辑页默认不显式展示映射规则，容易让人误以为“没映射也没关系”；
- 但在**批量修改同时选中不同平台账号**（OpenAI + Antigravity/Gemini）时，模型白名单/映射可能被跨平台策略覆盖；
- 结果是 OpenAI 账号的关键模型映射丢失或被改坏，后端选不到可用账号。

**修复方案（按优先级）**：
1. **快速修复（推荐）**：在批量修改中补回正确的透传映射（例如 `gpt-5.3-codex -> gpt-5.3-codex-spark`）。
2. **彻底重建**：删除并重新添加全部相关账号（最稳但成本高）。

**关键经验**：
- 如果某模型已被软件内置默认映射覆盖，通常不需要额外再加透传；
- 但当上游模型更新快于本仓库默认映射时，**手动批量添加透传映射**是最简单、最低风险的临时兜底方案；
- 批量操作前尽量按平台分组，不要混选不同平台账号。

---

### 坑 11：PR 提交前检查清单

提交 PR 前务必本地验证：

- [ ] `go test -tags=unit ./...` 通过
- [ ] `go test -tags=integration ./...` 通过
- [ ] `golangci-lint run ./...` 无新增问题
- [ ] `pnpm-lock.yaml` 已同步（如果改了 package.json）
- [ ] 所有 test stub 补全新接口方法（如果改了 interface）
- [ ] Ent 生成的代码已提交（如果改了 schema）

## 五、常用命令速查

### 数据库操作

```bash
# 连接数据库
psql -U sub2api -h 127.0.0.1 -d sub2api

# 查看所有用户
psql -U postgres -h 127.0.0.1 -c "\du"

# 查看所有数据库
psql -U postgres -h 127.0.0.1 -c "\l"

# 执行 SQL 文件
psql -U sub2api -h 127.0.0.1 -d sub2api -f migration.sql
```

### Git 操作

```bash
# 同步上游
git fetch upstream
git checkout main
git merge upstream/main
git push origin main

# 创建功能分支
git checkout -b feature/xxx

# Rebase 到最新 main
git fetch upstream
git rebase upstream/main
```

### 前端操作

```bash
# 安装依赖（必须用 pnpm）
cd frontend
pnpm install

# 开发服务器
pnpm dev

# 构建
pnpm build
```

### 后端操作

```bash
# 运行服务器
cd backend
go run ./cmd/server/

# 生成 Ent 代码
go generate ./ent

# 运行测试
go test -tags=unit ./...
go test -tags=integration ./...

# Lint 检查
golangci-lint run ./...
```

## 六、项目结构速览

```
sub2api-bmai/
├── backend/
│   ├── cmd/server/          # 主程序入口
│   ├── ent/                 # Ent ORM 生成代码
│   │   └── schema/          # 数据库 Schema 定义
│   ├── internal/
│   │   ├── handler/         # HTTP 处理器
│   │   ├── service/         # 业务逻辑
│   │   ├── repository/      # 数据访问层
│   │   └── server/          # 服务器配置
│   ├── migrations/          # 数据库迁移脚本
│   └── config.yaml          # 配置文件
├── frontend/
│   ├── src/
│   │   ├── api/             # API 调用
│   │   ├── components/      # Vue 组件
│   │   ├── views/           # 页面视图
│   │   ├── types/           # TypeScript 类型
│   │   └── i18n/            # 国际化
│   ├── package.json         # 依赖配置
│   └── pnpm-lock.yaml       # pnpm 锁文件（必须提交）
└── .claude/
    └── CLAUDE.md            # 本文档
```

## 七、参考资源

- [OpenCode 接入、免费模型验证与 serve 方案边界](deploy/OPENCODE.md)
- [OpenCode 最小请求组合与实验限制](deploy/OPENCODE_MINIMIZATION.md)
- [OpenCode 模板配对质量对照与 v2 候选](deploy/OPENCODE_QUALITY_COMPARISON.md)
- [本项目 OpenCode Bun 上游接入设计](deploy/OPENCODE_BUN_UPSTREAM_DESIGN.md)
- [OpenCode 免费模型 Bun 本地接入与验证](deploy/opencode/LOCAL_FREE.md)
- [OmniRoute 免费模型 Bun 接入设计](deploy/OMNIROUTE_FREE_MODELS_DESIGN.md)
- [上游仓库](https://github.com/Wei-Shaw/sub2api)
- [Ent 文档](https://entgo.io/docs/getting-started)
- [Vue3 文档](https://vuejs.org/)
- [pnpm 文档](https://pnpm.io/)
