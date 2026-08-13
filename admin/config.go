package admin

import (
	"fmt"
	"strings"
	"time"

	"stressbot/config"
	json "stressbot/internal/jsonx"
	"stressbot/internal/stresslog"
	"stressbot/state/shared"
)

// Config Admin 服务端配置（对应 admin.toml）。
type Config struct {
	Server       ServerConfig        `toml:"server"       json:"server"`       // 管理面（HTTP，面向浏览器）
	ControlPlane ControlPlaneConfig  `toml:"controlPlane" json:"controlPlane"` // 控制面（gRPC，面向 Agent）
	MySQL        *MySQLConfig        `toml:"mysql"        json:"mysql"`        // MySQL 历史归档（nil=不启用）
	Redis        *shared.RedisConfig `toml:"redis"        json:"redis"`        // Redis 共享状态（nil=不启用）
	Log          stresslog.Config    `toml:"log"          json:"log"`          // 日志
	Pprof        *config.PprofConfig `toml:"pprof"        json:"pprof"`        // pprof 调试（nil=不启用）
	Daemon       bool                `toml:"daemon"       json:"daemon"`       // 守护进程模式（仅 Linux）
}

// ServerConfig 管理面（HTTP，面向浏览器）配置。
type ServerConfig struct {
	Port       int    `toml:"port"       json:"port"`       // HTTP 监听端口（默认 7718）
	ListenHost string `toml:"listenHost" json:"listenHost"` // 监听地址（默认 127.0.0.1）
	StaticDir  string `toml:"staticDir"  json:"staticDir"`  // 前端静态文件目录（默认 cmd/web/dist）
}

// ControlPlaneConfig 控制面（gRPC，面向 Agent 节点）配置。
//
// 联动约束（与 Agent 心跳和租约）：
//   - HeartbeatInterval 是期望的 Agent 心报间隔，下发给 Agent
//   - UnhealthyAfter 同时作为任务租约时长，必须明显大于 HeartbeatInterval
//   - OfflineAfter 必须 > UnhealthyAfter
//
// 否则会出现「Admin 已把节点标 unhealthy/删除，但 Agent 任务还在跑」的状态错乱。
type ControlPlaneConfig struct {
	ListenHost        string `toml:"listenHost"        json:"listenHost"`        // gRPC 监听地址（默认 127.0.0.1）
	Port              int    `toml:"port"              json:"port"`              // gRPC 监听端口（默认 7720）
	HeartbeatInterval string `toml:"heartbeatInterval" json:"heartbeatInterval"` // 期望 Agent 心跳间隔（默认 10s）
	UnhealthyAfter    string `toml:"unhealthyAfter"    json:"unhealthyAfter"`    // 心跳超时后标记 unhealthy（默认 30s，同时=任务租约时长）
	OfflineAfter      string `toml:"offlineAfter"      json:"offlineAfter"`      // 超过此时间标记 offline 并删除（默认 60s）
}

// MySQLConfig MySQL 连接配置（对应 [mysql] 段；host 留空=不启用）。
// retentionDays 从原 [history] 段并入——历史归档强依赖 MySQL。
type MySQLConfig struct {
	Host          string                `toml:"host"          json:"host"`          // 主机地址
	Port          int                   `toml:"port"          json:"port"`          // 端口号（默认 3306）
	Username      string                `toml:"username"      json:"username"`      // 用户名
	Password      string                `toml:"password"      json:"password"`      // 密码
	Database      string                `toml:"database"      json:"database"`      // 数据库名
	RetentionDays int                   `toml:"retentionDays" json:"retentionDays"` // 历史数据保留天数（默认 90）
	Pool          config.ConnPoolConfig `toml:"pool"       json:"pool"`             // 连接池参数
}

