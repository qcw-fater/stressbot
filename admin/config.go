package admin

import (
	"fmt"
	"os"
	"strings"
	"time"

	"stressbot/sharedstate"
	stresslog "stressbot/utils/log"
	json "stressbot/utils/jsonx"
)

// Config Admin 服务端配置。
type Config struct {
	Port          int                      `json:"port"`          // HTTP 监听端口（默认 7718）
	PublicURL     string                   `json:"publicUrl"`     // 外部可达 URL（Agent 用来连接 Admin，如 http://192.168.1.100:7718）
	StaticDir     string                   `json:"staticDir"`     // 前端静态文件目录（默认 cmd/web/dist）
	AgentRegistry RegistryConfig           `json:"agentRegistry"` // Agent 注册与健康管理
	MySQL         *MySQLConfig             `json:"mysql"`         // MySQL 连接配置（全局共享 *sql.DB）
	Redis         *sharedstate.RedisConfig `json:"redis"`         // Redis 配置（host 非空时启用）
	History       *HistoryConfig           `json:"history"`       // 历史归档（仅 RetentionDays）
	Log           stresslog.Config         `json:"log"`           // 日志
	Pprof         *PprofConfig             `json:"pprof"`         // pprof 调试服务（nil 时不启用）
	Daemon        bool                     `json:"daemon"`        // 以守护进程模式运行（仅 Linux）
}

// RedisEnabled 返回服务器是否配置了 Redis（host 非空）。
func (c *Config) RedisEnabled() bool {
	return c.Redis != nil && c.Redis.Enabled()
}

// PprofConfig pprof 调试服务配置（Config.Pprof 为 nil 时不启用）。
type PprofConfig struct {
	Port int `json:"port"` // pprof 监听端口（默认 6060）
}

// RegistryConfig Agent 注册与健康管理配置。
//
// 联动约束（与 agent.heartbeatFailThreshold × agent.heartbeatInterval）：
//   - UnhealthyAfter 必须 ≥ agent 端容忍窗口（默认 3 × 10s = 30s）
//   - OfflineAfter   必须 > UnhealthyAfter
//
// 否则会出现"admin 已把节点标 unhealthy/删除，但 agent 任务还在跑"的状态错乱。
type RegistryConfig struct {
	UnhealthyAfter string `json:"unhealthyAfter"` // 心跳超时后标记 unhealthy
	OfflineAfter   string `json:"offlineAfter"`   // 超过此时间标记 offline 并删除
}

// HistoryConfig 历史归档配置（MySQL 已提升为 Config.MySQL）。
type HistoryConfig struct {
	RetentionDays int `json:"retentionDays"` // 历史数据保留天数（默认 90）
}

// MySQLConfig MySQL 连接配置。
type MySQLConfig struct {
	Host            string `json:"host"`            // 主机地址
	Port            int    `json:"port"`            // 端口号（默认 3306）
	Username        string `json:"username"`        // 用户名
	Password        string `json:"password"`        // 密码
	Database        string `json:"database"`        // 数据库名
	DialTimeout     string `json:"dialTimeout"`     // 拨号超时（DSN timeout）
	ReadTimeout     string `json:"readTimeout"`     // 读超时（DSN readTimeout）
	WriteTimeout    string `json:"writeTimeout"`    // 写超时（DSN writeTimeout）
	MaxOpenConns    int    `json:"maxOpenConns"`    // 最大打开连接数
	MaxIdleConns    int    `json:"maxIdleConns"`    // 最大空闲连接数
	ConnMaxLifetime string `json:"connMaxLifetime"` // 连接最大存活时间
}

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

// DefaultConfig 返回填充了默认值的配置。
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
			Path:       "log/admin.log",
			LogLevel:   "info",
			MaxSize:    100,
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
	if cfg.RedisEnabled() {
		if _, err := cfg.Redis.Resolve(); err != nil {
			return fmt.Errorf("invalid redis config: %w", err)
		}
	}
	// MySQL 连通性校验推迟到装配阶段（NewAdminServer 里 openDB + ping）。
	return nil
}
