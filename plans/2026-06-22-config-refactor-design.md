# 配置模型重构设计

## 背景

stressbot 当前有三套配置：Admin 配置（`conf/admin-config.json` + `admin/config.go`）、单机/Agent 配置（`conf/config.json` + `conf/agent-config.json` + `cmd/agent/main.go`）、日志配置（`utils/log/logger.go`）。三者存在多处不一致：

- `enabled` 字段泛滥（`history.enabled`、`pprof.enabled`、`monitor.enabled`、`monitor.httpEnabled`），与"是否填写地址"形成双重真相。
- MySQL 配置嵌套在 `history.mysql` 下，语义错误（MySQL 是基础设施，不是历史归档私有）。
- `shared.redis` 多了一层 `shared` 包装。
- 三套 `LogConfig` 字段名和字段集合各不相同（admin 用 `maxSizeMB`，agent 用 `maxSize`，单机连 maxSize 都没有）。
- 单机和 agent 的 `MonitorConfig` 字段集合不同。
- `standalone.bot` 用 `count`/`concurrentNum`，与 RobotConfig 的 `totalBots`/`concurrency` 命名不一致。

本次重构统一这些不一致，为后续"后端流程模板库"（将复用全局 MySQL）扫清错误边界。

除上述结构问题外，现有配置还有大量"像多个人写的"字段命名不一致：MySQL 用 `host`+`port` 分开、Redis 用 `addr` 合并；MySQL `user`、Redis `username`；Agent 段 `hb` 缩写和 `stress` 全称并存；`apdexT`、`hbInterval`、`connMaxLifetime`、`defaultClaimTTL` 等后缀和大小写断点混乱。本次一并做命名规范化。

## 目标

1. 所有"是否启用"统一用 `*T + nil` 判断，删除所有 `enabled` 字段（`agent.enabled` / `daemon` 这类行为开关除外）。
2. MySQL/Redis 提升为全局基础设施配置，由进程统一初始化。
3. 三套配置共用同一个 `LogConfig`、同一个 `MonitorConfig`。
4. 全配置字段命名规范化：地址格式、超时/间隔后缀、缩写展开、大小写断点统一。
5. `standalone.bot` 字段名与 RobotConfig 对齐。
6. 去掉 `shared` 包装层。

## 非目标

- 不改 `agent.enabled`（它是"二进制跑哪种模式"的开关，属于行为 bool，不是"是否配置"语义，与 `daemon` 同类）。
- 不解决密码明文存储（标注为后续部署侧改进）。
- 不做旧字段兼容读取或自动迁移（遵循项目"禁止兼容性兜底"约定）。
- 不改 `SharedRuntimeAssignment.Redis` 下发字段名（admin↔agent HTTP 契约）。
- 不改前端 `CapabilitiesResponse.sharedState` API 字段名。
- 不解决密码明文存储（标注为后续部署侧改进）。
- 不做旧字段兼容读取或自动迁移（遵循项目"禁止兼容性兜底"约定）。
- 不改 `SharedRuntimeAssignment.Redis` 下发字段名（admin↔agent HTTP 契约）。
- 不改前端 `CapabilitiesResponse.sharedState` API 字段名。
- 不做后端流程模板库、选库启动（本次之后独立阶段）。

## 统一原则

### 指针 + nil 判断

所有"可选基础设施/可选功能"配置用指针类型：

```go
type Config struct {
    MySQL   *MySQLConfig          `json:"mysql"`   // nil = 未配置 MySQL
    Redis   *RedisConfig          `json:"redis"`   // nil = 未配置 Redis
    Pprof   *PprofConfig          `json:"pprof"`   // nil = 未启用 pprof
    Monitor *MonitorConfig        `json:"monitor"` // nil = 未启用监控（单机/agent）
    History *HistoryConfig        `json:"history"` // nil = 未启用历史归档（admin）
    // ...
}
```

判断方式统一为 `cfg.Redis != nil`、`cfg.MySQL != nil`，不再有 `Enabled bool` 字段。

### 不保留兼容读取

旧字段（`history.enabled`、`history.mysql`、`shared.redis`、`*.enabled`）直接删除。配置文件按新结构一次性重写，不走自动迁移。这遵循项目 `feedback_no_compat_hacks` 约定。

## 全配置命名规范

本次重构对所有配置段做命名规范化，让整套配置看起来是同一套规范写出来的。

