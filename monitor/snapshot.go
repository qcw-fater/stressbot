package monitor

import (
	"fmt"
	"runtime"
	"slices"
	"time"
)

// SystemSnapshot 系统资源快照。
type SystemSnapshot struct {
	Goroutines int     `json:"goroutines"` // 当前 goroutine 数量
	MemAllocMB float64 `json:"memAllocMB"` // 已分配堆内存（MB）
	MemSysMB   float64 `json:"memSysMB"`   // 从系统申请的总内存（MB）
	GCCount    uint32  `json:"gcCount"`    // GC 完成次数
}

// ConnectionSnapshot 连接指标快照。
type ConnectionSnapshot struct {
	Established int64 `json:"established"` // 累计成功建立的连接数
	Active      int64 `json:"active"`      // 当前活跃连接数
	Closed      int64 `json:"closed"`      // 累计关闭连接数
	Failed      int64 `json:"failed"`      // 累计连接建立失败数
	Dropped     int64 `json:"dropped"`     // 累计连接意外断开数
}

// BandwidthSnapshot 全局带宽快照。
type BandwidthSnapshot struct {
	TotalSendBytes int64   `json:"totalSendBytes"` // 累计发送字节数
	TotalRecvBytes int64   `json:"totalRecvBytes"` // 累计接收字节数
	SendMBps       float64 `json:"sendMBps"`       // 平均发送速率（MB/s）
	RecvMBps       float64 `json:"recvMBps"`       // 平均接收速率（MB/s）
}

// ActionKind 动作的网络语义分类，决定「哪个耗时指标才是它的主指标」。
//
// 由运行期事实判定，不依赖配置：声明式和 Lua 动作都按它实际发生过什么来定型，
// 因为 Lua 脚本里做了什么只有跑起来才知道。判定优先级
// networked > listen > send > local——只要发生过往返，它就是往返类。
type ActionKind string

const (
	// ActionKindNetworked 往返类：发起过请求-响应（成功或失败）。主指标是 RTT，打 Apdex。
	ActionKindNetworked ActionKind = "networked"
	// ActionKindListen 监听类：只等服务端推送，不发起请求。主指标是等待时长
	// （开始等待 → 帧被内核收到），出分布不打分。
	//
	// 不打 Apdex 是因为这段时长的主体是服务端业务（匹配、等队友、等开局），秒级且
	// 每个动作的合理值天差地别，套一个全局毫秒级 T 只会让它们齐刷刷 frustrated——
	// 那个分数不携带任何信息。要看的是 P50/P99 和超时率。
	ActionKindListen ActionKind = "listen"
	// ActionKindSend 发送类：只发不等（即发即忘）。主指标是执行耗时，无服务端成分，不打分。
	ActionKindSend ActionKind = "send"
	// ActionKindLocal 本地类：无网络行为（setState、纯计算）。主指标是执行耗时，不打分。
	//
	// 本地动作必然 satisfied，若参与 Apdex 会系统性抬高总分——动作越多分越高，
	// 与服务端表现无关。
	ActionKindLocal ActionKind = "local"
)

