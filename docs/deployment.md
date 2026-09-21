# stressbot 部署与运维

> **对照仓库实际配置编写**（2026-09-20）。核对范围：`docker-compose.yml`、`Makefile`、`.github/workflows/release.yml`、`conf/deploy/` 目录全部内容、`conf/{admin,agent,stressbot}.toml`、`cmd/web/{nginx.conf,README.md,vite.config.ts}`、`internal/stresslog/`、`admin/httpapi/routes.go`、`admin/config.go`、`admin/mysql/schema_definition.go`、`admin/task/store.go`、`agent/session/supervisor.go`、`config/pprof.go`。其中静态托管行为（Admin 直连与 nginx 两种形态）经本机启动实例实测验证。
>
> 交叉引用：[docs/README.md](README.md) 文档地图；[architecture.md](architecture.md)「构建、发布与部署」一节——本文与其分工：那篇讲构建链路全景，本文展开**落地与运维**细节，CI 细节以 `.github/workflows/release.yml` 为准、本文只作摘要；日志字段约定与告警详见 [logging.md](logging.md)；分布式容错细节见 distributed-architecture.md / distributed-error-handling.md。

## 1. 部署拓扑

```
                    ┌──────────────────────────── Admin 服务器 ────────────────────────────┐
 浏览器 ──HTTP:7718──▶ [server] HTTP 管理面：前端静态托管 + /sbot/ REST API（OpenAPI 契约）  │
                    │                                                                        │
 多台 Agent ◀─gRPC:7720── [controlPlane] 控制面：注册 / 心跳 / 任务下发 / 指标回收          │
 (Agent 主动外拨)      └──────────────┬──────────────────────────────┬─────────────────────┘
   │                                  │(可选)                         │(可选)
   ├──TCP/UDP/HTTP──▶ 目标游戏服务器   ▼                              ▼
   │                                MySQL（历史归档、模板库）      Redis（share 共享状态）
   └──无本地监听端口（不开放 HTTP 控制面）
```

- **Admin**（`cmd/admin`）：中心调度。双监听面：管理面 HTTP（`[server]`，默认 `127.0.0.1:7718`，托管前端 + `/sbot/` API，`/sbot/openapi.yaml` 与 `/sbot/docs` 提供契约与 Swagger UI）；控制面 gRPC（`[controlPlane]`，默认 `127.0.0.1:7720`）。
- **Agent**（`cmd/agent`）：施压节点。主动连接 Admin 控制面，自身不开放任何本地控制面；对目标游戏服务器发起 TCP/UDP 连接与 HTTP 请求。
- **MySQL 可选**：`[mysql] host` 留空即禁用。禁用时历史归档与流程/模板库不可用，基本管理功能不受影响。
- **Redis 可选**：`[redis] host` 留空即禁用。供 Lua 脚本 `require("share")` 做跨机器人/跨节点共享状态。
- **单机模式**（`cmd/stressbot`）：单进程完成全部压测，不需要 Admin/Agent，配置 `conf/stressbot.toml`。

## 2. 获取与构建

### 2.1 源码构建

Go ≥ 1.26（`go.mod` 声明 `go 1.26.0`）。三个入口共用一个 module，均为 `-config` 指定 TOML 配置、`-d` 守护进程化（仅 Linux）：

```bash
go build -o stressbot.exe ./cmd/stressbot   # 单机压测
go build -o agent.exe      ./cmd/agent      # Agent 节点
go build -o admin.exe      ./cmd/admin      # Admin 服务器
```

版本号经 `-ldflags "-X main.Version=..."` 注入（各入口均有 `var Version = "dev"`）。

运行形式（开发期可 `go run` 等价替换）：

```bash
./admin.exe      -config conf/admin.toml
./agent.exe      -config conf/agent.toml
./stressbot.exe  -config conf/stressbot.toml
```

- 三入口公共 flag：`-config`（配置路径，默认 `conf/<模式名>.toml`）、`-d`（守护进程，仅 Linux）。
- 单机 stressbot 另有四个资源覆盖 flag（空值回退 `<config 所在目录>` 下同名子目录）：`-flow`（默认 `<conf>/flow/flow.json`）、`-proto`、`-scripts`、`-adapter`——切换压测场景无需挪文件，例如 `stressbot.exe -config conf/stressbot.toml -flow conf/flow/rank.json`。
- Admin / Agent 只读各自的 TOML，无资源覆盖 flag（资源经 Admin 下发的内容寻址资源包流转，见 docs/agent-implementation.md）。

