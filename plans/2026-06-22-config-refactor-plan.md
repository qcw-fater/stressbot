# 配置模型重构实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 stressbot 三套配置（Admin / 单机+Agent / Log）统一为"指针+nil 判断、全局基础设施、命名规范一致"的形态，为后续后端流程模板库扫清错误边界。

**Architecture:** MySQL/Redis 提升为全局顶层指针配置，由进程统一初始化；删除所有冗余 `enabled` 字段，用 `*T != nil` 判断；三套 LogConfig 合并为 `stresslog.Config`；全配置字段命名规范化（host+port 分开、展开缩写、单位进名字、interval/timeout 后缀统一）。不保留旧字段兼容读取。

**Tech Stack:** Go 1.22+（`net/http` ServeMux 方法路由、`database/sql` + `go-sql-driver/mysql`、`github.com/redis/go-redis/v9`）、`encoding/json` 反序列化、`lumberjack` 日志切割。

## Global Constraints

- **禁止兼容性兜底**：不写旧字段自动迁移、不用 `??` / `if old != "" { new = old }` 式回退。旧字段直接删除，配置文件按新结构一次性重写。（项目约定 `feedback_no_compat_hacks`）
- **日志用中文**：所有日志/错误信息中文（项目约定）。
- **Go 字段名与 JSON tag 一致风格**：`json:"fooBar"` 对应 Go 字段 `FooBar`。
- **每个任务结束必须 `go build ./...` 通过**：重构类改动没有行为测试入口，编译通过 + 配置加载探测是验证手段。
- **不发 FOREIGN KEY**：数据库只用逻辑外键（本次不新增表，约定备查）。
- **配置文件以 `conf/` 为准**：`deploy/` 下旧版配置快照不维护。
- **HTTP 下发契约不改**：`SharedRuntimeAssignment.Redis` 字段名/结构名不变（其内部字段随 RedisConfig 规范化同步，属于本次统一后的新契约）。

## 设计依据

详见 `plans/2026-06-22-config-refactor-design.md`。本计划是其可执行分解。

## 任务执行顺序依赖

```
Task 1 (RedisConfig 规范化) ──┐
Task 2 (MySQLConfig 规范化) ──┤
                              ├─→ Task 4 (Admin Config 收敛) ──→ Task 6 (AdminServer 装配 + HistoryStore 共享 db) ──→ Task 8 (配置文件 + 端到端)
Task 3 (LogConfig 统一)     ──┤                                                                      ↑
                              ├─→ Task 5 (Agent/Monitor/Standalone 收敛) ─────────────────────────┘
Task 7 (HistoryStore 改造) ───────────────────────────────────────────────────────────────────────┘
```

Task 1/2/3 互相独立可并行。Task 4 依赖 1+2。Task 5 依赖 1+3。Task 6 依赖 4+7。Task 7 依赖 2。Task 8 依赖 5+6。

---

## Task 1: RedisConfig 字段规范化

**Files:**
- Modify: `sharedstate/config.go:31-132`
- Modify: `sharedstate/redis_store.go`（构造函数读取 Host/Port/DBIndex/连接池）
- Modify: `sharedstate/config_test.go` 或新增 `sharedstate/config_test.go`（若不存在）
- Verify: `grep -rn "\.Addr\b\|\.DB\b\|\.PoolSize\b" sharedstate/ admin/ agent/`

**Interfaces:**
- Consumes: 无（源头改动）
- Produces: `RedisConfig{Host, Port int, ..., DBIndex int, MaxOpenConns, MaxIdleConns int, ConnMaxLifetime string}`、`Enabled()` 改为判断 `Host != ""`、`Resolve()` 返回 `ResolvedRedisConfig` 同步改名。

**说明：** 这是契约改动，会连带 admin/agent 下所有读 `RedisConfig.Addr/DB/PoolSize` 的地方编译失败。本任务**只改 sharedstate 包本身**，让 `go build ./sharedstate/...` 通过；admin/agent 的连带修复在 Task 4/5 做。因此本任务结束后 `go build ./...` **预期仍会失败**（跨包引用未修），验证手段改为 `go build ./sharedstate/...`。

- [ ] **Step 1: 改 `RedisConfig` 结构体字段**

将 `sharedstate/config.go:31-43` 改为：

```go
// RedisConfig Redis 连接配置（原始字符串形态，duration 用字符串便于配置文件书写）。
type RedisConfig struct {
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	DBIndex         int    `json:"dbIndex"`
	KeyPrefix       string `json:"keyPrefix"`
	DefaultClaimTTL string `json:"defaultClaimTTL"`
	OpTimeout       string `json:"opTimeout"`
	DialTimeout     string `json:"dialTimeout"`
	ReadTimeout     string `json:"readTimeout"`
	WriteTimeout    string `json:"writeTimeout"`
	MaxOpenConns    int    `json:"maxOpenConns"`
	MaxIdleConns    int    `json:"maxIdleConns"`
	ConnMaxLifetime string `json:"connMaxLifetime"`
}
```

删除原 `Addr string`、`DB int`、`PoolSize int`。

- [ ] **Step 2: 改 `ResolvedRedisConfig` 同步**

将 `sharedstate/config.go:46-58` 改为：

```go
// ResolvedRedisConfig 已解析（duration 转 time.Duration、填充默认值）的配置。
type ResolvedRedisConfig struct {
	Host            string
	Port            int
	Username        string
	Password        string
	DBIndex         int
	KeyPrefix       string
	DefaultClaimTTL time.Duration
	OpTimeout       time.Duration
	DialTimeout     time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}
```

- [ ] **Step 3: 改 `Enabled()` 判断条件**

将 `sharedstate/config.go:61-63` 改为：

```go
// Enabled 返回是否配置了 Redis 地址（host 为空表示不启用）。
func (c RedisConfig) Enabled() bool {
	return c.Host != ""
}
```

- [ ] **Step 4: 改 `Resolve()` 实现**

将 `sharedstate/config.go:67-101` 改为基于 `Host`/`Port`/`DBIndex` 构造解析结果。关键点：
- `c.Host == ""` 返回错误（原 `c.Addr == ""`）。
- `out := ResolvedRedisConfig{Host: c.Host, Port: c.Port, ..., DBIndex: c.DBIndex, MaxOpenConns: c.MaxOpenConns, MaxIdleConns: c.MaxIdleConns}`。
- duration 字段解析逻辑不变（`parseDurationDefault`）。
- 新增 `ConnMaxLifetime` 解析（和 duration 同款）。

