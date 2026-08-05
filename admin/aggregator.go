package admin

import (
	"math"
	"sort"
	"time"

	"stressbot/monitor"
)

// MetricsAggregator 压测/系统指标聚合器。
type MetricsAggregator struct {
	registry *AgentRegistry
	windows  *MetricsWindowStore
	now      func() time.Time
}

func NewMetricsAggregator(registry *AgentRegistry, windows *MetricsWindowStore, now func() time.Time) *MetricsAggregator {
	if now == nil {
		now = time.Now
	}
	return &MetricsAggregator{registry: registry, windows: windows, now: now}
}

// StressAggregate 压测聚合结果（含覆盖率）。
type StressAggregate struct {
	Snapshot        *monitor.CollectorSnapshot `json:"snapshot"`
	ReportingAgents int                        `json:"reportingAgents"`
	TotalAgents     int                        `json:"totalAgents"`
	OfflineAgents   int                        `json:"offlineAgents"`
	AssignedAgents  int                        `json:"assignedAgents"`
	FreshAgents     int                        `json:"freshAgents"`
	StaleAgents     int                        `json:"staleAgents"`
	CoverageRatio   float64                    `json:"coverageRatio"`
	AsOf            time.Time                  `json:"asOf"`
}

// AggregateStress 聚合指定任务的压测指标。
func (a *MetricsAggregator) AggregateStress(taskID string) (*StressAggregate, error) {
	agents := a.registry.List()
	assigned := make(map[string]*AgentNode)
	totalAgents := 0
	assignedAgents := 0
	offlineAgents := 0
	for _, agent := range agents {
		if agent.CurrentTaskID != taskID {
			continue
		}
		assignedAgents++
		assigned[agent.ID] = agent
		if agent.Status == AgentOffline {
			offlineAgents++
			continue
		}
		totalAgents++
	}

	now := a.now()
	states := a.windows.States(taskID)
	cumulative := make([]*monitor.CollectorSnapshot, 0, len(states))
	fresh := make([]AgentMetricState, 0, len(states))
	reportingAgents := 0
	staleAgents := 0
	for i := range states {
		state := &states[i]
		agent, ok := assigned[state.AgentID]
		if !ok {
			continue
		}
		reportingAgents++
		cumulative = append(cumulative, &state.Cumulative)
		freshFor := max(3*state.ExpectedEvery, 15*time.Second)
		if agent.Status == AgentOffline || now.Sub(state.ReceivedAt) > freshFor {
			staleAgents++
			continue
		}
		fresh = append(fresh, *state)
	}

	merged, err := monitor.MergeSnapshots(cumulative)
	if err != nil {
		return nil, err
	}
	if err := attachFreshWindow(merged, fresh); err != nil {
		return nil, err
	}
	merged.Timestamp = now
	coverage := 0.0
	if assignedAgents > 0 {
		coverage = float64(reportingAgents) / float64(assignedAgents)
	}
	return &StressAggregate{
		Snapshot:        merged,
		ReportingAgents: reportingAgents,
		TotalAgents:     totalAgents,
		OfflineAgents:   offlineAgents,
		AssignedAgents:  assignedAgents,
		FreshAgents:     len(fresh),
		StaleAgents:     staleAgents,
		CoverageRatio:   coverage,
		AsOf:            now,
	}, nil
}

