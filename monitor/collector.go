package monitor

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"stressbot/errcode"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// RequestTiming 单次 request-response 的耗时拆解。
type RequestTiming struct {
	SendCost             time.Duration
	WireRTT              time.Duration
	DecodeWait           time.Duration
	DecodeCost           time.Duration
	DispatchToActionWait time.Duration
	Observed             TimingStage
}

// TimingStage 区分“没有采集”与“实际测得 0ns”。
type TimingStage uint16

const (
	StageRTT TimingStage = 1 << iota
	StageListenWait
	StageBuild
	StageEncode
	StageSend
	StageDecodeWait
	StageDecode
	StageDispatchWait
	StageParseStore
)

func (s TimingStage) Has(stage TimingStage) bool { return s&stage != 0 }

// ClientTiming 单次 action 的客户端侧耗时拆解。
type ClientTiming struct {
	BuildCost      time.Duration
	EncodeCost     time.Duration
	SendCost       time.Duration
	DecodeWait     time.Duration
	DecodeCost     time.Duration
	DispatchWait   time.Duration
	ParseStoreCost time.Duration
	Observed       TimingStage
}

// ActionTiming 单次 action 执行的耗时拆解。
type ActionTiming struct {
	Requests []RequestTiming
	// FailedRequests 「发出去但没等回响应帧」的请求数（超时 / 断连 / 发送失败）。
	// 算不出 WireRTT，但按 frustrated 计入 RTT Apdex 分母，详见 engine.ActionTiming。
	FailedRequests int
	// ListenWaits 监听命中的等待时长样本；ListenReady 命中时消息已在队列（不可测）；
	// ListenTimeouts 监听超时。三者详见 engine.ActionTiming。
	ListenWaits    []time.Duration
	ListenReady    int
	ListenTimeouts int
	Client         ClientTiming
}

func (t *ActionTiming) wireRTTSum() time.Duration {
	var total time.Duration
	for _, req := range t.Requests {
		if req.WireRTT > 0 {
			total += req.WireRTT
		}
	}
	return total
}

// ActionResult 动作执行结果类型。
type ActionResult int

const (
	ResultSuccess  ActionResult = iota // 执行成功
	ResultFailure                      // 执行失败（非超时）
	ResultTimeout                      // 超时（TCPRequest/WaitListen 无响应）
	ResultCanceled                     // ctx 取消（任务停止/连接断开）
)

// ErrorEntry 错误分布条目，按 code 单维聚合。
type ErrorEntry struct {
	Code     uint64   `json:"code"`     // 错误码
	CodeName string   `json:"codeName"` // 错误码名称（ErrorCode.String()）；业务码为 ""
	Messages []string `json:"msgs"`     // 最近 N 条 Detail（最多 3 条，环形缓冲）
	Count    int64    `json:"count"`    // 该错误累计出现次数
}

// errKey 错误桶的键，按 code 唯一标识一类错误。
type errKey struct {
	Code uint64 // 错误码
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
	ErrorCode() uint64   // 返回错误码
	ErrorDetail() string // 返回错误详情（用于环形缓冲存储）
}