### 命名规则

| 维度 | 规则 | 说明 |
|---|---|---|
| 地址：基础设施 | `host` + `port` 分开 | mysql/redis 用，数值类型 port |
| 地址：HTTP 端点 | `xxxUrl` | admin/agent 的对外 HTTP 地址，含协议 |
| 周期性间隔 | `xxxInterval` | 如 `heartbeatInterval`、`metricsInterval` |
| 单次操作超时 | `xxxTimeout` | 如 `requestTimeout`、`dialTimeout` |
| 不用缩写 | 全称 | `hb`→`heartbeat`、`apdexT`→`apdexThresholdMs` |
| 带 TLB 单位的字段 | 单位进名字 | `maxSizeMB`、`apdexThresholdMs`、`retentionDays` |
| 行为布尔 | 裸 `bool` | `daemon`、`agent.enabled` 保留 |
| 是否配置 | 指针 `*T + nil` | `*MySQLConfig`/`*RedisConfig`/`*PprofConfig` 等 |

### MySQL 字段

| 字段 | 说明 |
|---|---|
| `host` + `port` | 地址分开（不变） |
| `username` | **由 `user` 改名** |
| `password` | 不变 |
| `database` | 不变 |
| `dialTimeout` | **新增**（对齐 Redis） |
| `readTimeout` | **新增** |
| `writeTimeout` | **新增** |
| `maxOpenConns` | 不变 |
| `maxIdleConns` | 不变 |
| `connMaxLifetime` | 不变 |

### Redis 字段

| 字段 | 说明 |
|---|---|
| `host` + `port` | **由 `addr` 拆开**（对齐 MySQL） |
| `username` | 不变 |
| `password` | 不变 |
| `dbIndex` | **由 `db` 改名**（语义更清晰） |
| `keyPrefix` | 不变 |
| `dialTimeout` | 不变 |
| `readTimeout` | 不变 |
| `writeTimeout` | 不变 |
| `maxOpenConns` | **新增**（对齐 MySQL，替代 `poolSize`） |
| `maxIdleConns` | **新增** |
| `connMaxLifetime` | **新增** |
| `defaultClaimTTL` | 不变（Redis 特有） |
| `opTimeout` | 不变（Redis 特有） |

### Agent 字段

| 旧 | 新 | 说明 |
|---|---|---|
| `enabled` | 保留 | 行为开关，不改 |
| `adminAddr` | `adminUrl` | HTTP 端点用 `Url` |
| `publicUrl` | 不变 | 已是 `Url` 风格 |
| `port` | 不变 | |
| `maxBots` | 不变 | |
| `hbInterval` | `heartbeatInterval` | 展开缩写 |
| `hbRequestTimeout` | `heartbeatTimeout` | 展开缩写 + 去冗余 |
| `hbFailThreshold` | `heartbeatFailThreshold` | 展开缩写 |
| `requestTimeout` | 不变 | |
| `reconnectMaxRetries` | 不变 | |
| `stressInterval` | `metricsInterval` | 更准确（压测+系统指标同步） |

### Admin 字段

| 旧 | 新 | 说明 |
|---|---|---|
| `shared` | **删除** | 提升为顶层 `redis` |
| `history.enabled` | **删除** | nil 判断 |
| `history.mysql` | **删除** | 提升为顶层 `mysql` |
| `history.retentionDays` | 不变 | |
| `pprof.enabled` | **删除** | 指针 nil |
| `pprof.port` | 不变 | |
| 其余 | 不变 | |

### Monitor 字段

| 旧 | 新 | 说明 |
|---|---|---|
| `enabled` | **删除** | 指针 nil |
| `httpEnabled` + `httpPort` | `http: { port }` | 合并为子指针 |
| `apdexT` | `apdexThresholdMs` | 展开缩写 + 标单位 |
| `timingDetail` | 不变 | |

### Log 字段（三套合一为 `stresslog.Config`）

| 字段 | JSON tag | 说明 |
|---|---|---|
| PrintConsole | `printConsole` | |
| LogLevel | `level` | |
| Path | `path` | **新增**（admin 原有，补进 stresslog.Config） |
| MaxSize | `maxSizeMB` | **tag 由 `maxSize` 改名** |
| MaxBackups | `maxBackups` | |
| MaxAge | `maxAge` | |
| LocalTime | `localTime` | |
| Compress | `compress` | |
| WeChatToken | `weChatToken` | |