func attachFreshWindow(cumulative *monitor.CollectorSnapshot, states []AgentMetricState) error {
	if len(states) == 0 {
		cumulative.Window = nil
		cumulative.Bandwidth.SendMBps = 0
		cumulative.Bandwidth.RecvMBps = 0
		for i := range cumulative.Actions {
			cumulative.Actions[i].PeriodQPS = 0
			cumulative.Actions[i].Executing = 0
		}
		cumulative.Summary.Executing = 0
		return nil
	}

	parts := make([]*monitor.CollectorSnapshot, 0, len(states))
	actionRates := make(map[string]float64)
	actionExecuting := make(map[string]int64)
	var startedAt, endedAt time.Time
	var sendBytes, recvBytes int64
	var sendMBps, recvMBps float64
	var runningRobots, activeConnections int64
	for i := range states {
		state := &states[i]
		window := &state.LatestWindow
		part := &monitor.CollectorSnapshot{
			ApdexT:               state.Cumulative.ApdexT,
			TimingDetail:         state.Cumulative.TimingDetail,
			UptimeSec:            window.DurationSeconds,
			Actions:              window.Actions,
			InvalidMetricSamples: window.InvalidMetricSamples,
		}
		parts = append(parts, part)
		if startedAt.IsZero() || window.StartedAt.Before(startedAt) {
			startedAt = window.StartedAt
		}
		if window.EndedAt.After(endedAt) {
			endedAt = window.EndedAt
		}
		duration := window.DurationSeconds
		if duration > 0 {
			for _, action := range window.Actions {
				actionRates[action.Name] += float64(action.SampleCount) / duration
			}
		}
		for _, action := range state.Cumulative.Actions {
			actionExecuting[action.Name] += action.Executing
		}
		runningRobots += state.Cumulative.Robots.Running
		activeConnections += state.Cumulative.Connections.Active
		sendBytes += window.Bandwidth.SendBytes
		recvBytes += window.Bandwidth.RecvBytes
		if duration > 0 {
			sendMBps += float64(window.Bandwidth.SendBytes) / 1024 / 1024 / duration
			recvMBps += float64(window.Bandwidth.RecvBytes) / 1024 / 1024 / duration
		}
	}

	windowMerged, err := monitor.MergeSnapshots(parts)
	if err != nil {
		return err
	}
	var qps float64
	for i := range windowMerged.Actions {
		rate := actionRates[windowMerged.Actions[i].Name]
		windowMerged.Actions[i].AvgQPS = rate
		windowMerged.Actions[i].PeriodQPS = rate
		qps += rate
	}
	windowMerged.Summary.AvgQPS = qps
	windowMerged.Summary.Executing = 0
	for _, executing := range actionExecuting {
		windowMerged.Summary.Executing += executing
	}
	cumulative.Window = &monitor.ReportWindow{
		StartedAt:       startedAt,
		EndedAt:         endedAt,
		DurationSeconds: endedAt.Sub(startedAt).Seconds(),
		Summary:         windowMerged.Summary,
		Bandwidth: monitor.WindowBandwidthSnapshot{
			SendBytes: sendBytes,
			RecvBytes: recvBytes,
			SendMBps:  sendMBps,
			RecvMBps:  recvMBps,
		},
		Actions:              windowMerged.Actions,
		InvalidMetricSamples: windowMerged.InvalidMetricSamples,
	}
	cumulative.Bandwidth.SendMBps = sendMBps
	cumulative.Bandwidth.RecvMBps = recvMBps
	cumulative.Robots.Running = runningRobots
	cumulative.Connections.Active = activeConnections
	cumulative.Summary.Executing = windowMerged.Summary.Executing
	for i := range cumulative.Actions {
		name := cumulative.Actions[i].Name
		cumulative.Actions[i].PeriodQPS = actionRates[name]
		cumulative.Actions[i].Executing = actionExecuting[name]
	}
	return nil
}