### 2.2 Makefile

| 目标 | 内容 |
| --- | --- |
| `make all`（默认） | `frontend` + `build` |
| `make frontend` | `cd cmd/web && npm install && npm run build` |
| `make build` | `build-linux` + `build-windows`：交叉编译 **agent / admin** 到 `bin/`（`CGO_ENABLED=0`、`-trimpath`、注入 git describe 版本）。**不含单机 stressbot 二进制** |
| `make build-current` | 当前平台编译 agent / admin 到 `bin/` |
| `make check` | `go build ./...` + `cd cmd/web && npx tsc --noEmit` |
| `make test` | `cd cmd/web && npm run test`（Vitest） |
| `make dev-agent` / `dev-admin` / `dev-stressbot` / `dev-web` | 各进程与前端 dev server 的开发运行 |
| `make clean` / `make version` | 清理 `bin/`、`cmd/web/dist`；显示版本号 |

### 2.3 GitHub Release（CI 摘要）

`.github/workflows/release.yml`：推送 `v*` tag 触发，Node 24 `npm ci && npm run build` 出前端，`CGO_ENABLED=0` 交叉编译 **6 个平台**（linux/amd64、linux/arm64、windows/amd64、windows/arm64、darwin/amd64、darwin/arm64）。每个平台的发布包含并逐一断言存在：

- 三个二进制：`stressbot` / `agent` / `admin`（Windows 带 `.exe`）；
- `conf/` 全量（含 `stressbot.toml`、`agent.toml`、`admin.toml`、`flow/`、`proto/`、`scripts/`、`adapter/`）；
- 包根 `dist/`（前端构建产物，由 `cmd/web/dist` 拷入）。
  nginx 样例随 `conf/` 带出，在 `conf/deploy/nginx/nginx.conf`。

Windows 打 zip、其余打 tar.gz，`softprops/action-gh-release` 附自动生成说明上传。

### 2.4 前端构建

```bash
cd cmd/web
npm ci            # 或 npm install
npm run build     # tsc -b && vite build → dist/
```

- `vite.config.ts` 中 `base: '/stressbot/'`：应用挂在子路径，对部署形态有硬性影响（见 §6）。
- sourcemap 默认关闭；排查线上问题时 `npx vite build --sourcemap` 单独出带映射的包。
- Admin 侧 `server.staticDir`（`conf/admin.toml`，默认 `cmd/web/dist`，相对进程工作目录）指向该产物。

## 3. Docker 演示栈（docker-compose.yml）

仓库根 `docker-compose.yml` 是一套**本机演示/排障栈**，含 6 个服务（注意其中含明文弱密码，勿直接暴露公网）：

| 服务 | 镜像 | 端口 | 关键配置 |
| --- | --- | --- | --- |
| `mysql` | mysql:latest | 3306 | `--character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci`；库 `stressbot`、root/123456；数据落宿主机 bind 挂载 `D:/docker/volumes/stressbot-mysql`；healthcheck `mysqladmin ping` |
| `redis` | redis:latest | 6379 | `redis-server --appendonly yes`；数据 `D:/docker/volumes/stressbot-redis`；healthcheck `redis-cli ping` |
| `nginx` | nginx:latest | 80 | 挂载 `conf/deploy/nginx/nginx.conf` → `/etc/nginx/conf.d/default.conf:ro`；挂载 `cmd/web/dist`（`npm run build` 产物）→ `/data/stressbot/web/dist:ro` 作前端静态根 |
| `loki` | grafana/loki:latest | 3100 | 配置 `conf/deploy/loki/config.yaml`；数据 `D:/docker/volumes/stressbot-loki` |
| `grafana` | grafana/grafana:latest | 3000 | admin/admin；只读挂载 `conf/deploy/grafana/provisioning`（数据源自動配置）；healthcheck `curl /api/health`；`depends_on: loki` |
| `vector` | timberio/vector:latest-alpine | — | 采集 Agent 日志进 Loki（详见 §7）；环境变量 `AGENT_ID=agent-001`；`depends_on: loki` |