// actionMetrics per-action 指标。同名动作由 mu 保证一致快照，不同动作并行记录。
type actionMetrics struct {
	mu              sync.Mutex
	successCount    atomic.Int64      // 成功次数
	failureCount    atomic.Int64      // 失败次数（非超时）
	timeoutCount    atomic.Int64      // 超时次数
	timeoutTotalNs  atomic.Int64      // 超时样本累计延迟（纳秒），用于精确计算平均超时延迟
	canceledCount   atomic.Int64      // 取消次数（ctx 取消）
	executing       atomic.Int64      // 当前正在执行中的并发数
	rtt             *LatencyHistogram // RTT 直方图：纯网络往返（仅成功且有 WireRTT 样本）
	listenWait      *LatencyHistogram // 监听等待直方图：开始等待 → 帧被内核收到
	totalDuration   *LatencyHistogram // 总耗时直方图：单次 action wallClock
	sendBytes       atomic.Int64      // 发送字节数（per-action，用于 ↑avg 列）
	recvBytes       atomic.Int64      // 接收字节数（per-action，用于 ↓avg 列）
	apdexSatisfied  atomic.Int64      // RTT Apdex 满意样本：响应时间 < T
	apdexTolerating atomic.Int64      // RTT Apdex 容忍样本：响应时间 >= T 且 < 4T
	rttFailedCount  atomic.Int64      // 无响应帧的请求数（超时/断连/发送失败）：进 RTT Apdex 分母记 frustrated
	errors          sync.Map          // errKey → *errorBucket，按 code 单维聚合的错误分布

	// 客户端开销、RTT 样本与总耗时样本计数：
	//   - clientCostSum/Count：累计客户端构建/解析开销（纳秒），用于 ClientAvgMs
	//   - rttSampleCount：RTT 直方图中的独立样本数（逐个 request 记录，非 action 粒度）
	//   - totalDurationSampleCount：总耗时直方图中的 action 样本数（单次 action 最多 1 个）
	//   - Lua 脚本单次 action 内可能多次 request，每次独立记录 WireRTT 到直方图
	// 客户端开销在所有结果分支（含失败/超时）都累加；rttSampleCount 统计所有有完整响应帧且 WireRTT > 0 的 request，
	// 包括服务端 headerErr/失败响应，不包括超时、发送失败、取消且未收到响应帧的分支。
	clientCostSum            atomic.Int64
	clientCostCount          atomic.Int64
	buildCostSum             atomic.Int64
	encodeCostSum            atomic.Int64
	sendCostSum              atomic.Int64
	decodeWaitSum            atomic.Int64
	decodeCostSum            atomic.Int64
	dispatchWaitSum          atomic.Int64
	parseStoreSum            atomic.Int64
	buildCostCount           atomic.Int64
	encodeCostCount          atomic.Int64
	sendCostCount            atomic.Int64
	decodeWaitCount          atomic.Int64
	decodeCostCount          atomic.Int64
	dispatchWaitCount        atomic.Int64
	parseStoreCount          atomic.Int64
	rttSampleCount           atomic.Int64
	totalDurationSampleCount atomic.Int64
	byteSampleCount          atomic.Int64

	// 监听类计数。等待时长与 RTT 分开统计：前者含服务端业务时长（匹配、等队友，秒级），
	// 后者是单请求处理延迟（毫秒级）。混进同一分布，两个数都会失去意义。
	listenWaitCount   atomic.Int64 // 等待时长样本数（listenWait 直方图分母）
	listenReadyCount  atomic.Int64 // 命中时消息已在队列：等待时长不可测，只计次
	listenTimeoutHits atomic.Int64 // 监听超时次数，单独成率，不进直方图
}

func newActionMetrics(relativeAccuracy float64, maxBins int) *actionMetrics {
	return &actionMetrics{
		rtt:           newLatencyHistogramWith(relativeAccuracy, maxBins),
		listenWait:    newLatencyHistogramWith(relativeAccuracy, maxBins),
		totalDuration: newLatencyHistogramWith(relativeAccuracy, maxBins),
	}
}

type TimingDetailLevel string

const (
	TimingRTTOnly     TimingDetailLevel = "rtt"
	TimingCodecDetail TimingDetailLevel = "codec"
	TimingFullDetail  TimingDetailLevel = "full"
)

func timingDetailRank(level TimingDetailLevel) int {
	switch level {
	case TimingFullDetail:
		return 2
	case TimingCodecDetail:
		return 1
	default:
		return 0
	}
}

// TimingDetailAtLeast 判断当前计时级别是否覆盖指定级别。
func TimingDetailAtLeast(current, required TimingDetailLevel) bool {
	return timingDetailRank(current) >= timingDetailRank(required)
}

// CollectorConfig 监控配置。
type CollectorConfig struct {
	ApdexThresholdMs int           `toml:"apdexThresholdMs" json:"apdexThresholdMs"` // Apdex T 阈值（毫秒），默认 100
	TimingDetail     string        `toml:"timingDetail"     json:"timingDetail"`     // 计时细分级别：rtt / codec / full，默认 rtt
	ReportInterval   string        `toml:"reportInterval"   json:"reportInterval"`   // 监控指标上报间隔（如 "5s"），仅单机模式用
	HTTP             *HTTPConfig   `toml:"http"             json:"http"`             // nil = 不启用 HTTP JSON 端点
	Sketch           SketchConfig  `toml:"sketch"           json:"sketch"`           // DDSketch 延迟分布精度参数
}

