package agent

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"stressbot/utils"
)

type Config struct {
	Enabled             bool   `json:"enabled"`
	ID                  string `json:"id"`
	AdminAddress        string `json:"adminAddress"`
	Name                string `json:"name"`
	MaxBots             int    `json:"maxBots"`
	HeartbeatInterval   string `json:"heartbeatInterval"`
	ReconnectMaxRetries int    `json:"reconnectMaxRetries"`
	MetricsInterval     string `json:"metricsInterval"`
	AppVersion          string `json:"-"`
}

type ResolvedConfig struct {
	ID                   string
	AdminAddress         string
	Name                 string
	MaxBots              int
	AppVersion           string
	TaskWorkDir          string
	MetricsInterval      time.Duration
	HeartbeatInterval    time.Duration
	ReconnectInterval    time.Duration
	ReconnectMaxInterval time.Duration
	ReconnectMaxRetries  int
	TaskReportTimeout    time.Duration
}

func (c *Config) Resolve() (*ResolvedConfig, error) {
	if c.ID == "" {
		return nil, fmt.Errorf("agent.id 不能为空")
	}
	if c.AdminAddress == "" {
		return nil, fmt.Errorf("agent.adminAddress 不能为空")
	}
	if _, _, err := net.SplitHostPort(c.AdminAddress); err != nil {
		return nil, fmt.Errorf("agent.adminAddress 必须是 host:port: %w", err)
	}
	hostname, _ := os.Hostname()
	name := c.Name
	if name == "" {
		name = hostname
	}
	if name == "" {
		name = c.ID
	}
	maxBots := c.MaxBots
	if maxBots <= 0 {
		maxBots = 5000
	}
	return &ResolvedConfig{
		ID: c.ID, AdminAddress: c.AdminAddress, Name: name, MaxBots: maxBots, AppVersion: c.AppVersion,
		TaskWorkDir:       os.TempDir(),
		MetricsInterval:   utils.ParseDurationDefault(c.MetricsInterval, 5*time.Second, "agent.metricsInterval"),
		HeartbeatInterval: utils.ParseDurationDefault(c.HeartbeatInterval, 10*time.Second, "agent.heartbeatInterval"),
		ReconnectInterval: 5 * time.Second, ReconnectMaxInterval: 30 * time.Second,
		ReconnectMaxRetries: resolveReconnectRetries(c.ReconnectMaxRetries), TaskReportTimeout: 30 * time.Second,
	}, nil
}

func resolveReconnectRetries(value int) int {
	if value == 0 {
		return -1
	}
	return value
}

func CollectStaticInfo() StaticInfo {
	hostname, _ := os.Hostname()
	return StaticInfo{Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, NumCPU: runtime.NumCPU(), GoVersion: runtime.Version(), StartedAt: time.Now()}
}
