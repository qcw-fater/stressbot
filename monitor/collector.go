package monitor

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/errcode"
	stresslog "stressbot/utils/log"
)

// ActionResult 动作执行结果类型。
type ActionResult int

const (
	ResultSuccess  ActionResult = iota // 执行成功
	ResultFailure                      // 执行失败（非超时）
	ResultTimeout                      // 超时（TCPRequest/WaitListen 无响应）
	ResultCanceled                     // ctx 取消（任务停止/连接断开）
)

// ErrorEntry 错误分布条目，按 (Kind, Code) 聚合。
type ErrorEntry struct {
	Kind     errcode.Kind `json:"kind"`     // 错误分类："framework" / "server"
	Code     uint64       `json:"code"`     // 错误码
	CodeName string       `json:"codeName"` // 错误码名称（ErrorCode.String()）；服务端错误为 ""
	Messages []string     `json:"msgs"`     // 最近 N 条 Detail（最多 3 条，环形缓冲）
	Count    int64        `json:"count"`    // 该错误累计出现次数
}

// errKey 错误桶的复合键，按 (Kind, Code) 唯一标识一类错误。
type errKey struct {
	Kind errcode.Kind // 错误分类
	Code uint64       // 错误码
}

// errorBucket 错误桶，原子计数 + 环形缓冲最近 3 条 Detail。
type errorBucket struct {
	count   atomic.Int64    // 累计出现次数
	msgRing [3]atomic.Value // 环形缓冲，存最近 3 条 Detail 字符串
	ringIdx atomic.Uint32   // 环形缓冲写入位置（递增取模）
}

func (b *errorBucket) record(detail string) {
	b.count.Add(1)
	idx := int((b.ringIdx.Add(1) - 1) % uint32(len(b.msgRing)))
	b.msgRing[idx].Store(detail)
}

func (b *errorBucket) snapshot() (count int64, msgs []string) {
	count = b.count.Load()
	seen := make(map[string]bool)
	for i := range b.msgRing {
		if v := b.msgRing[i].Load(); v != nil {
			s := v.(string)
			if s != "" && !seen[s] {
				msgs = append(msgs, s)
				seen[s] = true
			}
		}
	}
	return
}

// CodedError 带错误码的错误接口。monitor 包定义此接口以避免循环依赖 engine 包。
type CodedError interface {
	error
	ErrorKind() errcode.Kind // 返回错误分类（framework / server）
	ErrorCode() uint64       // 返回错误码
	ErrorDetail() string     // 返回错误详情（用于环形缓冲存储）
}

// actionMetrics per-action 指标，全部使用原子操作保证无锁热路径安全。
type actionMetrics struct {
	successCount    atomic.Int64      // 成功次数
	failureCount    atomic.Int64      // 失败次数（非超时）
	timeoutCount    atomic.Int64      // 超时次数
	timeoutTotalMs  atomic.Int64      // 超时样本累计延迟（毫秒），用于计算平均超时延迟
	canceledCount   atomic.Int64      // 取消次数（ctx 取消）
	executing       atomic.Int64      // 当前正在执行中的并发数
	latency         *LatencyHistogram // 延迟直方图（仅成功样本）
	sendBytes       atomic.Int64      // 发送字节数（per-action，用于 ↑avg 列）
	recvBytes       atomic.Int64      // 接收字节数（per-action，用于 ↓avg 列）
	apdexSatisfied  atomic.Int64      // Apdex 满意样本：响应时间 < T
	apdexTolerating atomic.Int64      // Apdex 容忍样本：响应时间 >= T 且 < 4T
	errors          sync.Map          // errKey → *errorBucket，按 (Kind, Code) 聚合的错误分布
}

func newActionMetrics() *actionMetrics {
	return &actionMetrics{latency: newLatencyHistogram()}
}