// SketchConfig DDSketch 延迟分布精度配置。
// 注意：DDSketch 合并要求两侧精度参数一致，Admin 与所有 Agent 必须使用相同值。
type SketchConfig struct {
	RelativeAccuracy float64 `toml:"relativeAccuracy" json:"relativeAccuracy"` // 相对误差（默认 0.01 = 1%），越小越精确越耗内存
	MaxBins          int     `toml:"maxBins"          json:"maxBins"`          // 最大桶数（默认 2048），越大覆盖范围越广
}

// HTTPConfig 监控 HTTP JSON 端点配置。
type HTTPConfig struct {
	Port int `toml:"port" json:"port"` // HTTP 端口号
}

// MetricsCollector 全局指标收集器（单例）。
// enabled=false 时所有方法均为 no-op，压测核心路径零开销。
type MetricsCollector struct {
	enabled         bool              // 是否启用
	cfg             CollectorConfig   // 运行期配置副本（除 ApdexThresholdMs 外）
	cfgMu           sync.RWMutex      // 保护 cfg 非热路径字段
	apdexT          atomic.Int32      // Apdex T 阈值（毫秒）热路径独立原子读写，与 cfgMu 解耦
	timingDetail    TimingDetailLevel // 计时细分级别
	windowOnly      bool              // true 表示活动区间子收集器，禁止递归创建 window
	transitionMu    sync.RWMutex      // Record 持读锁；TakeReportSnapshot 持写锁切换窗口
	window          atomic.Pointer[MetricsCollector]
	collectionEpoch atomic.Uint64 // 累计指标代次；Reset 后递增，允许 Admin 识别合法清零
	startTime       atomic.Int64  // 收集器启动时间（UnixNano）；Reset 写、Uptime 读，用原子避免 time.Time 多字结构撕裂

	actions sync.Map   // string → *actionMetrics，按 action 名称索引
	namesMu sync.Mutex // 保护 names 切片的追加
	names   []string   // 按首次出现顺序排列的 action 名称，保证输出稳定

	robotsStarted atomic.Int64 // 已启动的机器人总数
	robotsRunning atomic.Int64 // 当前运行中的机器人数量
	robotsStopped atomic.Int64 // 正常停止的机器人数量
	robotsErrored atomic.Int64 // 异常退出的机器人数量
	totalActions  atomic.Int64 // 累计执行的动作总数（含回调）

	connEstablished atomic.Int64 // 成功建立的连接数
	connActive      atomic.Int64 // 当前活跃连接数
	connClosed      atomic.Int64 // 累计关闭连接数（主动 + 异常）
	connFailed      atomic.Int64 // 连接建立失败数
	connDropped     atomic.Int64 // 连接意外断开数（服务端关闭/网络异常）

	totalSendBytes       atomic.Int64 // 全局累计发送字节数（由 network 层上报，含心跳等全部流量）
	totalRecvBytes       atomic.Int64 // 全局累计接收字节数
	invalidMetricSamples atomic.Int64 // 被拒绝的负耗时或 DDSketch Add 失败样本

	rampUpCurrentStage atomic.Int32 // 渐进加压当前阶段（1-based，0 = 未启用或未开始）
	rampUpTotalStages  atomic.Int32 // 渐进加压总阶段数（0 = 未启用或未开始）
}

var (
	global     *MetricsCollector
	globalOnce sync.Once
)

func NormalizeTimingDetail(value string) TimingDetailLevel {
	switch TimingDetailLevel(value) {
	case TimingCodecDetail:
		return TimingCodecDetail
	case TimingFullDetail:
		return TimingFullDetail
	case TimingRTTOnly, "":
		return TimingRTTOnly
	default:
		return TimingRTTOnly
	}
}

// Init 初始化全局单例。sync.Once 保证幂等，多次调用不会重置。
// 调用方（cmd/agent/main.go）保证只在 cfg.Monitor != nil 时调用 Init，
// 因此这里固定 enabled=true（nil = 调用方根本不调 Init）。
func Init(cfg CollectorConfig) {
	globalOnce.Do(func() {
		level := NormalizeTimingDetail(cfg.TimingDetail)
		if cfg.TimingDetail == "" {
			stresslog.Warn("[MONITOR] monitor.timingDetail 未配置，使用默认值", zap.String("timingDetail", string(level)))
			cfg.TimingDetail = string(level)
		} else if TimingDetailLevel(cfg.TimingDetail) != level {
			stresslog.Warn("[MONITOR] monitor.timingDetail 配置无效，使用默认值", zap.String("configured", cfg.TimingDetail), zap.String("timingDetail", string(level)))
			cfg.TimingDetail = string(level)
		}
		global = NewCollector(cfg)
	})
}