// ActionSnapshot per-action 完整快照（只读，用于 JSON/CSV/控制台输出）。
//
// RTT 字段含义说明（参见 plans/rtt-timing-breakdown.md）：
//   - RTT 直方图记录的是"纯网络往返"耗时（不含客户端 proto 构建/解析等开销）。
//   - 只统计有完整响应帧且 WireRTT > 0 的 request 样本；服务端 headerErr/失败响应也计入，
//     超时、发送失败、取消且未收到响应帧的分支不产生 RTT 样本。
//   - 但 RTT Apdex 的分母额外含 RTTFailedCount（无响应帧的请求，按 frustrated 计）：
//     直方图要干净（没有时延值就不能进），计分要完整（最慢的样本不能缺席）。
//   - 因此 avg/p50/p95/p99 的分母是 RTTSampleCount，而不是 action 成功数或总样本数。
//   - 纯客户端动作（如 lua 内仅做 connect/set_secret_key/set_state）RTTSampleCount=0，
//     此时 RTT.Count=0，前端 ActionsTab 应显示 "—"。
//   - TotalDuration 直方图记录的是 action 从开始执行到结束的总耗时（wallClock），
//     单次 action 最多产生 1 个样本，包含 RTT、监听等待、Lua 逻辑、解析和状态写入等。
//   - ClientAvgMs 反映客户端构建/解析平均耗时，所有结果分支累计，独立于网络指标。
type ActionSnapshot struct {
	Name                      string            `json:"name"`                      // 动作名称
	SampleCount               int64             `json:"sampleCount"`               // 总样本数（成功 + 失败 + 超时）
	SuccessCount              int64             `json:"successCount"`              // 成功次数
	FailureCount              int64             `json:"failureCount"`              // 失败次数（非超时）
	TimeoutCount              int64             `json:"timeoutCount"`              // 超时次数
	CanceledCount             int64             `json:"canceledCount"`             // 取消次数
	Executing                 int64             `json:"executing"`                 // 当前执行中的并发数
	SuccessRate               float64           `json:"successRate"`               // 成功率（0~1）
	AvgSendBytes              float64           `json:"avgSendBytes"`              // 平均每次完成/记录的发送 WireBytes
	AvgRecvBytes              float64           `json:"avgRecvBytes"`              // 平均每次完成/记录的接收 WireBytes
	ByteSampleCount           int64             `json:"byteSampleCount"`           // 实际开始并形成记录的动作数
	Kind                      ActionKind        `json:"kind"`                      // 动作分类，决定哪个耗时是主指标；仅 networked 打 Apdex
	RTTApdex                  float64           `json:"rttApdex"`                  // RTT Apdex 评分（0~1），仅 Kind=networked 时有意义
	RTT                       HistogramSnapshot `json:"rtt"`                       // RTT 直方图快照（WireRTT）
	ListenWait                HistogramSnapshot `json:"listenWait"`                // 监听等待直方图（开始等待 → 帧被内核收到），Kind=listen 的主指标
	ListenReadyCount          int64             `json:"listenReadyCount"`          // 命中时消息已在队列的次数：等待时长不可测，不进分布
	ListenTimeoutCount        int64             `json:"listenTimeoutCount"`        // 监听超时次数（不进分布，单独成率）
	ListenTimeoutRate         float64           `json:"listenTimeoutRate"`         // 监听超时率 = 超时 /（命中 + 已就绪 + 超时）
	TotalDuration             HistogramSnapshot `json:"totalDuration"`             // action 总耗时直方图快照（wallClock），仅作诊断
	TimeoutAvgMs              float64           `json:"timeoutAvgMs"`              // 平均超时延迟（毫秒）
	ClientAvgMs               float64           `json:"nonRTTAvgMs"`               // 非 RTT 平均耗时（毫秒）
	BuildAvgMs                float64           `json:"buildAvgMs"`                // 构建平均耗时（毫秒）
	EncodeAvgMs               float64           `json:"encodeAvgMs"`               // 编码平均耗时（毫秒）
	SendAvgMs                 float64           `json:"sendAvgMs"`                 // 发送平均耗时（毫秒）
	DecodeWaitAvgMs           float64           `json:"decodeWaitAvgMs"`           // decode 排队平均耗时（毫秒）
	DecodeAvgMs               float64           `json:"decodeAvgMs"`               // 解码平均耗时（毫秒）
	DispatchToActionWaitAvgMs float64           `json:"dispatchToActionWaitAvgMs"` // 分发到 action 平均等待（毫秒）
	ParseStoreAvgMs           float64           `json:"parseStoreAvgMs"`           // 解析和状态写入平均耗时（毫秒）
	BuildSampleCount          int64             `json:"buildSampleCount"`
	EncodeSampleCount         int64             `json:"encodeSampleCount"`
	SendSampleCount           int64             `json:"sendSampleCount"`
	DecodeWaitSampleCount     int64             `json:"decodeWaitSampleCount"`
	DecodeSampleCount         int64             `json:"decodeSampleCount"`
	DispatchWaitSampleCount   int64             `json:"dispatchWaitSampleCount"`
	ParseStoreSampleCount     int64             `json:"parseStoreSampleCount"`
	RTTSampleCount            int64             `json:"rttSampleCount"`           // 有完整响应帧且 WireRTT > 0 的 request 数
	RTTApdexSampleCount       int64             `json:"rttApdexSampleCount"`      // RTT Apdex 分母：有响应帧样本 + 无响应帧失败请求
	ListenWaitSampleCount     int64             `json:"listenWaitSampleCount"`    // 等待时长可测的监听命中数
	TotalDurationSampleCount  int64             `json:"totalDurationSampleCount"` // 总耗时 action 样本数
	AvgQPS                    float64           `json:"avgQps"`                   // 全周期平均 QPS
	PeriodQPS                 float64           `json:"periodQps"`                // 上次快照到当前的区间 QPS
	Errors                    []ErrorEntry      `json:"errors,omitempty"`         // 错误分布（仅失败/超时时有值）

	// 跨节点聚合所需的原始计数。延迟原始分布直接位于各 HistogramSnapshot.Sketch。
	ApdexSatisfied    int64 `json:"apdexSatisfied,omitempty"`
	ApdexTolerating   int64 `json:"apdexTolerating,omitempty"`
	RTTFailedCount    int64 `json:"rttFailedCount,omitempty"`
	TotalSendBytes    int64 `json:"totalSendBytes,omitempty"`
	TotalRecvBytes    int64 `json:"totalRecvBytes,omitempty"`
	TimeoutTotalNs    int64 `json:"timeoutTotalNs,omitempty"`
	ClientCostSumNs   int64 `json:"clientCostSumNs,omitempty"`
	ClientCostCount   int64 `json:"clientCostCount"`
	BuildCostSumNs    int64 `json:"buildCostSumNs,omitempty"`
	EncodeCostSumNs   int64 `json:"encodeCostSumNs,omitempty"`
	SendCostSumNs     int64 `json:"sendCostSumNs,omitempty"`
	DecodeWaitSumNs   int64 `json:"decodeWaitSumNs,omitempty"`
	DecodeCostSumNs   int64 `json:"decodeCostSumNs,omitempty"`
	DispatchWaitSumNs int64 `json:"dispatchWaitSumNs,omitempty"`
	ParseStoreSumNs   int64 `json:"parseStoreSumNs,omitempty"`
}

// SnapshotSummary 是跨动作的统一汇总指标。百分位数由原始直方图合并后重算，
// Apdex 由原始满意/容忍计数和完整请求分母计算；展示层不得再从单动作指标二次聚合。
type SnapshotSummary struct {
	SampleCount               int64             `json:"sampleCount"`
	SuccessCount              int64             `json:"successCount"`
	FailureCount              int64             `json:"failureCount"`
	TimeoutCount              int64             `json:"timeoutCount"`
	CanceledCount             int64             `json:"canceledCount"`
	Executing                 int64             `json:"executing"`
	SuccessRate               float64           `json:"successRate"`
	RTTApdex                  float64           `json:"rttApdex"`
	RTTApdexSampleCount       int64             `json:"rttApdexSampleCount"`
	RTT                       HistogramSnapshot `json:"rtt"`
	ListenWait                HistogramSnapshot `json:"listenWait"`
	TotalDuration             HistogramSnapshot `json:"totalDuration"`
	ClientAvgMs               float64           `json:"nonRTTAvgMs"`
	ClientCostCount           int64             `json:"clientCostCount"`
	BuildAvgMs                float64           `json:"buildAvgMs"`
	EncodeAvgMs               float64           `json:"encodeAvgMs"`
	SendAvgMs                 float64           `json:"sendAvgMs"`
	DecodeWaitAvgMs           float64           `json:"decodeWaitAvgMs"`
	DecodeAvgMs               float64           `json:"decodeAvgMs"`
	DispatchToActionWaitAvgMs float64           `json:"dispatchToActionWaitAvgMs"`
	ParseStoreAvgMs           float64           `json:"parseStoreAvgMs"`
	BuildSampleCount          int64             `json:"buildSampleCount"`
	EncodeSampleCount         int64             `json:"encodeSampleCount"`
	SendSampleCount           int64             `json:"sendSampleCount"`
	DecodeWaitSampleCount     int64             `json:"decodeWaitSampleCount"`
	DecodeSampleCount         int64             `json:"decodeSampleCount"`
	DispatchWaitSampleCount   int64             `json:"dispatchWaitSampleCount"`
	ParseStoreSampleCount     int64             `json:"parseStoreSampleCount"`
	AvgQPS                    float64           `json:"avgQps"`
}

