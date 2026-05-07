package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config Admin 服务端配置。
type Config struct {
	ListenAddr string         `json:"listenAddr"`
	PublicURL  string         `json:"publicUrl"`
	StaticDir  string         `json:"staticDir"`
	DataDir    string         `json:"dataDir"`

	AgentRegistry RegistryConfig `json:"agentRegistry"`
	Task          TaskSection    `json:"task"`
	History       HistoryConfig  `json:"history"`
	Log           LogConfig      `json:"log"`
}

type RegistryConfig struct {
	UnhealthyAfter string `json:"unhealthyAfter"`
	OfflineAfter   string `json:"offlineAfter"`
}

type TaskSection struct {
	MaxFlowSizeMB   int    `json:"maxFlowSizeMB"`
	DeadlineDefault string `json:"deadlineDefault"`
}

type HistoryConfig struct {
	Enabled         bool          `json:"enabled"`
	MySQL           MySQLConfig   `json:"mysql"`
	SamplerInterval string        `json:"samplerInterval"`
	RetentionDays   int           `json:"retentionDays"`
	PruneRunAt      string        `json:"pruneRunAt"`
}

type MySQLConfig struct {
	DSN             string `json:"dsn"`
	MaxOpenConns    int    `json:"maxOpenConns"`
	MaxIdleConns    int    `json:"maxIdleConns"`
	ConnMaxLifetime string `json:"connMaxLifetime"`
}

type LogConfig struct {
	Level      string `json:"level"`
	Path       string `json:"path"`
	MaxSizeMB  int    `json:"maxSizeMB"`
	MaxBackups int    `json:"maxBackups"`
}

// Defaults 返回填充了默认值的配置。
func DefaultConfig() Config {
	return Config{
		ListenAddr: ":8080",
		StaticDir:  "web/dist",
		DataDir:    "data",
		AgentRegistry: RegistryConfig{
			UnhealthyAfter: "30s",
			OfflineAfter:   "60s",
		},
		Task: TaskSection{
			MaxFlowSizeMB:   10,
			DeadlineDefault: "1h",
		},
		History: HistoryConfig{
			Enabled:         true,
			SamplerInterval: "10s",
			RetentionDays:   90,
			PruneRunAt:      "03:00",
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
	if cfg.ListenAddr == "" {
		return fmt.Errorf("listenAddr is required")
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
	return nil
}