要点与坑：

- 全部服务 `restart: unless-stopped`；`docker compose up -d` 起栈。
- Admin/Agent 本体**不在** compose 内，跑在宿主机上；容器内 nginx 经 `host.docker.internal:7718` 回连宿主机 Admin（`conf/deploy/nginx/nginx.conf` 中 `/sbot/` 的 `proxy_pass`）。
- MySQL 无需手工建表：Admin 启动连接后自动执行内嵌 DDL（实测启动日志「MySQL 已连接且数据库结构初始化完成」，见 §8）。
- Vector 的 checkpoint 与磁盘缓冲**必须**落 named volume（`vector-data`）：挂 Windows 宿主机目录会触发 ENODATA（os error 61）使 Vector 崩溃循环——这是配置里显式写明的教训。
- Grafana 的看板 JSON（`conf/deploy/grafana/dashboards/agent-logs.json`）**没有**对应的 provisioning 挂载（compose 只挂了 `provisioning/datasources`），需要在 Grafana 界面手工导入。
- MySQL/Redis 若要给宿主机 Admin 用，`conf/admin.toml` 指向 `127.0.0.1:3306/6379` 即可（端口已映射出来）。
- 仓库内配置与日志挂载使用相对路径（`./conf/deploy/...`、`./cmd/web/dist`、`./log`）；数据卷仍是宿主机绝对路径（`D:/docker/volumes/...`），换机器需替换。

## 4. conf/deploy/ 中间件配置导读

部署中间件配置随仓库入库（`conf/deploy/`），docker compose 直接按相对路径挂载使用：

| 路径 | 说明 |
| --- | --- |
| `nginx/nginx.conf` | 生产样例：与另一个 GM 站点共用 80 端口的完整 server 配置。stressbot 相关三段：`^~ /stressbot/` alias 到 `/data/stressbot/web/dist/` + `@stressbot` SPA 兜底、`/sbot/` 反代 `host.docker.internal:7718`、静态资源正则段一年强缓存（见 §6）。裸机使用时把 `host.docker.internal` 换成 `127.0.0.1` |
| `loki/config.yaml` | Loki 单机本地配置：filesystem + tsdb v13、无认证、3100 端口、保留 168h——与 `conf/agent.toml` 的 `log.maxAge = 7` 对齐，本地日志过期即查不到 |
| `grafana/provisioning/datasources/loki.yaml` | Grafana 启动时自动配置 Loki 数据源（`http://loki:3100`），无需在界面手动添加 |
| `grafana/dashboards/agent-logs.json` | Agent 日志看板：`agent_id` 变量筛选节点、错误计数 / 级别趋势 / 明细表。compose 只挂了 `provisioning/datasources`，看板需在 Grafana 界面手工导入一次 |
| `vector/vector-agent.toml` | Vector 采集端配置：file source 只盯 `agent.log`、解析 zap JSON 并注入 `AGENT_ID` 节点身份、256MB 磁盘缓冲写入 Loki（详见 §7.2） |

## 5. 进程守护

### 5.1 supervisor（Linux）

用 supervisor 等 init 系统守护 Admin/Agent 的要点（以下为可直接使用的正确示例，换其它 init 系统时同样适用）：

```ini
[program:stressbot-admin]
command=/opt/stressbot/admin -config /opt/stressbot/conf/admin.toml
directory=/opt/stressbot          ; 工作目录影响 staticDir、log.path 等相对路径解析
autostart=true
autorestart=unexpected            ; 只在非预期退出码时重启，配置错误导致的主动退出不会被无限拉起
exitcodes=0
startsecs=5                       ; 稳定运行 5s 才算启动成功
startretries=3
stopsignal=TERM
stopwaitsecs=20                   ; Admin 20s / Agent 30s——给优雅退出留窗口（Agent 停任务、上报终态报告比 Admin 慢）
stopasgroup=true
killasgroup=true
user=stressbot
redirect_stderr=true
stdout_logfile=/var/log/stressbot/admin-supervisor.log
stdout_logfile_maxbytes=50MB
stdout_logfile_backups=5
```

