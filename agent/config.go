package agent

import (
	"fmt"
	"os"
	"runtime"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// AgentConfig Agent 配置（JSON 反序列化形态）。
type AgentConfig struct {
	Enabled    bool   `json:"enabled"`
	AdminAddr  string `json:"adminAddr"`
	Name       string `json:"name"`
	ListenAddr string `json:"listenAddr"`
	MaxBots    int    `json:"maxBots"`

	StressInterval string `json:"stressInterval"`
	SystemInterval string `json:"systemInterval"`
	HBInterval     string `json:"heartbeatInterval"`
	HBFailInterval string `json:"heartbeatFailInterval"`

	// 重连/请求超时（v2 配置，统一控制 Agent → Admin 的所有 HTTP 调用）
	RequestTimeout      string `json:"requestTimeout"`      // 单次请求超时；默认 30s
	ReconnectInterval   string `json:"reconnectInterval"`   // 注册重连初始间隔；默认 5s
	ReconnectMaxInterval string `json:"reconnectMaxInterval"` // 重连指数退避上限；默认 60s
	ReconnectMaxRetries int    `json:"reconnectMaxRetries"` // 最大重连次数；-1=持续重连（默认）；0 视为 -1
	TaskReportTimeout   string `json:"taskReportTimeout"`   // 任务完成上报总超时；默认 30s

	// 兼容旧版字段（运行时忽略，启动时仅打 Warn 日志，不再生效）
	MaxHeartbeatFailures     int    `json:"maxHeartbeatFailures"`     // [deprecated]
	TaskRunAdminLostExit     bool   `json:"taskRunAdminLostExit"`     // [deprecated]
	ReconnectEnabled         bool   `json:"reconnectEnabled"`         // [deprecated]
	RegisterRetryMaxInterval string `json:"registerRetryMaxInterval"` // [deprecated] 改为 reconnectMaxInterval

	TaskWorkDir   string `json:"taskWorkDir"`
	AppVersion    string `json:"appVersion"`
	AdapterScript string `json:"adapterScript"`
}

// ResolvedConfig Agent 解析后的运行期配置（所有 duration 已转换）。
type ResolvedConfig struct {
	AdminAddr     string
	Name          string
	ListenAddr    string
	MaxBots       int
	AppVersion    string
	TaskWorkDir   string
	AdapterScript string

	StressInterval time.Duration
	SystemInterval time.Duration
	HBInterval     time.Duration
	HBFailInterval time.Duration

	// 请求/重连策略
	RequestTimeout       time.Duration // 单次 HTTP 请求超时（心跳/上报/任务完成等）
	ReconnectInterval    time.Duration // 注册重连初始间隔
	ReconnectMaxInterval time.Duration // 重连指数退避上限
	ReconnectMaxRetries  int           // -1 = 持续重连
	TaskReportTimeout    time.Duration // 任务完成上报总超时
}

// 默认值（在多处复用，集中定义便于审计）
const (
	DefaultStressInterval       = 5 * time.Second
	DefaultSystemInterval       = 5 * time.Second
	DefaultHBInterval           = 10 * time.Second
	DefaultRequestTimeout       = 30 * time.Second
	DefaultReconnectInterval    = 5 * time.Second
	DefaultReconnectMaxInterval = 60 * time.Second
	DefaultReconnectMaxRetries  = -1 // 持续重连
	DefaultTaskReportTimeout    = 30 * time.Second
)

// Resolve 将 AgentConfig 解析为 ResolvedConfig，填充默认值并校验。
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

	stress := parseDuration(c.StressInterval, DefaultStressInterval, "agent.stressInterval")
	system := parseDuration(c.SystemInterval, DefaultSystemInterval, "agent.systemInterval")
	hb := parseDuration(c.HBInterval, DefaultHBInterval, "agent.heartbeatInterval")
	hbFail := parseDuration(c.HBFailInterval, hb, "agent.heartbeatFailInterval")

	// 兼容旧字段 registerRetryMaxInterval：若新字段为空、旧字段非空，则用旧字段值并打 Warn
	reconnectMaxRaw := c.ReconnectMaxInterval
	if reconnectMaxRaw == "" && c.RegisterRetryMaxInterval != "" {
		stresslog.Warn("[CONFIG] agent.registerRetryMaxInterval 已迁移为 agent.reconnectMaxInterval",
			zap.String("legacy", c.RegisterRetryMaxInterval))
		reconnectMaxRaw = c.RegisterRetryMaxInterval
	}
	reconnectMax := parseDuration(reconnectMaxRaw, DefaultReconnectMaxInterval, "agent.reconnectMaxInterval")
	reconnectInit := parseDuration(c.ReconnectInterval, DefaultReconnectInterval, "agent.reconnectInterval")
	if reconnectInit > reconnectMax {
		stresslog.Warn("[CONFIG] agent.reconnectInterval > reconnectMaxInterval，截断到上限",
			zap.Duration("interval", reconnectInit),
			zap.Duration("max", reconnectMax))
		reconnectInit = reconnectMax
	}

	requestTimeout := parseDuration(c.RequestTimeout, DefaultRequestTimeout, "agent.requestTimeout")
	taskReportTimeout := parseDuration(c.TaskReportTimeout, DefaultTaskReportTimeout, "agent.taskReportTimeout")

	// reconnectMaxRetries: 0 视为未配置 → 走默认（-1 持续重连）
	maxRetries := c.ReconnectMaxRetries
	if maxRetries == 0 {
		maxRetries = DefaultReconnectMaxRetries
		stresslog.Warn("[CONFIG] agent.reconnectMaxRetries 为空，使用默认值",
			zap.Int("default", maxRetries),
			zap.String("note", "-1 表示持续重连"))
	}

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

	// 废弃配置告警（保留旧字段解析以兼容现网配置，但运行时不再生效）
	if c.MaxHeartbeatFailures > 0 {
		stresslog.Warn("[CONFIG] agent.maxHeartbeatFailures 已废弃，心跳失败不再退出进程",
			zap.Int("configured", c.MaxHeartbeatFailures))
	}
	if c.TaskRunAdminLostExit {
		stresslog.Warn("[CONFIG] agent.taskRunAdminLostExit 已废弃，心跳失败时自动取消任务",
			zap.Bool("configured", c.TaskRunAdminLostExit))
	}
	if c.ReconnectEnabled {
		stresslog.Warn("[CONFIG] agent.reconnectEnabled 已废弃，Agent 始终保持重连",
			zap.Bool("configured", c.ReconnectEnabled))
	}

	return &ResolvedConfig{
		AdminAddr:            c.AdminAddr,
		Name:                 name,
		ListenAddr:           listen,
		MaxBots:              maxBots,
		AppVersion:           c.AppVersion,
		TaskWorkDir:          workDir,
		AdapterScript:        adapterScript,
		StressInterval:       stress,
		SystemInterval:       system,
		HBInterval:           hb,
		HBFailInterval:       hbFail,
		RequestTimeout:       requestTimeout,
		ReconnectInterval:    reconnectInit,
		ReconnectMaxInterval: reconnectMax,
		ReconnectMaxRetries:  maxRetries,
		TaskReportTimeout:    taskReportTimeout,
	}, nil
}

// CollectStaticInfo 采集本机静态信息。
func CollectStaticInfo() StaticInfo {
	hostname, _ := os.Hostname()

	// 从 runtime 读取可用内存信息（近似）
	// gopsutil 在 sysmon 中采集更精确的值，这里用 runtime 做基础值
	var memTotalMB uint64

	return StaticInfo{
		Hostname:   hostname,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		NumCPU:     runtime.NumCPU(),
		MemTotalMB: memTotalMB,
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