```go
func (c RedisConfig) Resolve() (ResolvedRedisConfig, error) {
	if c.Host == "" {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: redis.host 为空，未启用共享状态")
	}

	port := c.Port
	if port == 0 {
		port = 6379
	}

	out := ResolvedRedisConfig{
		Host:         c.Host,
		Port:         port,
		Username:     c.Username,
		Password:     c.Password,
		DBIndex:      c.DBIndex,
		KeyPrefix:    c.KeyPrefix,
		MaxOpenConns: c.MaxOpenConns,
		MaxIdleConns: c.MaxIdleConns,
	}
	if out.KeyPrefix == "" {
		out.KeyPrefix = defaultKeyPrefix
	}

	var err error
	if out.DefaultClaimTTL, err = parseDurationDefault(c.DefaultClaimTTL, defaultClaimTTL); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 defaultClaimTTL 失败: %w", err)
	}
	if out.OpTimeout, err = parseDurationDefault(c.OpTimeout, defaultOpTimeout); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 opTimeout 失败: %w", err)
	}
	if out.DialTimeout, err = parseDurationDefault(c.DialTimeout, defaultDialTimeout); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 dialTimeout 失败: %w", err)
	}
	if out.ReadTimeout, err = parseDurationDefault(c.ReadTimeout, defaultReadWriteTimout); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 readTimeout 失败: %w", err)
	}
	if out.WriteTimeout, err = parseDurationDefault(c.WriteTimeout, defaultReadWriteTimout); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 writeTimeout 失败: %w", err)
	}
	if out.ConnMaxLifetime, err = parseDurationDefault(c.ConnMaxLifetime, 0); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 connMaxLifetime 失败: %w", err)
	}
	return out, nil
}
```

- [ ] **Step 5: 改 `AddrMasked()` / `MaskAddr`**

`MaskAddr(addr string)` 当前接收 `host:port` 合并串。改为接收 host+port 后内部拼接脱敏，或新增 `MaskHostPort(host string, port int) string`。

将 `sharedstate/config.go:104-119` 改为：

```go
// AddrMasked 返回脱敏后的地址（用于 capabilities 展示）。
func (c ResolvedRedisConfig) AddrMasked() string {
	return MaskHostPort(c.Host, c.Port)
}

// MaskHostPort 对 host+port 脱敏：隐藏主机（避免泄露内网细节），仅保留端口。
func MaskHostPort(host string, port int) string {
	if host == "" {
		return ""
	}
	return fmt.Sprintf("***:%d", port)
}
```

删除原 `MaskAddr(addr string)`（或保留但标注 deprecated，本次直接删，调用方在 Task 4/5 改）。

- [ ] **Step 6: 改 `redis_store.go` 构造函数**

打开 `sharedstate/redis_store.go`，找到 `NewRedisStore` 读取 `resolved.Addr` / `resolved.PoolSize` 的地方，改为：
- redis 客户端 `Addr` 字段用 `fmt.Sprintf("%s:%d", resolved.Host, resolved.Port)` 拼接。
- `DB` 用 `resolved.DBIndex`。
- 连接池：`PoolSize` → `MaxOpenConns`（若 >0）、`MinIdleConns` → `MaxIdleConns`（若 >0）、`ConnMaxLifetime` 用 `resolved.ConnMaxLifetime`。

具体改动见 `NewRedisStore` 现有实现里 `redis.Options{...}` 的赋值，把字段名对上。

- [ ] **Step 7: 修复 sharedstate 包内单元测试**

若 `sharedstate/redis_store_test.go` / `codec_test.go` 引用了 `RedisConfig{Addr:...}` / `resolved.Addr`，改为 `{Host:..., Port:...}` / `resolved.Host`。

- [ ] **Step 8: 编译验证**

Run: `go build ./sharedstate/...`
Expected: 编译通过（admin/agent 包此时仍失败，本任务不负责）。

- [ ] **Step 9: 提交**

```bash
git add sharedstate/config.go sharedstate/redis_store.go sharedstate/*_test.go
git commit -m "refactor: RedisConfig 字段规范化（host+port 分开、dbIndex、连接池对齐 MySQL）"
```

---

## Task 2: MySQLConfig 字段规范化

**Files:**
- Modify: `admin/config.go:56-76`（MySQLConfig 结构 + DSN）
- Verify: `grep -rn "\.User\b\|cfg\.History\.MySQL" admin/`

**Interfaces:**
- Consumes: 无
- Produces: `MySQLConfig{Host, Port int, Username, Password, Database string, DialTimeout, ReadTimeout, WriteTimeout string, MaxOpenConns, MaxIdleConns int, ConnMaxLifetime string}`、`DSN()` 带 timeout 参数。

**说明：** 本任务只改 `MySQLConfig` 结构和 `DSN()` 方法。`cfg.History.MySQL` 的引用（admin/admin.go 装配处）在 Task 4 改。本任务结束后 `go build ./admin/...` 可能失败（admin.go 还读 cfg.History.MySQL），验证用 `go vet ./admin/config.go` 单文件级或推迟到 Task 4。

- [ ] **Step 1: 改 `MySQLConfig` 结构体**

将 `admin/config.go:56-66` 改为：

```go
// MySQLConfig MySQL 连接配置。
type MySQLConfig struct {
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	Database        string `json:"database"`
	DialTimeout     string `json:"dialTimeout"`
	ReadTimeout     string `json:"readTimeout"`
	WriteTimeout    string `json:"writeTimeout"`
	MaxOpenConns    int    `json:"maxOpenConns"`
	MaxIdleConns    int    `json:"maxIdleConns"`
	ConnMaxLifetime string `json:"connMaxLifetime"`
}
```

`User` → `Username`，新增 `DialTimeout`/`ReadTimeout`/`WriteTimeout`。

- [ ] **Step 2: 改 `DSN()` 带 timeout**

将 `admin/config.go:69-76` 改为：

```go
// DSN 拼接标准 MySQL 连接字符串，含 timeout 参数。
func (c MySQLConfig) DSN() string {
	port := c.Port
	if port == 0 {
		port = 3306
	}
	// timeout 参数：各字段未配则用驱动默认（不写进 DSN）。
	var params []string
	if c.DialTimeout != "" {
		params = append(params, "timeout="+c.DialTimeout)
	}
	if c.ReadTimeout != "" {
		params = append(params, "readTimeout="+c.ReadTimeout)
	}
	if c.WriteTimeout != "" {
		params = append(params, "writeTimeout="+c.WriteTimeout)
	}
	extra := "parseTime=true&loc=Local"
	if len(params) > 0 {
		extra += "&" + strings.Join(params, "&")
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?%s",
		c.Username, c.Password, c.Host, port, c.Database, extra)
}
```