// CollectorConfig 监控配置。
type CollectorConfig struct {
	Enabled     bool `json:"enabled"`     // 是否启用监控
	HTTPEnabled bool `json:"httpEnabled"` // 是否启用 HTTP JSON 端点
	HTTPPort    int  `json:"httpPort"`    // HTTP 端口号
	ApdexT      int  `json:"apdexT"`      // Apdex T 阈值（毫秒），默认 100
}

// MetricsCollector 全局指标收集器（单例）。
// enabled=false 时所有方法均为 no-op，压测核心路径零开销。
type MetricsCollector struct {
	enabled   bool            // 是否启用
	cfg       CollectorConfig // 运行期配置副本
	cfgMu     sync.RWMutex    // 保护 cfg.ApdexT 的运行期读写（任务级可调）
	startTime time.Time       // 收集器启动时间

	actions sync.Map   // string → *actionMetrics，按 action 名称索引
	namesMu sync.Mutex // 保护 names 切片的追加
	names   []string   // 按首次出现顺序排列的 action 名称，保证输出稳定

	robotsStarted atomic.Int64 // 已启动的机器人总数
	robotsRunning atomic.Int64 // 当前运行中的机器人数量
	robotsStopped atomic.Int64 // 正常停止的机器人数量
	robotsErrored atomic.Int64 // 异常退出的机器人数量
	totalActions  atomic.Int64 // 累计执行的动作总数（含回调）

	connEstablished atomic.Int64 // 成功建立的连接数
	connFailed      atomic.Int64 // 连接建立失败数
	connDropped     atomic.Int64 // 连接意外断开数（服务端关闭/网络异常）

	totalSendBytes atomic.Int64 // 全局累计发送字节数（由 network 层上报，含心跳等全部流量）
	totalRecvBytes atomic.Int64 // 全局累计接收字节数
}

var global *MetricsCollector

// Init 初始化全局单例。进程生命周期内只调用一次，任务级调整 ApdexT 用 SetApdexT。
func Init(cfg CollectorConfig) {
	global = &MetricsCollector{
		enabled:   cfg.Enabled,
		cfg:       cfg,
		startTime: time.Now(),
	}
	if global.cfg.ApdexT <= 0 {
		global.cfg.ApdexT = 100
	}
}

// Global 返回全局单例。
func Global() *MetricsCollector {
	return global
}

// Reset 重置所有计数器，用于新任务开始前清零。
func (c *MetricsCollector) Reset() {
	stresslog.Info("[MONITOR] 指标收集器已重置")
	c.startTime = time.Now()
	c.actions.Clear()
	c.names = c.names[:0]
	c.robotsStarted.Store(0)
	c.robotsRunning.Store(0)
	c.robotsStopped.Store(0)
	c.robotsErrored.Store(0)
	c.totalActions.Store(0)
	c.connEstablished.Store(0)
	c.connFailed.Store(0)
	c.connDropped.Store(0)
	c.totalSendBytes.Store(0)
	c.totalRecvBytes.Store(0)
}

// SetApdexT 任务级调整 Apdex T 值（毫秒），≤0 不修改。
func (c *MetricsCollector) SetApdexT(t int) {
	if t <= 0 {
		return
	}
	c.cfgMu.Lock()
	c.cfg.ApdexT = t
	c.cfgMu.Unlock()
}

// RecordActionStart 记录动作开始执行（递增 executing 计数）。
func (c *MetricsCollector) RecordActionStart(name string) {
	if !c.enabled {
		return
	}
	am := c.getOrCreateAction(name)
	am.executing.Add(1)
}

// AddBandwidth 累计全局收发字节数（由 network 层调用，含心跳/监听等全部流量）。
func (c *MetricsCollector) AddBandwidth(send, recv int64) {
	if c == nil || !c.enabled {
		return
	}
	if send > 0 {
		c.totalSendBytes.Add(send)
	}
	if recv > 0 {
		c.totalRecvBytes.Add(recv)
	}
}