// DSN 拼接标准 MySQL 连接字符串，含 timeout 参数。
func (c MySQLConfig) DSN() string {
	port := c.Port
	if port == 0 {
		port = 3306
	}
	// timeout 参数：各字段未配则用驱动默认（不写进 DSN）。
	var params []string
	if c.Pool.DialTimeout != "" {
		params = append(params, "timeout="+c.Pool.DialTimeout)
	}
	if c.Pool.ReadTimeout != "" {
		params = append(params, "readTimeout="+c.Pool.ReadTimeout)
	}
	if c.Pool.WriteTimeout != "" {
		params = append(params, "writeTimeout="+c.Pool.WriteTimeout)
	}
	extra := "parseTime=true&loc=Local"
	if len(params) > 0 {
		extra += "&" + strings.Join(params, "&")
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?%s",
		c.Username, c.Password, c.Host, port, c.Database, extra)
}

// RedisEnabled 返回服务器是否配置了 Redis（host 非空）。
func (c *Config) RedisEnabled() bool {
	return c.Redis != nil && c.Redis.Enabled()
}

// MySQLEnabled 返回服务器是否配置了 MySQL；host 为空时明确禁用。
func (c *Config) MySQLEnabled() bool {
	return c.MySQL != nil && strings.TrimSpace(c.MySQL.Host) != ""
}

// Defaults 返回填充了默认值的 Admin 配置。
func Defaults() Config {
	return Config{
		Server: ServerConfig{
			Port:       7718,
			ListenHost: "127.0.0.1",
			StaticDir:  "cmd/web/dist",
		},
		ControlPlane: ControlPlaneConfig{
			ListenHost:        "127.0.0.1",
			Port:              7720,
			HeartbeatInterval: "10s",
			UnhealthyAfter:    "30s",
			OfflineAfter:      "60s",
		},
		MySQL: &MySQLConfig{
			RetentionDays: 90,
			Pool: config.ConnPoolConfig{
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: "1h",
				DialTimeout:     "5s",
				ReadTimeout:     "30s",
				WriteTimeout:    "30s",
			},
		},
		Log: stresslog.Config{
			Path:       "log/admin.log",
			LogLevel:   "info",
			MaxSize:    100,
			MaxBackups: 10,
			MaxAge:     30,
		},
	}
}

// LoadConfig 从 TOML 文件加载配置。
func LoadConfig(path string) (*Config, error) {
	cfg, err := config.LoadTOML(path, Defaults())
	if err != nil {
		return nil, err
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validateConfig 校验配置合法性和联动约束。
func validateConfig(cfg *Config) error {
	if cfg.Server.Port <= 0 {
		return fmt.Errorf("server.port is required and must be > 0")
	}
	if cfg.ControlPlane.Port <= 0 {
		return fmt.Errorf("controlPlane.port is required and must be > 0")
	}
	if cfg.Server.ListenHost == "" {
		return fmt.Errorf("server.listenHost is required")
	}
	if cfg.ControlPlane.ListenHost == "" {
		return fmt.Errorf("controlPlane.listenHost is required")
	}
	heartbeat, err := time.ParseDuration(cfg.ControlPlane.HeartbeatInterval)
	if err != nil {
		return fmt.Errorf("invalid controlPlane.heartbeatInterval: %w", err)
	}
	if heartbeat <= 0 {
		return fmt.Errorf("controlPlane.heartbeatInterval must be > 0")
	}
	lease, err := time.ParseDuration(cfg.ControlPlane.UnhealthyAfter)
	if err != nil {
		return fmt.Errorf("invalid controlPlane.unhealthyAfter: %w", err)
	}
	if lease <= heartbeat {
		return fmt.Errorf("controlPlane.unhealthyAfter must be greater than controlPlane.heartbeatInterval")
	}
	offline, err := time.ParseDuration(cfg.ControlPlane.OfflineAfter)
	if err != nil {
		return fmt.Errorf("invalid controlPlane.offlineAfter: %w", err)
	}
	if offline <= lease {
		return fmt.Errorf("controlPlane.offlineAfter must be greater than controlPlane.unhealthyAfter")
	}
	if cfg.RedisEnabled() {
		if _, err := cfg.Redis.Resolve(); err != nil {
			return fmt.Errorf("invalid redis config: %w", err)
		}
	}
	// MySQL 连通性校验推迟到装配阶段（NewAdminServer 里 openDB + ping）。
	return nil
}

// 用于测试中直接用 JSON 构造配置的场景（历史归档 JSON 序列化等仍用 sonic）。
// 生产加载走 LoadConfig（TOML）。
var _ = json.Unmarshal