// AggregateSystem aggregates only the requested Agent scope. A nil scope means
// all registered Agents; a non-nil empty scope intentionally means no Agents.
func (a *MetricsAggregator) AggregateSystem(agentIDs []string) ClusterSystemSnapshot {
	now := a.now()
	cluster := ClusterSystemSnapshot{Timestamp: now, Agents: make([]AgentSystemBrief, 0)}
	agents := a.registry.List()
	byID := make(map[string]*AgentNode, len(agents))
	for _, agent := range agents {
		byID[agent.ID] = agent
	}
	ids := uniqueSystemAgentIDs(agentIDs, agents)
	cluster.AgentCount = len(ids)

	var hostCPUWeighted, hostCPUWeight float64
	var processCPUWeighted, processCPUWeight float64
	var totalMemory, usedMemory uint64
	var netSend, netRecv float64
	var totalRSS, totalHeap uint64
	var totalGoroutines int
	var totalThreads, totalFDs int32

	for _, id := range ids {
		agent, ok := byID[id]
		if !ok {
			cluster.OfflineCount++
			cluster.MissingAgents++
			cluster.Agents = append(cluster.Agents, AgentSystemBrief{AgentID: id, Status: string(AgentOffline)})
			continue
		}

		brief := AgentSystemBrief{
			AgentID:         agent.ID,
			Name:            agent.Name,
			Status:          string(agent.Status),
			LastHeartbeatAt: agent.LastHeartbeatAt,
		}
		switch agent.Status {
		case AgentIdle, AgentBusy:
			cluster.OnlineCount++
		case AgentUnhealthy:
			cluster.UnhealthyCount++
		case AgentOffline:
			cluster.OfflineCount++
		}

		if agent.LatestSystem == nil || agent.SystemUpdatedAt.IsZero() {
			cluster.MissingAgents++
			cluster.Agents = append(cluster.Agents, brief)
			continue
		}

		receivedAt := agent.SystemUpdatedAt
		brief.ReceivedAt = new(receivedAt)
		if !agent.LatestSystem.Timestamp.IsZero() {
			brief.SampledAt = new(agent.LatestSystem.Timestamp)
		}
		age := now.Sub(receivedAt)
		brief.SnapshotAgeSeconds = new(age.Seconds())
		fresh := (agent.Status == AgentIdle || agent.Status == AgentBusy) && age >= 0 && age <= systemSnapshotFreshFor(agent.SystemInterval)
		if !fresh {
			brief.IsStale = true
			cluster.StaleAgents++
			cluster.Agents = append(cluster.Agents, brief)
			continue
		}

		cluster.ReportingAgents++
		snapshot := agent.LatestSystem
		cpuWeight := float64(agent.StaticInfo.NumCPU)
		if cpuWeight <= 0 {
			cpuWeight = 1
		}

		if value := validPercent(snapshot.HostCPUPercent); value != nil {
			brief.HostCPUPercent = value
			cluster.HostCPUReportingAgents++
			hostCPUWeighted += *value * cpuWeight
			hostCPUWeight += cpuWeight
			if cluster.MaxHostCPUPercent == nil || *value > *cluster.MaxHostCPUPercent {
				cluster.MaxHostCPUPercent = new(*value)
				cluster.HotHostCPUAgentID = agent.ID
				cluster.HotHostCPUAgentName = agent.Name
			}
		}

		if total, used, percent, ok := validHostMemory(snapshot); ok {
			brief.HostMemPercent = new(percent)
			cluster.HostMemoryReportingAgents++
			totalMemory += total
			usedMemory += used
			if cluster.MaxHostMemPercent == nil || percent > *cluster.MaxHostMemPercent {
				cluster.MaxHostMemPercent = new(percent)
				cluster.HotHostMemAgentID = agent.ID
				cluster.HotHostMemAgentName = agent.Name
			}
		}

		if value := validNonNegative(snapshot.HostNetSendBytesPerSec); value != nil {
			brief.HostNetSendBytesPerSec = value
			cluster.HostNetSendReportingAgents++
			netSend += *value
		}
		if value := validNonNegative(snapshot.HostNetRecvBytesPerSec); value != nil {
			brief.HostNetRecvBytesPerSec = value
			cluster.HostNetRecvReportingAgents++
			netRecv += *value
		}

		if value := validPercent(snapshot.ProcessCPUPercent); value != nil {
			brief.ProcessCPUPercent = value
			cluster.ProcessCPUReportingAgents++
			processCPUWeighted += *value * cpuWeight
			processCPUWeight += cpuWeight
			if cluster.MaxProcessCPUPercent == nil || *value > *cluster.MaxProcessCPUPercent {
				cluster.MaxProcessCPUPercent = new(*value)
				cluster.HotProcessCPUAgentID = agent.ID
				cluster.HotProcessCPUAgentName = agent.Name
			}
		}
		if snapshot.ProcessRSSBytes != nil {
			value := *snapshot.ProcessRSSBytes
			brief.ProcessRSSBytes = new(value)
			cluster.ProcessRSSReportingAgents++
			totalRSS += value
			if cluster.MaxProcessRSSBytes == nil || value > *cluster.MaxProcessRSSBytes {
				cluster.MaxProcessRSSBytes = new(value)
				cluster.HotProcessRSSAgentID = agent.ID
				cluster.HotProcessRSSAgentName = agent.Name
			}
		}

		heap := snapshot.ProcessHeapBytes
		goroutines := snapshot.ProcessGoroutines
		brief.ProcessHeapBytes = new(heap)
		brief.ProcessGoroutines = new(goroutines)
		totalHeap += heap
		totalGoroutines += goroutines
		if snapshot.ProcessThreads != nil && *snapshot.ProcessThreads >= 0 {
			value := *snapshot.ProcessThreads
			brief.ProcessThreads = new(value)
			cluster.ProcessThreadsReportingAgents++
			totalThreads += value
		}
		if snapshot.ProcessFDs != nil && *snapshot.ProcessFDs >= 0 {
			value := *snapshot.ProcessFDs
			brief.ProcessFDs = new(value)
			cluster.ProcessFDsReportingAgents++
			totalFDs += value
			if cluster.MaxProcessFDs == nil || value > *cluster.MaxProcessFDs {
				cluster.MaxProcessFDs = new(value)
				cluster.HotProcessFDsAgentID = agent.ID
				cluster.HotProcessFDsAgentName = agent.Name
			}
		}
		cluster.Agents = append(cluster.Agents, brief)
	}

	if cluster.AgentCount > 0 {
		cluster.CoverageRatio = float64(cluster.ReportingAgents) / float64(cluster.AgentCount)
	}
	if hostCPUWeight > 0 {
		cluster.AvgHostCPUPercent = new(hostCPUWeighted / hostCPUWeight)
	}
	if totalMemory > 0 {
		cluster.TotalHostMemBytes = new(totalMemory)
		cluster.UsedHostMemBytes = new(usedMemory)
		cluster.AvgHostMemPercent = new(float64(usedMemory) / float64(totalMemory) * 100)
	}
	if cluster.HostNetSendReportingAgents > 0 {
		cluster.TotalHostNetSendBytesPerSec = new(netSend)
	}
	if cluster.HostNetRecvReportingAgents > 0 {
		cluster.TotalHostNetRecvBytesPerSec = new(netRecv)
	}
	if processCPUWeight > 0 {
		cluster.AvgProcessCPUPercent = new(processCPUWeighted / processCPUWeight)
	}
	if cluster.ReportingAgents > 0 {
		cluster.TotalProcessHeapBytes = new(totalHeap)
		cluster.TotalProcessGoroutines = new(totalGoroutines)
	}
	if cluster.ProcessRSSReportingAgents > 0 {
		cluster.TotalProcessRSSBytes = new(totalRSS)
	}
	if cluster.ProcessThreadsReportingAgents > 0 {
		cluster.TotalProcessThreads = new(totalThreads)
	}
	if cluster.ProcessFDsReportingAgents > 0 {
		cluster.TotalProcessFDs = new(totalFDs)
	}
	return cluster
}

