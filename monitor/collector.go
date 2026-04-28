package monitor

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ActionResult 动作执行结果类型。
type ActionResult int

const (
	ResultSuccess  ActionResult = iota // 执行成功
	ResultFailure                      // 执行失败（非超时）
	ResultTimeout                      // 超时（TCPRequest/WaitListen 无响应）
	ResultSkipped                      // 跳过（必填字段为空，ErrActionSkip）
)

// actionMetrics per-action 指标，全部原子操作。
type actionMetrics struct {
	successCount    atomic.Int64
	failureCount    atomic.Int64
	timeoutCount    atomic.Int64
	timeoutTotalMs  atomic.Int64          // 超时样本总延迟（毫秒），用于算 avg
	skippedCount    atomic.Int64
	executing       atomic.Int64          // 当前正在执行的机器人数量
	latency         *LatencyHistogram
	sendBytes       atomic.Int64
	recvBytes       atomic.Int64
	apdexSatisfied  atomic.Int64 // 响应时间 < T
	apdexTolerating atomic.Int64 // 响应时间 >= T 且 < 4T
	errors          sync.Map     // string → *atomic.Int64（错误消息 → 出现次数）
}

func newActionMetrics() *actionMetrics {
	return &actionMetrics{latency: newLatencyHistogram()}
}

// CollectorConfig 监控配置。
type CollectorConfig struct {
	Enabled        bool   `json:"enabled"`
	ReportInterval string `json:"reportInterval"`
	HTTPEnabled    bool   `json:"httpEnabled"`
	HTTPPort       int    `json:"httpPort"`
	CsvPath        string `json:"csvPath"`
	ApdexT         int    `json:"apdexT"` // Apdex T 值（毫秒），默认 100
}

// MetricsCollector 全局指标收集器（单例）。
// enabled=false 时所有方法均为 no-op，压测核心路径零开销。
type MetricsCollector struct {
	enabled   bool
	cfg       CollectorConfig
	startTime time.Time

	actions sync.Map // string → *actionMetrics
	namesMu sync.Mutex
	names   []string // 按首次出现顺序排列，保证输出稳定

	robotsStarted atomic.Int64
	robotsRunning atomic.Int64
	robotsStopped atomic.Int64
	robotsErrored atomic.Int64
	totalActions  atomic.Int64

	// 连接指标
	connEstablished atomic.Int64
	connFailed      atomic.Int64
	connDropped     atomic.Int64

	// 全局带宽
	totalSendBytes atomic.Int64
	totalRecvBytes atomic.Int64
}

var global *MetricsCollector

// Init 初始化全局单例。
func Init(cfg CollectorConfig) {
	global = &MetricsCollector{
		enabled:   cfg.Enabled,
		cfg:       cfg,
		startTime: time.Now(),
	}
	if global.cfg.ApdexT <= 0 {
		global.cfg.ApdexT = 100
	}
	if global.cfg.HTTPPort <= 0 {
		global.cfg.HTTPPort = 6060
	}
	if global.cfg.CsvPath == "" {
		global.cfg.CsvPath = "log/metrics.csv"
	}
}

// Global 返回全局单例。
func Global() *MetricsCollector {
	return global
}

// RecordActionStart 记录动作开始执行（递增 executing 计数）。
func (c *MetricsCollector) RecordActionStart(name string) {
	if !c.enabled {
		return
	}
	am := c.getOrCreateAction(name)
	am.executing.Add(1)
}

// RecordAction 记录一次动作执行结果（热路径，纯原子操作）。
// sendBytes / recvBytes 传 0 表示不统计（Lua 动作等无法获取字节数）。
// errMsg 传空字符串表示不记录错误详情。
func (c *MetricsCollector) RecordAction(name string, result ActionResult, duration time.Duration, sendBytes, recvBytes int, errMsg string) {
	if !c.enabled {
		return
	}
	c.totalActions.Add(1)
	am := c.getOrCreateAction(name)
	am.executing.Add(-1)

	switch result {
	case ResultSuccess:
		am.successCount.Add(1)
		am.latency.Record(duration)
		if sendBytes > 0 {
			am.sendBytes.Add(int64(sendBytes))
			c.totalSendBytes.Add(int64(sendBytes))
		}
		if recvBytes > 0 {
			am.recvBytes.Add(int64(recvBytes))
			c.totalRecvBytes.Add(int64(recvBytes))
		}
		// Apdex 分类
		T := int64(c.cfg.ApdexT)
		ms := duration.Milliseconds()
		switch {
		case ms < T:
			am.apdexSatisfied.Add(1)
		case ms < 4*T:
			am.apdexTolerating.Add(1)
		}
	case ResultFailure:
		am.failureCount.Add(1)
		if errMsg != "" {
			c.recordError(am, errMsg)
		}
	case ResultTimeout:
		am.timeoutCount.Add(1)
		am.timeoutTotalMs.Add(duration.Milliseconds())
	case ResultSkipped:
		am.skippedCount.Add(1)
	}
}