### Standalone 字段

| 旧 | 新 | 说明 |
|---|---|---|
| `bot.count` | `bot.totalBots` | 对齐 RobotConfig |
| `bot.concurrentNum` | `bot.concurrency` | 对齐 RobotConfig |
| `bot.accountPrefix` | 不变 | |
| `bot.startNumber` | 不变 | |
| `bot.mainService` | 不变 | |

### 不改的下发契约

- `SharedRuntimeAssignment.Redis`（admin→agent HTTP）：字段名 `Redis` 不变，类型仍为 `sharedstate.RedisConfig`（其内部字段名随 Redis 规范化同步改，但 JSON 序列化保持 `host`/`port`/`dbIndex` 等，属本次统一后的新契约）。
- `CapabilitiesResponse.sharedState`（admin→前端 HTTP）：字段名不改。

## Admin 配置（`admin/config.go` + `conf/admin-config.json`）

### 新结构

```json
{
  "port": 7718,
  "publicUrl": "http://127.0.0.1:7718",
  "staticDir": "cmd/web/dist",

  "agentRegistry": {
    "unhealthyAfter": "30s",
    "offlineAfter": "60s"
  },

  "mysql": {
    "host": "127.0.0.1",
    "port": 3306,
    "username": "root",
    "password": "123456",
    "database": "stressbot",
    "dialTimeout": "5s",
    "readTimeout": "30s",
    "writeTimeout": "30s",
    "maxOpenConns": 10,
    "maxIdleConns": 5,
    "connMaxLifetime": "1h"
  },

  "redis": {
    "host": "127.0.0.1",
    "port": 6379,
    "username": "",
    "password": "",
    "dbIndex": 0,
    "keyPrefix": "stressbot",
    "dialTimeout": "5s",
    "readTimeout": "2s",
    "writeTimeout": "2s",
    "maxOpenConns": 10,
    "maxIdleConns": 5,
    "connMaxLifetime": "1h",
    "defaultClaimTTL": "30s",
    "opTimeout": "2s"
  },

  "history": {
    "retentionDays": 90
  },

  "log": { "level": "info", "path": "log/admin.log", "maxSizeMB": 100, "maxBackups": 10 },

  "pprof": { "port": 6060 },

  "daemon": false
}
```

### Go 结构

```go
type Config struct {
    Port          int                `json:"port"`
    PublicURL     string             `json:"publicUrl"`
    StaticDir     string             `json:"staticDir"`
    AgentRegistry RegistryConfig     `json:"agentRegistry"`
    MySQL         *MySQLConfig       `json:"mysql"`
    Redis         *sharedstate.RedisConfig `json:"redis"`
    History       *HistoryConfig     `json:"history"`
    Log           LogConfig          `json:"log"`
    Pprof         *PprofConfig       `json:"pprof"`
    Daemon        bool               `json:"daemon"`
}

type HistoryConfig struct {
    RetentionDays int `json:"retentionDays"`
}

type MySQLConfig struct { /* 字段不变 */ }
func (c *MySQLConfig) DSN() string { /* 不变 */ }
func (c *MySQLConfig) Enabled() bool { return c != nil && c.Host != "" && c.Database != "" }
```

### 关键改动

- `Shared *sharedstate.Config` → `Redis *sharedstate.RedisConfig`（顶层，指针）。
- `SharedEnabled()` 重命名为 `RedisEnabled()`，实现改为 `c.Redis != nil && c.Redis.Enabled()`。10 个调用方全部改名。
- `HistoryConfig.Enabled` / `HistoryConfig.MySQL` 删除。MySQL 提升到顶层 `Config.MySQL *MySQLConfig`。
- `HistoryConfig` 只剩 `RetentionDays`，改为指针 `*HistoryConfig`（nil = 不归档）。判断"是否归档"用 `cfg.MySQL != nil && cfg.History != nil`。
- `PprofConfig.Enabled` 删除，`Pprof` 改为 `*PprofConfig`（nil = 不启用）。
- `MySQLConfig` 从 `admin` 包提升到能被多模块共享的位置。因为后续流程模板库也要用，建议放在新包 `infra` 或直接留在 `admin` 但导出。本次先留在 `admin` 包导出，后续如需跨包再提取。

