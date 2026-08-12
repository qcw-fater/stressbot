package agent

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"stressbot/utils"
)

// Config Agent 节点配置（对应 agent.toml [agent] 段）。
type Config struct {
	ID                  string           `toml:"id"                  json:"id"`                  // 节点 ID（必填，全局唯一）
	AdminAddress        string           `toml:"adminAddress"        json:"adminAddress"`        // Admin 控制面地址（必填，host:port）
	Name                string           `toml:"name"                json:"name"`                // 节点名（留空=hostname）
	MaxBots             int              `toml:"maxBots"             json:"maxBots"`             // 最大机器人数（默认 5000）
	HeartbeatInterval   string           `toml:"heartbeatInterval"   json:"heartbeatInterval"`   // 心跳上报间隔（默认 10s）
	ReconnectMaxRetries int              `toml:"reconnectMaxRetries" json:"reconnectMaxRetries"` // 重连次数（-1=无限，0→-1）
	MetricsInterval     string           `toml:"metricsInterval"     json:"metricsInterval"`     // 指标上报间隔（默认 5s）
	TaskWorkDir         string           `toml:"taskWorkDir"         json:"taskWorkDir"`         // 任务工作目录（留空=系统临时目录）
	Reconnect           ReconnectConfig  `toml:"reconnect"           json:"reconnect"`           // gRPC 重连退避参数
	AppVersion          string           `toml:"-"                   json:"-"`                   // 编译时注入，不来自配置
}

// ReconnectConfig gRPC 重连退避参数（对应 [agent.reconnect] 子表）。
type ReconnectConfig struct {
	InitialInterval   string `toml:"initialInterval"   json:"initialInterval"`   // 初始重连间隔（默认 5s）
	MaxInterval       string `toml:"maxInterval"       json:"maxInterval"`       // 最大重连间隔（指数退避上限，默认 30s）
	TaskReportTimeout string `toml:"taskReportTimeout" json:"taskReportTimeout"` // 任务最终报告确认超时（默认 30s）
}

// ResolvedConfig 已解析（duration 转 time.Duration、填充默认值）的配置。
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

// Defaults 返回填充了默认值的 Agent 配置。
func Defaults() Config {
	return Config{
		MaxBots: 5000,
		Reconnect: ReconnectConfig{
			InitialInterval:   "5s",
			MaxInterval:       "30s",
			TaskReportTimeout: "30s",
		},
	}
}

// Resolve 校验并解析配置，填充默认值。
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
	taskWorkDir := c.TaskWorkDir
	if taskWorkDir == "" {
		taskWorkDir = os.TempDir()
	}
	return &ResolvedConfig{
		ID: c.ID, AdminAddress: c.AdminAddress, Name: name, MaxBots: maxBots, AppVersion: c.AppVersion,
		TaskWorkDir:       taskWorkDir,
		MetricsInterval:   utils.ParseDurationDefault(c.MetricsInterval, 5*time.Second, "agent.metricsInterval"),
		HeartbeatInterval: utils.ParseDurationDefault(c.HeartbeatInterval, 10*time.Second, "agent.heartbeatInterval"),
		ReconnectInterval: utils.ParseDurationDefault(c.Reconnect.InitialInterval, 5*time.Second, "agent.reconnect.initialInterval"),
		ReconnectMaxInterval: utils.ParseDurationDefault(c.Reconnect.MaxInterval, 30*time.Second, "agent.reconnect.maxInterval"),
		ReconnectMaxRetries:  resolveReconnectRetries(c.ReconnectMaxRetries),
		TaskReportTimeout:    utils.ParseDurationDefault(c.Reconnect.TaskReportTimeout, 30*time.Second, "agent.reconnect.taskReportTimeout"),
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
