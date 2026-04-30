package admin

import (
	"time"

	"stressbot/monitor"
)

// MetricsAggregator 压测/系统指标聚合器。
type MetricsAggregator struct {
	registry *AgentRegistry
}

func NewMetricsAggregator(registry *AgentRegistry) *MetricsAggregator {
	return &MetricsAggregator{registry: registry}
}

// AggregateStress 聚合指定任务的压测指标。
func (a *MetricsAggregator) AggregateStress(taskID string) *monitor.CollectorSnapshot {
	agents := a.registry.List()

	var snaps []*monitor.CollectorSnapshot
	for _, agent := range agents {
		if agent.Status == AgentOffline {
			continue
		}
		if agent.CurrentTaskID != taskID {
			continue
		}
		if agent.LatestStress == nil {
			continue
		}
		snaps = append(snaps, agent.LatestStress)
	}

	return monitor.MergeSnapshots(snaps)
}

// AggregateSystem 聚合集群系统指标。
func (a *MetricsAggregator) AggregateSystem() ClusterSystemSnapshot {
	agents := a.registry.List()

	var cluster ClusterSystemSnapshot
	cluster.Timestamp = Now()

	var totalCPUWeight float64 // 按核数加权
	var weightedCPUSum float64
	var totalMem float64
	var totalMemPercent float64

	for _, agent := range agents {
		if agent.Status == AgentOffline {
			cluster.OfflineCount++
			continue
		}
		cluster.AgentCount++
		cluster.OnlineCount++
		if agent.Status == AgentUpgrading {
			cluster.UpgradingCount++
		}

		sys := agent.LatestSystem
		if sys == nil {
			continue
		}

		// CPU 加权平均
		cpuWeight := float64(agent.StaticInfo.NumCPU)
		if cpuWeight == 0 {
			cpuWeight = 1
		}
		weightedCPUSum += sys.CPUPercent * cpuWeight
		totalCPUWeight += cpuWeight

		// 最大 CPU 的 Agent
		if sys.CPUPercent > cluster.MaxCPUPercent {
			cluster.MaxCPUPercent = sys.CPUPercent
			cluster.HotAgentID = agent.ID
			cluster.HotAgentName = agent.Name
		}

		// 内存求和
		cluster.TotalMemMB += sys.MemTotalMB
		cluster.UsedMemMB += sys.MemUsedMB
		totalMem += float64(sys.MemTotalMB)
		if sys.MemTotalMB > 0 {
			totalMemPercent += sys.MemPercent
		}

		// 网络
		cluster.NetSendKBps += sys.NetSendKBps
		cluster.NetRecvKBps += sys.NetRecvKBps

		// 进程
		cluster.TotalGoroutine += sys.NumGoroutine
		cluster.TotalThread += sys.NumThread
		cluster.TotalFD += sys.NumFD

		// Agent 摘要
		cluster.Agents = append(cluster.Agents, AgentSystemBrief{
			AgentID:      agent.ID,
			Name:         agent.Name,
			Status:       string(agent.Status),
			CPUPercent:   sys.CPUPercent,
			MemPercent:   sys.MemPercent,
			NumGoroutine: sys.NumGoroutine,
			NetSendKBps:  sys.NetSendKBps,
			NetRecvKBps:  sys.NetRecvKBps,
			LastSeen:     agent.LastHeartbeatAt.Unix(),
		})
	}

	if totalCPUWeight > 0 {
		cluster.AvgCPUPercent = weightedCPUSum / totalCPUWeight
	}
	if cluster.TotalMemMB > 0 {
		cluster.AvgMemPercent = float64(cluster.UsedMemMB) / float64(cluster.TotalMemMB) * 100
	}

	return cluster
}

// Now 当前时间（可被测试覆盖）。
var Now = func() time.Time {
	return time.Now()
}