## 单机/Agent 配置（`cmd/agent/main.go` + `conf/config.json` + `conf/agent-config.json`）

### 新 `Config` 结构

```go
type Config struct {
    Log       stresslog.Config        `json:"log"`
    Monitor   *monitor.CollectorConfig `json:"monitor"`  // nil = 不启用监控
    Pprof     *PprofConfig            `json:"pprof"`      // nil = 不启用 pprof
    Standalone *StandaloneConfig      `json:"standalone"` // nil = 非单机模式
    Agent     agent.Config            `json:"agent"`
    Redis     *sharedstate.RedisConfig `json:"redis"`     // nil = 未配置 Redis
    Daemon    bool                    `json:"daemon"`
}
```

### `StandaloneConfig.Bot` 字段改名

```go
type StandaloneConfig struct {
    Bot struct {
        AccountPrefix string `json:"accountPrefix"`
        StartNumber   int    `json:"startNumber"`
        TotalBots     int    `json:"totalBots"`     // 原 count
        Concurrency   int    `json:"concurrency"`   // 原 concurrentNum
        MainService   string `json:"mainService"`
    } `json:"bot"`
    StateExtra map[string]string `json:"stateExtra"`
    Duration   string            `json:"duration"`
}
```

字段读取处（`cmd/agent/main.go` 里装配 `robot.ManagerConfig` 的地方）同步改名。

### 关键改动

- `Shared *sharedstate.Config` → `Redis *sharedstate.RedisConfig`。`main.go:264-296` 读 Redis 的三处（`Enabled()`/`Resolve()`/`NewRedisStore`）改为 `cfg.Redis.*`。
- `Monitor` 从值类型 `monitor.CollectorConfig` 改为指针 `*monitor.CollectorConfig`，nil 判断替代 `monitor.enabled`。
- `Pprof` 改为 `*PprofConfig`，nil 判断替代 `pprof.enabled`。
- `Standalone` 已是指针，不变。
- 单机配置文件 `conf/config.json` 的 `shared.redis` 段提升为顶层 `redis`，`monitor` 去掉 `enabled`/`httpEnabled`（用 nil 判断 + 内部 http 子配置），`standalone.bot` 字段改名。

## LogConfig 统一

当前三套：

- `admin.LogConfig`：`level/path/maxSizeMB/maxBackups`
- `stresslog.Config`（`utils/log/logger.go`）：`printConsole/level/maxSize/maxBackups/maxAge/localTime/compress/weChatToken`
- 实际单机配置只有 `level/printConsole`，agent 配置用 `maxSize`（非 `maxSizeMB`）

统一方案：**三处都用 `stresslog.Config`**（字段最全的那套），admin 的 `LogConfig` 删除，直接复用 `stresslog.Config`。

字段对齐（统一命名）：

| 字段 | JSON tag | 含义 |
|---|---|---|
| PrintConsole | `printConsole` | 控制台输出 |
| LogLevel | `level` | 日志等级 |
| MaxSize | `maxSizeMB` | **改 tag 为 `maxSizeMB`**，统一语义（MB） |
| MaxBackups | `maxBackups` | 保留个数 |
| MaxAge | `maxAge` | 保留天数 |
| LocalTime | `localTime` | 本地时间 |
| Compress | `compress` | 压缩 |
| WeChatToken | `weChatToken` | 企微 Hook |
| Path | `path` | **新增**（admin 需要，stresslog 原本没有） |

`stresslog.Config` 增加 `Path string` 字段，`maxSize` 的 JSON tag 改为 `maxSizeMB`。admin 删除自有 `LogConfig`，改用 `stresslog.Config`。

## MonitorConfig 统一

当前 `monitor.CollectorConfig`：

```go
type CollectorConfig struct {
    Enabled      bool   `json:"enabled"`
    HTTPEnabled  bool   `json:"httpEnabled"`
    HTTPPort     int    `json:"httpPort"`
    ApdexT       int    `json:"apdexT"`
    TimingDetail string `json:"timingDetail"`
}
```

改动：

- 删除 `Enabled`（用 `*CollectorConfig` nil 判断）。
- `HTTPEnabled` + `HTTPPort` 合并为 `*HTTPConfig` 子指针（nil = 不启用 HTTP 端点）：