// NewCollector 创建独立收集器。分布式任务和测试可显式持有实例，避免依赖全局单例。
func NewCollector(cfg CollectorConfig) *MetricsCollector {
	level := NormalizeTimingDetail(cfg.TimingDetail)
	c := &MetricsCollector{enabled: true, cfg: cfg, timingDetail: level}
	now := time.Now()
	c.collectionEpoch.Store(1)
	c.startTime.Store(now.UnixNano())
	t := cfg.ApdexThresholdMs
	if t <= 0 {
		t = 100
	}
	c.cfg.ApdexThresholdMs = t
	c.cfg.TimingDetail = string(level)
	c.apdexT.Store(int32(t))
	c.window.Store(c.newWindowCollector(now))
	return c
}

// Global 返回全局单例。
func Global() *MetricsCollector {
	return global
}

func (c *MetricsCollector) newWindowCollector(start time.Time) *MetricsCollector {
	w := &MetricsCollector{
		enabled:      c.enabled,
		cfg:          c.cfg,
		timingDetail: c.timingDetail,
		windowOnly:   true,
	}
	w.startTime.Store(start.UnixNano())
	w.collectionEpoch.Store(c.collectionEpoch.Load())
	w.apdexT.Store(c.apdexT.Load())
	return w
}

func (c *MetricsCollector) currentWindowCollector() *MetricsCollector {
	if c.windowOnly {
		return nil
	}
	if w := c.window.Load(); w != nil {
		return w
	}
	w := c.newWindowCollector(time.Now())
	if c.window.CompareAndSwap(nil, w) {
		return w
	}
	return c.window.Load()
}

// Reset 重置所有计数器，用于新任务开始前清零。
func (c *MetricsCollector) Reset() {
	if !c.windowOnly {
		c.transitionMu.Lock()
		defer c.transitionMu.Unlock()
	}
	if stresslog.GetLogger() != nil {
		stresslog.Info("[MONITOR] 指标收集器已重置")
	}
	now := time.Now()
	c.startTime.Store(now.UnixNano())
	c.collectionEpoch.Add(1)
	c.actions.Clear()
	c.namesMu.Lock()
	c.names = c.names[:0]
	c.namesMu.Unlock()
	c.robotsStarted.Store(0)
	c.robotsRunning.Store(0)
	c.robotsStopped.Store(0)
	c.robotsErrored.Store(0)
	c.totalActions.Store(0)
	c.connEstablished.Store(0)
	c.connActive.Store(0)
	c.connClosed.Store(0)
	c.connFailed.Store(0)
	c.connDropped.Store(0)
	c.totalSendBytes.Store(0)
	c.totalRecvBytes.Store(0)
	c.invalidMetricSamples.Store(0)
	c.rampUpCurrentStage.Store(0)
	c.rampUpTotalStages.Store(0)
	if !c.windowOnly {
		c.window.Store(c.newWindowCollector(now))
	}
}

// SetApdexT 任务级调整 Apdex T 值（毫秒），≤0 不修改。
// 热路径只读 c.apdexT（atomic），这里同步更新原子字段；
// cfg.ApdexThresholdMs 仅用于快照导出，写时一并维护以保持可见。
func (c *MetricsCollector) SetApdexT(t int) {
	if t <= 0 {
		return
	}
	c.apdexT.Store(int32(t))
	c.cfgMu.Lock()
	c.cfg.ApdexThresholdMs = t
	c.cfgMu.Unlock()
	if w := c.window.Load(); w != nil {
		w.apdexT.Store(int32(t))
	}
}

// RecordActionStart 记录动作开始执行（递增 executing 计数）。
func (c *MetricsCollector) RecordActionStart(name string) {
	if c == nil || !c.enabled {
		return
	}
	am := c.getOrCreateAction(name)
	am.mu.Lock()
	am.executing.Add(1)
	am.mu.Unlock()
}