supervisor 的 stdout 只是兜底输出，进程自身文件日志另有路径（§7.1）。

### 5.2 Windows

- 三个入口均支持 `-d` flag 或配置 `daemon = true` 守护进程化，但**仅 Linux 生效，Windows 下忽略**（`internal/daemon`）。
- Windows 长期运行建议用 `nssm` 等工具注册系统服务（等价于 supervisor 的「自动重启 + 日志重定向」），或开独立窗口直接运行——注意相对配置路径依赖当前工作目录；停止靠 Ctrl+C / 进程终止。

## 6. 反向代理与静态托管

前端以 `base: '/stressbot/'` 构建，`dist/index.html` 内所有资源引用带 `/stressbot/` 前缀。两种托管形态：

### 形态 A：Admin 内置 staticDir（同源）

`admin/httpapi/routes.go` 把 `http.FileServer(http.Dir(staticDir))` 挂在 mux 根 `/`，`/sbot/*` API 与静态资源同端口同源。**注意（本机实测验证）：当前实现没有 `/stressbot/` 前缀改写——直连 `http://127.0.0.1:7718/` 返回 200 的 `dist/index.html`，但其引用的 `/stressbot/assets/*` 会 404；`/stressbot/` 本身也是 404。** 即默认 `staticDir = cmd/web/dist` 的直连形态与前端 base 路径不匹配，页面无法加载。

可行的同源用法（已实测）：让 `staticDir` 指向一个**根目录**、把 dist 内容放进其 `stressbot/` 子目录（如 `webroot/stressbot/index.html`），则 `/stressbot/assets/*` 按 URL 路径直接命中，`/sbot/` API 同源可用。

影响面：开发工作流（`npm run dev`）不经此路径——页面由 Vite dev server 供给、API 由其代理直连 Admin，静态托管是否可用与开发形态无关。会踩到这一错位的是「同源直连部署」场景，典型如 Release 包用户不起 nginx、直接访问 7718。

### 形态 B：nginx 独立部署（当前实际可用形态）

参考 `cmd/web/nginx.conf` 与 `conf/deploy/nginx/nginx.conf`（后者即 compose 挂载的那份，两份的 stressbot 段一致）：

```nginx
location ^~ /stressbot/ {                    # ^~ 前缀匹配优先于下方 .css/.js 正则缓存段
    alias /data/stressbot/web/dist/;         # /stressbot/xxx → dist/xxx，与 base 对齐
    try_files $uri $uri/ @stressbot;
}
location @stressbot {                        # SPA 路由兜底：/stressbot/任意 → index.html
    root /data/stressbot/web/dist;
    rewrite .* /index.html break;
}
location /sbot/ {                            # API 反代 Admin（容器内用 host.docker.internal）
    proxy_pass http://127.0.0.1:7718;
    proxy_set_header Host $host;             # + X-Real-IP / X-Forwarded-For / X-Forwarded-Proto
}
location ~* \.(?:css|js|jpg|jpeg|png|gif|ico|svg|woff2?)$ {   # 静态资源一年强缓存
    expires 1y;
    add_header Cache-Control "public, immutable";
}
```

部署动作（`cmd/web/nginx.conf` 头部注释）：`cp -r dist /data/stressbot/web/` 后 `nginx -t && nginx -s reload`。两份示例都是「与其它项目共用 80 端口按前缀分流」的写法，并带 `client_max_body_size 100m`（前端资源上传是 multipart，需要放大默认 1m 限制）。公网暴露建议在反代层做 TLS 终结与认证，并拒绝把 `/sbot/agent/*` 内部上行路由从公网入口转发。

## 7. 日志与观测接入

### 7.1 进程本地日志（internal/stresslog）

三个进程共用 `internal/stresslog`（zap + lumberjack），配置在各自 TOML 的 `[log]` 段：

| 进程 | 默认路径 | 轮转（样例配置） |
| --- | --- | --- |
| Admin | `log/admin.log` | 100MB × 10 份，保留 30 天，gzip |
| Agent | `log/agent.log` | 100MB × 5 份，保留 7 天，gzip |
| stressbot 单机 | `log/stressbot.log` | 100MB × 5 份，保留 7 天，gzip |