```go
type CollectorConfig struct {
    ApdexT       int         `json:"apdexT"`
    TimingDetail string      `json:"timingDetail"`
    HTTP         *HTTPConfig `json:"http"`  // nil = 不启用 HTTP JSON 端点
}

type HTTPConfig struct {
    Port int `json:"port"`
}
```

单机和 agent 共用同一个 `CollectorConfig`。agent 配置原本只有 `apdexT`/`timingDetail`，现在如果需要 HTTP 端点就配 `monitor.http.port`，不需要就留空。

## Redis 配置下发契约（不改）

`SharedRuntimeAssignment.Redis` 字段（`admin/types.go:238` + `agent/types.go:125`）类型为 `sharedstate.RedisConfig`，是 admin→agent 的 HTTP 下发契约。

重构后：
- admin 内部读 `cfg.Redis`（顶层 `*sharedstate.RedisConfig`）。
- 下发时仍塞进 `SharedRuntimeAssignment.Redis`（值类型 `sharedstate.RedisConfig`）。
- 字段名 `Redis` 不变，序列化兼容。
- `sharedstate.Config` 外层包装结构废弃，所有引用方改为直接用 `sharedstate.RedisConfig`。

## MySQL 全局实例

### AdminServer 持有共享 db

```go
type AdminServer struct {
    cfg   Config
    db    *sql.DB              // 全局 MySQL 实例，nil = 未配置
    redis sharedstate.ResolvedRedisConfig // 启动时 resolve，仅 Redis 配置时有效

    history *HistoryStore
    // 未来：flows *FlowTemplateStore
}
```

启动流程（`NewAdminServer`）：

1. `cfg.MySQL != nil && cfg.MySQL.Enabled()` 时，`openDB(*cfg.MySQL)` 一次，存 `s.db`，执行 `initMySQLSchema(s.db)`。
2. `s.history = NewHistoryStore(s.db, cfg.History)`（HistoryStore 不再自己 openDB/Close）。
3. `cfg.Redis != nil && cfg.Redis.Enabled()` 时，`cfg.Redis.Resolve()` + ping 校验，存 `s.redis`。
4. `validateConfig` 把 MySQL/Redis 的连通性校验从 `NewHistoryStore` 上移到这里。
5. `Shutdown` 统一 `s.db.Close()`。

### HistoryStore 改造

```go
type HistoryStore struct {
    db            *sql.DB    // 共享，不 Close
    retentionDays int
    prune         time.Duration
    cancel        context.CancelFunc
}

func NewHistoryStore(db *sql.DB, cfg *HistoryConfig) *HistoryStore
```

- 去掉自己的 `openDB`/`Close` 调用（db 生命周期由 AdminServer 管）。
- `cfg` 只剩 `RetentionDays`。
- `initSchema` 改为接收 `*sql.DB` 的独立函数 `initMySQLSchema(db)`（或保留方法但用共享 db）。

### Schema 文件改名

`admin/history_schema.go` → `admin/mysql_schema.go`，因为该文件将承载所有 MySQL 表的 DDL（未来含 flow_template）。本次只改名，DDL 内容不变。

## 错误码语义

`HISTORY_DISABLED`（`admin/errors.go`）的语义从"history 模块未启用"变为"全局 MySQL 未配置"。

- 前端 `EditorPage.tsx:161` 的探测逻辑（`listHistory({limit:1})` 捕获 `HISTORY_DISABLED`）不变。
- 错误消息文案可更新为"服务器未配置 MySQL，历史归档不可用"。

## 配置文件改动清单

| 文件 | 改动 |
|---|---|
| `conf/admin-config.json` | `history.mysql` → 顶层 `mysql`（`user`→`username`、新增 `dialTimeout`/`readTimeout`/`writeTimeout`）；`shared.redis` → 顶层 `redis`（`addr`→`host`+`port`、`db`→`dbIndex`、`poolSize`→`maxOpenConns`/`maxIdleConns`/`connMaxLifetime`）；删 `history.enabled`/`pprof.enabled`；`pprof` 改 `{port:6060}` 或删整段；`log` 字段对齐 `stresslog.Config` |
| `conf/config.json` | `shared.redis` → 顶层 `redis`（同上字段规范）；删 `monitor.enabled`/`monitor.httpEnabled`；`monitor.http` 改子段；`monitor.apdexT`→`apdexThresholdMs`；`standalone.bot.count`→`totalBots`、`concurrentNum`→`concurrency`；`agent.adminAddr`→`adminUrl`、`hbInterval`→`heartbeatInterval` 等 agent 字段改名 |
| `conf/agent-config.json` | log 字段对齐（`maxSize`→`maxSizeMB` 等）；monitor 对齐；agent 段字段改名（`adminAddr`→`adminUrl`、`hbInterval`→`heartbeatInterval`、`hbRequestTimeout`→`heartbeatTimeout`、`hbFailThreshold`→`heartbeatFailThreshold`、`stressInterval`→`metricsInterval`） |
| `deploy/admin-config.json` | **不维护**（历史快照，含废弃字段 `purgeAfter`）。设计文档声明 deploy/ 下是发布产物的历史副本，配置结构以 `conf/` 为准 |