// RecordPendingCanceled 记录由于上游分支中止而从未开始执行的动作。
// 它只增加取消数，不进入吞吐、字节、错误、Apdex 或 executing。
func (c *MetricsCollector) RecordPendingCanceled(name string) {
	if c == nil || !c.enabled {
		return
	}
	if !c.windowOnly {
		c.transitionMu.RLock()
		defer c.transitionMu.RUnlock()
	}
	am := c.getOrCreateAction(name)
	am.mu.Lock()
	am.canceledCount.Add(1)
	am.mu.Unlock()
	if !c.windowOnly {
		c.currentWindowCollector().RecordPendingCanceled(name)
	}
}

// AddBandwidth 累计全局收发字节数（由 network 层调用，含心跳/监听等全部流量）。
func (c *MetricsCollector) AddBandwidth(send, recv int64) {
	if c == nil || !c.enabled {
		return
	}
	if !c.windowOnly {
		c.transitionMu.RLock()
		defer c.transitionMu.RUnlock()
	}
	if send > 0 {
		c.totalSendBytes.Add(send)
	}
	if recv > 0 {
		c.totalRecvBytes.Add(recv)
	}
	if !c.windowOnly {
		c.currentWindowCollector().AddBandwidth(send, recv)
	}
}

// RecordAction 记录一次动作执行结果。不同动作并行，同名动作仅持有短时聚合锁。
func (c *MetricsCollector) RecordAction(
	name string, result ActionResult,
	timing ActionTiming, wallClock time.Duration,
	sendBytes, recvBytes int, err error,
) {
	c.recordAction(name, result, timing, wallClock, sendBytes, recvBytes, err, true)
}