文件侧是 **JSON Lines**（zap JSON encoder；时间戳 UTC RFC3339Nano；每行带 `service` 进程名字段与 `caller`）。写入经 256KB 缓冲、每秒批量刷盘；**error 及以上级别立即刷缓冲**，崩溃前不丢关键日志。`printConsole = true` 时控制台同步输出人类可读格式。DPanic 及以上自动推企业微信 webhook 告警（`[log] weChatToken`，仅单机配置样例中给出，留空不告警）。字段命名与 zap 内建键（`message`/`level`/`timestamp`/`caller` 等）严格避让（有全仓测试守护）。字段约定、日志 API 与刷新语义的完整说明见 [logging.md](logging.md)。

### 7.2 Vector → Loki → Grafana（Agent 日志链路）

compose 栈 + `conf/deploy/` 配置构成的链路，要点：

- **采集**（`conf/deploy/vector/vector-agent.toml`）：file source 只盯 `agent.log` 一个文件（宿主机 `log/` 只读挂载为 `/var/log/stressbot`）；默认 `start_at_beginning = false` 只采启动后新增行。
- **解析**（remap）：`parse_json(.message)` 展平 zap JSON → `parse_timestamp(.timestamp, "%+")` 规范时间 → 注入 `AGENT_ID` 环境变量作节点身份（多台 Agent 各自改 compose 的 env）→ `level` 小写化。
- **标签纪律**：Loki label 只放低基数维度（`job=stressbot-agent`、`agent_id`、`level`）；taskID 等高基数字段留在行内 JSON，查询时 `| json` 过滤。
- **缓冲**：sink 磁盘缓冲 256MB，Loki 不可达或压测打满网卡时不丢日志；`data_dir` 必须在 Docker named volume 上（§3 的 ENODATA 坑）。
- **存储**（`conf/deploy/loki/config.yaml`）：单机 filesystem + tsdb v13、无认证、保留 168h——与 `conf/agent.toml` 的 `log.maxAge = 7` 对齐，本地日志过期即查不到。
- **查看**（Grafana）：数据源由 `conf/deploy/grafana/provisioning/datasources/loki.yaml` 自动配置；看板 `conf/deploy/grafana/dashboards/agent-logs.json`（`agent_id` 变量筛选节点、错误计数/级别趋势/明细表）需手工导入。

## 8. 升级与运维注意

- **Admin 重启即判死活跃任务**：任务仓储加载时把所有活跃态任务恢复为 `failed`（`admin/task/store.go`，日志「恢复活跃任务为 failed」）。重启 Admin 前先正常停止任务，否则任务与归档直接进 failed。
- **MySQL 表结构无迁移链**：由 `admin/mysql/schema_definition.go` 内嵌 DDL 在启动时 `CREATE TABLE IF NOT EXISTS` 建 11 张表（`task_history`、`task_assignment`、`task_report`、`task_aggregated`、`task_timeseries`、`task_config_archive`、`task_agent_events`、`task_meta`、`flow_template`、`action_template`、`listen_template`）。已有库的增量变更无自动机制，升级时人工比对新旧 DDL 执行。历史数据按 `mysql.retentionDays`（默认 90 天）每日清理。
- **Agent 断线重连**：指数退避（`retry.NewExponentialBackOff`，Factor 2、Jitter 1）——`agent.reconnect.initialInterval = 5s` 倍增至 `maxInterval = 30s` 封顶，实际等待在 `[0, 2×maxInterval]` 抖动；`reconnectMaxRetries = -1` 无限重连；会话存活超过 maxInterval 后退避重置。**租约联动**：断连超过 Admin 的 `controlPlane.leaseDuration`（45s）任务被取消，因此必须满足 `2×maxInterval + 拨号握手 < leaseDuration(45s) < offlineAfter(60s)`——调大 reconnect 参数前先核这条不等式。
- **容量规划**：`agent.maxBots`（默认 5000，≤0 也取 5000）是 Admin 分配任务的上限，不会超配。施压机自身 CPU 饱和会污染 P99/Apdex——压测中看 pprof 端口的 `/debug/sched`（Go 调度延迟分位数）区分「服务器劣化」与「施压机拥塞」；必要时增加 Agent 节点分摊。`monitor.sketch`（relativeAccuracy/maxBins）**Admin 与所有 Agent 必须一致**，否则 sketch 合并结果错误。
- **配置纪律**：TOML 严格模式（拼错字段名启动失败）；`${VAR}` / `${VAR:-default}` 环境变量展开，密码推荐 `${STRESSBOT_MYSQL_PASSWORD}` 注入（未定义直接启动报错，不静默置空）。
- **优雅退出与停止方式**：三进程均以 SIGINT/SIGTERM 触发优雅退出（`signal.NotifyContext`），Admin 退出时先关 Agent 会话（下发「Admin 正在关闭」）、gRPC 最多等 10s 强停、HTTP 关 keep-alive 后最多 10s 收尾——supervisor 的 `stopwaitsecs`（20/30s）即按此留的余量。Agent 停止单台走 Admin：`POST /sbot/agents/{id}/shutdown`，批量 `POST /sbot/agents/shutdown-all`。
- **升级发布顺序**：先升级并重启 Admin（活跃任务清 failed），再逐台重启 Agent（Agent 重启自动重注册）；前端更新只需替换 nginx 静态目录后 reload。