// RecordAction 记录一次动作执行结果（热路径，纯原子操作）。
func (c *MetricsCollector) RecordAction(name string, result ActionResult, duration time.Duration, sendBytes, recvBytes int, err error) {
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
		// per-action 字节统计仍保留（用于 ActionsTab 的 ↑avg / ↓avg 列）；
		// 全局带宽（totalSendBytes / totalRecvBytes）由 network 层的 AddBandwidth 统一统计，
		// 这里**不再累加**，避免与 network 层的统计双计。
		if sendBytes > 0 {
			am.sendBytes.Add(int64(sendBytes))
		}
		if recvBytes > 0 {
			am.recvBytes.Add(int64(recvBytes))
		}
		// Apdex 分类
		c.cfgMu.RLock()
		T := int64(c.cfg.ApdexT)
		c.cfgMu.RUnlock()
		ms := duration.Milliseconds()
		switch {
		case ms < T:
			am.apdexSatisfied.Add(1)
		case ms < 4*T:
			am.apdexTolerating.Add(1)
		}
	case ResultFailure:
		am.failureCount.Add(1)
		if err != nil {
			c.recordError(am, err)
		}
	case ResultTimeout:
		am.timeoutCount.Add(1)
		am.timeoutTotalMs.Add(duration.Milliseconds())
	case ResultCanceled:
		am.canceledCount.Add(1)
	}
}

// recordError 记录错误到分布 map，按 (Kind, Code) 聚合。
func (c *MetricsCollector) recordError(am *actionMetrics, err error) {
	var ce CodedError
	if !errors.As(err, &ce) {
		return
	}
	key := errKey{Kind: ce.ErrorKind(), Code: ce.ErrorCode()}
	detail := ce.ErrorDetail()
	if len(detail) > 120 {
		detail = detail[:120]
	}
	if v, ok := am.errors.Load(key); ok {
		v.(*errorBucket).record(detail)
		return
	}
	b := &errorBucket{}
	b.record(detail)
	if actual, loaded := am.errors.LoadOrStore(key, b); loaded {
		actual.(*errorBucket).record(detail)
	}
}

// RobotStarted / RobotRunning / RobotStopped / RobotErrored 机器人生命周期钩子。
func (c *MetricsCollector) RobotStarted() {
	if c.enabled {
		c.robotsStarted.Add(1)
	}
}
func (c *MetricsCollector) RobotRunning() {
	if c.enabled {
		c.robotsRunning.Add(1)
	}
}
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
func (c *MetricsCollector) ConnEstablished() {
	if c.enabled {
		c.connEstablished.Add(1)
	}
}
func (c *MetricsCollector) ConnFailed() {
	if c.enabled {
		c.connFailed.Add(1)
	}
}
func (c *MetricsCollector) ConnDropped() {
	if c.enabled {
		c.connDropped.Add(1)
	}
}

// RecordCallbackSuccess 记录一次推送回调成功（仅计数，无延迟）。
func (c *MetricsCollector) RecordCallbackSuccess(name string) {
	if !c.enabled {
		return
	}
	c.totalActions.Add(1)
	am := c.getOrCreateAction("callback:" + name)
	am.successCount.Add(1)
}

// RecordCallbackError 记录一次推送回调失败。
func (c *MetricsCollector) RecordCallbackError(name string, err error) {
	if !c.enabled {
		return
	}
	c.totalActions.Add(1)
	am := c.getOrCreateAction("callback:" + name)
	am.failureCount.Add(1)
	if err != nil {
		c.recordError(am, err)
	}
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

// CollectErrors 收集指定 action 的错误分布（只读快照）。
func (am *actionMetrics) CollectErrors() []ErrorEntry {
	var entries []ErrorEntry
	am.errors.Range(func(key, value any) bool {
		k := key.(errKey)
		count, msgs := value.(*errorBucket).snapshot()
		entries = append(entries, ErrorEntry{
			Kind:     k.Kind,
			Code:     k.Code,
			CodeName: errcode.ErrorCode(k.Code).String(),
			Messages: msgs,
			Count:    count,
		})
		return true
	})
	return entries
}