func (c *MetricsCollector) recordAction(
	name string, result ActionResult,
	timing ActionTiming, wallClock time.Duration,
	sendBytes, recvBytes int, err error,
	trackExecuting bool,
) {
	if c == nil || !c.enabled {
		return
	}
	if !c.windowOnly {
		c.transitionMu.RLock()
		defer c.transitionMu.RUnlock()
	}
	am := c.getOrCreateAction(name)
	am.mu.Lock()
	defer am.mu.Unlock()
	if result != ResultCanceled {
		c.totalActions.Add(1)
	}
	if trackExecuting {
		am.executing.Add(-1)
	}

	// 记录 wallClock 时延 / Apdex / 客户端分项的判据：result != Canceled || wallClock > 0。
	// 只排除"取消补记"这一类伪样本，而非一切 wallClock==0：
	//   - 取消补记（执行器对未执行节点的配平记账，result=Canceled 且 wallClock=0）必须排除：
	//     否则 0ms 样本会压低 P50/P90/P99，且 0ms<T 恒记 satisfied 令 Apdex 虚高；
	//   - 真实执行后才取消的样本（result=Canceled 且 wallClock>0）仍按契约完整记录；
	//   - 成功/失败/超时一律记录——纯客户端动作（setState/clearState）耗时可能被时钟粒度
	//     测成 0（Windows 时钟粒度较粗），此前用 wallClock>0 会把这类快动作静默丢样本。
	// 客户端分项（build/encode/...）与分母 clientCostCount 必须同处一个采样条件下累加，
	// 否则会出现"分子含取消样本、分母不含"的均值偏高（snapshot 按 clientCostCount 求均值）。
	// canceledCount 仍在下方单独累加，字节数照常计，取消事件不丢失。
	if result != ResultCanceled || wallClock > 0 {
		clientCost := max(wallClock-timing.wireRTTSum(), 0)
		if clientCost > 0 {
			am.clientCostSum.Add(clientCost.Nanoseconds())
		}
		am.clientCostCount.Add(1)
		// wallClock 只留直方图作诊断，不再打 Apdex：它是「施压机排队 + 故意 sleep +
		// 服务端响应」的混合量，施压机越忙这个数越大，用它评分等于把施压机自身的
		// 负载算成服务端的账。评分统一由 RTT 承担（见下方 Requests 循环）。
		if err := am.totalDuration.Record(wallClock); err != nil {
			c.invalidMetricSamples.Add(1)
		} else {
			am.totalDurationSampleCount.Add(1)
		}
		addDuration := func(dst, count *atomic.Int64, observed TimingStage, stage TimingStage, d time.Duration) {
			if !observed.Has(stage) && d == 0 {
				return
			}
			if d < 0 {
				c.invalidMetricSamples.Add(1)
				return
			}
			if d > 0 {
				dst.Add(d.Nanoseconds())
			}
			count.Add(1)
		}
		observed := timing.Client.Observed
		addDuration(&am.buildCostSum, &am.buildCostCount, observed, StageBuild, timing.Client.BuildCost)
		addDuration(&am.encodeCostSum, &am.encodeCostCount, observed, StageEncode, timing.Client.EncodeCost)
		addDuration(&am.sendCostSum, &am.sendCostCount, observed, StageSend, timing.Client.SendCost)
		addDuration(&am.decodeWaitSum, &am.decodeWaitCount, observed, StageDecodeWait, timing.Client.DecodeWait)
		addDuration(&am.decodeCostSum, &am.decodeCostCount, observed, StageDecode, timing.Client.DecodeCost)
		addDuration(&am.dispatchWaitSum, &am.dispatchWaitCount, observed, StageDispatchWait, timing.Client.DispatchWait)
		addDuration(&am.parseStoreSum, &am.parseStoreCount, observed, StageParseStore, timing.Client.ParseStoreCost)
	}
	for _, req := range timing.Requests {
		if !req.Observed.Has(StageRTT) && req.WireRTT == 0 {
			continue
		}
		if err := am.rtt.Record(req.WireRTT); err != nil {
			c.invalidMetricSamples.Add(1)
			continue
		}
		am.rttSampleCount.Add(1)
		threshold := time.Duration(c.apdexT.Load()) * time.Millisecond
		switch {
		case req.WireRTT < threshold:
			am.apdexSatisfied.Add(1)
		case req.WireRTT < 4*threshold:
			am.apdexTolerating.Add(1)
		}
	}
	// 无响应帧的请求只进 Apdex 分母（frustrated），不进 RTT 直方图——
	// 它们没有可用的时延值，混进直方图会污染 P50/P99；但从分母里漏掉它们，
	// 会让「服务端超时越多、RTT Apdex 越高」这种反向失真发生。
	if timing.FailedRequests > 0 {
		am.rttFailedCount.Add(int64(timing.FailedRequests))
	}
	// 监听等待自成一列，不与 RTT 合流、也不打 Apdex：这段时长的主体是服务端业务
	// （匹配、等队友、等开局），秒级且没有普遍阈值，用毫秒级的 T 去评必然全 frustrated。
	// 要看的是它的分布（P50/P99）而不是一个被压死的分数。
	for _, wait := range timing.ListenWaits {
		if wait <= 0 {
			continue
		}
		if err := am.listenWait.Record(wait); err != nil {
			c.invalidMetricSamples.Add(1)
		} else {
			am.listenWaitCount.Add(1)
		}
	}
	if timing.ListenReady > 0 {
		am.listenReadyCount.Add(int64(timing.ListenReady))
	}
	if timing.ListenTimeouts > 0 {
		am.listenTimeoutHits.Add(int64(timing.ListenTimeouts))
	}

	if sendBytes > 0 {
		am.sendBytes.Add(int64(sendBytes))
	}
	if recvBytes > 0 {
		am.recvBytes.Add(int64(recvBytes))
	}
	am.byteSampleCount.Add(1)

	switch result {
	case ResultSuccess:
		am.successCount.Add(1)
	case ResultFailure:
		am.failureCount.Add(1)
		if err != nil {
			c.recordError(am, err)
		}
	case ResultTimeout:
		am.timeoutCount.Add(1)
		if wallClock > 0 {
			am.timeoutTotalNs.Add(wallClock.Nanoseconds())
		}
		if err != nil {
			c.recordError(am, err)
		}
	case ResultCanceled:
		am.canceledCount.Add(1)
	}
	if !c.windowOnly {
		c.currentWindowCollector().recordAction(name, result, timing, wallClock, sendBytes, recvBytes, err, false)
	}
}

