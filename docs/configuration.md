# 配置体系技术文档

> 本文档对照实际代码编写（2026-09-20），反映已实施状态。
> 核对范围：`config/`（load.go / duration.go / pool.go / pprof.go）、`config/validation/`、`standalone/`、`runner/`、`agent/config.go`、`admin/config.go`、`agent/task/{types,runner}.go`、`robot/{robot,manager}.go`、`monitor/collector.go`、`state/shared/config.go`、`internal/stresslog/logger.go`、三个 `cmd/*/main.go`，以及三份 `conf/*.toml` 样例。
> 流程配置（flow.json）的数据模型见 `docs/flow-node-system.md`；codec 配置（`*_codec.json`）见 `docs/adapter-layer.md`；分布式任务下发链路见 `docs/distributed-architecture.md`——本文只写「配置如何被加载、校验、生效」，不重复其内容。

---

## 1. 配置全景

### 1.1 三进程配置文件总览

| 进程 | 配置文件 | 顶层段 | 结构定义 |
| --- | --- | --- | --- |
| `cmd/stressbot`（单机） | `conf/stressbot.toml` | `daemon` / `log` / `monitor` / `pprof` / `standalone` / 可选 `redis` / `network` | `standalone/config.go:38-46` |
| `cmd/agent`（Agent 节点） | `conf/agent.toml` | `daemon` / `log` / `monitor` / `pprof` / `agent` | `agent/config.go:17-24` |
| `cmd/admin`（Admin 服务器） | `conf/admin.toml` | `server` / `controlPlane` / 可选 `mysql` / `redis` / `log` / `pprof` / `daemon` | `admin/config.go:18-27` |

三份配置均为 TOML，共用同一个加载器 `config.LoadTOML`（见 §2）。Admin 没有 `[monitor]` 段——Admin 只聚合 Agent 上报的指标，自身不采集压测指标（`admin/config.go:18-27` 无该字段）。

进程资源（flow / proto / scripts / adapter）**不是** TOML 配置：单机模式经 CLI flag 指定（§7.1），分布式模式随任务经 gRPC 下发（§9）。

### 1.2 单机模式 `conf/stressbot.toml`

根结构 `standalone.Config`（`standalone/config.go:38-46`）：

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `daemon` | bool | `false` | 守护进程模式（仅 Linux）；`-d` flag 或该字段任一触发（`cmd/stressbot/main.go:80-93`）；Windows 下打印 `[DAEMON] Windows 不支持守护进程模式…` 后进程直接退出（`internal/daemon/daemon.go:25-28`），不是忽略 |
| `log` | `*stresslog.Config` | nil | 日志配置，见 §1.5.1；nil 时入口用 `log/stressbot.log` 兜底（`cmd/stressbot/main.go:100-104`） |
| `monitor` | `*monitor.CollectorConfig` | nil | nil = 不启用监控（`standalone/app.go:59-64` 仅在非 nil 时 `monitor.Init`；控制台摘要与 `/metrics` HTTP 在机器人全部创建完成后才启动，`standalone/app.go:126`、`198-224`） |
| `pprof` | `*config.PprofConfig` | nil | nil = 不启用 pprof，见 §5.2 |
| `standalone` | `*Settings` | 非_nil_ | 单机压测参数，见下表 |
| `redis` | `*shared.RedisConfig` | nil | host 空 = 不启用；脚本用 `share` 时必填，见 §1.5.3 |
| `network` | `network.EngineConfig` | 零值=默认 | gnet 引擎调优参数，经 `runner.StartDialer` 注入（`network/engine_config.go`）；任一项 ≤ 0 回退编译期默认（读/写缓冲 32KB、并发拨号上限 512、maxBodyLen 16MB、事件循环 `runtime.NumCPU()`）。stressbot.toml 与 agent.toml 均支持 |