需在 `admin/config.go` 顶部 import 加 `"strings"`（若未有）。

- [ ] **Step 3: 改 `openDB` 函数**

`admin/history.go:77-98` 的 `openDB(cfg MySQLConfig)` 内部读 `cfg.MaxOpenConns`/`cfg.MaxIdleConns`/`cfg.ConnMaxLifetime` 不变（字段名没变），但确认逻辑保留。**本步骤无改动**，仅确认。

- [ ] **Step 4: 提交**

```bash
git add admin/config.go
git commit -m "refactor: MySQLConfig 字段规范化（username、新增 timeout 三字段、DSN 带 timeout）"
```

---

## Task 3: LogConfig 统一为 stresslog.Config

**Files:**
- Modify: `utils/log/logger.go:26-35`（Config 加 Path、MaxSize tag 改名）
- Modify: `admin/config.go:78-84`（删除 admin.LogConfig，改用 stresslog.Config）
- Modify: `admin/config.go:13-24`（Config.Log 类型改）
- Modify: `admin/config.go:87-111`（DefaultConfig 的 Log 默认值）
- Modify: `cmd/agent/main.go:63`（Config.Log 已是 stresslog.Config，确认）

**Interfaces:**
- Consumes: 无
- Produces: 统一的 `stresslog.Config`（含 Path、MaxSize tag=`maxSizeMB`）。

- [ ] **Step 1: 改 `stresslog.Config`**

将 `utils/log/logger.go:26-35` 改为：

```go
type Config struct {
	Path        string `json:"path" yaml:"path"`             // 日志文件路径
	PrintConsole bool   `json:"printConsole" yaml:"printConsole"` // 是否控制台输出
	LogLevel     string `json:"level" yaml:"logLevel"`        // 日志等级[debug, info, warn, error]
	MaxSize      int    `json:"maxSizeMB" yaml:"maxSizeMB"`   // 日志文件大小，超过则切割，单位M
	MaxBackups   int    `json:"maxBackups" yaml:"maxBackups"` // 日志文件最大保留个数
	MaxAge       int    `json:"maxAge" yaml:"maxAge"`         // 日志文件最大保存天数
	LocalTime    bool   `json:"localTime" yaml:"localTime"`   // 是否使用服务器本地时间
	Compress     bool   `json:"compress" yaml:"compress"`     // 日志是否压缩
	WeChatToken  string `json:"weChatToken" yaml:"weChatToken"` // 企微Hook密钥
}
```

新增 `Path`，`MaxSize` 的 JSON tag 由 `maxSize` 改为 `maxSizeMB`。

- [ ] **Step 2: 改 `defaultConfig`**

将 `utils/log/logger.go:37-45` 的 `defaultConfig` 补上 `Path` 默认值（如 `"log/stressbot.log"`），保持原有其他默认。

- [ ] **Step 3: 删除 admin 自有 LogConfig**

删除 `admin/config.go:78-84` 的 `type LogConfig struct {...}` 整段。

- [ ] **Step 4: 改 admin.Config.Log 类型**

将 `admin/config.go:20` 的 `Log LogConfig` 改为 `Log stresslog.Config`（import `stresslog "stressbot/utils/log"`，若 admin/config.go 未引入则加）。

- [ ] **Step 5: 改 admin.DefaultConfig 的 Log 默认值**

将 `admin/config.go:104-109` 的 `Log: LogConfig{...}` 改为 `Log: stresslog.Config{Path: "log/admin.log", LogLevel: "info", MaxSize: 100, MaxBackups: 10}`（字段名用 stresslog.Config 的 Go 字段名）。

- [ ] **Step 6: 确认 admin 其他读 LogConfig 的地方**

Run: `grep -n "LogConfig\|cfg.Log\." admin/*.go`
Expected: admin 包内不再有 `LogConfig` 类型引用；`cfg.Log.Level` 改为 `cfg.Log.LogLevel`（若 admin.go 有读 Level 的地方）。

修复所有 `cfg.Log.Level` → `cfg.Log.LogLevel`、`cfg.Log.Path`（已有）、`cfg.Log.MaxSizeMB`（字段名 MaxSize 不变，tag 变）。

- [ ] **Step 7: 编译验证**

Run: `go build ./utils/log/...`
Expected: 通过。

- [ ] **Step 8: 提交**

```bash
git add utils/log/logger.go admin/config.go
git commit -m "refactor: 统一 LogConfig 为 stresslog.Config（新增 path、maxSize tag 改名）"
```

---

## Task 4: Admin Config 结构收敛

**依赖：** Task 1（RedisConfig）、Task 2（MySQLConfig）、Task 3（LogConfig）。

**Files:**
- Modify: `admin/config.go`（Config 结构、SharedEnabled→RedisEnabled、HistoryConfig 瘦身、PprofConfig 删 Enabled、DefaultConfig、validateConfig）
- Modify: `admin/handlers.go`（SharedEnabled→RedisEnabled、cfg.Shared.Redis→cfg.Redis）
- Modify: `admin/shared_cleanup.go`（同上，3 处）
- Modify: `admin/admin.go`（SharedEnabled→RedisEnabled、cfg.Shared.Redis→cfg.Redis；**装配改造留到 Task 6**）

**Interfaces:**
- Consumes: Task 1 的 `*sharedstate.RedisConfig`、Task 2 的 `*MySQLConfig`、Task 3 的 `stresslog.Config`。
- Produces: `admin.Config{MySQL *MySQLConfig, Redis *sharedstate.RedisConfig, History *HistoryConfig, Pprof *PprofConfig, ...}`、`RedisEnabled()` 方法。

**说明：** 本任务改 admin 包的**配置结构与读取**，但**不改装配逻辑**（HistoryStore 共享 db 的装配在 Task 6）。本任务结束后 `go build ./admin/...` 应能通过（装配逻辑暂时还读旧字段的话会失败，因此装配相关行在本任务里先做最小改动让其编译过，真正的 HistoryStore 改造在 Task 6/7）。

- [ ] **Step 1: 改 `Config` 结构**

将 `admin/config.go:13-24` 改为：

```go
// Config Admin 服务端配置。
type Config struct {
	Port          int                      `json:"port"`
	PublicURL     string                   `json:"publicUrl"`
	StaticDir     string                   `json:"staticDir"`
	AgentRegistry RegistryConfig           `json:"agentRegistry"`
	MySQL         *MySQLConfig             `json:"mysql"`
	Redis         *sharedstate.RedisConfig `json:"redis"`
	History       *HistoryConfig           `json:"history"`
	Log           stresslog.Config         `json:"log"`
	Pprof         *PprofConfig             `json:"pprof"`
	Daemon        bool                     `json:"daemon"`
}
```

