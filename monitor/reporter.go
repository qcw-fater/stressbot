package monitor

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"stressbot/utils"
)

// Reporter 定时向控制台输出指标摘要。
type Reporter struct {
	collector  *MetricsCollector // 指标收集器引用
	interval   time.Duration     // 报告间隔
	prevCounts map[string]int64  // 上次快照时各 action 的样本数，用于计算 periodQPS
	prevTime   time.Time         // 上次报告时间，用于计算区间 QPS
	stopCh     chan struct{}     // 停止信号通道
	stopOnce   sync.Once         // 保证 stopCh 只关闭一次
}

// NewReporter 创建定时控制台报告器。
func NewReporter(c *MetricsCollector, interval time.Duration) *Reporter {
	return &Reporter{
		collector:  c,
		interval:   interval,
		prevCounts: make(map[string]int64),
		stopCh:     make(chan struct{}),
	}
}

// Start 启动定时报告。
func (r *Reporter) Start() {
	r.prevTime = time.Now()
	utils.GetWorkPool().GoWithStop(func(poolStop <-chan struct{}) {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.report()
			case <-r.stopCh:
				return
			case <-poolStop:
				return
			}
		}
	})
}

// Stop 停止报告。
func (r *Reporter) Stop() {
	r.stopOnce.Do(func() { close(r.stopCh) })
}

func (r *Reporter) report() {
	now := time.Now()
	periodSec := now.Sub(r.prevTime).Seconds()
	r.prevTime = now

	snap := r.collector.Snapshot(r.prevCounts, periodSec)
	uptime := snap.Uptime.Round(time.Second)

	fmt.Printf("[MONITOR] %s | goroutines=%d | mem=%.1fMB | gc=%d | total=%d\n",
		uptime, snap.System.Goroutines, snap.System.MemAllocMB, snap.System.GCCount, snap.TotalActions)
	fmt.Printf("[MONITOR] robots: %d运行 %d停止 %d错误 | conn: %d建立 %d失败 %d断连 | %.1f/%.1f MB/s\n",
		snap.Robots.Running, snap.Robots.Stopped, snap.Robots.Errored,
		snap.Connections.Established, snap.Connections.Failed, snap.Connections.Dropped,
		snap.Bandwidth.SendMBps, snap.Bandwidth.RecvMBps)

	if len(snap.Actions) == 0 {
		return
	}

	// RTT 是纯网络往返耗时（不含客户端构建/解析），
	// client 列单独反映客户端 CPU 开销。
	fmt.Printf("[MONITOR] %-22s %4s %4s %4s %8s %8s %8s %5s %4s %5s\n",
		"动作", "成功", "失败", "超时", "RTT avg", "RTT p95", "client", "apdex", "exec", "qps")

	var hasErrors bool
	for _, a := range snap.Actions {
		if a.SampleCount == 0 && a.Executing == 0 {
			continue
		}
		rttAvg := "    --ms"
		rttP95 := "    --ms"
		if a.RTTSampleCount > 0 {
			rttAvg = fmt.Sprintf("%6.0fms", a.RTT.AvgMs)
			rttP95 = fmt.Sprintf("%6.0fms", a.RTT.P95Ms)
		}
		fmt.Printf("[MONITOR] %-22s %4d %4d %4d %8s %8s %6.0fms %5.2f %4d %5.1f",
			a.Name,
			a.SuccessCount, a.FailureCount, a.TimeoutCount,
			rttAvg, rttP95, a.ClientAvgMs,
			a.RTTApdex, a.Executing, a.PeriodQPS)
		if a.TimeoutCount > 0 && a.TimeoutAvgMs > 0 {
			fmt.Printf(" tout=%.0fms", a.TimeoutAvgMs)
		}
		fmt.Println()
		if len(a.Errors) > 0 {
			hasErrors = true
		}
	}

	if hasErrors {
		fmt.Printf("[MONITOR] errors: ")
		first := true
		for _, a := range snap.Actions {
			if len(a.Errors) == 0 {
				continue
			}
			for _, e := range sortedErrors(a.Errors) {
				if !first {
					fmt.Printf(", ")
				}
				label := "业务"
				if e.Code < 100 {
					label = "框架"
				}
				name := e.CodeName
				if name == "" {
					name = fmt.Sprintf("#%d", e.Code)
				}
				fmt.Printf("%s→[%s %s]×%d %s", a.Name, label, name, e.Count, truncateError(firstMsg(e.Messages), 40))
				first = false
			}
		}
		fmt.Println()
	}
}

func firstMsg(msgs []string) string {
	if len(msgs) == 0 {
		return ""
	}
	s := msgs[0]
	if len(msgs) > 1 {
		s += fmt.Sprintf(" (+%d more)", len(msgs)-1)
	}
	return s
}

func truncateError(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// sortedErrors 按次数降序排列错误列表。
func sortedErrors(entries []ErrorEntry) []ErrorEntry {
	sorted := make([]ErrorEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Count > sorted[j].Count
	})
	return sorted
}
