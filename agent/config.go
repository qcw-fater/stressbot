package agent

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// AgentConfig Agent 配置（JSON 反序列化形态）。
// 仅保留用户需要关心的字段，其余内部参数硬编码在 Resolve() 中。
type AgentConfig struct {
	Enabled    bool   `json:"enabled"`   // 是否启用 Agent 模式
	AdminAddr  string `json:"adminAddr"` // Admin 服务器地址（如 http://192.168.1.100:8080）
	Port       int    `json:"port"`      // 本地 HTTP 监听端口（默认 7070，Admin 通过此端口回调 Agent）
	MaxBots    int    `json:"maxBots"`   // 单节点最大机器人数量（默认 5000）
	AppVersion string `json:"-"`         // 编译时注入，不从 JSON 读取
}

// ResolvedConfig Agent 运行期配置（所有参数已解析为最终值）。
type ResolvedConfig struct {
	AdminAddr            string        // Admin 服务器地址
	Name                 string        // 节点名称（主机名）
	Port                 int           // 本地 HTTP 监听端口
	MaxBots              int           // 单节点最大机器人数量
	AppVersion           string        // 应用版本号
	TaskWorkDir          string        // 任务工作目录（系统临时目录）
	AdapterScript        string        // 协议适配器脚本路径
	StressInterval       time.Duration // 压力指标上报间隔
	SystemInterval       time.Duration // 系统指标上报间隔
	HBInterval           time.Duration // 心跳发送间隔
	HBFailInterval       time.Duration // 心跳失败后重试间隔
	RequestTimeout       time.Duration // 单次 HTTP 请求超时
	ReconnectInterval    time.Duration // 注册重连初始间隔
	ReconnectMaxInterval time.Duration // 重连指数退避上限
	ReconnectMaxRetries  int           // 最大重连次数（-1=持续重连）
	TaskReportTimeout    time.Duration // 任务完成上报总超时
}

// Resolve 将 AgentConfig 解析为 ResolvedConfig，校验必填项。
func (c *AgentConfig) Resolve() (*ResolvedConfig, error) {
	if c.AdminAddr == "" {
		return nil, fmt.Errorf("agent.adminAddr 不能为空")
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	maxBots := c.MaxBots
	if maxBots <= 0 {
		maxBots = 5000
	}

	port := c.Port
	if port <= 0 {
		port = 7070
	}

	return &ResolvedConfig{
		AdminAddr:            c.AdminAddr,
		Name:                 hostname,
		Port:                 port,
		MaxBots:              maxBots,
		AppVersion:           c.AppVersion,
		TaskWorkDir:          os.TempDir(),
		AdapterScript:        "conf/adapter/codec.lua",
		StressInterval:       5 * time.Second,
		SystemInterval:       5 * time.Second,
		HBInterval:           10 * time.Second,
		HBFailInterval:       10 * time.Second,
		RequestTimeout:       30 * time.Second,
		ReconnectInterval:    5 * time.Second,
		ReconnectMaxInterval: 60 * time.Second,
		ReconnectMaxRetries:  -1,
		TaskReportTimeout:    30 * time.Second,
	}, nil
}

// CollectStaticInfo 采集本机静态信息。
func CollectStaticInfo() StaticInfo {
	hostname, _ := os.Hostname()
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