- [ ] **Step 2: 删 `SharedEnabled()`，加 `RedisEnabled()`**

将 `admin/config.go:26-29` 改为：

```go
// RedisEnabled 返回服务器是否配置了 Redis（host 非空）。
func (c *Config) RedisEnabled() bool {
	return c.Redis != nil && c.Redis.Enabled()
}
```

- [ ] **Step 3: 瘦身 `HistoryConfig`**

将 `admin/config.go:49-54` 改为：

```go
// HistoryConfig 历史归档配置（MySQL 已提升为 Config.MySQL）。
type HistoryConfig struct {
	RetentionDays int `json:"retentionDays"` // 历史数据保留天数（默认 90）
}
```

删除 `Enabled`、`MySQL` 字段。

- [ ] **Step 4: 改 `PprofConfig`**

将 `admin/config.go:31-35` 改为：

```go
// PprofConfig pprof 调试服务配置（Config.Pprof 为 nil 时不启用）。
type PprofConfig struct {
	Port int `json:"port"` // pprof 监听端口（默认 6060）
}
```

删除 `Enabled` 字段。

- [ ] **Step 5: 改 `DefaultConfig`**

将 `admin/config.go:87-111` 改为（去掉 History 嵌套的 MySQL/Enabled，MySQL 提到顶层；Pprof 默认不设——nil 即不启用；HistoryRetention 默认 90）：

```go
func DefaultConfig() Config {
	return Config{
		Port:      7718,
		StaticDir: "cmd/web/dist",
		AgentRegistry: RegistryConfig{
			UnhealthyAfter: "30s",
			OfflineAfter:   "60s",
		},
		MySQL: &MySQLConfig{
			MaxOpenConns:    10,
			MaxIdleConns:    5,
			ConnMaxLifetime: "1h",
			DialTimeout:     "5s",
			ReadTimeout:     "30s",
			WriteTimeout:    "30s",
		},
		History: &HistoryConfig{RetentionDays: 90},
		Log: stresslog.Config{
			Path:      "log/admin.log",
			LogLevel:  "info",
			MaxSize:   100,
			MaxBackups: 10,
		},
	}
}
```

- [ ] **Step 6: 改 `validateConfig`**

将 `admin/config.go:131-150` 改为：

```go
func validateConfig(cfg *Config) error {
	if cfg.Port <= 0 {
		return fmt.Errorf("port is required and must be > 0")
	}
	if cfg.PublicURL == "" {
		return fmt.Errorf("publicUrl is required")
	}
	if _, err := time.ParseDuration(cfg.AgentRegistry.UnhealthyAfter); cfg.AgentRegistry.UnhealthyAfter != "" && err != nil {
		return fmt.Errorf("invalid agentRegistry.unhealthyAfter: %w", err)
	}
	if _, err := time.ParseDuration(cfg.AgentRegistry.OfflineAfter); cfg.AgentRegistry.OfflineAfter != "" && err != nil {
		return fmt.Errorf("invalid agentRegistry.offlineAfter: %w", err)
	}
	if cfg.RedisEnabled() {
		if _, err := cfg.Redis.Resolve(); err != nil {
			return fmt.Errorf("invalid redis config: %w", err)
		}
	}
	// MySQL 连通性校验推迟到装配阶段（NewAdminServer 里 openDB + ping）。
	return nil
}
```

- [ ] **Step 7: 批量替换 admin 包内 `SharedEnabled()` → `RedisEnabled()`**

Run: `grep -rn "SharedEnabled" admin/`
Expected: 输出所有调用点（admin/admin.go、admin/handlers.go、admin/shared_cleanup.go、admin/config.go 已改）。

逐个改为 `RedisEnabled()`。

- [ ] **Step 8: 批量替换 `cfg.Shared.Redis` → `cfg.Redis`（admin 包）**

Run: `grep -rn "cfg\.Shared\.Redis\|s\.cfg\.Shared\.Redis" admin/`
Expected: `admin/handlers.go:150`（capabilities 脱敏）、`admin/handlers.go:824`（下发赋值）、`admin/shared_cleanup.go:122`（cleanup resolve）等。

逐个改为 `cfg.Redis` / `s.cfg.Redis`。注意：
- `handleCapabilities` 的脱敏地址：`sharedstate.MaskAddr(s.cfg.Shared.Redis.Addr)` → `s.cfg.Redis.Resolve()` 后取 `AddrMasked()`，或直接 `resolved := mustResolve(s.cfg.Redis); resolved.AddrMasked()`。具体见 `admin/handlers.go:142-148` 现有逻辑，改为读 `s.cfg.Redis` 后 resolve。
- `startTaskBackground` 下发：`Redis: *s.cfg.Redis`（解引用指针，赋值给 SharedRuntimeAssignment.Redis 值类型字段）。

- [ ] **Step 9: 改 admin/admin.go 装配处（最小改动让其编译）**

`admin/admin.go:75-90` 当前：
```go
if cfg.History.Enabled {
    history, err := NewHistoryStore(cfg.History)
    ...
}
```

临时改为（Task 6 会进一步重构为共享 db）：
```go
if cfg.MySQL != nil && cfg.History != nil {
    history, err := NewHistoryStore(*cfg.MySQL, cfg.History)
    if err != nil {
        return nil, fmt.Errorf("init history store: %w", err)
    }
    s.history = history
    ...
}
```

注意：此时 `NewHistoryStore` 签名还是老的（Task 7 才改）。所以本步骤**先保持 NewHistoryStore 老签名**，但参数来源改为 `*cfg.MySQL`。这会导致编译失败（NewHistoryStore 还接收 HistoryConfig）。

**因此本任务的 Step 9 实际要和 Task 7 协调**：为了让 `go build ./admin/...` 在本任务结束时通过，把 Task 7 的 `NewHistoryStore` 签名改造**提前到本任务一起做**，或本任务先不编译验证 admin 包、合并到 Task 6/7 一起验证。

**决策：** 将 Task 7（HistoryStore 改造）合并进本任务的 Step 9-12，避免中间编译失败态。

- [ ] **Step 10: 改 `NewHistoryStore` 签名（原 Task 7 内容）**

将 `admin/history.go:41-75` 改为：