// RobotSnapshot 机器人状态快照。
type RobotSnapshot struct {
	Started int64 `json:"started"` // 已启动的机器人总数
	Running int64 `json:"running"` // 当前运行中的机器人数量
	Stopped int64 `json:"stopped"` // 正常停止的机器人数量
	Errored int64 `json:"errored"` // 异常退出的机器人数量
}

// RampUpSnapshot 渐进加压阶段快照。
type RampUpSnapshot struct {
	CurrentStage int `json:"currentStage"` // 当前阶段（1-based，0 = 未启用）
	TotalStages  int `json:"totalStages"`  // 总阶段数（0 = 未启用）
}

// CollectorSnapshot 全局指标快照，包含系统、机器人、连接、带宽和所有 action 的聚合数据。
type CollectorSnapshot struct {
	Timestamp            time.Time          `json:"timestamp"`       // 快照时间
	CollectionEpoch      uint64             `json:"collectionEpoch"` // 累计指标代次，Reset 后递增
	Uptime               time.Duration      `json:"uptime"`          // 运行时长
	UptimeSec            float64            `json:"uptimeSeconds"`   // 运行时长（秒）
	TotalActions         int64              `json:"totalActions"`    // 累计动作总数
	ApdexT               int                `json:"apdexT"`          // 当前 Apdex T 阈值（毫秒）
	TimingDetail         TimingDetailLevel  `json:"timingDetail"`    // 实际计时细分级别：rtt / codec / full
	Summary              SnapshotSummary    `json:"summary"`         // 跨动作统一汇总，前端和历史采样的唯一总指标来源
	System               SystemSnapshot     `json:"system"`          // 系统资源快照
	Robots               RobotSnapshot      `json:"robots"`          // 机器人状态快照
	RampUp               RampUpSnapshot     `json:"rampUp"`          // 渐进加压阶段快照
	Connections          ConnectionSnapshot `json:"connections"`     // 连接指标快照
	Bandwidth            BandwidthSnapshot  `json:"bandwidth"`       // 带宽快照
	Actions              []ActionSnapshot   `json:"actions"`         // 所有 action 的快照列表
	InvalidMetricSamples int64              `json:"invalidMetricSamples"`
	Window               *ReportWindow      `json:"window"`
}

type ReportMeta struct {
	Sequence                uint64
	StartedAt               time.Time
	EndedAt                 time.Time
	ExpectedIntervalSeconds float64
}

type WindowBandwidthSnapshot struct {
	SendBytes int64   `json:"sendBytes"`
	RecvBytes int64   `json:"recvBytes"`
	SendMBps  float64 `json:"sendMBps"`
	RecvMBps  float64 `json:"recvMBps"`
}

type ReportWindow struct {
	Sequence                uint64                  `json:"sequence,omitempty"`
	StartedAt               time.Time               `json:"startedAt"`
	EndedAt                 time.Time               `json:"endedAt"`
	DurationSeconds         float64                 `json:"durationSeconds"`
	ExpectedIntervalSeconds float64                 `json:"expectedIntervalSeconds"`
	Summary                 SnapshotSummary         `json:"summary"`
	Bandwidth               WindowBandwidthSnapshot `json:"bandwidth"`
	Actions                 []ActionSnapshot        `json:"actions"`
	InvalidMetricSamples    int64                   `json:"invalidMetricSamples"`
}

// PublicCopy returns a snapshot suitable for public HTTP and UI responses.
// DDSketch payloads are an internal Agent/Admin merge format and must not be
// exposed or accidentally retained by presentation consumers.
func (s *CollectorSnapshot) PublicCopy() *CollectorSnapshot {
	if s == nil {
		return nil
	}
	public := *s
	public.Summary = publicSummaryCopy(s.Summary)
	public.Actions = publicActionCopies(s.Actions)
	if s.Window != nil {
		window := *s.Window
		window.Sequence = 0
		window.Summary = publicSummaryCopy(s.Window.Summary)
		window.Actions = publicActionCopies(s.Window.Actions)
		public.Window = &window
	}
	return &public
}

func publicSummaryCopy(summary SnapshotSummary) SnapshotSummary {
	summary.RTT.Sketch = nil
	summary.RTT.SumNs = 0
	summary.ListenWait.Sketch = nil
	summary.ListenWait.SumNs = 0
	summary.TotalDuration.Sketch = nil
	summary.TotalDuration.SumNs = 0
	return summary
}

func publicActionCopies(actions []ActionSnapshot) []ActionSnapshot {
	if actions == nil {
		return nil
	}
	public := make([]ActionSnapshot, len(actions))
	for i := range actions {
		public[i] = actions[i]
		public[i].RTT.Sketch = nil
		public[i].RTT.SumNs = 0
		public[i].ListenWait.Sketch = nil
		public[i].ListenWait.SumNs = 0
		public[i].TotalDuration.Sketch = nil
		public[i].TotalDuration.SumNs = 0
		public[i].ApdexSatisfied = 0
		public[i].ApdexTolerating = 0
		public[i].RTTFailedCount = 0
		public[i].TotalSendBytes = 0
		public[i].TotalRecvBytes = 0
		public[i].TimeoutTotalNs = 0
		public[i].ClientCostSumNs = 0
		public[i].BuildCostSumNs = 0
		public[i].EncodeCostSumNs = 0
		public[i].SendCostSumNs = 0
		public[i].DecodeWaitSumNs = 0
		public[i].DecodeCostSumNs = 0
		public[i].DispatchWaitSumNs = 0
		public[i].ParseStoreSumNs = 0
		if actions[i].Errors != nil {
			public[i].Errors = make([]ErrorEntry, len(actions[i].Errors))
			for j := range actions[i].Errors {
				public[i].Errors[j] = actions[i].Errors[j]
				public[i].Errors[j].Messages = slices.Clone(actions[i].Errors[j].Messages)
			}
		}
	}
	return public
}

