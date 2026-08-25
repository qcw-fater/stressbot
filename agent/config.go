package agent

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"stressbot/agent/metrics"
	"stressbot/config"
	"stressbot/internal/stresslog"
	"stressbot/monitor"
)

// AppConfig 是 agent.toml 的完整配置；Config 仅对应其中的 [agent] 段。
type AppConfig struct {
	Log     *stresslog.Config        `toml:"log"`
	Monitor *monitor.CollectorConfig `toml:"monitor"`
	Pprof   *config.PprofConfig      `toml:"pprof"`
	Agent   Config                   `toml:"agent"`
	Daemon  bool                     `toml:"daemon"`
}

func appDefaults() AppConfig {
	return AppConfig{Agent: Defaults()}
}

// LoadConfig 加载 Agent 进程配置，并注入构建版本。
func LoadConfig(path, version string) (*AppConfig, error) {
	cfg, err := config.LoadTOML(path, appDefaults())
	if err != nil {
		return nil, err
	}
	cfg.Agent.AppVersion = version
	return cfg, nil
}

// NewFromConfig 完成 Agent 应用所需的配置解析与指标初始化。
func NewFromConfig(cfg *AppConfig) (*Agent, error) {
	if cfg == nil {
		return nil, errors.New("Agent 配置不能为空")
	}
	resolved, err := cfg.Agent.Resolve()
	if err != nil {
		return nil, fmt.Errorf("Agent 配置校验失败: %w", err)
	}
	monitorConfig := monitor.CollectorConfig{}
	if cfg.Monitor != nil {
		monitorConfig = *cfg.Monitor
	}
	monitor.Init(monitorConfig)
	monitor.RegisterHandlers(monitor.Global())
	return New(resolved, monitor.Global())
}

// Config Agent 节点配置（对应 agent.toml [agent] 段）。
type Config struct {
	ID                  string          `toml:"id"                  json:"id"`                  // 节点 ID（必填，全局唯一）
	AdminAddress        string          `toml:"adminAddress"        json:"adminAddress"`        // Admin 控制面地址（必填，host:port）
	Name                string          `toml:"name"                json:"name"`                // 节点名（留空=hostname）
	MaxBots             int             `toml:"maxBots"             json:"maxBots"`             // 最大机器人数（默认 5000）
	HeartbeatInterval   string          `toml:"heartbeatInterval"   json:"heartbeatInterval"`   // 心跳上报间隔（默认 10s）
	ReconnectMaxRetries int             `toml:"reconnectMaxRetries" json:"reconnectMaxRetries"` // 重连次数（-1=无限，0→-1）
	MetricsInterval     string          `toml:"metricsInterval"     json:"metricsInterval"`     // 指标上报间隔（默认 5s）
	TaskWorkDir         string          `toml:"taskWorkDir"         json:"taskWorkDir"`         // 任务工作目录（留空=系统临时目录）
	Reconnect           ReconnectConfig `toml:"reconnect"           json:"reconnect"`           // gRPC 重连退避参数
	AppVersion          string          `toml:"-"                   json:"-"`                   // 编译时注入，不来自配置
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
		return nil, errors.New("agent.id 不能为空")
	}
	if c.AdminAddress == "" {
		return nil, errors.New("agent.adminAddress 不能为空")
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
		TaskWorkDir:          taskWorkDir,
		MetricsInterval:      config.ParseDurationDefault(c.MetricsInterval, 5*time.Second, "agent.metricsInterval"),
		HeartbeatInterval:    config.ParseDurationDefault(c.HeartbeatInterval, 10*time.Second, "agent.heartbeatInterval"),
		ReconnectInterval:    config.ParseDurationDefault(c.Reconnect.InitialInterval, 5*time.Second, "agent.reconnect.initialInterval"),
		ReconnectMaxInterval: config.ParseDurationDefault(c.Reconnect.MaxInterval, 30*time.Second, "agent.reconnect.maxInterval"),
		ReconnectMaxRetries:  resolveReconnectRetries(c.ReconnectMaxRetries),
		TaskReportTimeout:    config.ParseDurationDefault(c.Reconnect.TaskReportTimeout, 30*time.Second, "agent.reconnect.taskReportTimeout"),
	}, nil
}

func resolveReconnectRetries(value int) int {
	if value == 0 {
		return -1
	}
	return value
}

// CollectStaticInfo 采集节点静态信息：主机名、OS/架构/CPU 核数、Go 版本与进程启动时间。
func CollectStaticInfo() metrics.StaticInfo {
	hostname, _ := os.Hostname()
	return metrics.StaticInfo{Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, NumCPU: runtime.NumCPU(), GoVersion: runtime.Version(), StartedAt: time.Now()}
}