```go
// HistoryStore MySQL 历史归档存储。
// db 由 AdminServer 统一管理（共享全局 MySQL 实例），HistoryStore 不负责 Close。
type HistoryStore struct {
	db            *sql.DB
	retentionDays int
	prune         time.Duration
	cancel        context.CancelFunc
}

// NewHistoryStore 创建历史归档存储。
// db 必须非 nil（由 AdminServer 装配时传入共享 *sql.DB）。
// cfg 可为 nil（表示不启用历史归档，retentionDays 用默认 90）。
func NewHistoryStore(db *sql.DB, cfg *HistoryConfig) *HistoryStore {
	retention := 90
	if cfg != nil && cfg.RetentionDays > 0 {
		retention = cfg.RetentionDays
	}
	return &HistoryStore{
		db:            db,
		retentionDays: retention,
		prune:         24 * time.Hour,
	}
}
```

删除原 `if !cfg.Enabled { return nil, nil }`、`openDB`、`initSchema` 调用（initSchema 移到包级函数，Task 6 装配时调）。

- [ ] **Step 11: 把 `initSchema` 改为包级函数**

将 `admin/history.go:100-109` 改为：

```go
// initMySQLSchema 初始化所有 MySQL 表（幂等，CREATE IF NOT EXISTS）。
// 由 AdminServer 装配时调用一次。
func initMySQLSchema(db *sql.DB) error {
	ctx := context.Background()
	for _, ddl := range allDDL {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	dropLegacyForeignKeys(ctx, db)
	return nil
}
```

`dropLegacyForeignKeys` 也改为接收 `db *sql.DB` 参数（原是方法 `h.dropLegacyForeignKeys`，改包级函数）。

- [ ] **Step 12: 改 HistoryStore 其他方法读取 retentionDays**

Run: `grep -n "h.cfg.RetentionDays\|h.cfg\." admin/history.go`
Expected: `PruneExpired`（~1360）、`StartPruneLoop`（~1423）等。

逐个改 `h.cfg.RetentionDays` → `h.retentionDays`。

- [ ] **Step 13: 改 HistoryStore.Close 不再 Close db**

Run: `grep -n "func (h \*HistoryStore) Close" admin/history.go`
将 `Close()` 方法改为只 cancel prune loop，不 `h.db.Close()`（db 由 AdminServer 管）：

```go
func (h *HistoryStore) Close() {
	if h.cancel != nil {
		h.cancel()
	}
}
```

- [ ] **Step 14: 改 admin/admin.go 的 openDB 引用**

`openDB` 现在还在 `admin/history.go` 里（Step 11 没删它）。把它移到 `admin/admin.go` 或保留在 history.go 但确认是包级函数。本步骤确认 `openDB(MySQLConfig)` 仍可用，Task 6 装配时调。

- [ ] **Step 15: 编译验证**

Run: `go build ./admin/...`
Expected: 通过（含 history.go 的所有改造）。

- [ ] **Step 16: 提交**

```bash
git add admin/config.go admin/handlers.go admin/shared_cleanup.go admin/history.go admin/admin.go
git commit -m "refactor: admin 配置收敛（MySQL/Redis 全局指针、删 enabled、命名规范化、HistoryStore 共享 db 签名）"
```

---

## Task 5: Agent / Monitor / Standalone 收敛

**依赖：** Task 1（RedisConfig）、Task 3（LogConfig）。

**Files:**
- Modify: `cmd/agent/main.go`（Config 结构、StandaloneConfig.Bot 字段、main 读 cfg.Shared.Redis、读 cfg.Monitor/Pprof）
- Modify: `agent/config.go`（Config 字段改名、ResolvedConfig 同步、Resolve() 改）
- Modify: `monitor/collector.go`（CollectorConfig 删 Enabled/HTTPEnabled/HTTPPort、ApdexT→ApdexThresholdMs、加 HTTP *HTTPConfig）

**Interfaces:**
- Consumes: Task 1 的 RedisConfig、Task 3 的 stresslog.Config。
- Produces: `cmd/agent/main.go Config{Redis *sharedstate.RedisConfig, Monitor *monitor.CollectorConfig, Pprof *PprofConfig, ...}`、`agent.Config{AdminUrl, HeartbeatInterval, ...}`。

- [ ] **Step 1: 改 `cmd/agent/main.go` 的 Config 结构**

将 `cmd/agent/main.go:61-70` 改为：

```go
type Config struct {
	Log        stresslog.Config          `json:"log"`
	Monitor    *monitor.CollectorConfig  `json:"monitor"`   // nil = 不启用监控
	Pprof      *PprofConfig              `json:"pprof"`      // nil = 不启用 pprof
	Standalone *StandaloneConfig         `json:"standalone"` // nil = 非单机模式
	Agent      agent.Config              `json:"agent"`
	Redis      *sharedstate.RedisConfig  `json:"redis"`      // nil = 未配置 Redis
	Daemon     bool                      `json:"daemon"`
}
```

`Shared *sharedstate.Config` → `Redis *sharedstate.RedisConfig`；`Monitor` 值→指针；`Pprof` 值→指针。

- [ ] **Step 2: 改 `StandaloneConfig.Bot` 字段名**

将 `cmd/agent/main.go:40-46` 的匿名 Bot 结构改为：

```go
Bot struct {
    AccountPrefix string `json:"accountPrefix"`
    StartNumber   int    `json:"startNumber"`
    TotalBots     int    `json:"totalBots"`     // 原 count
    Concurrency   int    `json:"concurrency"`   // 原 concurrentNum
    MainService   string `json:"mainService"`
} `json:"bot"`
```

- [ ] **Step 3: 改 main 里读 Bot 字段处**

Run: `grep -n "Bot\.Count\|Bot\.ConcurrentNum" cmd/agent/main.go`
装配 ManagerConfig 处（~单机分支）改为读 `cfg.Standalone.Bot.TotalBots` / `.Concurrency`。

- [ ] **Step 4: 改 main 里读 cfg.Shared.Redis 三处**

`cmd/agent/main.go:266-274` 当前：
```go
if cfg.Shared == nil || !cfg.Shared.Redis.Enabled() { ... }
resolved, rerr := cfg.Shared.Redis.Resolve()
store, serr := sharedstate.NewRedisStore(resolved, ...)
```

改为：
```go
if cfg.Redis == nil || !cfg.Redis.Enabled() { ... }
resolved, rerr := cfg.Redis.Resolve()
store, serr := sharedstate.NewRedisStore(resolved, ...)
```

- [ ] **Step 5: 改 main 里读 cfg.Monitor（nil 判断）**