// Snapshot 生成当前全局快照。
// prevCounts 由调用方（Reporter）维护，用于计算 periodQPS。传 nil 表示不计算。
func (c *MetricsCollector) Snapshot(prevCounts map[string]int64, periodSec float64) *CollectorSnapshot {
	if !c.windowOnly {
		c.transitionMu.RLock()
		defer c.transitionMu.RUnlock()
	}
	return c.snapshotUnlocked(prevCounts, periodSec)
}

func (c *MetricsCollector) snapshotUnlocked(prevCounts map[string]int64, periodSec float64) *CollectorSnapshot {
	if !c.enabled {
		// 即使监控关闭，Actions 字段也保证非 nil 以满足契约
		return &CollectorSnapshot{CollectionEpoch: c.collectionEpoch.Load(), TimingDetail: c.timingDetail, Actions: []ActionSnapshot{}}
	}
	uptime := c.Uptime()
	uptimeSec := uptime.Seconds()

	// 系统指标
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	sys := SystemSnapshot{
		Goroutines: runtime.NumGoroutine(),
		MemAllocMB: float64(m.Alloc) / 1024 / 1024,
		MemSysMB:   float64(m.Sys) / 1024 / 1024,
		GCCount:    m.NumGC,
	}

	// 带宽
	totalSend := c.totalSendBytes.Load()
	totalRecv := c.totalRecvBytes.Load()
	var sendMBps, recvMBps float64
	if uptimeSec > 0 {
		sendMBps = float64(totalSend) / 1024 / 1024 / uptimeSec
		recvMBps = float64(totalRecv) / 1024 / 1024 / uptimeSec
	}

	apdexT := int(c.apdexT.Load())
	snap := &CollectorSnapshot{
		Timestamp:            time.Now(),
		CollectionEpoch:      c.collectionEpoch.Load(),
		Uptime:               uptime,
		UptimeSec:            uptimeSec,
		TotalActions:         c.totalActions.Load(),
		ApdexT:               apdexT,
		TimingDetail:         c.timingDetail,
		InvalidMetricSamples: c.invalidMetricSamples.Load(),
		System:               sys,
		Robots: RobotSnapshot{
			Started: c.robotsStarted.Load(),
			Running: c.robotsRunning.Load(),
			Stopped: c.robotsStopped.Load(),
			Errored: c.robotsErrored.Load(),
		},
		RampUp: RampUpSnapshot{
			CurrentStage: int(c.rampUpCurrentStage.Load()),
			TotalStages:  int(c.rampUpTotalStages.Load()),
		},
		Connections: ConnectionSnapshot{
			Established: c.connEstablished.Load(),
			Active:      c.connActive.Load(),
			Closed:      c.connClosed.Load(),
			Failed:      c.connFailed.Load(),
			Dropped:     c.connDropped.Load(),
		},
		Bandwidth: BandwidthSnapshot{
			TotalSendBytes: totalSend,
			TotalRecvBytes: totalRecv,
			SendMBps:       sendMBps,
			RecvMBps:       recvMBps,
		},
	}

	for _, name := range c.ActionNames() {
		v, ok := c.actions.Load(name)
		if !ok {
			continue
		}
		am := v.(*actionMetrics)
		am.mu.Lock()

		succ := am.successCount.Load()
		fail := am.failureCount.Load()
		tout := am.timeoutCount.Load()
		canceled := am.canceledCount.Load()
		exec := am.executing.Load()
		total := succ + fail + tout

		timeoutTotalNs := am.timeoutTotalNs.Load()
		var timeoutAvgMs float64
		if tout > 0 {
			timeoutAvgMs = float64(timeoutTotalNs) / float64(tout) / 1e6
		}

		satisfied := am.apdexSatisfied.Load()
		tolerating := am.apdexTolerating.Load()
		rttSamples := am.rttSampleCount.Load()
		rttFailed := am.rttFailedCount.Load()
		listenWaitSamples := am.listenWaitCount.Load()
		listenReady := am.listenReadyCount.Load()
		listenTimeouts := am.listenTimeoutHits.Load()
		totalDurationSamples := am.totalDurationSampleCount.Load()
		clientCostSum := am.clientCostSum.Load()
		clientCostCount := am.clientCostCount.Load()

		avgMs := func(sum int64, count int64) float64 {
			if count <= 0 {
				return 0
			}
			return float64(sum) / float64(count) / 1e6
		}
		clientAvgMs := avgMs(clientCostSum, clientCostCount)
		buildSamples := am.buildCostCount.Load()
		encodeSamples := am.encodeCostCount.Load()
		sendSamples := am.sendCostCount.Load()
		decodeWaitSamples := am.decodeWaitCount.Load()
		decodeSamples := am.decodeCostCount.Load()
		dispatchWaitSamples := am.dispatchWaitCount.Load()
		parseStoreSamples := am.parseStoreCount.Load()
		buildCostSum := am.buildCostSum.Load()
		encodeCostSum := am.encodeCostSum.Load()
		sendCostSum := am.sendCostSum.Load()
		decodeWaitSum := am.decodeWaitSum.Load()
		decodeCostSum := am.decodeCostSum.Load()
		dispatchWaitSum := am.dispatchWaitSum.Load()
		parseStoreSum := am.parseStoreSum.Load()
		buildAvgMs := avgMs(buildCostSum, buildSamples)
		encodeAvgMs := avgMs(encodeCostSum, encodeSamples)
		sendAvgMs := avgMs(sendCostSum, sendSamples)
		decodeWaitAvgMs := avgMs(decodeWaitSum, decodeWaitSamples)
		decodeAvgMs := avgMs(decodeCostSum, decodeSamples)
		dispatchWaitAvgMs := avgMs(dispatchWaitSum, dispatchWaitSamples)
		parseStoreAvgMs := avgMs(parseStoreSum, parseStoreSamples)

		// RTT Apdex 分母 = 有响应帧的样本 + 无响应帧的失败请求（后者按 frustrated 计）。
		//   - 纯客户端动作（两者皆 0）不参与，避免大量「成功但无网络往返」的样本把分数虚抬；
		//   - 失败请求必须在分母里：它们是服务端表现最差的那批，从分母漏掉会让指标反向失真
		//     （超时越多、分数越高）。
		rttApdexSamples := rttSamples + rttFailed
		var rttApdex float64
		if rttApdexSamples > 0 {
			rttApdex = (float64(satisfied) + float64(tolerating)*0.5) / float64(rttApdexSamples)
		}
		listenTotal := listenWaitSamples + listenReady + listenTimeouts
		var listenTimeoutRate float64
		if listenTotal > 0 {
			listenTimeoutRate = float64(listenTimeouts) / float64(listenTotal)
		}
		kind := classifyActionKind(rttApdexSamples, listenTotal, am.sendBytes.Load())

		var successRate float64
		if total > 0 {
			successRate = float64(succ) / float64(total)
		}
		var avgSend, avgRecv float64
		totalSendBytes := am.sendBytes.Load()
		totalRecvBytes := am.recvBytes.Load()
		byteSamples := am.byteSampleCount.Load()
		if byteSamples > 0 {
			avgSend = float64(totalSendBytes) / float64(byteSamples)
			avgRecv = float64(totalRecvBytes) / float64(byteSamples)
		}

		rttSnap := am.rtt.Snapshot()
		listenWaitSnap := am.listenWait.Snapshot()
		totalDurationSnap := am.totalDuration.Snapshot()

		var avgQPS, periodQPS float64
		if uptimeSec > 0 {
			avgQPS = float64(total) / uptimeSec
		}
		if periodSec > 0 && prevCounts != nil {
			prev := prevCounts[name]
			diff := total - prev
			if diff > 0 {
				periodQPS = float64(diff) / periodSec
			}
			prevCounts[name] = total
		}

		var errs []ErrorEntry
		if fail > 0 || tout > 0 {
			errs = am.CollectErrors()
		}

		snap.Actions = append(snap.Actions, ActionSnapshot{
			Name:                      name,
			SampleCount:               total,
			SuccessCount:              succ,
			FailureCount:              fail,
			TimeoutCount:              tout,
			CanceledCount:             canceled,
			TimeoutAvgMs:              timeoutAvgMs,
			ClientAvgMs:               clientAvgMs,
			BuildAvgMs:                buildAvgMs,
			EncodeAvgMs:               encodeAvgMs,
			SendAvgMs:                 sendAvgMs,
			DecodeWaitAvgMs:           decodeWaitAvgMs,
			DecodeAvgMs:               decodeAvgMs,
			DispatchToActionWaitAvgMs: dispatchWaitAvgMs,
			ParseStoreAvgMs:           parseStoreAvgMs,
			BuildSampleCount:          buildSamples,
			EncodeSampleCount:         encodeSamples,
			SendSampleCount:           sendSamples,
			DecodeWaitSampleCount:     decodeWaitSamples,
			DecodeSampleCount:         decodeSamples,
			DispatchWaitSampleCount:   dispatchWaitSamples,
			ParseStoreSampleCount:     parseStoreSamples,
			RTTSampleCount:            rttSamples,
			RTTApdexSampleCount:       rttApdexSamples,
			ListenWaitSampleCount:     listenWaitSamples,
			TotalDurationSampleCount:  totalDurationSamples,
			Executing:                 exec,
			SuccessRate:               successRate,
			AvgSendBytes:              avgSend,
			AvgRecvBytes:              avgRecv,
			ByteSampleCount:           byteSamples,
			Kind:                      kind,
			RTTApdex:                  rttApdex,
			RTT:                       rttSnap,
			ListenWait:                listenWaitSnap,
			ListenReadyCount:          listenReady,
			ListenTimeoutCount:        listenTimeouts,
			ListenTimeoutRate:         listenTimeoutRate,
			TotalDuration:             totalDurationSnap,
			AvgQPS:                    avgQPS,
			PeriodQPS:                 periodQPS,
			Errors:                    errs,
			ApdexSatisfied:            satisfied,
			ApdexTolerating:           tolerating,
			RTTFailedCount:            rttFailed,
			TotalSendBytes:            totalSendBytes,
			TotalRecvBytes:            totalRecvBytes,
			TimeoutTotalNs:            timeoutTotalNs,
			ClientCostSumNs:           clientCostSum,
			ClientCostCount:           clientCostCount,
			BuildCostSumNs:            buildCostSum,
			EncodeCostSumNs:           encodeCostSum,
			SendCostSumNs:             sendCostSum,
			DecodeWaitSumNs:           decodeWaitSum,
			DecodeCostSumNs:           decodeCostSum,
			DispatchWaitSumNs:         dispatchWaitSum,
			ParseStoreSumNs:           parseStoreSum,
		})
		am.mu.Unlock()
	}
	// 契约保证：Actions 字段在 JSON 中始终是数组，不是 null。
	// 历史 bug：stopping 阶段或刚启动还没动作时，Actions 是 nil slice →
	// JSON 序列化成 "actions": null → 前端 `for...of snapshot.actions` 抛
	// "snapshot.actions is not iterable" 把整个 EditorPage 崩掉。
	// 在源头初始化为空切片，前端任何调用点都不再需要 `?? []` 兜底。
	if snap.Actions == nil {
		snap.Actions = []ActionSnapshot{}
	}
	snap.Summary = BuildSnapshotSummary(snap.Actions)
	return snap
}