// recordError 记录错误到分布 map，按 code 聚合。
func (c *MetricsCollector) recordError(am *actionMetrics, err error) {
	ce, ok := errors.AsType[CodedError](err)
	if !ok {
		return
	}
	key := errKey{Code: ce.ErrorCode()}
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
// 全部对 nil receiver 安全：monitor 未 Init（global==nil）时静默 no-op，
// 避免热路径 monitor.Global().RobotXxx() 在未启用监控时 panic。
func (c *MetricsCollector) RobotStarted() {
	if c != nil && c.enabled {
		c.robotsStarted.Add(1)
	}
}
func (c *MetricsCollector) RobotRunning() {
	if c != nil && c.enabled {
		c.robotsRunning.Add(1)
	}
}
func (c *MetricsCollector) RobotStopped() {
	if c != nil && c.enabled {
		c.robotsRunning.Add(-1)
		c.robotsStopped.Add(1)
	}
}
func (c *MetricsCollector) RobotErrored() {
	if c != nil && c.enabled {
		c.robotsRunning.Add(-1)
		c.robotsErrored.Add(1)
	}
}

// SetRampUpStage 设置渐进加压当前阶段（由 Manager 在每个阶段开始时调用）。
// current 为 1-based 阶段序号，total 为总阶段数。传 0,0 表示加压结束或未启用。
func (c *MetricsCollector) SetRampUpStage(current, total int) {
	if c != nil && c.enabled {
		c.rampUpCurrentStage.Store(int32(current))
		c.rampUpTotalStages.Store(int32(total))
	}
}

// RampUpStage 返回当前渐进加压阶段（1-based）和总阶段数。均为 0 表示未启用。
func (c *MetricsCollector) RampUpStage() (current, total int) {
	if c == nil {
		return 0, 0
	}
	return int(c.rampUpCurrentStage.Load()), int(c.rampUpTotalStages.Load())
}

// 连接生命周期钩子。对 nil receiver 安全（monitor 未 Init 时 no-op）。
func (c *MetricsCollector) ConnEstablished() {
	if c != nil && c.enabled {
		c.connEstablished.Add(1)
		c.connActive.Add(1)
	}
}
func (c *MetricsCollector) ConnClosed() {
	if c != nil && c.enabled {
		c.connActive.Add(-1)
		c.connClosed.Add(1)
	}
}
func (c *MetricsCollector) ConnFailed() {
	if c != nil && c.enabled {
		c.connFailed.Add(1)
	}
}
func (c *MetricsCollector) ConnDropped() {
	if c != nil && c.enabled {
		c.connDropped.Add(1)
	}
}

// RecordCallback 记录一次推送回调结果。
func (c *MetricsCollector) RecordCallback(
	name string, result ActionResult,
	timing ActionTiming, wallClock time.Duration,
	sendBytes, recvBytes int, err error,
) {
	c.recordAction("callback:"+name, result, timing, wallClock, sendBytes, recvBytes, err, false)
}

// RecordCallbackSuccess 记录一次推送回调成功。
func (c *MetricsCollector) RecordCallbackSuccess(name string) {
	c.RecordCallback(name, ResultSuccess, ActionTiming{}, 0, 0, 0, nil)
}

// RecordCallbackError 记录一次推送回调失败。
func (c *MetricsCollector) RecordCallbackError(name string, err error) {
	c.RecordCallback(name, ResultFailure, ActionTiming{}, 0, 0, 0, err)
}

func (c *MetricsCollector) getOrCreateAction(name string) *actionMetrics {
	if v, ok := c.actions.Load(name); ok {
		return v.(*actionMetrics)
	}
	am := newActionMetrics(c.cfg.Sketch.RelativeAccuracy, c.cfg.Sketch.MaxBins)
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
	return time.Since(time.Unix(0, c.startTime.Load()))
}

// ActionNames 返回按首次出现顺序排列的 action 名称列表（快照副本）。
func (c *MetricsCollector) ActionNames() []string {
	c.namesMu.Lock()
	names := make([]string, len(c.names))
	copy(names, c.names)
	c.namesMu.Unlock()
	return names
}

// Enabled 返回是否启用。对 nil receiver 安全（返回 false）。
func (c *MetricsCollector) Enabled() bool {
	return c != nil && c.enabled
}

func (c *MetricsCollector) TimingDetail() TimingDetailLevel {
	if c == nil {
		return TimingRTTOnly
	}
	return c.timingDetail
}

// CollectErrors 收集指定 action 的错误分布（只读快照）。
func (am *actionMetrics) CollectErrors() []ErrorEntry {
	var entries []ErrorEntry
	am.errors.Range(func(key, value any) bool {
		k := key.(errKey)
		count, msgs := value.(*errorBucket).snapshot()
		entries = append(entries, ErrorEntry{
			Code:     k.Code,
			CodeName: errcode.ErrorCode(k.Code).String(),
			Messages: msgs,
			Count:    count,
		})
		return true
	})
	return entries
}