## Go 代码改动清单（按文件）

### `admin/config.go`
- `Config`：删 `Shared`/`History.MySQL`/`PprofConfig.Enabled`；加 `MySQL *MySQLConfig`、`Redis *sharedstate.RedisConfig`；`History`/`Pprof` 改指针。
- 删 `SharedEnabled()`，加 `RedisEnabled()`。
- `HistoryConfig` 只剩 `RetentionDays`。
- `MySQLConfig`：`User`→`Username`（JSON tag `user`→`username`）；新增 `DialTimeout`/`ReadTimeout`/`WriteTimeout`（字符串 duration）；`DSN()` 拼接时带上 timeout 参数。
- `DefaultConfig` 调整默认值结构。
- `validateConfig`：MySQL/Redis 连通性校验。

### `admin/admin.go`
- `NewAdminServer`：装配全局 `s.db` + resolve redis；`NewHistoryStore(s.db, cfg.History)`；10 处 `SharedEnabled()` → `RedisEnabled()`；`cfg.Shared.Redis` 读取 → `cfg.Redis`。
- `Shutdown`：`s.db.Close()`。

### `admin/history.go`
- `HistoryStore` 结构体去 `cfg HistoryConfig`，改 `db *sql.DB` + `retentionDays int`。
- `NewHistoryStore` 签名改。
- 删 `openDB`（移到 admin.go 或保留为包级函数但由 admin.go 调）。
- `Close` 不再 Close db。
- `initSchema` 用共享 db。

### `admin/history_schema.go` → `admin/mysql_schema.go`
- 改名。DDL 内容不变。

### `admin/handlers.go`
- `handleCapabilities`：`s.cfg.SharedEnabled()` → `s.cfg.RedisEnabled()`；`s.cfg.Shared.Redis` → `s.cfg.Redis`。
- `handleStartTask` / `startTaskBackground`：同上。

### `admin/shared_cleanup.go`
- 3 处 `SharedEnabled()` → `RedisEnabled()`；`cfg.Shared.Redis.Resolve()` → `cfg.Redis.Resolve()`。

### `admin/types.go`
- `SharedRuntimeAssignment.Redis` 字段名/类型不变（HTTP 下发契约）；其内部 `RedisConfig` 字段名随 sharedstate 规范化（host+port、dbIndex）同步，序列化后的 JSON 字段名一并更新为本次规范后的新契约。

### `cmd/agent/main.go`
- `Config`：`Shared *sharedstate.Config` → `Redis *sharedstate.RedisConfig`；`Monitor` 值→指针；`Pprof` 值→指针。
- `StandaloneConfig.Bot`：`Count`→`TotalBots`、`ConcurrentNum`→`Concurrency`。
- `main` 里读 `cfg.Shared.Redis` 三处 → `cfg.Redis`。
- 装配 `ManagerConfig` 时字段名同步。

### `agent/config.go`
- `Config`：`AdminAddr`→`AdminUrl`（JSON tag `adminAddr`→`adminUrl`）；`HBInterval`→`HeartbeatInterval`（`hbInterval`→`heartbeatInterval`）；`HBRequestTimeout`→`HeartbeatTimeout`（`hbRequestTimeout`→`heartbeatTimeout`）；`HBFailThreshold`→`HeartbeatFailThreshold`（`hbFailThreshold`→`heartbeatFailThreshold`）；`StressInterval`→`MetricsInterval`（`stressInterval`→`metricsInterval`）。
- `ResolvedConfig` 同步改名：`AdminAddr`→`AdminUrl`、`HBInterval`→`HeartbeatInterval`、`HBFailInterval`→`HeartbeatFailInterval`、`HBRequestTimeout`→`HeartbeatTimeout`、`HBFailThreshold`→`HeartbeatFailThreshold`、`StressInterval`/`SystemInterval`→`MetricsInterval`。
- `Resolve()` 内的字段引用、默认值日志标签、`utils.ParseDurationDefault` 的第三参数（字段名诊断串）全部同步。
- `agent.enabled` 保留不改。