// TakeReportSnapshot 在一个记录边界上切换活动区间，并返回同一时刻的累计快照。
func (c *MetricsCollector) TakeReportSnapshot(meta ReportMeta) *CollectorSnapshot {
	if c == nil || !c.enabled {
		return &CollectorSnapshot{Actions: []ActionSnapshot{}, Window: &ReportWindow{Actions: []ActionSnapshot{}}}
	}
	duration := meta.EndedAt.Sub(meta.StartedAt).Seconds()
	if duration <= 0 {
		duration = meta.ExpectedIntervalSeconds
	}
	if duration <= 0 {
		duration = 1
	}

	c.transitionMu.Lock()
	oldWindow := c.currentWindowCollector()
	c.window.Store(c.newWindowCollector(meta.EndedAt))
	cumulative := c.snapshotUnlocked(nil, 0)
	c.transitionMu.Unlock()

	interval := oldWindow.snapshotUnlocked(nil, 0)
	for i := range interval.Actions {
		interval.Actions[i].AvgQPS = float64(interval.Actions[i].SampleCount) / duration
		interval.Actions[i].PeriodQPS = interval.Actions[i].AvgQPS
	}
	interval.Summary = BuildSnapshotSummary(interval.Actions)
	interval.Summary.AvgQPS = float64(interval.Summary.SampleCount) / duration
	sendMBps := float64(interval.Bandwidth.TotalSendBytes) / 1024 / 1024 / duration
	recvMBps := float64(interval.Bandwidth.TotalRecvBytes) / 1024 / 1024 / duration
	cumulative.Window = &ReportWindow{
		Sequence:                meta.Sequence,
		StartedAt:               meta.StartedAt,
		EndedAt:                 meta.EndedAt,
		DurationSeconds:         duration,
		ExpectedIntervalSeconds: meta.ExpectedIntervalSeconds,
		Summary:                 interval.Summary,
		Bandwidth: WindowBandwidthSnapshot{
			SendBytes: interval.Bandwidth.TotalSendBytes,
			RecvBytes: interval.Bandwidth.TotalRecvBytes,
			SendMBps:  sendMBps,
			RecvMBps:  recvMBps,
		},
		Actions:              interval.Actions,
		InvalidMetricSamples: interval.InvalidMetricSamples,
	}
	return cumulative
}

