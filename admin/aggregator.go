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

// StressAggregate 压测聚合结果（含覆盖率）。
type StressAggregate struct {
	Snapshot        *monitor.CollectorSnapshot `json:"snapshot"`
	ReportingAgents int                        `json:"reportingAgents"`
	TotalAgents     int                        `json:"totalAgents"`
	OfflineAgents   int                        `json:"offlineAgents"`
	AssignedAgents  int                        `json:"assignedAgents"`
}

// AggregateStress 聚合指定任务的压测指标。
func (a *MetricsAggregator) AggregateStress(taskID string) *StressAggregate {
	agents := a.registry.List()

	var snaps []*monitor.CollectorSnapshot
	totalAgents := 0
	assignedAgents := 0
	offlineAgents := 0
	for _, agent := range agents {
		if agent.CurrentTaskID != taskID {
			continue
		}
		assignedAgents++
		if agent.Status == AgentOffline {
			offlineAgents++
			continue
		}
		totalAgents++
		if agent.LatestStress == nil {
			continue
		}
		snaps = append(snaps, agent.LatestStress)
	}

	return &StressAggregate{
		Snapshot:        monitor.MergeSnapshots(snaps),
		ReportingAgents: len(snaps),
		TotalAgents:     totalAgents,
		OfflineAgents:   offlineAgents,
		AssignedAgents:  assignedAgents,
	}
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

		// 网络（求和）
		cluster.TotalNetSendKBps += sys.NetSendKBps
		cluster.TotalNetRecvKBps += sys.NetRecvKBps

		// 进程（求和）
		cluster.TotalGoroutines += sys.NumGoroutine
		cluster.TotalThreads += sys.NumThread
		cluster.TotalFDs += sys.NumFD

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

	return cluster
}

// Now 当前时间（可被测试覆盖）。
var Now = func() time.Time {
	return time.Now()
}