### `agent/types.go`
- `SharedRuntimeAssignment.Redis` 字段名/类型不变；内部字段随 sharedstate 规范化同步。

### `monitor/collector.go`
- `CollectorConfig`：删 `Enabled`/`HTTPEnabled`/`HTTPPort`；`ApdexT`→`ApdexThresholdMs`（JSON tag `apdexT`→`apdexThresholdMs`）；加 `HTTP *HTTPConfig`。
- 读取 `cfg.Enabled` 的地方改为 nil 判断（由调用方保证 `*CollectorConfig` 非 nil 才启用）。
- 新增 `HTTPConfig { Port int }` 结构。

### `utils/log/logger.go`
- `Config`：加 `Path string`；`MaxSize` 的 JSON tag `maxSize` → `maxSizeMB`。
- `defaultConfig` 补 `Path` 默认值。

### `sharedstate/config.go`
- `Config` 外层结构废弃（标记 deprecated 或直接删，引用方改用 `RedisConfig`）。
- `RedisConfig`：`Addr`→`Host`+`Port`（JSON tag `addr` 拆为 `host`+`port`）；`DB`→`DBIndex`（`db`→`dbIndex`）；删除 `PoolSize`，新增 `MaxOpenConns`/`MaxIdleConns`/`ConnMaxLifetime`（与 MySQL 对齐）。
- `ResolvedRedisConfig` 同步：`Addr`→`Host`+`Port`、`DB`→`DBIndex`、`PoolSize`→连接池三字段。
- `Enabled()`：由 `Addr != ""` 改为 `Host != ""`。
- `Resolve()`：校验 `Host`、构造内部 redis 客户端所需地址；连接池参数传递。
- `AddrMasked()`/`MaskAddr`：从 `host:port` 拼接后脱敏，或改为接收 host+port。

### 调用方连带（grep 确认）
- `RedisConfig.Addr` 的所有读取点（capabilities 脱敏、redis_store 构造、下发赋值）改 `Host`+`Port`。
- `RedisConfig.DB` 的所有读取点改 `DBIndex`。
- `RedisConfig.PoolSize` 的读取点改 `MaxOpenConns` 等。
- `cfg.Agent.AdminAddr` 在 agent 各处（注册、心跳、任务拉取）改 `AdminUrl`。
- monitor `ApdexT` 读取点改 `ApdexThresholdMs`。

## 前端改动

- `CapabilitiesResponse.sharedState` 字段**不改**（HTTP 契约）。
- `HISTORY_DISABLED` 错误文案若展示给用户，更新文案（可选）。
- 其余前端配置无改动。

## 验证计划

1. `go build ./...`（admin + agent + 单机三套配置加载路径都覆盖）。
2. `cd cmd/web && npx tsc -b`（确认前端无类型错误，预期无改动）。
3. 用新 `conf/admin-config.json` 启动 Admin：
   - MySQL 配置 → 历史归档可用。
   - Redis 配置 → `/sbot/capabilities` 返回 `sharedState=true`。
   - MySQL 留空 → 历史接口返回 `HISTORY_DISABLED`。
   - Redis 留空 → `sharedState=false`，share 脚本预检拦截。
4. 用新 `conf/config.json` 启动单机：
   - `standalone.bot.totalBots`/`concurrency` 生效。
   - Redis 配置 → sharedStore 可用。
5. 用新 `conf/agent-config.json` 启动 Agent：
   - log `maxSizeMB` 字段生效。
   - 连接到 Admin，任务下发 Redis 配置正常。
6. 确认 `deploy/admin-config.json` 不被构建/运行引用（历史快照）。

## 后续阶段（不在本次）

- 后端流程模板库（`flow_template` 表 + CRUD），复用本次的全局 `cfg.MySQL`。
- 选库启动（`TaskStartModal` 流程来源选择 + `startTask` flow 入参 + `stashAndReplaceCanvas`）。
- 历史任务 `flow_template_id` 逻辑外键溯源。
