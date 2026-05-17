package agent

import (
	"fmt"
	"os"
	"runtime"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// AgentConfig Agent 配置。
type AgentConfig struct {
	Enabled     bool   `json:"enabled"`
	AdminAddr   string `json:"adminAddr"`
	Name        string `json:"name"`
	ListenAddr  string `json:"listenAddr"`
	MaxBots     int    `json:"maxBots"`

	StressInterval string `json:"stressInterval"`
	SystemInterval string `json:"systemInterval"`
	HBInterval     string `json:"heartbeatInterval"`
	HBFailInterval string `json:"heartbeatFailInterval"` // 心跳失败时的重试间隔，默认与 heartbeatInterval 相同

	RegisterRetryMaxInterval string `json:"registerRetryMaxInterval"`

	// Admin 断连退出策略
	MaxHeartbeatFailures int  `json:"maxHeartbeatFailures"`       // 连续心跳失败次数阈值，达到后停止任务。0=永不停止
	TaskRunAdminLostExit bool `json:"taskRunAdminLostExit"` // 任务运行中与 Admin 断联时停止当前任务
	ReconnectEnabled     bool `json:"reconnectEnabled"`           // Admin 断联后是否保持进程重连。false=停止后退出进程 // 任务运行中与 Admin 断联时立即退出

	TaskWorkDir   string `json:"taskWorkDir"`
	AppVersion    string `json:"appVersion"`
	AdapterScript string `json:"adapterScript"`
}

// ResolvedConfig 解析后的 Agent 配置（所有 Duration 已转换）。
type ResolvedConfig struct {
	AdminAddr       string
	Name            string
	ListenAddr      string
	MaxBots         int
	AppVersion      string
	TaskWorkDir     string
	AdapterScript   string

	StressInterval time.Duration
	SystemInterval time.Duration
	HBInterval     time.Duration
	HBFailInterval time.Duration // 心跳失败时的重试间隔
	RegisterRetryMax time.Duration

	// Admin 断连退出策略
	MaxHeartbeatFailures int
	TaskRunAdminLostExit bool
	ReconnectEnabled     bool
}

// Resolve 将原始 AgentConfig 解析为 ResolvedConfig，填充默认值并校验。
func (c *AgentConfig) Resolve() (*ResolvedConfig, error) {
	if c.AdminAddr == "" {
		return nil, fmt.Errorf("agent.adminAddr 不能为空")
	}

	name := c.Name
	if name == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "unknown"
		}
		name = hostname
		stresslog.Warn("[CONFIG] agent.name 为空，使用主机名", zap.String("default", name))
	}

	listen := c.ListenAddr
	if listen == "" {
		listen = ":7070"
		stresslog.Warn("[CONFIG] agent.listenAddr 为空，使用默认值", zap.String("default", listen))
	}

	maxBots := c.MaxBots
	if maxBots <= 0 {
		maxBots = 5000
		stresslog.Warn("[CONFIG] agent.maxBots 非法，使用默认值", zap.Int("default", maxBots))
	}

	stress := parseDuration(c.StressInterval, 5*time.Second, "agent.stressInterval")
	system := parseDuration(c.SystemInterval, 5*time.Second, "agent.systemInterval")
	hb := parseDuration(c.HBInterval, 10*time.Second, "agent.heartbeatInterval")
	hbFail := parseDuration(c.HBFailInterval, hb, "agent.heartbeatFailInterval")
	retryMax := parseDuration(c.RegisterRetryMaxInterval, 60*time.Second, "agent.registerRetryMaxInterval")

	// 心跳间隔必须远小于 Admin 的 unhealthy 阈值（通常 30s）
	if hb >= 25*time.Second {
		return nil, fmt.Errorf("agent.heartbeatInterval (%s) 必须 < 25s（Admin unhealthy 阈值通常为 30s）", hb)
	}

	workDir := c.TaskWorkDir
	if workDir == "" {
		workDir = os.TempDir()
		stresslog.Warn("[CONFIG] agent.taskWorkDir 为空，使用系统临时目录", zap.String("default", workDir))
	}

	adapterScript := c.AdapterScript
	if adapterScript == "" {
		adapterScript = "conf/adapter/codec.lua"
		stresslog.Warn("[CONFIG] agent.adapterScript 为空，使用默认值", zap.String("default", adapterScript))
	}

	return &ResolvedConfig{
		AdminAddr:       c.AdminAddr,
		Name:            name,
		ListenAddr:      listen,
		MaxBots:         maxBots,
		AppVersion:      c.AppVersion,
		TaskWorkDir:     workDir,
		AdapterScript:   adapterScript,
		StressInterval:       stress,
		SystemInterval:       system,
		HBInterval:           hb,
		HBFailInterval:       hbFail,
		RegisterRetryMax:     retryMax,
		MaxHeartbeatFailures: c.MaxHeartbeatFailures,
		TaskRunAdminLostExit: c.TaskRunAdminLostExit,
	}, nil
}

// CollectStaticInfo 采集本机静态信息。
func CollectStaticInfo() StaticInfo {
	hostname, _ := os.Hostname()

	var memTotalMB uint64
	// 从 runtime 读取可用内存信息（近似）
	// gopsutil 在 sysmon 中采集更精确的值，这里用 runtime 做基础值

	return StaticInfo{
		Hostname:   hostname,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		NumCPU:     runtime.NumCPU(),
		MemTotalMB: memTotalMB, // sysmon 初始化后会更新
		GoVersion:  runtime.Version(),
		StartedAt:  time.Now(),
	}
}

func parseDuration(s string, fallback time.Duration, label string) time.Duration {
	if s == "" {
		stresslog.Warn("[CONFIG] 配置为空，使用默认值",
			zap.String("key", label),
			zap.String("default", fallback.String()))
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		stresslog.Warn("[CONFIG] 配置非法，使用默认值",
			zap.String("key", label),
			zap.String("value", s),
			zap.String("default", fallback.String()),
			zap.NamedError("parseError", err))
		return fallback
	}
	return d
}
