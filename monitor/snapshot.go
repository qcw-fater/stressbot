package monitor

import (
	"runtime"
	"slices"
	"time"

	"stressbot/errcode"
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

// ActionSnapshot per-action 完整快照（只读，用于 JSON/CSV/控制台输出）。
type ActionSnapshot struct {
	Name          string            `json:"name"`          // 动作名称
	SampleCount   int64             `json:"sampleCount"`   // 总样本数（成功 + 失败 + 超时）
	SuccessCount  int64             `json:"successCount"`  // 成功次数
	FailureCount  int64             `json:"failureCount"`  // 失败次数（非超时）
	TimeoutCount  int64             `json:"timeoutCount"`  // 超时次数
	SkippedCount  int64             `json:"skippedCount"`  // 跳过次数
	CanceledCount int64             `json:"canceledCount"` // 取消次数
	Executing     int64             `json:"executing"`     // 当前执行中的并发数
	SuccessRate   float64           `json:"successRate"`   // 成功率（0~1）
	AvgSendBytes  float64           `json:"avgSendBytes"`  // 平均每次成功的发送字节数
	AvgRecvBytes  float64           `json:"avgRecvBytes"`  // 平均每次成功的接收字节数
	Apdex         float64           `json:"apdex"`         // Apdex 评分（0~1）
	Latency       HistogramSnapshot `json:"latency"`       // 延迟直方图快照
	TimeoutAvgMs  float64           `json:"timeoutAvgMs"`  // 平均超时延迟（毫秒）
	AvgQPS        float64           `json:"avgQps"`        // 全周期平均 QPS
	PeriodQPS     float64           `json:"periodQps"`     // 上次快照到当前的区间 QPS
	Errors        []ErrorEntry      `json:"errors,omitempty"` // 错误分布（仅失败/超时时有值）

	// 跨节点聚合所需的原始数据（omitempty 向后兼容单机模式）
	LatencySumNs        int64   `json:"latencySumNs,omitempty"`        // 延迟总和（纳秒），用于分布式合并
	LatencyBucketCounts []int64 `json:"latencyBucketCounts,omitempty"` // 延迟直方图桶计数，用于分布式合并
	ApdexSatisfied      int64   `json:"apdexSatisfied,omitempty"`      // Apdex 满意样本数，用于分布式合并
	ApdexTolerating     int64   `json:"apdexTolerating,omitempty"`     // Apdex 容忍样本数，用于分布式合并
	TotalSendBytes      int64   `json:"totalSendBytes,omitempty"`      // 累计发送字节数，用于分布式合并
	TotalRecvBytes      int64   `json:"totalRecvBytes,omitempty"`      // 累计接收字节数，用于分布式合并
}

// RobotSnapshot 机器人状态快照。
type RobotSnapshot struct {
	Started  int64 `json:"started"`  // 已启动的机器人总数
	Running  int64 `json:"running"`  // 当前运行中的机器人数量
	Stopped  int64 `json:"stopped"`  // 正常停止的机器人数量
	Errored  int64 `json:"errored"`  // 异常退出的机器人数量
}

// CollectorSnapshot 全局指标快照，包含系统、机器人、连接、带宽和所有 action 的聚合数据。
type CollectorSnapshot struct {
	Timestamp    time.Time          `json:"timestamp"`    // 快照时间
	Uptime       time.Duration      `json:"uptime"`       // 运行时长
	UptimeSec    float64            `json:"uptimeSeconds"` // 运行时长（秒）
	TotalActions int64              `json:"totalActions"`  // 累计动作总数
	ApdexT       int                `json:"apdexT"`        // 当前 Apdex T 阈值（毫秒）
	System       SystemSnapshot     `json:"system"`        // 系统资源快照
	Robots       RobotSnapshot      `json:"robots"`        // 机器人状态快照
	Connections  ConnectionSnapshot `json:"connections"`   // 连接指标快照
	Bandwidth    BandwidthSnapshot  `json:"bandwidth"`     // 带宽快照
	Actions      []ActionSnapshot   `json:"actions"`       // 所有 action 的快照列表
}

// Snapshot 生成当前全局快照。
// prevCounts 由调用方（Reporter）维护，用于计算 periodQPS。传 nil 表示不计算。
func (c *MetricsCollector) Snapshot(prevCounts map[string]int64, periodSec float64) *CollectorSnapshot {
	if !c.enabled {
		return &CollectorSnapshot{}
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

	c.cfgMu.RLock()
	apdexT := c.cfg.ApdexT
	c.cfgMu.RUnlock()
	snap := &CollectorSnapshot{
		Timestamp:    time.Now(),
		Uptime:       uptime,
		UptimeSec:    uptimeSec,
		TotalActions: c.totalActions.Load(),
		ApdexT:       apdexT,
		System:       sys,
		Robots: RobotSnapshot{
			Started: c.robotsStarted.Load(),
			Running: c.robotsRunning.Load(),
			Stopped: c.robotsStopped.Load(),
			Errored: c.robotsErrored.Load(),
		},
		Connections: ConnectionSnapshot{
			Established: c.connEstablished.Load(),
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

		succ := am.successCount.Load()
		fail := am.failureCount.Load()
		tout := am.timeoutCount.Load()
		skip := am.skippedCount.Load()
		canceled := am.canceledCount.Load()
		exec := am.executing.Load()
		total := succ + fail + tout

		var timeoutAvgMs float64
		if tout > 0 {
			timeoutAvgMs = float64(am.timeoutTotalMs.Load()) / float64(tout)
		}

		satisfied := am.apdexSatisfied.Load()
		tolerating := am.apdexTolerating.Load()

		var apdex float64
		if total > 0 {
			apdex = (float64(satisfied) + float64(tolerating)*0.5) / float64(total)
		}

		var successRate float64
		if total > 0 {
			successRate = float64(succ) / float64(total)
		}

		var avgSend, avgRecv float64
		totalSendBytes := am.sendBytes.Load()
		totalRecvBytes := am.recvBytes.Load()
		if succ > 0 {
			avgSend = float64(totalSendBytes) / float64(succ)
			avgRecv = float64(totalRecvBytes) / float64(succ)
		}

		latSnap := am.latency.Snapshot()

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
			Name:                name,
			SampleCount:         total,
			SuccessCount:        succ,
			FailureCount:        fail,
			TimeoutCount:        tout,
			SkippedCount:        skip,
			CanceledCount:       canceled,
			TimeoutAvgMs:        timeoutAvgMs,
			Executing:           exec,
			SuccessRate:         successRate,
			AvgSendBytes:        avgSend,
			AvgRecvBytes:        avgRecv,
			Apdex:               apdex,
			Latency:             latSnap,
			AvgQPS:              avgQPS,
			PeriodQPS:           periodQPS,
			Errors:              errs,
			LatencySumNs:        latSnap.SumNs,
			LatencyBucketCounts: latSnap.BucketCounts,
			ApdexSatisfied:      satisfied,
			ApdexTolerating:     tolerating,
			TotalSendBytes:      totalSendBytes,
			TotalRecvBytes:      totalRecvBytes,
		})
	}
	return snap
}

// MergeSnapshots 合并多个 CollectorSnapshot，用于分布式场景下聚合多 Agent 指标。
func MergeSnapshots(snaps []*CollectorSnapshot) *CollectorSnapshot {
	if len(snaps) == 0 {
		return &CollectorSnapshot{}
	}
	if len(snaps) == 1 {
		return snaps[0]
	}

	merged := &CollectorSnapshot{
		Timestamp: time.Now(),
		ApdexT:    snaps[0].ApdexT,
	}

	var maxUptime float64
	for _, s := range snaps {
		merged.TotalActions += s.TotalActions
		merged.Robots.Started += s.Robots.Started
		merged.Robots.Running += s.Robots.Running
		merged.Robots.Stopped += s.Robots.Stopped
		merged.Robots.Errored += s.Robots.Errored
		merged.Connections.Established += s.Connections.Established
		merged.Connections.Failed += s.Connections.Failed
		merged.Connections.Dropped += s.Connections.Dropped
		merged.Bandwidth.TotalSendBytes += s.Bandwidth.TotalSendBytes
		merged.Bandwidth.TotalRecvBytes += s.Bandwidth.TotalRecvBytes
		if s.UptimeSec > maxUptime {
			maxUptime = s.UptimeSec
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
	for _, s := range snaps {
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

		var totalSatisfied, totalTolerating int64
		for _, a := range agg.snaps {
			ma.SampleCount += a.SampleCount
			ma.SuccessCount += a.SuccessCount
			ma.FailureCount += a.FailureCount
			ma.TimeoutCount += a.TimeoutCount
			ma.SkippedCount += a.SkippedCount
			ma.CanceledCount += a.CanceledCount
			ma.Executing += a.Executing
			ma.LatencySumNs += a.LatencySumNs
			totalSatisfied += a.ApdexSatisfied
			totalTolerating += a.ApdexTolerating
			ma.TotalSendBytes += a.TotalSendBytes
			ma.TotalRecvBytes += a.TotalRecvBytes
		}

		// 合并延迟直方图
		latSnaps := make([]HistogramSnapshot, len(agg.snaps))
		for i, a := range agg.snaps {
			latSnaps[i] = a.Latency
		}
		ma.Latency = MergeHistograms(latSnaps)
		ma.LatencySumNs = ma.Latency.SumNs
		ma.LatencyBucketCounts = ma.Latency.BucketCounts
		ma.ApdexSatisfied = totalSatisfied
		ma.ApdexTolerating = totalTolerating

		if ma.SampleCount > 0 {
			ma.SuccessRate = float64(ma.SuccessCount) / float64(ma.SampleCount)
			ma.Apdex = (float64(totalSatisfied) + float64(totalTolerating)*0.5) / float64(ma.SampleCount)
		}
		if ma.SuccessCount > 0 {
			ma.AvgSendBytes = float64(ma.TotalSendBytes) / float64(ma.SuccessCount)
			ma.AvgRecvBytes = float64(ma.TotalRecvBytes) / float64(ma.SuccessCount)
		}
		if maxUptime > 0 {
			ma.AvgQPS = float64(ma.SampleCount) / maxUptime
		}
		if ma.TimeoutCount > 0 {
			var totalTimeoutMs int64
			for _, a := range agg.snaps {
				totalTimeoutMs += int64(a.TimeoutAvgMs * float64(a.TimeoutCount))
			}
			ma.TimeoutAvgMs = float64(totalTimeoutMs) / float64(ma.TimeoutCount)
		}

		// 合并错误 — 按 (Kind, Code) 聚合，Messages 取并集去重
		type mergedErrorKey struct{ Kind errcode.Kind; Code uint64 }
		errMap := make(map[mergedErrorKey]*ErrorEntry)
		for _, a := range agg.snaps {
			for _, e := range a.Errors {
				k := mergedErrorKey{Kind: e.Kind, Code: e.Code}
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

	return merged
}
