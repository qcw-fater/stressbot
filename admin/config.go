package admin

import (
	"fmt"
	"os"
	"time"

	"stressbot/sharedstate"
	json "stressbot/utils/jsonx"
)

// Config Admin 服务端配置。
type Config struct {
	Port      int    `json:"port"`      // HTTP 监听端口（默认 7718）
	PublicURL string `json:"publicUrl"` // 外部可达 URL（Agent 用来连接 Admin，如 http://192.168.1.100:7718）
	StaticDir string `json:"staticDir"` // 前端静态文件目录（默认 cmd/web/dist）

	AgentRegistry RegistryConfig      `json:"agentRegistry"` // Agent 注册与健康管理
	History       HistoryConfig       `json:"history"`       // 历史归档
	Log           LogConfig           `json:"log"`           // 日志
	Pprof         PprofConfig         `json:"pprof"`         // pprof 调试服务
	Shared        *sharedstate.Config `json:"shared"`        // 共享状态（Redis）配置，下发给 Agent
	Daemon        bool                `json:"daemon"`        // 以守护进程模式运行（仅 Linux）
}

// SharedEnabled 返回服务器是否配置了共享状态（Redis 地址非空）。
func (c *Config) SharedEnabled() bool {
	return c.Shared != nil && c.Shared.Redis.Enabled()
}

// PprofConfig pprof 调试服务配置。
type PprofConfig struct {
	Enabled bool `json:"enabled"` // 是否启用 pprof（默认 false）
	Port    int  `json:"port"`    // pprof 监听端口（默认 6060）
}

// RegistryConfig Agent 注册与健康管理配置。
//
// 联动约束（与 agent.hbFailThreshold × agent.hbInterval）：
//   - UnhealthyAfter 必须 ≥ agent 端容忍窗口（默认 3 × 10s = 30s）
//   - OfflineAfter   必须 > UnhealthyAfter
//
// 否则会出现"admin 已把节点标 unhealthy/删除，但 agent 任务还在跑"的状态错乱。
type RegistryConfig struct {
	UnhealthyAfter string `json:"unhealthyAfter"` // 心跳超时后标记 unhealthy
	OfflineAfter   string `json:"offlineAfter"`   // 超过此时间标记 offline 并删除
}

// HistoryConfig 历史归档配置。
type HistoryConfig struct {
	Enabled       bool        `json:"enabled"`       // 是否启用 MySQL 历史归档
	MySQL         MySQLConfig `json:"mysql"`         // MySQL 连接配置
	RetentionDays int         `json:"retentionDays"` // 历史数据保留天数（默认 90）
}

// MySQLConfig MySQL 连接配置。
type MySQLConfig struct {
	Host            string `json:"host"`            // 主机地址
	Port            int    `json:"port"`            // 端口号（默认 3306）
	User            string `json:"user"`            // 用户名
	Password        string `json:"password"`        // 密码
	Database        string `json:"database"`        // 数据库名
	MaxOpenConns    int    `json:"maxOpenConns"`    // 最大打开连接数
	MaxIdleConns    int    `json:"maxIdleConns"`    // 最大空闲连接数
	ConnMaxLifetime string `json:"connMaxLifetime"` // 连接最大存活时间
}

// DSN 拼接标准 MySQL 连接字符串。
func (c MySQLConfig) DSN() string {
	port := c.Port
	if port == 0 {
		port = 3306
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&loc=Local",
		c.User, c.Password, c.Host, port, c.Database)
}

// LogConfig 日志配置。
type LogConfig struct {
	Level      string `json:"level"`      // 日志级别（debug/info/warn/error）
	Path       string `json:"path"`       // 日志文件路径
	MaxSizeMB  int    `json:"maxSizeMB"`  // 单个日志文件最大体积（MB）
	MaxBackups int    `json:"maxBackups"` // 保留的旧日志文件数
}

// DefaultConfig 返回填充了默认值的配置。
func DefaultConfig() Config {
	return Config{
		Port:      7718,
		StaticDir: "cmd/web/dist",
		AgentRegistry: RegistryConfig{
			UnhealthyAfter: "30s",
			OfflineAfter:   "60s",
		},
		History: HistoryConfig{
			Enabled:       true,
			RetentionDays: 90,
			MySQL: MySQLConfig{
				MaxOpenConns:    10,
				MaxIdleConns:    5,
				ConnMaxLifetime: "1h",
			},
		},
		Log: LogConfig{
			Level:      "info",
			Path:       "log/admin.log",
			MaxSizeMB:  100,
			MaxBackups: 10,
		},
	}
}

// LoadConfig 从文件加载配置。
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

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
	if cfg.SharedEnabled() {
		if _, err := cfg.Shared.Redis.Resolve(); err != nil {
			return fmt.Errorf("invalid shared config: %w", err)
		}
	}
	return nil
}