// BuildSnapshotSummary 从动作原始计数和直方图构建跨动作汇总。
func BuildSnapshotSummary(actions []ActionSnapshot) SnapshotSummary {
	summary, err := buildSnapshotSummary(actions)
	if err != nil {
		panic("监控内部延迟分布损坏: " + err.Error())
	}
	return summary
}

func buildSnapshotSummary(actions []ActionSnapshot) (SnapshotSummary, error) {
	var summary SnapshotSummary
	rttSnaps := make([]HistogramSnapshot, 0, len(actions))
	listenWaitSnaps := make([]HistogramSnapshot, 0, len(actions))
	totalDurationSnaps := make([]HistogramSnapshot, 0, len(actions))
	var apdexPoints float64
	var clientWeight, clientSumNs int64
	var buildSumNs, encodeSumNs, sendSumNs, decodeWaitSumNs, decodeSumNs, dispatchSumNs, parseStoreSumNs int64

	for _, action := range actions {
		summary.SampleCount += action.SampleCount
		summary.SuccessCount += action.SuccessCount
		summary.FailureCount += action.FailureCount
		summary.TimeoutCount += action.TimeoutCount
		summary.CanceledCount += action.CanceledCount
		summary.Executing += action.Executing
		summary.AvgQPS += action.AvgQPS

		denominator := action.RTTApdexSampleCount
		summary.RTTApdexSampleCount += denominator
		apdexPoints += float64(action.ApdexSatisfied) + float64(action.ApdexTolerating)*0.5

		if action.RTT.Count > 0 {
			rttSnaps = append(rttSnaps, action.RTT)
		}
		if action.ListenWait.Count > 0 {
			listenWaitSnaps = append(listenWaitSnaps, action.ListenWait)
		}
		if action.TotalDuration.Count > 0 {
			totalDurationSnaps = append(totalDurationSnaps, action.TotalDuration)
		}

		weight := action.ClientCostCount
		if weight > 0 {
			clientWeight += weight
			clientSumNs += action.ClientCostSumNs
		}
		summary.BuildSampleCount += action.BuildSampleCount
		summary.EncodeSampleCount += action.EncodeSampleCount
		summary.SendSampleCount += action.SendSampleCount
		summary.DecodeWaitSampleCount += action.DecodeWaitSampleCount
		summary.DecodeSampleCount += action.DecodeSampleCount
		summary.DispatchWaitSampleCount += action.DispatchWaitSampleCount
		summary.ParseStoreSampleCount += action.ParseStoreSampleCount
		buildSumNs += action.BuildCostSumNs
		encodeSumNs += action.EncodeCostSumNs
		sendSumNs += action.SendCostSumNs
		decodeWaitSumNs += action.DecodeWaitSumNs
		decodeSumNs += action.DecodeCostSumNs
		dispatchSumNs += action.DispatchWaitSumNs
		parseStoreSumNs += action.ParseStoreSumNs
	}

	if summary.SampleCount > 0 {
		summary.SuccessRate = float64(summary.SuccessCount) / float64(summary.SampleCount)
	}
	if summary.RTTApdexSampleCount > 0 {
		summary.RTTApdex = apdexPoints / float64(summary.RTTApdexSampleCount)
	}
	var err error
	summary.RTT, err = MergeHistograms(rttSnaps)
	if err != nil {
		return SnapshotSummary{}, fmt.Errorf("合并 RTT 分布: %w", err)
	}
	summary.ListenWait, err = MergeHistograms(listenWaitSnaps)
	if err != nil {
		return SnapshotSummary{}, fmt.Errorf("合并监听等待分布: %w", err)
	}
	summary.TotalDuration, err = MergeHistograms(totalDurationSnaps)
	if err != nil {
		return SnapshotSummary{}, fmt.Errorf("合并总耗时分布: %w", err)
	}
	if clientWeight > 0 {
		summary.ClientAvgMs = divideMetricSumNs(clientSumNs, clientWeight)
	}
	summary.ClientCostCount = clientWeight
	summary.BuildAvgMs = divideMetricSumNs(buildSumNs, summary.BuildSampleCount)
	summary.EncodeAvgMs = divideMetricSumNs(encodeSumNs, summary.EncodeSampleCount)
	summary.SendAvgMs = divideMetricSumNs(sendSumNs, summary.SendSampleCount)
	summary.DecodeWaitAvgMs = divideMetricSumNs(decodeWaitSumNs, summary.DecodeWaitSampleCount)
	summary.DecodeAvgMs = divideMetricSumNs(decodeSumNs, summary.DecodeSampleCount)
	summary.DispatchToActionWaitAvgMs = divideMetricSumNs(dispatchSumNs, summary.DispatchWaitSampleCount)
	summary.ParseStoreAvgMs = divideMetricSumNs(parseStoreSumNs, summary.ParseStoreSampleCount)
	return summary, nil
}