`cmd/agent/main.go` 多处读 `cfg.Monitor.Enabled` / `cfg.Monitor.HTTPEnabled` / `cfg.Monitor.HTTPPort` / `cfg.Monitor.ApdexT`：

- `:165-170` 的 `monitor.Init(monitor.CollectorConfig{...cfg.Monitor...})` → `monitor.Init(*cfg.Monitor)`（解引用，若非 nil）。
- `:238-242` 的 `if cfg.Monitor.Enabled` → `if cfg.Monitor != nil`。
- `:316` 的 `if cfg.Monitor.Enabled` → `if cfg.Monitor != nil`。
- `:320-322` 的 `if cfg.Monitor.HTTPEnabled` → `if cfg.Monitor.HTTP != nil`，`cfg.Monitor.HTTPPort` → `cfg.Monitor.HTTP.Port`。
- `:364` 的 `if cfg.Monitor.Enabled` → `if cfg.Monitor != nil`。

- [ ] **Step 6: 改 main 里读 cfg.Pprof（nil 判断）**

`cmd/agent/main.go:152,328` 的 `if cfg.Pprof.Enabled` → `if cfg.Pprof != nil`。

- [ ] **Step 7: 改 `agent/config.go` Config 字段改名**

将 `agent/config.go:14-27` 改为：

```go
type Config struct {
	Enabled               bool   `json:"enabled"`               // 保留：模式开关
	AdminUrl              string `json:"adminUrl"`              // 原 adminAddr
	PublicURL             string `json:"publicUrl"`
	Port                  int    `json:"port"`
	MaxBots               int    `json:"maxBots"`
	HeartbeatInterval     string `json:"heartbeatInterval"`     // 原 hbInterval
	HeartbeatTimeout      string `json:"heartbeatTimeout"`      // 原 hbRequestTimeout
	HeartbeatFailThreshold int   `json:"heartbeatFailThreshold"` // 原 hbFailThreshold
	RequestTimeout        string `json:"requestTimeout"`
	ReconnectMaxRetries   int    `json:"reconnectMaxRetries"`
	MetricsInterval       string `json:"metricsInterval"`       // 原 stressInterval
	AppVersion            string `json:"-"`
}
```

- [ ] **Step 8: 改 `ResolvedConfig` 同步**

将 `agent/config.go:30-50` 的 `ResolvedConfig` 字段改名：`AdminAddr`→`AdminUrl`、`HBInterval`→`HeartbeatInterval`、`HBFailInterval`→`HeartbeatFailInterval`、`HBRequestTimeout`→`HeartbeatTimeout`、`HBFailThreshold`→`HeartbeatFailThreshold`、`StressInterval`/`SystemInterval`→`MetricsInterval`（合并为一个，因为原 SystemInterval = StressInterval）。

注意：合并后 `SystemInterval` 字段移除，原读取 `r.SystemInterval` 的地方（task_runner 等）改读 `r.MetricsInterval`。需 grep `SystemInterval` 全部调用点。

- [ ] **Step 9: 改 `Resolve()` 实现**

`agent/config.go:52-125` 的 Resolve 方法，字段引用全部同步改名：

- `c.AdminAddr` → `c.AdminUrl`（校验 `c.AdminUrl == ""`）。
- `utils.ParseDurationDefault(c.StressInterval, ...)` → `c.MetricsInterval`，第三参数诊断串也改 `"agent.metricsInterval"`。
- `c.HBInterval` → `c.HeartbeatInterval`（诊断串 `"agent.heartbeatInterval"`）。
- `c.HBRequestTimeout` → `c.HeartbeatTimeout`（诊断串 `"agent.heartbeatTimeout"`）。
- `c.HBFailThreshold` → `c.HeartbeatFailThreshold`。
- 返回的 `ResolvedConfig{AdminAddr:...}` → `{AdminUrl: c.AdminUrl, ...HeartbeatInterval:..., MetricsInterval:..., SystemInterval: metricsInterval}`（若合并则只填 MetricsInterval）。

- [ ] **Step 10: grep 并修复 agent 包内所有读取点**

Run: `grep -rn "AdminAddr\|HBInterval\|HBRequestTimeout\|HBFailThreshold\|HBFailInterval\|StressInterval\|SystemInterval" agent/`
Expected: `agent/agent.go`（注册、心跳）、`agent/task_runner.go`、`agent/reporter.go` 等。

逐个改为新字段名。

- [ ] **Step 11: 改 `monitor/collector.go` CollectorConfig**

将 `monitor/collector.go:182-188` 改为：

```go
type CollectorConfig struct {
	ApdexThresholdMs int         `json:"apdexThresholdMs"` // 原 apdexT
	TimingDetail     string      `json:"timingDetail"`
	HTTP             *HTTPConfig `json:"http"` // nil = 不启用 HTTP 端点
}

// HTTPConfig 监控 HTTP JSON 端点配置。
type HTTPConfig struct {
	Port int `json:"port"`
}
```

删除 `Enabled`/`HTTPEnabled`/`HTTPPort`。

- [ ] **Step 12: 改 monitor 包内读取点**

Run: `grep -rn "\.Enabled\b\|\.HTTPEnabled\|\.HTTPPort\|\.ApdexT\b" monitor/`
Expected: `MetricsCollector.Enabled()` 方法（保留，是运行期方法不是配置字段）、各处读 `cfg.HTTPEnabled`/`cfg.HTTPPort`/`cfg.ApdexT`。

- `monitor.Init` 接收 `CollectorConfig`：调用方（cmd/agent/main.go）保证只在 `cfg.Monitor != nil` 时调 Init，Init 内部不再看 Enabled。
- `cfg.ApdexT` → `cfg.ApdexThresholdMs`，`SetApdexT` 方法名保留（运行期 API）但读取配置时用新字段。
- `cfg.HTTPEnabled`/`cfg.HTTPPort` → `cfg.HTTP != nil` / `cfg.HTTP.Port`（调用方 main.go 已在 Step 5 改）。

注意 `MetricsCollector.Enabled()` 方法（`monitor/collector.go:551`）是运行期方法（返回 collector 是否启用），**不改**。配置字段 `CollectorConfig.Enabled` 删除后，`MetricsCollector.enabled` 字段由 `monitor.Init` 调用时设为 true（因为只有启用才 Init）。

- [ ] **Step 13: 编译验证**

Run: `go build ./...`
Expected: 通过（admin + agent + 单机 + monitor 全部）。

- [ ] **Step 14: 提交**

```bash
git add cmd/agent/main.go agent/config.go agent/*.go monitor/collector.go
git commit -m "refactor: agent/monitor/standalone 配置收敛（字段改名、指针 nil、apdexThresholdMs）"
```