`[standalone]` 段（`Settings`，`standalone/config.go:30-35`）：

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `bot.accountPrefix` | string | `"bot_"` | 账号前缀（空串回填，`standalone/config.go:74-76`） |
| `bot.startNumber` | int | `1` | 起始编号（0 回填，`standalone/config.go:68-70`） |
| `bot.totalBots` | int | `1` | 机器人总数（0 回填，`standalone/config.go:71-73`） |
| `bot.concurrency` | int | `0` | 创建期批量限速：每创建 N 个暂停 1 秒，≤ 0 = 不限速（`robot/manager.go:186-195`）；不是运行期并发 |
| `bot.mainService` | string | **必填** | 缺失即启动报错（`standalone/config.go:77-79`）；仅以它命名的 TCP 连接意外断开会停止机器人 |
| `stateExtra` | map[string]string | 无 | 注入每个 Robot 的初始 state；不得覆盖保留键 `id`/`index`/`account`，命中后每个机器人各 Warn 一次并忽略（`robot/robot.go:166-178`，保留键表 `flow/prepare.go:10-14`）；值只能是字符串，TOML 整数报 `toml: cannot decode TOML integer into string` |
| `duration` | string | `"0"` | Go duration 文本；空或 `"0"` = 不限时（运行到信号或全部机器人流程自然结束），必须带单位（`"10"` 报错），负数等同不限时（`standalone/app.go:147-156`）；在全部机器人创建完成（含整个渐进加压）后才开始计时（`robot/manager.go:244`、`353`） |
| `rampUp` | `*robot.RampUpConfig` | nil | 渐进加压；配置了 stages 时各阶段 `count` 之和必须等于 `totalBots`，否则启动报错（`standalone/config.go:84-92`） |

`rampUp.stages[]` 每阶段字段（`robot/manager.go:32-37`）：`count`（必填）、`concurrency`（0 = 用全局值）、`holdSec`（0 表示未填、回退默认 30s，`robot/manager.go:29`、`293-298`；负数不校验、等同不保持；最后一阶段不保持）、`reset`（本阶段开始前清空已有机器人，仅当仍有运行中机器人时生效）。渐进加压的执行语义与阶段段落归档见 `docs/ramp-up.md`。

### 1.3 Agent `conf/agent.toml`

根结构 `agent.AppConfig`（`agent/config.go:17-24`）：`log` / `monitor` / `pprof` / `daemon` 同 §1.2；`[agent]` 段对应 `agent.Config`（`agent/config.go:59-70`）：

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `id` | string | **必填** | 节点 ID，全局唯一（`agent/config.go:109-111`） |
| `adminAddress` | string | **必填** | Admin 控制面地址，必须 host:port（`agent/config.go:112-117`） |
| `name` | string | 空→hostname→id | 显示名（`agent/config.go:118-125`） |
| `maxBots` | int | `5000` | <= 0 时回填 5000（`agent/config.go:126-129`） |
| `heartbeatInterval` | string | `"10s"` | Agent→Admin 心跳间隔，`ParsePositiveDuration`（`agent/config.go:147`） |
| `metricsInterval` | string | `"5s"` | 指标上报间隔，同上（`agent/config.go:146`） |
| `reconnectMaxRetries` | int | `-1`（无限） | 0 也解释为 -1（`agent/config.go:159-164`） |
| `taskWorkDir` | string | 空→`os.TempDir()` | 任务工作目录（`agent/config.go:130-133`） |
| `reconnect.initialInterval` | string | `"5s"` | 首次重连等待，此后指数退避（`agent/config.go:148`） |
| `reconnect.maxInterval` | string | `"30s"` | 退避上限；2 倍抖动上界 + 握手必须明显小于 Admin 的 `leaseDuration`（`admin/config.go:38-47` 注释、`conf/agent.toml:115-120`） |
| `reconnect.taskReportTimeout` | string | `"30s"` | 任务最终报告确认超时（`agent/config.go:151`） |

Agent 没有 `enabled`、`publicUrl` 或本地 HTTP 控制面；任务运行参数与资源全部由 Admin 下发（§9）。

### 1.4 Admin `conf/admin.toml`

根结构 `admin.Config`（`admin/config.go:18-27`），默认值集中于 `admin.Defaults()`（`admin/config.go:126-161`）：

`[server]`（管理面 HTTP，`admin/config.go:30-34`）：

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `port` | int | `7718` | 必须 > 0（`admin/config.go:177-179`） |
| `listenHost` | string | `"127.0.0.1"` | 必填（`admin/config.go:183-185`） |
| `staticDir` | string | `"cmd/web/dist"` | 前端静态托管目录，相对进程工作目录。注意：Admin 直连托管当前与前端 `/stressbot/` base 不匹配（直连 7718 页面白屏），生产用 nginx 形态或子目录变通，详见 [deployment.md](deployment.md) §6 |