// recordError 记录错误消息到分布 map。
func (c *MetricsCollector) recordError(am *actionMetrics, errMsg string) {
	// 截断过长的错误消息
	if len(errMsg) > 120 {
		errMsg = errMsg[:120]
	}
	if v, ok := am.errors.Load(errMsg); ok {
		v.(*atomic.Int64).Add(1)
		return
	}
	counter := &atomic.Int64{}
	actual, loaded := am.errors.LoadOrStore(errMsg, counter)
	if loaded {
		actual.(*atomic.Int64).Add(1)
	} else {
		counter.Add(1)
	}
}

// RobotStarted / RobotRunning / RobotStopped / RobotErrored 机器人生命周期钩子。
func (c *MetricsCollector) RobotStarted() { if c.enabled { c.robotsStarted.Add(1) } }
func (c *MetricsCollector) RobotRunning() { if c.enabled { c.robotsRunning.Add(1) } }
func (c *MetricsCollector) RobotStopped() {
	if c.enabled {
		c.robotsRunning.Add(-1)
		c.robotsStopped.Add(1)
	}
}
func (c *MetricsCollector) RobotErrored() {
	if c.enabled {
		c.robotsRunning.Add(-1)
		c.robotsErrored.Add(1)
	}
}

// 连接生命周期钩子。
func (c *MetricsCollector) ConnEstablished() { if c.enabled { c.connEstablished.Add(1) } }
func (c *MetricsCollector) ConnFailed()      { if c.enabled { c.connFailed.Add(1) } }
func (c *MetricsCollector) ConnDropped()     { if c.enabled { c.connDropped.Add(1) } }

// RecordCallback 记录一次推送回调触发（仅计数，无延迟）。
func (c *MetricsCollector) RecordCallback(name string) {
	if !c.enabled {
		return
	}
	c.totalActions.Add(1)
	am := c.getOrCreateAction("callback:" + name)
	am.successCount.Add(1)
}

func (c *MetricsCollector) getOrCreateAction(name string) *actionMetrics {
	if v, ok := c.actions.Load(name); ok {
		return v.(*actionMetrics)
	}
	am := newActionMetrics()
	actual, loaded := c.actions.LoadOrStore(name, am)
	if !loaded {
		c.namesMu.Lock()
		c.names = append(c.names, name)
		c.namesMu.Unlock()
	}
	return actual.(*actionMetrics)
}

// Uptime 返回运行时长。
func (c *MetricsCollector) Uptime() time.Duration {
	return time.Since(c.startTime)
}

// ActionNames 返回按首次出现顺序排列的 action 名称列表（快照副本）。
func (c *MetricsCollector) ActionNames() []string {
	c.namesMu.Lock()
	names := make([]string, len(c.names))
	copy(names, c.names)
	c.namesMu.Unlock()
	return names
}

// Enabled 返回是否启用。
func (c *MetricsCollector) Enabled() bool {
	return c.enabled
}

// ErrorEntry 错误分布条目。
type ErrorEntry struct {
	Message string `json:"msg"`
	Count   int64  `json:"count"`
}

// CollectErrors 收集指定 action 的错误分布（只读快照）。
func (am *actionMetrics) CollectErrors() []ErrorEntry {
	var entries []ErrorEntry
	am.errors.Range(func(key, value any) bool {
		entries = append(entries, ErrorEntry{
			Message: fmt.Sprintf("%v", key),
			Count:   value.(*atomic.Int64).Load(),
		})
		return true
	})
	return entries
}
