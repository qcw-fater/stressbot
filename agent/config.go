package agent

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"stressbot/utils"
)

// Config Agent 配置（JSON 反序列化形态）。
// 仅保留用户需要关心的字段，其余内部参数硬编码在 Resolve() 中。
type Config struct {
	Enabled             bool   `json:"enabled"`             // 是否启用 Agent 模式
	AdminAddr           string `json:"adminAddr"`           // Admin 服务器地址（如 http://192.168.1.100:7718）
	PublicURL           string `json:"publicUrl"`           // Agent 对外可达地址（如 http://192.168.1.200:7719），不填自动获取本机 IP
	Port                int    `json:"port"`                // 本地 HTTP 监听端口（默认 7719，Admin 通过此端口回调 Agent）
	MaxBots             int    `json:"maxBots"`             // 单节点最大机器人数量（默认 5000）
	HBInterval          string `json:"hbInterval"`          // 心跳发送间隔（默认 10s）
	RequestTimeout      string `json:"requestTimeout"`      // 单次 HTTP 请求超时（默认 30s）
	ReconnectMaxRetries int    `json:"reconnectMaxRetries"` // 最大重连次数，-1=持续重连（默认 -1）
	StressInterval      string `json:"stressInterval"`      // 压力指标上报间隔（默认 5s）
	AppVersion          string `json:"-"`                   // 编译时注入，不从 JSON 读取
}

// ResolvedConfig Agent 运行期配置（所有参数已解析为最终值）。
type ResolvedConfig struct {
	AdminAddr            string        // Admin 服务器地址
	Name                 string        // 节点名称（主机名）
	Address              string        // Agent 对外可达地址（如 http://192.168.1.200:7719）
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

// Resolve 将 Config 解析为 ResolvedConfig，校验必填项。
func (c *Config) Resolve() (*ResolvedConfig, error) {
	if c.AdminAddr == "" {
		return nil, fmt.Errorf("agent.adminAddr 不能为空")
	}

	hostname, _ := os.Hostname() // hostname 可选，获取失败不影响功能
	if hostname == "" {
		hostname = "unknown"
	}

	maxBots := c.MaxBots
	if maxBots <= 0 {
		maxBots = 5000
	}

	port := c.Port
	if port <= 0 {
		port = 7719
	}

	stressInterval := utils.ParseDurationDefault(c.StressInterval, 5*time.Second, "agent.stressInterval")
	hbInterval := utils.ParseDurationDefault(c.HBInterval, 10*time.Second, "agent.hbInterval")
	requestTimeout := utils.ParseDurationDefault(c.RequestTimeout, 30*time.Second, "agent.requestTimeout")

	// 解析 Agent 对外地址：用户配置优先，否则自动获取本机 IP
	address := c.PublicURL
	if address == "" {
		address = buildAddress(port)
	}

	return &ResolvedConfig{
		AdminAddr:            c.AdminAddr,
		Name:                 hostname,
		Address:              address,
		Port:                 port,
		MaxBots:              maxBots,
		AppVersion:           c.AppVersion,
		TaskWorkDir:          os.TempDir(),
		AdapterScript:        "conf/adapter/codec.lua",
		StressInterval:       stressInterval,
		SystemInterval:       stressInterval, // 系统指标与压力指标同步上报
		HBInterval:           hbInterval,
		HBFailInterval:       hbInterval, // 失败重试间隔与心跳间隔一致
		RequestTimeout:       requestTimeout,
		ReconnectInterval:    5 * time.Second,
		ReconnectMaxInterval: 60 * time.Second,
		ReconnectMaxRetries:  resolveReconnectRetries(c.ReconnectMaxRetries),
		TaskReportTimeout:    30 * time.Second,
	}, nil
}

// resolveReconnectRetries 处理 reconnectMaxRetries：JSON 零值(0)视为未配置，回退 -1（持续重连）。
func resolveReconnectRetries(v int) int {
	if v == 0 {
		return -1
	}
	return v
}

// CollectStaticInfo 采集本机静态信息。
func CollectStaticInfo() StaticInfo {
	hostname, _ := os.Hostname() // 同上
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