`[controlPlane]`（Agent 控制面 gRPC，`admin/config.go:48-55`）：

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `port` | int | `7720` | 必须 > 0（`admin/config.go:180-182`） |
| `listenHost` | string | `"127.0.0.1"` | 必填（`admin/config.go:186-188`） |
| `heartbeatInterval` | string | `"10s"` | 期望的 Agent 心跳间隔 |
| `unhealthyAfter` | string | `"30s"` | 心跳超时标记 unhealthy |
| `leaseDuration` | string | `"45s"` | 任务租约时长：Agent 断连超过该时长才取消任务 |
| `offlineAfter` | string | `"60s"` | 超时标记 offline 并从注册表删除 |

四时长在加载期强校验以下四条（`admin/config.go:189-219`）：`heartbeatInterval < unhealthyAfter`、`heartbeatInterval < leaseDuration`、`unhealthyAfter < offlineAfter`、`leaseDuration < offlineAfter`。即 `heartbeat` 最小、`offline` 最大，`unhealthy` 与 `lease` 之间无大小约束（默认 30s / 45s）。设计意图见 `admin/config.go:38-47`：租约窗口要盖住 Agent 重连最坏耗时（`maxInterval` 2 倍抖动 + 拨号），又要先于 Admin 离线清理接管任务生死。

`[mysql]`（可选历史归档，`admin/config.go:59-67`；host 空 = 不启用，`admin/config.go:122-124`）：

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `host` / `port` | string / int | 空 / `3306` | port 0 回退 3306（`admin/config.go:74-77`） |
| `username` / `password` / `database` | string | 空 | 密码推荐 `${STRESSBOT_MYSQL_PASSWORD}` 环境变量注入 |
| `retentionDays` | int | `90` | 历史保留天数，超期自动清理 |
| `pool.*` | `config.ConnPoolConfig` | dial 5s / read 30s / write 30s / maxOpen 10 / maxIdle 5 / lifetime 1h | 见 §1.5.4；DSN 由驱动 `FormatDSN` 生成而非手工拼接（`admin/config.go:69-102`） |

`[redis]`（可选共享状态）与 `[log]` 见 §1.5。Admin 启动时按内嵌 DDL 创建表结构，不维护迁移链（`admin/mysql/schema_definition.go`）。

### 1.5 共享子结构

#### 1.5.1 `[log]`（`stresslog.Config`，`internal/stresslog/logger.go:34-44`）

| 字段 | 类型 | 段省略时 | 说明 |
| --- | --- | --- | --- |
| `path` | string | `log/stressbot.log`（入口另有按进程兜底） | 日志文件路径 |
| `level` | string | `debug` | debug / info / warn / error；段提供但 level 空 → `debug`（`internal/stresslog/logger.go:95-97`） |
| `printConsole` | bool | `true` | 是否同时输出 stdout；**写了 `[log]` 段却没写此键时为 `false`**（段提供时直接用文件值，只有 level/maxSizeMB/maxBackups/maxAge 四项有空值回填，`internal/stresslog/logger.go:91-107`） |
| `maxSizeMB` | int | `500` | 单文件轮转阈值（`internal/stresslog/logger.go:98-100`） |
| `maxBackups` | int | `1` | 旧文件保留数 |
| `maxAge` | int | `7` | 旧文件保留天数 |
| `localTime` / `compress` | bool | `true` | 轮转文件名时区 / gzip 压缩；同理，段提供但未写时为 `false` |
| `weChatToken` | string | 空 | DPanic 级严重错误推送企业微信（`internal/stresslog/logger.go:190-195`） |

段省略时的默认来自 `defaultConfig()`（`internal/stresslog/logger.go:78-89`）。文件日志为 JSON，`timestamp` 恒为 UTC（`internal/stresslog/logger.go:129-131`）。Admin 的 `Defaults()` 在 TOML 层预填了更紧的值（admin.log / info / 100MB / 10 份 / 30 天，`admin/config.go:153-159`），因此 Admin 的实际默认以 `admin.Defaults()` 为准。