func uniqueSystemAgentIDs(agentIDs []string, agents []*AgentNode) []string {
	set := make(map[string]struct{})
	if agentIDs == nil {
		for _, agent := range agents {
			set[agent.ID] = struct{}{}
		}
	} else {
		for _, id := range agentIDs {
			if id != "" {
				set[id] = struct{}{}
			}
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func systemSnapshotFreshFor(interval string) time.Duration {
	value, err := time.ParseDuration(interval)
	if err != nil || value <= 0 {
		value = 5 * time.Second
	}
	value *= 3
	if value < 15*time.Second {
		return 15 * time.Second
	}
	return value
}

func validPercent(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 100 {
		return nil
	}
	return new(*value)
}

func validNonNegative(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		return nil
	}
	return new(*value)
}

func validHostMemory(snapshot *SystemSnapshot) (uint64, uint64, float64, bool) {
	if snapshot.HostMemTotalBytes == nil || snapshot.HostMemUsedBytes == nil {
		return 0, 0, 0, false
	}
	total := *snapshot.HostMemTotalBytes
	used := *snapshot.HostMemUsedBytes
	if total == 0 || used > total {
		return 0, 0, 0, false
	}
	return total, used, float64(used) / float64(total) * 100, true
}

//go:fix inline
func float64Pointer(value float64) *float64 { return new(value) }

//go:fix inline
func uint64Pointer(value uint64) *uint64 { return new(value) }

//go:fix inline
func int32Pointer(value int32) *int32 { return new(value) }

//go:fix inline
func intPointer(value int) *int { return new(value) }

//go:fix inline
func timePointer(value time.Time) *time.Time { return new(value) }

func taskSystemAgentIDs(task *Task) []string {
	if task == nil {
		return nil
	}
	ids := make([]string, 0, len(task.Assignments))
	for _, assignment := range task.Assignments {
		ids = append(ids, assignment.AgentID)
	}
	return ids
}

// Now 当前时间（可被测试覆盖）。
var Now = func() time.Time {
	return time.Now()
}