---

## Task 6: AdminServer 全局 MySQL 装配

**依赖：** Task 4。

**Files:**
- Modify: `admin/admin.go`（NewAdminServer 装配全局 db + redis、Shutdown 关闭 db）

**Interfaces:**
- Consumes: Task 4 的 `Config.MySQL *MySQLConfig`、`Config.Redis *sharedstate.RedisConfig`、`initMySQLSchema`、`NewHistoryStore(db, cfg)`。
- Produces: AdminServer 持有 `db *sql.DB`（全局共享）。

- [ ] **Step 1: 改 `NewAdminServer` 装配 MySQL**

`admin/admin.go:74-90` 当前装配 HistoryStore 的分支，改为先建全局 db：

```go
// MySQL（可选）
if cfg.MySQL != nil && cfg.MySQL.Host != "" {
	db, err := openDB(*cfg.MySQL)
	if err != nil {
		return nil, fmt.Errorf("connect mysql: %w", err)
	}
	s.db = db
	if err := initMySQLSchema(s.db); err != nil {
		db.Close()
		return nil, fmt.Errorf("init mysql schema: %w", err)
	}
	stresslog.Info("[ADMIN] MySQL 已连接",
		zap.String("addr", fmt.Sprintf("%s:%d", cfg.MySQL.Host, cfg.MySQL.Port)),
		zap.String("database", cfg.MySQL.Database))

	// History（依赖 MySQL）
	if cfg.History != nil {
		s.history = NewHistoryStore(s.db, cfg.History)
		sampler := NewSampler(10*time.Second, s.aggregator, s.history, s.agents, s.tasks)
		s.sampler = sampler
	}
} else {
	stresslog.Info("[ADMIN] MySQL 未配置：历史归档与未来流程库接口将返回 DISABLED")
}
```

- [ ] **Step 2: 改 Redis 装配（resolve 全局）**

`admin/admin.go:96-115` 当前：
```go
if cfg.SharedEnabled() {
    resolved, rerr := cfg.Shared.Redis.Resolve()
    ...
}
```

改为：
```go
if cfg.RedisEnabled() {
	resolved, rerr := cfg.Redis.Resolve()
	if rerr != nil {
		return nil, fmt.Errorf("共享状态配置无效: %w", rerr)
	}
	s.redis = resolved
	pingStore, perr := sharedstate.NewRedisStore(resolved, "admin-ping")
	if perr != nil {
		return nil, fmt.Errorf("连接共享状态(Redis)失败 (addr=%s): %w", resolved.AddrMasked(), perr)
	}
	_ = pingStore.Close()
	stresslog.Info("[ADMIN] 共享状态已启用",
		zap.String("addr", resolved.AddrMasked()),
		zap.Int("dbIndex", resolved.DBIndex),
		zap.String("keyPrefix", resolved.KeyPrefix))
	s.sharedCleanup = newSharedCleanupQueue("data")
} else {
	stresslog.Info("[ADMIN] 共享状态未启用：未配置 Redis（redis.host 为空）")
}
```

- [ ] **Step 3: 改 admin.go 其他读 s.cfg.Shared / cfg.Shared 处**

Run: `grep -n "Shared\|shared" admin/admin.go`
修复所有：`s.cfg.SharedEnabled()` → `s.cfg.RedisEnabled()`（Task 4 已改大部分，确认无遗漏）；`cfg.Shared.Redis` → `cfg.Redis`。

- [ ] **Step 4: 改 Shutdown 关闭 db**

`admin/admin.go:178-192` 的 Shutdown，在 `s.history.Close()` 后加：

```go
if s.db != nil {
	s.db.Close()
}
```

- [ ] **Step 5: AdminServer 结构体加 db 字段**

`admin/admin.go:28-47` 的 `AdminServer` 结构体加：

```go
db    *sql.DB
redis sharedstate.ResolvedRedisConfig
```

- [ ] **Step 6: 改 handlers/shared_cleanup 读 Redis**

确认 Task 4 Step 8 已把 `s.cfg.Shared.Redis` → `s.cfg.Redis`。本步骤再 grep 确认：

Run: `grep -rn "Shared" admin/`
Expected: 无 `cfg.Shared` / `s.cfg.Shared` 残留（只有 `SharedRuntimeAssignment`/`SharedUsed` 等业务字段，不改）。

- [ ] **Step 7: 编译验证**

Run: `go build ./...`
Expected: 通过。

- [ ] **Step 8: 提交**

```bash
git add admin/admin.go
git commit -m "refactor: AdminServer 全局 MySQL 装配 + Redis 全局 resolve"
```

---

## Task 7: schema 文件改名

**依赖：** Task 4（initMySQLSchema 已是包级函数）。

**Files:**
- Rename: `admin/history_schema.go` → `admin/mysql_schema.go`

- [ ] **Step 1: git mv 改名**

```bash
git mv admin/history_schema.go admin/mysql_schema.go
```

- [ ] **Step 2: 改文件内 package 注释**

`admin/mysql_schema.go:1-6` 的注释从"MySQL DDL — 历史归档 8 张表"改为"MySQL DDL — Admin 所有表（历史归档 + 未来流程模板库）"。

- [ ] **Step 3: 编译验证**

Run: `go build ./admin/...`
Expected: 通过（改名不影响符号，allDDL 仍在 admin 包）。

- [ ] **Step 4: 提交**

```bash
git add admin/mysql_schema.go
git commit -m "refactor: history_schema.go → mysql_schema.go（DDL 文件归属 Admin 全局）"
```

---

## Task 8: 配置文件改写 + 端到端验证

**依赖：** Task 5、Task 6、Task 7。

**Files:**
- Modify: `conf/admin-config.json`
- Modify: `conf/config.json`
- Modify: `conf/agent-config.json`

- [ ] **Step 1: 改写 `conf/admin-config.json`**