#### 1.5.2 `[monitor]`（`monitor.CollectorConfig`，`monitor/collector.go:239-245`）

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `apdexThresholdMs` | int | `100` | Apdex T 阈值；<= 0 回填 100（`monitor/collector.go:340-344`）；分布式任务运行期可被任务参数 `apdexT` 覆盖（`agent/task/runner.go:138`） |
| `timingDetail` | string | `"rtt"` | 三档 `rtt` / `codec` / `full`（`monitor/collector.go:216-220`）；空串或非法值 Warn 后回退 `rtt`（`monitor/collector.go:302-314`、`325-327`） |
| `reportInterval` | string | `"5s"` | 控制台摘要打印间隔，仅单机模式消费；非法或 <= 0 启动报错（`standalone/config.go:93-97` + `standalone/app.go:202-205`）；摘要经 stdout 打印、不进日志文件，在机器人全部创建完成后才开始，首行累计自 `monitor.Init` 起 |
| `http.port` | int | 无默认 | 监控 HTTP JSON 端点（`/metrics`），整段省略 = 不启用（`standalone/app.go:210-217`）；端口启动失败只打 error、压测继续 |
| `sketch.relativeAccuracy` | float64 | `0.01` | DDSketch 相对误差；**Admin 与所有 Agent 必须一致，否则 sketch 合并结果错误**（`monitor/collector.go:247-252` 注释） |
| `sketch.maxBins` | int | `2048` | 最大桶数 |

#### 1.5.3 `[redis]`（`shared.RedisConfig`，`state/shared/config.go:31-40`）

单机与 Admin 共用同一结构。`host` 空 = 不启用（`state/shared/config.go:60-63`）。默认值：`port` 6379、`keyPrefix` `"stressbot"`、`defaultClaimTTL` 30s、`opTimeout` 2s、pool dial 5s / read 2s / write 2s / `connMaxLifetime` 0（`state/shared/config.go:19-25`、`67-109`）。

单机模式特有行为：启动时扫描脚本目录检测 `share` 模块使用（`standalone/app.go:242-267`，剥离注释后匹配 `require("share")`），未使用时即使配置了 Redis 也不连接；脚本用了 `share` 但未配置 Redis 会直接失败（`standalone/app.go:167-169`），runId 为 `standalone-<毫秒时间戳>-<pid>`，退出时 `Cleanup`（`standalone/app.go:174`、`185-196`）；Admin 侧配置了 Redis 则启动 PING 验证连通性（`admin/app.go:201-211`）。

#### 1.5.4 连接池 `[*.pool]`（`config.ConnPoolConfig`，`config/pool.go:5-12`）

MySQL 与 Redis 共用同一形态：`dialTimeout` / `readTimeout` / `writeTimeout` / `maxOpenConns` / `maxIdleConns` / `connMaxLifetime`，全部可选。两个消费方的时长解析语义不同，见 §3.2。

#### 1.5.5 `[pprof]`（`config.PprofConfig`，`config/pprof.go:25-27`）

仅一个字段 `port`（默认 6060，<= 0 时入口回填，`cmd/stressbot/main.go:111-118`）。指针为 nil = 不启用，三种模式行为一致。端口被占用时只打 `[DEBUG] pprof 服务启动失败`，进程继续运行。

---

## 2. 配置加载管线

### 2.1 `LoadTOML` 统一入口

三种进程的 TOML 都经 `config.LoadTOML[T]` 加载（`config/load.go:30-55`），顺序为：

1. **读取文件**（`config/load.go:31-34`）；
2. **以 defaults 为基底解码**——调用方传入预填默认值的结构体（如 `standalone.Defaults()`、`admin.Defaults()`），零值字段被文件覆盖（`config/load.go:36-38`）；
3. **严格未知字段检查**——`DisallowUnknownFields()`，拼写错误的字段名导致启动失败，错误信息中文并列出全部未知键（`config/load.go:37`、`39-46`）；
4. **环境变量展开**——解码完成后反射遍历字符串字段做 `${VAR}` 展开（`config/load.go:50-52`），见 §4；
5. 返回。

注意展开发生在**严格解码之后**：环境变量只出现在字符串值里，不参与键名或结构。

### 2.2 各进程的加载入口与后置校验

| 进程 | 入口 | 后置校验 |
| --- | --- | --- |
| 单机 | `standalone.LoadConfig`（`standalone/config.go:63-94`） | bot 三项空值回填；`mainService` 必填；rampUp count 之和 = totalBots |
| Agent | `agent.LoadConfig`（`agent/config.go:31-38`，注入编译版本） | `cfg.Agent.Resolve()`（`agent/config.go:108-157`）：id/adminAddress 必填与格式、name/maxBots/taskWorkDir 回填、五个时长经 `ParsePositiveDuration` 解析 |
| Admin | `admin.LoadConfig`（`admin/config.go:164-173`） | `validateConfig`（`admin/config.go:176-237`）：端口/host 必填、四时长次序联动（§1.4）、Redis `Resolve()`、MySQL `DSN()` 与 `ConnMaxLifetime()` 的时长合法性 |