func divideMetricSumNs(sumNs, count int64) float64 {
	if count == 0 {
		return 0
	}
	return float64(sumNs) / float64(count) / 1e6
}

// classifyActionKind 按运行期发生过的网络行为给动作定型。
//
// 优先级 networked > listen > send > local：一个动作可能兼有多种行为（Lua 脚本里
// 先请求再监听很常见），按「最强的语义」归类，保证它的主指标不会被弱语义盖掉。
func classifyActionKind(roundTrips, listenEvents, sendBytes int64) ActionKind {
	switch {
	case roundTrips > 0:
		return ActionKindNetworked
	case listenEvents > 0:
		return ActionKindListen
	case sendBytes > 0:
		return ActionKindSend
	default:
		return ActionKindLocal
	}
}

// MergeSnapshots 合并多个 CollectorSnapshot，用于分布式场景下聚合多 Agent 指标。
//
// 契约保证：返回的 *CollectorSnapshot 的 Actions 字段始终非 nil（最少是空切片），
// 避免前端 JSON 解析后调 for...of 抛 "actions is not iterable"。
func MergeSnapshots(snaps []*CollectorSnapshot) (*CollectorSnapshot, error) {
	valid := make([]*CollectorSnapshot, 0, len(snaps))
	for _, snap := range snaps {
		if snap != nil {
			valid = append(valid, snap)
		}
	}
	if len(valid) == 0 {
		return &CollectorSnapshot{TimingDetail: TimingRTTOnly, Actions: []ActionSnapshot{}}, nil
	}
	if len(valid) == 1 {
		out := *valid[0]
		if out.Actions == nil {
			out.Actions = []ActionSnapshot{}
		}
		out.TimingDetail = NormalizeTimingDetail(string(out.TimingDetail))
		var err error
		out.Summary, err = buildSnapshotSummary(out.Actions)
		if err != nil {
			return nil, err
		}
		return &out, nil
	}

	merged := &CollectorSnapshot{
		Timestamp:    time.Now(),
		ApdexT:       valid[0].ApdexT,
		TimingDetail: NormalizeTimingDetail(string(valid[0].TimingDetail)),
	}

	var maxUptime float64
	merged.RampUp.TotalStages = valid[0].RampUp.TotalStages // 所有 Agent 阶段数相同
	first := true
	for _, s := range valid {
		if s.CollectionEpoch > merged.CollectionEpoch {
			merged.CollectionEpoch = s.CollectionEpoch
		}
		if timingDetailRank(NormalizeTimingDetail(string(s.TimingDetail))) < timingDetailRank(merged.TimingDetail) {
			merged.TimingDetail = NormalizeTimingDetail(string(s.TimingDetail))
		}
		merged.TotalActions += s.TotalActions
		merged.InvalidMetricSamples += s.InvalidMetricSamples
		merged.Robots.Started += s.Robots.Started
		merged.Robots.Running += s.Robots.Running
		merged.Robots.Stopped += s.Robots.Stopped
		merged.Robots.Errored += s.Robots.Errored
		merged.Connections.Established += s.Connections.Established
		merged.Connections.Active += s.Connections.Active
		merged.Connections.Closed += s.Connections.Closed
		merged.Connections.Failed += s.Connections.Failed
		merged.Connections.Dropped += s.Connections.Dropped
		merged.Bandwidth.TotalSendBytes += s.Bandwidth.TotalSendBytes
		merged.Bandwidth.TotalRecvBytes += s.Bandwidth.TotalRecvBytes
		if s.UptimeSec > maxUptime {
			maxUptime = s.UptimeSec
		}
		// 取最小 currentStage：最慢的 Agent 决定整体阶段进度
		if s.RampUp.CurrentStage > 0 && (first || s.RampUp.CurrentStage < merged.RampUp.CurrentStage) {
			merged.RampUp.CurrentStage = s.RampUp.CurrentStage
			first = false
		}
	}
	merged.UptimeSec = maxUptime
	merged.Uptime = time.Duration(maxUptime * float64(time.Second))
	if maxUptime > 0 {
		merged.Bandwidth.SendMBps = float64(merged.Bandwidth.TotalSendBytes) / 1024 / 1024 / maxUptime
		merged.Bandwidth.RecvMBps = float64(merged.Bandwidth.TotalRecvBytes) / 1024 / 1024 / maxUptime
	}

	// 按 action name 合并
	type actionAgg struct {
		snaps []ActionSnapshot
	}
	actionMap := make(map[string]*actionAgg)
	var order []string
	for _, s := range valid {
		for _, a := range s.Actions {
			if _, ok := actionMap[a.Name]; !ok {
				actionMap[a.Name] = &actionAgg{}
				order = append(order, a.Name)
			}
			actionMap[a.Name].snaps = append(actionMap[a.Name].snaps, a)
		}
	}

	for _, name := range order {
		agg := actionMap[name]
		var ma ActionSnapshot
		ma.Name = name

		var rttSatisfied, rttTolerating int64
		for _, a := range agg.snaps {
			ma.SampleCount += a.SampleCount
			ma.SuccessCount += a.SuccessCount
			ma.FailureCount += a.FailureCount
			ma.TimeoutCount += a.TimeoutCount
			ma.CanceledCount += a.CanceledCount
			ma.Executing += a.Executing
			rttSatisfied += a.ApdexSatisfied
			rttTolerating += a.ApdexTolerating
			ma.TotalSendBytes += a.TotalSendBytes
			ma.TotalRecvBytes += a.TotalRecvBytes
			ma.TimeoutTotalNs += a.TimeoutTotalNs
			ma.ByteSampleCount += a.ByteSampleCount
			ma.RTTSampleCount += a.RTTSampleCount
			ma.RTTFailedCount += a.RTTFailedCount
			ma.ListenWaitSampleCount += a.ListenWaitSampleCount
			ma.ListenReadyCount += a.ListenReadyCount
			ma.ListenTimeoutCount += a.ListenTimeoutCount
			ma.TotalDurationSampleCount += a.TotalDurationSampleCount
			ma.ClientCostSumNs += a.ClientCostSumNs
			ma.ClientCostCount += a.ClientCostCount
			ma.BuildCostSumNs += a.BuildCostSumNs
			ma.EncodeCostSumNs += a.EncodeCostSumNs
			ma.SendCostSumNs += a.SendCostSumNs
			ma.DecodeWaitSumNs += a.DecodeWaitSumNs
			ma.DecodeCostSumNs += a.DecodeCostSumNs
			ma.DispatchWaitSumNs += a.DispatchWaitSumNs
			ma.ParseStoreSumNs += a.ParseStoreSumNs
			ma.BuildSampleCount += a.BuildSampleCount
			ma.EncodeSampleCount += a.EncodeSampleCount
			ma.SendSampleCount += a.SendSampleCount
			ma.DecodeWaitSampleCount += a.DecodeWaitSampleCount
			ma.DecodeSampleCount += a.DecodeSampleCount
			ma.DispatchWaitSampleCount += a.DispatchWaitSampleCount
			ma.ParseStoreSampleCount += a.ParseStoreSampleCount
		}

		// 合并延迟直方图
		rttSnaps := make([]HistogramSnapshot, len(agg.snaps))
		listenWaitSnaps := make([]HistogramSnapshot, len(agg.snaps))
		totalDurationSnaps := make([]HistogramSnapshot, len(agg.snaps))
		for i, a := range agg.snaps {
			rttSnaps[i] = a.RTT
			listenWaitSnaps[i] = a.ListenWait
			totalDurationSnaps[i] = a.TotalDuration
		}
		var err error
		ma.RTT, err = MergeHistograms(rttSnaps)
		if err != nil {
			return nil, fmt.Errorf("合并动作 %q RTT 分布: %w", name, err)
		}
		ma.ApdexSatisfied = rttSatisfied
		ma.ApdexTolerating = rttTolerating
		ma.ListenWait, err = MergeHistograms(listenWaitSnaps)
		if err != nil {
			return nil, fmt.Errorf("合并动作 %q 监听等待分布: %w", name, err)
		}
		ma.TotalDuration, err = MergeHistograms(totalDurationSnaps)
		if err != nil {
			return nil, fmt.Errorf("合并动作 %q 总耗时分布: %w", name, err)
		}

		if ma.SampleCount > 0 {
			ma.SuccessRate = float64(ma.SuccessCount) / float64(ma.SampleCount)
		}
		// RTT Apdex 分母 = 有响应帧的样本 + 无响应帧的失败请求，与单机模式同口径。
		rttApdexSamples := ma.RTTSampleCount + ma.RTTFailedCount
		ma.RTTApdexSampleCount = rttApdexSamples
		if rttApdexSamples > 0 {
			ma.RTTApdex = (float64(rttSatisfied) + float64(rttTolerating)*0.5) / float64(rttApdexSamples)
		}
		listenTotal := ma.ListenWaitSampleCount + ma.ListenReadyCount + ma.ListenTimeoutCount
		if listenTotal > 0 {
			ma.ListenTimeoutRate = float64(ma.ListenTimeoutCount) / float64(listenTotal)
		}
		ma.Kind = classifyActionKind(rttApdexSamples, listenTotal, ma.TotalSendBytes)
		byteSamples := ma.ByteSampleCount
		if byteSamples > 0 {
			ma.AvgSendBytes = float64(ma.TotalSendBytes) / float64(byteSamples)
			ma.AvgRecvBytes = float64(ma.TotalRecvBytes) / float64(byteSamples)
		}
		if ma.ClientCostCount > 0 {
			ma.ClientAvgMs = float64(ma.ClientCostSumNs) / float64(ma.ClientCostCount) / 1e6
		}
		ma.BuildAvgMs = divideMetricSumNs(ma.BuildCostSumNs, ma.BuildSampleCount)
		ma.EncodeAvgMs = divideMetricSumNs(ma.EncodeCostSumNs, ma.EncodeSampleCount)
		ma.SendAvgMs = divideMetricSumNs(ma.SendCostSumNs, ma.SendSampleCount)
		ma.DecodeWaitAvgMs = divideMetricSumNs(ma.DecodeWaitSumNs, ma.DecodeWaitSampleCount)
		ma.DecodeAvgMs = divideMetricSumNs(ma.DecodeCostSumNs, ma.DecodeSampleCount)
		ma.DispatchToActionWaitAvgMs = divideMetricSumNs(ma.DispatchWaitSumNs, ma.DispatchWaitSampleCount)
		ma.ParseStoreAvgMs = divideMetricSumNs(ma.ParseStoreSumNs, ma.ParseStoreSampleCount)
		if maxUptime > 0 {
			ma.AvgQPS = float64(ma.SampleCount) / maxUptime
		}
		if ma.TimeoutCount > 0 {
			ma.TimeoutAvgMs = divideMetricSumNs(ma.TimeoutTotalNs, ma.TimeoutCount)
		}

		// 合并错误 — 按 code 聚合，Messages 取并集去重
		type mergedErrorKey struct {
			Code uint64
		}
		errMap := make(map[mergedErrorKey]*ErrorEntry)
		for _, a := range agg.snaps {
			for _, e := range a.Errors {
				k := mergedErrorKey{Code: e.Code}
				if existing, ok := errMap[k]; ok {
					existing.Count += e.Count
					for _, m := range e.Messages {
						if !slices.Contains(existing.Messages, m) {
							existing.Messages = append(existing.Messages, m)
						}
					}
					if len(existing.Messages) > 5 {
						existing.Messages = existing.Messages[:5]
					}
				} else {
					cp := e
					errMap[k] = &cp
				}
			}
		}
		for _, e := range errMap {
			ma.Errors = append(ma.Errors, *e)
		}

		merged.Actions = append(merged.Actions, ma)
	}

	// 契约保证（见函数文档）：Actions 字段始终非 nil。
	if merged.Actions == nil {
		merged.Actions = []ActionSnapshot{}
	}
	var err error
	merged.Summary, err = buildSnapshotSummary(merged.Actions)
	if err != nil {
		return nil, err
	}
	return merged, nil
}
