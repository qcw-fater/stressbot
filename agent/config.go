package agent

import (
	"fmt"
	"os"
	"runtime"
	"time"
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

	RegisterRetryMaxInterval string `json:"registerRetryMaxInterval"`
	TaskWorkDir               string `json:"taskWorkDir"`
	AppVersion                string `json:"appVersion"`
	AdapterScript             string `json:"adapterScript"`
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
	RegisterRetryMax time.Duration
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
	}

	listen := c.ListenAddr
	if listen == "" {
		listen = ":7070"
	}

	maxBots := c.MaxBots
	if maxBots <= 0 {
		maxBots = 5000
	}

	stress := parseDuration(c.StressInterval, 5*time.Second)
	system := parseDuration(c.SystemInterval, 5*time.Second)
	hb := parseDuration(c.HBInterval, 10*time.Second)
	retryMax := parseDuration(c.RegisterRetryMaxInterval, 60*time.Second)

	// 心跳间隔必须远小于 Admin 的 unhealthy 阈值（通常 30s）
	if hb >= 25*time.Second {
		return nil, fmt.Errorf("agent.heartbeatInterval (%s) 必须 < 25s（Admin unhealthy 阈值通常为 30s）", hb)
	}

	workDir := c.TaskWorkDir
	if workDir == "" {
		workDir = os.TempDir()
	}

	adapterScript := c.AdapterScript
	if adapterScript == "" {
		adapterScript = "conf/adapter/codec.lua"
	}

	return &ResolvedConfig{
		AdminAddr:       c.AdminAddr,
		Name:            name,
		ListenAddr:      listen,
		MaxBots:         maxBots,
		AppVersion:      c.AppVersion,
		TaskWorkDir:     workDir,
		AdapterScript:   adapterScript,
		StressInterval:  stress,
		SystemInterval:  system,
		HBInterval:      hb,
		RegisterRetryMax: retryMax,
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

func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