Agent 的 `Resolve()` 用 **firstErr 聚合**：五个时长全部解析完才返回第一条错误（`agent/config.go:134-142`），保证一次报错不漏其他错。Admin 的 `validateConfig` 则是逐条短路返回。

Admin 装配阶段（`NewServer` / `Run`）会用 `ParsePositiveDuration` **再次解析** controlPlane 四时长并传给 gRPC 服务器与 AgentRegistry（`admin/app.go:144-151`、`251-269`）——加载期校验次序，装配期取数值，两处使用同一解析器与同一默认值，不会漂移。

错误信息全部中文，例如：`配置文件 %s 包含未知字段: %s`（`config/load.go:44-45`）、`agent.id 不能为空`（`agent/config.go:110`）、`standalone.rampUp.stages 各阶段 count 之和 (%d) 不等于 standalone.bot.totalBots (%d)`（`standalone/config.go:90`）。

---

## 3. 时长配置契约

### 3.1 两个解析器

`config/duration.go` 定义统一口径（`config/duration.go:9-18` 注释）——**留空才回退默认，写错一律报错**：

| 解析器 | 语义 | 拒绝条件 |
| --- | --- | --- |
| `ParseDuration`（`config/duration.go:21-33`） | 留空回退 fallback；0 是合法显式值，语义由调用方定义 | 非法文本、负数 |
| `ParsePositiveDuration`（`config/duration.go:37-49`） | 留空回退 fallback | 非法文本、<= 0 |

「未填」的判定是 TrimSpace 后空串（`config/duration.go:52-56`）。

### 3.2 使用点清单

| 配置项 | 解析器 | 默认 | 位置 |
| --- | --- | --- | --- |
| `agent.heartbeatInterval` / `metricsInterval` | Positive | 10s / 5s | `agent/config.go:146-147` |
| `agent.reconnect.initialInterval` / `maxInterval` / `taskReportTimeout` | Positive | 5s / 30s / 30s | `agent/config.go:148-151` |
| `admin.controlPlane.heartbeatInterval` / `leaseDuration`（装配期） | Positive | 10s / 45s | `admin/app.go:251-258` |
| `admin.controlPlane.unhealthyAfter` / `offlineAfter`（装配期） | Positive | 30s / 60s | `admin/app.go:144-151` |
| 任务下发参数 `heartbeatInterval` / `tcpTimeout` / `httpTimeout` | Positive | 5s / 60s / 10s | `agent/task/runner.go:142-156` |
| `mysql.pool.*` 四项 + `connMaxLifetime` | `ParseDuration` | 0（不写进 DSN / 不限制） | `admin/config.go:78-114` |
| `redis.defaultClaimTTL` / `opTimeout` / `pool.dial|read|writeTimeout` | Positive | 30s / 2s / 5s / 2s / 2s | `state/shared/config.go`（`Resolve`） |
| `redis.pool.connMaxLifetime` | `ParseDuration` | 0（不限制） | `state/shared/config.go`（`Resolve`） |
| 单机 `monitor.reportInterval` | Positive | 5s | `standalone/config.go`（`LoadConfig` 校验）+ `standalone/app.go` |

选型规则：**间隔与超时用 Positive**（0 会退化成忙循环或立即超时）；**以 0 表示「不限制」的项用 `ParseDuration`**——MySQL 驱动的超时与 `connMaxLifetime` 中 0 是合法的「不限制」显式值（`admin/config.go:109-114` 注释）。

例外（如实记录，历史现状）：

- Admin `validateConfig` 对四时长用裸 `time.ParseDuration` + 逐项次序断言（`admin/config.go:189-219`）；正常配置不会以空串进入这里——省略键时由 `admin.Defaults()` 基底（`admin/config.go:134-141`）提供非空默认值，但显式写空串（`heartbeatInterval = ""`）会在 `ParseDuration` 处报「不是合法时长」。
- `standalone.duration` 用独立的 `parseDuration`（`standalone/app.go:136-145`）：空或 `"0"` 合法（一直运行），非法文本报错。

### 3.3 设计动机