改为（新结构 + 规范化字段）：

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

  "log": {
    "path": "log/admin.log",
    "level": "info",
    "maxSizeMB": 100,
    "maxBackups": 10
  },

  "pprof": { "port": 6060 },

  "daemon": false
}
```

- [ ] **Step 2: 改写 `conf/config.json`**

改为：

```json
{
  "log": {
    "path": "log/stressbot.log",
    "level": "info",
    "printConsole": true,
    "maxSizeMB": 100,
    "maxBackups": 5,
    "maxAge": 7,
    "localTime": true,
    "compress": true
  },
  "monitor": {
    "apdexThresholdMs": 100,
    "timingDetail": "rtt",
    "http": { "port": 6061 }
  },
  "pprof": { "port": 6060 },
  "standalone": {
    "bot": {
      "accountPrefix": "bot_",
      "startNumber": 20,
      "totalBots": 100,
      "concurrency": 20,
      "mainService": "logic"
    },
    "stateExtra": {
      "authAddr": "http://127.0.0.1:20000",
      "version": "0.31.49.171222",
      "channel": "mine",
      "platform": "1000"
    }
  },
  "agent": {
    "enabled": false
  },
  "redis": {
    "host": "127.0.0.1",
    "port": 6379,
    "keyPrefix": "stressbot",
    "defaultClaimTTL": "30s",
    "opTimeout": "2s"
  },
  "daemon": false
}
```

注意：删了 `monitor.enabled`/`monitor.httpEnabled`；`standalone.bot` 字段改名；`shared.redis` → 顶层 `redis`。

- [ ] **Step 3: 改写 `conf/agent-config.json`**

改为（agent 字段改名 + log/monitor 对齐）：

```json
{
  "log": {
    "path": "log/agent.log",
    "level": "info",
    "printConsole": true,
    "maxSizeMB": 100,
    "maxBackups": 5,
    "maxAge": 7,
    "localTime": true,
    "compress": true
  },
  "monitor": {
    "apdexThresholdMs": 100,
    "timingDetail": "rtt"
  },
  "pprof": { "port": 6060 },
  "agent": {
    "enabled": true,
    "adminUrl": "http://127.0.0.1:7718",
    "publicUrl": "http://127.0.0.1:7719",
    "port": 7719,
    "maxBots": 5000,
    "heartbeatInterval": "10s",
    "heartbeatTimeout": "5s",
    "heartbeatFailThreshold": 3,
    "requestTimeout": "30s",
    "reconnectMaxRetries": -1,
    "metricsInterval": "5s"
  },
  "daemon": false
}
```

- [ ] **Step 4: 编译验证**

Run: `go build ./...`
Expected: 通过。

- [ ] **Step 5: Admin 启动验证**

Run: `go run ./cmd/admin -config conf/admin-config.json`
Expected:
- 日志含 `[ADMIN] MySQL 已连接`（若 MySQL 在跑）或 `[ADMIN] MySQL 未配置`。
- 日志含 `[ADMIN] 共享状态已启用`（若 Redis 在跑）或 `未启用`。
- 监听 7718，前端能打开。

- [ ] **Step 6: capabilities 验证**

Run: `curl http://127.0.0.1:7718/sbot/capabilities`
Expected: `{"sharedState": true/false, "sharedAddr": "***:6379"/""}`（取决于 Redis 是否配置）。

- [ ] **Step 7: 单机启动验证**

Run: `go run ./cmd/agent -config conf/config.json`
Expected: 日志显示加载新配置（bot.totalBots/bot.concurrency 字段生效），不报配置解析错误。

- [ ] **Step 8: Agent 启动验证**

Run: `go run ./cmd/agent -config conf/agent-config.json`
Expected: Agent 连接到 Admin，心跳正常（heartbeatInterval 等字段生效）。

- [ ] **Step 9: 历史归档可用性验证**

在 Admin 运行下，启动一个任务，结束后：
Run: `curl http://127.0.0.1:7718/sbot/history?limit=1`
Expected: 返回历史记录（若 MySQL 配置）或 `HISTORY_DISABLED` 错误（若 MySQL 未配置）。

- [ ] **Step 10: 提交**

```bash
git add conf/admin-config.json conf/config.json conf/agent-config.json
git commit -m "refactor: 三套配置文件改写为新结构（全局 MySQL/Redis + 命名规范化）"
```

---

## Self-Review

**1. Spec coverage（对照 `plans/2026-06-22-config-refactor-design.md`）**

- ✅ 指针+nil 判断：Task 4（admin History/Pprof 指针）、Task 5（agent Monitor/Pprof 指针）。
- ✅ MySQL/Redis 全局化：Task 4（Config.MySQL/Redis 顶层）、Task 6（AdminServer 全局 db）。
- ✅ LogConfig 统一：Task 3。
- ✅ MonitorConfig 统一：Task 5 Step 11-12。
- ✅ standalone.bot 字段：Task 5 Step 2-3。
- ✅ shared 包装层去掉：Task 4 Step 1、Task 5 Step 4。
- ✅ RedisConfig 规范化（host+port/dbIndex/连接池）：Task 1。
- ✅ MySQLConfig 规范化（username/timeout）：Task 2。
- ✅ Agent 字段改名：Task 5 Step 7-10。
- ✅ schema 改名：Task 7。
- ✅ 配置文件改写：Task 8。
- ✅ HistoryStore 共享 db：Task 4 Step 10-13、Task 6。
- ✅ HISTORY_DISABLED 语义：Task 6 Step 1 日志体现（未配 MySQL 时）。
- ✅ 不改的契约（SharedRuntimeAssignment.Redis、CapabilitiesResponse.sharedState）：Task 4 Step 8 保持下发字段名不变、Task 6 Step 6 capabilities 响应字段不变。

**2. Placeholder scan**

- 无 TBD/TODO。
- 每个步骤都有具体代码或具体 grep 命令。
- Task 4 Step 9-12 合并了原 Task 7（HistoryStore 改造），避免中间编译失败态，已在 Step 9 的"决策"段落说明。

**3. Type consistency**

- `RedisConfig.Host`/`Port`/`DBIndex`：Task 1 定义，Task 4/5/6/8 引用一致。
- `MySQLConfig.Username`：Task 2 定义，Task 4/8 引用一致。
- `Config.MySQL *MySQLConfig` / `Config.Redis *sharedstate.RedisConfig`：Task 4 定义，Task 6 引用一致。
- `NewHistoryStore(db *sql.DB, cfg *HistoryConfig)`：Task 4 Step 10 定义，Task 6 Step 1 引用一致。
- `initMySQLSchema(db *sql.DB)`：Task 4 Step 11 定义，Task 6 Step 1 引用一致。
- `agent.Config.AdminUrl`/`HeartbeatInterval`/`MetricsInterval`：Task 5 Step 7 定义，Step 9/10 引用一致。

---

## Execution Handoff

计划完成，保存于 `plans/2026-06-22-config-refactor-plan.md`。两种执行方式：

**1. Subagent-Driven（推荐）** - 每个 Task 派一个全新 subagent，任务间我做 review，迭代快。

**2. Inline Execution** - 本会话内用 executing-plans 批量执行，带检查点。

选哪种？