## 9. 健康检查与端口清单

stressbot 自身进程**没有专用 /healthz 端点**；管理面探活可用 `GET /sbot/capabilities`（实测 200，无副作用只读），compose 内中间件各有自己的 healthcheck。

| 进程 / 服务 | 端口 | 协议 | 配置位置 | 默认状态 |
| --- | --- | --- | --- | --- |
| Admin 管理面 | 7718 | HTTP（前端静态 + `/sbot/` API） | `conf/admin.toml [server]`（`listenHost` 默认 127.0.0.1） | 启用 |
| Admin 控制面 | 7720 | gRPC（Agent 上行） | `conf/admin.toml [controlPlane]` | 启用 |
| Admin pprof | 6060 | HTTP（`/debug/pprof` + `/debug/*` 观测端点） | `conf/admin.toml [pprof]` | 样例中注释掉（未启用） |
| Agent | — | 无监听端口（主动外拨 Admin） | — | — |
| Agent pprof | 6060 | HTTP（`/debug/pprof` + `/metrics` 指标快照） | `conf/agent.toml [pprof]` | 启用 |
| stressbot 单机 monitor | 6061 | HTTP（`/metrics` JSON，供外部采集） | `conf/stressbot.toml [monitor.http]` | 启用 |
| stressbot 单机 pprof | 6060 | HTTP | `conf/stressbot.toml [pprof]` | 启用 |
| compose: nginx | 80 | HTTP | `docker-compose.yml` | 演示栈 |
| compose: MySQL | 3306 | TCP | 同上（utf8mb4） | 演示栈 |
| compose: Redis | 6379 | TCP | 同上（appendonly） | 演示栈 |
| compose: Loki | 3100 | HTTP | 同上 | 演示栈 |
| compose: Grafana | 3000 | HTTP | 同上 | 演示栈 |

同机混部时注意 6060 冲突：pprof 端口在三种模式下默认都是 6060，`config.StartPprofServer` 监听 `:port` 全接口；单机进程还另占 6061。

pprof 端口上除标准 `/debug/pprof` 外，还经 `http.DefaultServeMux` 挂了 stressbot 专有的调优观测端点（`internal/debughttp`、`script`、`protocol/protox` 等包 init 注册，启用 pprof 的进程自动获得）：

| 端点 | 用途 |
| --- | --- |
| `/debug/sched` | Go 调度延迟直方图——量化施压机自身负载对 P99/Apdex 的污染（压测中看 `sinceLast` 组） |
| `/debug/statekeys` | `robot.get` / `robot.get_path` 按 key 计数，定位整读热点；默认关闭，`?enable=1` 打开，`?reset=1` 分窗口 |
| `/debug/dedup` | 广播 wire 去重缓存命中统计 |
| `/debug/wire` | wire 消费路径影子采样状态（失配自动降级该 schema） |

Agent 模式未启用 monitor HTTP 端点（指标经 gRPC 上报），但 pprof 端口上的 `/metrics` 仍可看本地快照（`conf/agent.toml [pprof]` 注释）。