`config/duration.go:9-18`：不把「解析失败」和「值非正」并进「未填」一起回退默认——那样配置打错不会被发现，告警淹在启动日志里，等压测跑完才看出实际生效的超时窗口根本不是配的那个。fallback 只服务「这一项没填」一种情形。任务级时长同样贯彻该原则：任务下发的时长写错就让这次任务失败，不退化成默认值跑出失真的结果（`agent/task/runner.go:140-141` 注释）。

---

## 4. 环境变量展开

展开由 `ExpandConfigStrings` 反射遍历完成（`config/load.go:68-74`），在严格解码之后执行（§2.1）：

| 语法 | 语义 |
| --- | --- |
| `${VAR}` | 展开；未定义则**启动报错**（fail-loud，避免密码静默为空） |
| `${VAR:-default}` | 展开；未定义用 default |
| `$$` | 转义为字面 `$` |
| 不含 `$` 的字符串 | 原样返回，零开销（快速路径，`config/load.go:166-168`） |

实现上调用 `envsubst.EvalEnv(s, true)` 的严格模式（`config/load.go:170`）。覆盖的类型（`config/load.go:77-148`）：`string`、`*string`、`map[string]string` 的 value（仅此一种 map，其他 map 跳过）、struct（递归，跳过不可导出字段）、slice/array（逐元素递归，且元素类型不可能含字符串时整段跳过，`config/load.go:128-143`、`151-161`）。`int` / `bool` 等标量不展开。

典型用法：MySQL 密码 `${STRESSBOT_MYSQL_PASSWORD}`（`conf/admin.toml:79-82`）、Redis 密码 `${STRESSBOT_REDIS_PASSWORD:-}`（`conf/stressbot.toml:174-175`）。

---

## 5. 监控与调试相关配置

### 5.1 `[monitor]`

字段表见 §1.5.2。要点：

- `timingDetail` 三档由 `TimingDetailLevel` 定义（`monitor/collector.go:212-220`）：`rtt` 仅总往返；`codec` 增加编解码各阶段；`full` 增加全部客户端分项（帧解析、状态存储等）。档位比较用 `TimingDetailAtLeast`（`monitor/collector.go:233-236`）。归一化时空串与非法值均回退 `rtt` 并 Warn（`monitor/collector.go:302-314`）。
- `monitor.Init` 经 `sync.Once` 幂等（`monitor/collector.go:319-331`）；单机仅在配置了 `[monitor]` 段时调用（`standalone/app.go:54-59`），Agent 在 `NewFromConfig` 中无条件初始化（nil 段用零值结构，`agent/config.go:49-55`）。
- `sketch.relativeAccuracy` / `maxBins` 决定 DDSketch 精度与覆盖范围；DDSketch 合并要求两侧参数一致，Admin 与所有 Agent 必须同值（`monitor/collector.go:247-252`）。
- `http.port` 启动独立 HTTP 端点暴露实时指标 JSON（`standalone/app.go:201-208`）；Agent 模式不开此端点，但 pprof 端口上注册了 `/metrics` 路由供本机诊断（`conf/agent.toml:56-57`）。
- 指标模型、Apdex 口径与导出格式见 `docs/monitoring-system.md`。

### 5.2 `[pprof]` 与观测端点

`config.StartPprofServer`（`config/pprof.go:44-85`）在配置端口启动 pprof HTTP 服务，刻意共用 `http.DefaultServeMux`：除 `net/http/pprof` 外，`debughttp`、`protox`、`codec`、`network`、`script` 各包在 init 时把 `/debug/*` 调优观测端点挂到默认 mux，由该服务统一对外（`config/pprof.go:40-43` 注释，端点清单见 AGENTS.md「调优观测端点」）。请求头读取限时 10s 防 slowloris，但不设 WriteTimeout 以免掐断长时间 profile 采集（`config/pprof.go:30-34`）。

---

## 6. JSON Schema 契约校验（`config/validation/`）

### 6.1 定位与分工

`config/validation/` 负责在**领域解码之前**校验 flow / codec JSON 配置的结构契约（`config/validation/validator.go:1-2`）。分工：

- **Schema 只管结构**：字段类型、必填、枚举、未知属性等；
- **领域解码与业务不变量由调用方负责**——flow 的图引用与跨字段不变量由 engine 加载期校验（`config/validation/validator.go:24` 注释），codec 的算法与 pipeline 引用检查由 codec 包 `Schema.Validate` 负责（`config/validation/validator.go:33` 注释、`protocol/codec/schema.go:266-268`）。

