package monitor

import (
	"runtime"
	"time"
)

// SystemSnapshot 系统资源快照。
type SystemSnapshot struct {
	Goroutines int     `json:"goroutines"`
	MemAllocMB float64 `json:"memAllocMB"`
	MemSysMB   float64 `json:"memSysMB"`
	GCCount    uint32  `json:"gcCount"`
}

// ConnectionSnapshot 连接指标快照。
type ConnectionSnapshot struct {
	Established int64 `json:"established"`
	Failed      int64 `json:"failed"`
	Dropped     int64 `json:"dropped"`
}

// BandwidthSnapshot 全局带宽快照。
type BandwidthSnapshot struct {
	TotalSendBytes int64   `json:"totalSendBytes"`
	TotalRecvBytes int64   `json:"totalRecvBytes"`
	SendMBps       float64 `json:"sendMBps"`
	RecvMBps       float64 `json:"recvMBps"`
}

// ActionSnapshot per-action 完整快照（只读，用于 JSON/CSV/控制台）。
type ActionSnapshot struct {
	Name         string            `json:"name"`
	SampleCount  int64             `json:"sampleCount"`
	SuccessCount int64             `json:"successCount"`
	FailureCount int64             `json:"failureCount"`
	TimeoutCount int64             `json:"timeoutCount"`
	SkippedCount int64             `json:"skippedCount"`
	Executing    int64             `json:"executing"`
	SuccessRate  float64           `json:"successRate"`
	AvgSendBytes float64           `json:"avgSendBytes"`
	AvgRecvBytes float64           `json:"avgRecvBytes"`
	Apdex        float64           `json:"apdex"`
	Latency      HistogramSnapshot `json:"latency"`
	TimeoutAvgMs float64           `json:"timeoutAvgMs"`
	AvgQPS       float64           `json:"avgQps"`
	PeriodQPS    float64           `json:"periodQps"`
	Errors       []ErrorEntry      `json:"errors,omitempty"`

	// 跨节点聚合所需原始数据（omitempty 向后兼容单机模式）
	LatencySumNs        int64   `json:"latencySumNs,omitempty"`
	LatencyBucketCounts []int64 `json:"latencyBucketCounts,omitempty"`
	ApdexSatisfied      int64   `json:"apdexSatisfied,omitempty"`
	ApdexTolerating     int64   `json:"apdexTolerating,omitempty"`
	TotalSendBytes      int64   `json:"totalSendBytes,omitempty"`
	TotalRecvBytes      int64   `json:"totalRecvBytes,omitempty"`
}

// RobotSnapshot 机器人状态快照。
type RobotSnapshot struct {
	Started  int64 `json:"started"`
	Running  int64 `json:"running"`
	Stopped  int64 `json:"stopped"`
	Errored  int64 `json:"errored"`
}

// CollectorSnapshot 全局快照。
type CollectorSnapshot struct {
	Timestamp    time.Time          `json:"timestamp"`
	Uptime       time.Duration      `json:"uptime"`
	UptimeSec    float64            `json:"uptimeSeconds"`
	TotalActions int64              `json:"totalActions"`
	ApdexT       int                `json:"apdexT"`
	System       SystemSnapshot     `json:"system"`
	Robots       RobotSnapshot      `json:"robots"`
	Connections  ConnectionSnapshot `json:"connections"`
	Bandwidth    BandwidthSnapshot  `json:"bandwidth"`
	Actions      []ActionSnapshot   `json:"actions"`
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

	snap := &CollectorSnapshot{
		Timestamp:    time.Now(),
		Uptime:       uptime,
		UptimeSec:    uptimeSec,
		TotalActions: c.totalActions.Load(),
		ApdexT:       c.cfg.ApdexT,
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

		// 合并错误
		errMap := make(map[string]int64)
		for _, a := range agg.snaps {
			for _, e := range a.Errors {
				errMap[e.Message] += e.Count
			}
		}
		for msg, count := range errMap {
			ma.Errors = append(ma.Errors, ErrorEntry{Message: msg, Count: count})
		}

		merged.Actions = append(merged.Actions, ma)
	}

	return merged
}