两份 Schema 经 `go:embed` 内嵌进二进制（`config/validation/embed.go:8-9`），用 `jsonschema`（Draft 2020-12）编译一次后复用（`config/validation/validator.go:42-79`）。

### 6.2 覆盖范围

| Schema | 顶层属性 | 必填 | `$defs` 主要条目 |
| --- | --- | --- | --- |
| `flow.schema.json` | `nodes` / `actions` / `listens` | `nodes`、`actions`（`flow.schema.json:6`） | node / switchCase / weightedOption / retry / onError / action / binding / filter / store / listen 等 |
| `codec.schema.json`（165 行） | `version` / `endianDefault` / `frame` / `header` / `routeKeyTemplate` / `pipeline` / `heartbeat` | 前五项（`codec.schema.json:6`） | frame / field / flagBit / valueSource / step / offset / produce / condition / guard / heartbeat 等 |

### 6.3 调用点与前端同源

后端调用点：

- `runner/resources.go:86`——单机/Agent 任务加载 flow.json 时先 `ValidateFlow` 再 `Unmarshal` + `PrepareTaskFlow`（`runner/resources.go:81-97`）；
- `protocol/codec/schema.go:259`——`LoadSchema` 加载 codec.json 时先 `ValidateCodec` 再领域解码与业务校验；
- Admin HTTP API 上传入口：任务创建的 flow（`admin/httpapi/task_routes.go:59`）、codec 资源（`admin/httpapi/task_routes.go:150`、`admin/httpapi/codec.go:65`）、流程模板（`admin/template/flow_template.go:73`）。

前端用 Ajv（`ajv/dist/2020`）直接 import 同两份 Schema 文件做编辑器内同源校验（`cmd/web/src/services/schemaValidator.ts:1-16`），保证「保存前校验」与「后端加载期校验」是同一份契约。flow 数据模型本身见 `docs/flow-node-system.md`，codec 配置见 `docs/adapter-layer.md`。

---

## 7. 单机模式生命周期（`cmd/stressbot` + `standalone/`）

### 7.1 CLI flag

`cmd/stressbot/main.go:41-50`：

| flag | 默认 | 说明 |
| --- | --- | --- |
| `-config` | `conf/stressbot.toml` | 配置文件路径（非必填，有默认值） |
| `-flow` | 空 | 流程配置文件路径；空则回退 `<config 所在目录>/flow/flow.json` |
| `-proto` | 空 | proto 目录；空则回退 `<conf>/proto` |
| `-scripts` | 空 | Lua 脚本目录；空则回退 `<conf>/scripts` |
| `-adapter` | 空 | 适配器目录（`*_codec.json` + `errors.json`）；空则回退 `<conf>/adapter` |
| `-d` | `false` | 守护进程化（重新以 daemon 方式 exec） |

资源路径回退由 `standalone.ResolvePaths` 统一解析，非空覆盖值转绝对路径（`standalone/config.go:105-128`）。`-flow` 指向另一份 flow.json 即可切换压测场景，无需挪文件。

### 7.2 启动序列

入口（`cmd/stressbot/main.go:57-125`）：解析 flag → `-d` 或 TOML `daemon` 时守护化（`main.go:80-93`）→ `standalone.LoadConfig` → `ResolvePaths` → 初始化日志 → 信号 ctx → 可选 pprof → `standalone.Run`。

`standalone.Run`（`standalone/app.go:24-134`）按序：

1. `runner.LoadResources` 加载 codec + errors.json + proto + flow（缺失 `errors.json` 仅 Warn 降级，`standalone/app.go:45-47`）；
2. `monitor.Init` 启用监控（配置了 `[monitor]` 时）；
3. `runner.NewRuntimePool` 预编译 Lua 脚本——失败仅 Warn 不中断（`standalone/app.go:61-66`）；
4. 解析 `standalone.duration`；探测脚本 `share` 使用并按需连接 Redis（`standalone/app.go:68-79`）；
5. 组装 `robot.ManagerConfig`（含请求超时 60s、HTTP 超时 10s 两个硬编码默认，`standalone/app.go:81-94`）；
6. `runner.StartDialer` 启动 gnet 网络引擎（`standalone/app.go:96-100`）；
7. 配置了 `rampUp.stages` 则 `StartWithRampUp` 渐进加压，否则 `StartAll` 全量启动（每 `concurrency` 个批次间暂停 1 秒，最后一批不等待；`robot/manager.go:181-190`）（`standalone/app.go:102-118`）；
8. 启动监控 reporter 与可选 HTTP 端点（`standalone/app.go:120`）。

### 7.3 运行与停止

主流程阻塞在 `select`：收到 SIGINT/SIGTERM（ctx.Done）或 `manager.Done()`（`duration` 到期）即退出（`standalone/app.go:122-127`）。随后依次 `stopMonitoring` → `manager.StopAll` → `exportMetrics`（`standalone/app.go:129-132`）。

### 7.4 导出与清理

- CSV 导出：写 `metrics/metrics_<时间戳>.csv`，目录自动创建，失败仅记错误日志（`standalone/app.go:217-231`）；
- 网络引擎停止经 `runner.StopDialer`：提交到共享协程池，最多等 30s，超时强制返回交由 OS 回收（`runner/cleanup.go:13-48`）；
- Redis 共享状态：清理（限时 10s）后关闭（`standalone/app.go:174-185`）；
- protobuf 工厂与 Lua 池经 `defer resources.Close()` 释放（`standalone/app.go:44`）。

---

## 8. runner 资源加载链（`runner/`）

`runner` 装配单机模式与 Agent 任务共用的数据面运行资源，仅四个文件：

| 文件 | 导出内容 | 职责 |
| --- | --- | --- |
| `runner/config.go:4-10` | `ResourcePaths` | 一次压测运行所需的四个资源位置（flow / proto / scripts / adapter） |
| `runner/resources.go:25-65` | `LoadResources` | 按序：`protocol.InferCodecMap` 推断 codec 映射 → 探测可选 `errors.json` → `protocol.LoadCodecResolver` → `protox` 动态加载 .proto → `loadFlow`（ValidateFlow → Unmarshal → `flow.PrepareTaskFlow`）→ 回填 action 名称 |
| `runner/resources.go:67-79` | `Resources.Close` / `NewRuntimePool` | 释放任务级 protobuf 工厂；创建 Lua 运行时池并预编译脚本 |
| `runner/start.go:10-17` | `StartDialer` | 用已加载的协议元信息（`protocol.PickMetaAdapter`）启动 gnet 引擎 |
| `runner/cleanup.go:13-48` | `StopDialer` | 有界等待（30s）的网络引擎停止，协程池拒绝时同步降级 |

两个消费方完全对称：单机 `standalone/app.go:38/61/96/100` 与 Agent 任务 `agent/task/runner.go:105/160/165/168`——Agent 侧资源目录来自下发的资源包解压目录（`agent/task/runner.go:95-110`），单机侧来自 CLI flag 解析结果。这是「同一套压测数据面在两种模式下零分叉」的装配基础。

---

## 9. 分布式配置下发

Agent 没有「本地任务配置文件」：`agent.toml` 只描述节点自身（身份、Admin 地址、容量、重连与上报节奏），任务的全部运行参数——机器人批量参数、stateExtra、`heartbeatInterval` / `tcpTimeout` / `httpTimeout` / `apdexT` / 日志级别、rampUp、共享状态运行时（`agent/task/types.go:36-56` 的 `Assignment`）——以及 flow / proto / scripts / adapter 资源包，均随任务经 Admin 的 gRPC 控制面下发；资源包由命令执行器下载、校验并原子发布到 `taskWorkDir`（`agent/task/runner.go:95-99`）。任务级时长在 Agent 侧用与 TOML 相同的 `ParsePositiveDuration` 契约解析，写错即任务失败（§3.2）。下发链路、命令日志与资源包内容寻址细节见 `docs/distributed-architecture.md` 与 `docs/resource-sync.md`。

---

## 10. 相关文档

| 主题 | 文档 |
| --- | --- |
| 流程配置 flow.json 数据模型与节点语义 | `docs/flow-node-system.md` |
| 声明式 codec 配置与适配层 | `docs/adapter-layer.md` |
| 分布式架构、任务下发与 gRPC 控制面 | `docs/distributed-architecture.md` |
| 资源包同步与内容寻址 | `docs/resource-sync.md` |
| 渐进加压阶段语义 | `docs/ramp-up.md` |
| 监控指标模型与导出 | `docs/monitoring-system.md` |
