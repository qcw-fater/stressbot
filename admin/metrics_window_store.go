package admin

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"stressbot/monitor"
)

type MetricWindowAcceptStatus string

const (
	MetricWindowAccepted  MetricWindowAcceptStatus = "accepted"
	MetricWindowDuplicate MetricWindowAcceptStatus = "duplicate"
)

type MetricWindowAcceptResult struct {
	Status MetricWindowAcceptStatus
}

// AgentMetricState is the last committed cumulative/window pair for one task Agent.
// ReceivedAt always comes from the Admin clock and is the only freshness timestamp.
type AgentMetricState struct {
	AgentID       string
	LastSequence  uint64
	ReceivedAt    time.Time
	ExpectedEvery time.Duration
	Cumulative    monitor.CollectorSnapshot
	LatestWindow  monitor.ReportWindow
}

type MetricHistoryWindow struct {
	AgentID      string
	ApdexT       int
	TimingDetail monitor.TimingDetailLevel
	Robots       monitor.RobotSnapshot
	Connections  monitor.ConnectionSnapshot
	Window       monitor.ReportWindow
}

type MetricHistoryBatch struct {
	Token   [sha256.Size]byte
	Windows []MetricHistoryWindow
}

// MetricsWindowStore commits sequenced Agent windows exactly once.
type MetricsWindowStore struct {
	mu              sync.RWMutex
	byTask          map[string]map[string]*AgentMetricState
	pendingHistory  map[string][]MetricHistoryWindow
	historyInFlight map[string]*MetricHistoryBatch
	terminalTasks   map[string]struct{}
	now             func() time.Time
}

func NewMetricsWindowStore(now func() time.Time) *MetricsWindowStore {
	if now == nil {
		now = time.Now
	}
	return &MetricsWindowStore{
		byTask:          make(map[string]map[string]*AgentMetricState),
		pendingHistory:  make(map[string][]MetricHistoryWindow),
		historyInFlight: make(map[string]*MetricHistoryBatch),
		terminalTasks:   make(map[string]struct{}),
		now:             now,
	}
}

func (s *MetricsWindowStore) Accept(
	report StressReport,
	expectedTaskID string,
	expectedEvery time.Duration,
	expectedApdexT time.Duration,
) (MetricWindowAcceptResult, error) {
	if report.AgentID == "" {
		return MetricWindowAcceptResult{}, fmt.Errorf("指标上报缺少节点 ID")
	}
	if report.TaskID == "" || report.TaskID != expectedTaskID {
		return MetricWindowAcceptResult{}, fmt.Errorf("指标上报任务不匹配: report=%q current=%q", report.TaskID, expectedTaskID)
	}
	if report.Snapshot == nil || report.Snapshot.Window == nil {
		return MetricWindowAcceptResult{}, fmt.Errorf("指标上报缺少统计窗口")
	}
	window := report.Snapshot.Window
	if window.Sequence == 0 {
		return MetricWindowAcceptResult{}, fmt.Errorf("指标窗口序列号必须从 1 开始")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	taskStates := s.byTask[report.TaskID]
	if taskStates == nil {
		taskStates = make(map[string]*AgentMetricState)
		s.byTask[report.TaskID] = taskStates
	}
	previous := taskStates[report.AgentID]
	if previous != nil && window.Sequence <= previous.LastSequence {
		return MetricWindowAcceptResult{Status: MetricWindowDuplicate}, nil
	}
	expectedSequence := uint64(1)
	if previous != nil {
		expectedSequence = previous.LastSequence + 1
	}
	if window.Sequence != expectedSequence {
		return MetricWindowAcceptResult{}, fmt.Errorf(
			"指标窗口序列不连续: 节点=%s 收到=%d 期望=%d",
			report.AgentID, window.Sequence, expectedSequence,
		)
	}
	if err := validateMetricReport(report.Snapshot, expectedEvery, expectedApdexT); err != nil {
		return MetricWindowAcceptResult{}, err
	}
	if previous != nil {
		switch {
		case report.Snapshot.CollectionEpoch < previous.Cumulative.CollectionEpoch:
			return MetricWindowAcceptResult{}, fmt.Errorf(
				"节点 %s 指标代次回退: report=%d current=%d",
				report.AgentID, report.Snapshot.CollectionEpoch, previous.Cumulative.CollectionEpoch,
			)
		case report.Snapshot.CollectionEpoch == previous.Cumulative.CollectionEpoch:
			if err := validateCumulativeMonotonic(&previous.Cumulative, report.Snapshot); err != nil {
				return MetricWindowAcceptResult{}, fmt.Errorf("节点 %s 累计指标回退: %w", report.AgentID, err)
			}
		}
	}

	cumulative := *report.Snapshot
	cumulative.Window = nil
	state := &AgentMetricState{
		AgentID:       report.AgentID,
		LastSequence:  window.Sequence,
		ReceivedAt:    s.now(),
		ExpectedEvery: expectedEvery,
		Cumulative:    cumulative,
		LatestWindow:  *window,
	}
	taskStates[report.AgentID] = state
	s.pendingHistory[report.TaskID] = append(s.pendingHistory[report.TaskID], MetricHistoryWindow{
		AgentID:      report.AgentID,
		ApdexT:       report.Snapshot.ApdexT,
		TimingDetail: report.Snapshot.TimingDetail,
		Robots:       report.Snapshot.Robots,
		Connections:  report.Snapshot.Connections,
		Window:       *window,
	})
	return MetricWindowAcceptResult{Status: MetricWindowAccepted}, nil
}

func validateMetricReport(snapshot *monitor.CollectorSnapshot, expectedEvery, expectedApdexT time.Duration) error {
	if snapshot.CollectionEpoch == 0 {
		return fmt.Errorf("指标采集代次必须从 1 开始")
	}
	switch snapshot.TimingDetail {
	case monitor.TimingRTTOnly, monitor.TimingCodecDetail, monitor.TimingFullDetail:
	default:
		return fmt.Errorf("指标计时级别无效: %q", snapshot.TimingDetail)
	}
	window := snapshot.Window
	if !window.StartedAt.Before(window.EndedAt) {
		return fmt.Errorf("指标窗口时间范围无效")
	}
	actualDuration := window.EndedAt.Sub(window.StartedAt)
	if math.Abs(window.DurationSeconds-actualDuration.Seconds()) > float64(time.Millisecond)/float64(time.Second) {
		return fmt.Errorf("指标窗口时长与起止时间不一致")
	}
	if expectedEvery <= 0 || math.Abs(window.ExpectedIntervalSeconds-expectedEvery.Seconds()) > float64(time.Millisecond)/float64(time.Second) {
		return fmt.Errorf("指标上报周期不匹配: report=%.6fs expected=%.6fs", window.ExpectedIntervalSeconds, expectedEvery.Seconds())
	}
	if snapshot.ApdexT != int(expectedApdexT/time.Millisecond) {
		return fmt.Errorf("Apdex 阈值不匹配: report=%dms expected=%dms", snapshot.ApdexT, expectedApdexT/time.Millisecond)
	}
	if snapshot.TotalActions < 0 || snapshot.InvalidMetricSamples < 0 || window.InvalidMetricSamples < 0 ||
		window.Bandwidth.SendBytes < 0 || window.Bandwidth.RecvBytes < 0 ||
		snapshot.Bandwidth.TotalSendBytes < 0 || snapshot.Bandwidth.TotalRecvBytes < 0 ||
		snapshot.Robots.Started < 0 || snapshot.Robots.Running < 0 || snapshot.Robots.Stopped < 0 || snapshot.Robots.Errored < 0 ||
		snapshot.Connections.Established < 0 || snapshot.Connections.Active < 0 || snapshot.Connections.Closed < 0 ||
		snapshot.Connections.Failed < 0 || snapshot.Connections.Dropped < 0 {
		return fmt.Errorf("指标报告含负数计数")
	}
	if !validDerivedValue(window.Bandwidth.SendMBps, float64(window.Bandwidth.SendBytes)/1024/1024/window.DurationSeconds) ||
		!validDerivedValue(window.Bandwidth.RecvMBps, float64(window.Bandwidth.RecvBytes)/1024/1024/window.DurationSeconds) {
		return fmt.Errorf("指标窗口带宽速率与字节数不一致")
	}
	if err := validateMetricActions("累计", snapshot.Actions); err != nil {
		return err
	}
	if err := validateMetricActions("窗口", window.Actions); err != nil {
		return err
	}
	if totalActionSamples(snapshot.Actions) != snapshot.TotalActions {
		return fmt.Errorf("累计动作总数与动作明细不一致")
	}
	if totalActionSamples(window.Actions) != window.Summary.SampleCount {
		return fmt.Errorf("窗口样本总数与动作明细不一致")
	}
	return nil
}

func validateMetricActions(scope string, actions []monitor.ActionSnapshot) error {
	seen := make(map[string]struct{}, len(actions))
	for _, action := range actions {
		if action.Name == "" {
			return fmt.Errorf("%s动作名称为空", scope)
		}
		if _, exists := seen[action.Name]; exists {
			return fmt.Errorf("%s动作 %q 重复", scope, action.Name)
		}
		seen[action.Name] = struct{}{}
		if action.SampleCount < 0 || action.SuccessCount < 0 || action.FailureCount < 0 || action.TimeoutCount < 0 ||
			action.CanceledCount < 0 || action.Executing < 0 || action.ByteSampleCount < 0 ||
			action.RTTSampleCount < 0 || action.RTTApdexSampleCount < 0 || action.RTTFailedCount < 0 ||
			action.ListenWaitSampleCount < 0 || action.ListenReadyCount < 0 || action.ListenTimeoutCount < 0 ||
			action.TotalDurationSampleCount < 0 || action.BuildSampleCount < 0 || action.EncodeSampleCount < 0 ||
			action.SendSampleCount < 0 || action.DecodeWaitSampleCount < 0 || action.DecodeSampleCount < 0 ||
			action.DispatchWaitSampleCount < 0 || action.ParseStoreSampleCount < 0 ||
			action.TotalSendBytes < 0 || action.TotalRecvBytes < 0 || action.TimeoutTotalNs < 0 ||
			action.ClientCostCount < 0 || action.ClientCostSumNs < 0 || action.BuildCostSumNs < 0 ||
			action.EncodeCostSumNs < 0 || action.SendCostSumNs < 0 || action.DecodeWaitSumNs < 0 ||
			action.DecodeCostSumNs < 0 || action.DispatchWaitSumNs < 0 || action.ParseStoreSumNs < 0 {
			return fmt.Errorf("%s动作 %q 含负数计数", scope, action.Name)
		}
		if action.SampleCount != action.SuccessCount+action.FailureCount+action.TimeoutCount {
			return fmt.Errorf("%s动作 %q 样本总数与结果计数不一致", scope, action.Name)
		}
		if action.RTT.Count != action.RTTSampleCount || action.ListenWait.Count != action.ListenWaitSampleCount || action.TotalDuration.Count != action.TotalDurationSampleCount {
			return fmt.Errorf("%s动作 %q 分布样本数与原始计数不一致", scope, action.Name)
		}
		if action.RTTApdexSampleCount != action.RTTSampleCount+action.RTTFailedCount {
			return fmt.Errorf("%s动作 %q Apdex 分母不一致", scope, action.Name)
		}
		if action.ApdexSatisfied < 0 || action.ApdexTolerating < 0 || action.ApdexSatisfied+action.ApdexTolerating > action.RTTSampleCount {
			return fmt.Errorf("%s动作 %q Apdex 原始计数无效", scope, action.Name)
		}
		if !validActionDerivedValues(action) {
			return fmt.Errorf("%s动作 %q 派生指标与原始计数不一致", scope, action.Name)
		}
		for name, histogram := range map[string]monitor.HistogramSnapshot{
			"RTT": action.RTT, "监听等待": action.ListenWait, "总耗时": action.TotalDuration,
		} {
			if histogram.Count == 0 {
				if histogram.SumNs != 0 || len(histogram.Sketch) != 0 || histogramHasValue(histogram) {
					return fmt.Errorf("%s动作 %q 的%s空分布携带数据", scope, action.Name, name)
				}
				continue
			}
			if histogram.SumNs < 0 || !validHistogramValues(histogram) {
				return fmt.Errorf("%s动作 %q 的%s展示值无效", scope, action.Name, name)
			}
			if _, err := monitor.MergeHistograms([]monitor.HistogramSnapshot{histogram}); err != nil {
				return fmt.Errorf("%s动作 %q 的%s分布无效: %w", scope, action.Name, name, err)
			}
		}
	}
	return nil
}

func validActionDerivedValues(action monitor.ActionSnapshot) bool {
	if !validDerivedValue(action.SuccessRate, ratio(action.SuccessCount, action.SampleCount)) ||
		!validDerivedValue(action.AvgSendBytes, ratio(action.TotalSendBytes, action.ByteSampleCount)) ||
		!validDerivedValue(action.AvgRecvBytes, ratio(action.TotalRecvBytes, action.ByteSampleCount)) ||
		!validDerivedValue(action.RTTApdex, apdexValue(action)) ||
		!validDerivedValue(action.ListenTimeoutRate, ratio(
			action.ListenTimeoutCount,
			action.ListenWaitSampleCount+action.ListenReadyCount+action.ListenTimeoutCount,
		)) || !finiteNonNegative(action.AvgQPS) || !finiteNonNegative(action.PeriodQPS) {
		return false
	}
	for _, metric := range []struct {
		average float64
		sumNs   int64
		count   int64
	}{
		{action.TimeoutAvgMs, action.TimeoutTotalNs, action.TimeoutCount},
		{action.ClientAvgMs, action.ClientCostSumNs, action.ClientCostCount},
		{action.BuildAvgMs, action.BuildCostSumNs, action.BuildSampleCount},
		{action.EncodeAvgMs, action.EncodeCostSumNs, action.EncodeSampleCount},
		{action.SendAvgMs, action.SendCostSumNs, action.SendSampleCount},
		{action.DecodeWaitAvgMs, action.DecodeWaitSumNs, action.DecodeWaitSampleCount},
		{action.DecodeAvgMs, action.DecodeCostSumNs, action.DecodeSampleCount},
		{action.DispatchToActionWaitAvgMs, action.DispatchWaitSumNs, action.DispatchWaitSampleCount},
		{action.ParseStoreAvgMs, action.ParseStoreSumNs, action.ParseStoreSampleCount},
	} {
		if metric.count == 0 && metric.sumNs != 0 {
			return false
		}
		expected := 0.0
		if metric.count > 0 {
			expected = float64(metric.sumNs) / float64(metric.count) / float64(time.Millisecond)
		}
		if !validDerivedValue(metric.average, expected) {
			return false
		}
	}
	expectedKind := monitor.ActionKindLocal
	switch {
	case action.RTTApdexSampleCount > 0:
		expectedKind = monitor.ActionKindNetworked
	case action.ListenWaitSampleCount+action.ListenReadyCount+action.ListenTimeoutCount > 0:
		expectedKind = monitor.ActionKindListen
	case action.TotalSendBytes > 0:
		expectedKind = monitor.ActionKindSend
	}
	return action.Kind == expectedKind
}

func ratio(numerator, denominator int64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func apdexValue(action monitor.ActionSnapshot) float64 {
	if action.RTTApdexSampleCount == 0 {
		return 0
	}
	return (float64(action.ApdexSatisfied) + float64(action.ApdexTolerating)*0.5) / float64(action.RTTApdexSampleCount)
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func validDerivedValue(actual, expected float64) bool {
	if !finiteNonNegative(actual) || !finiteNonNegative(expected) {
		return false
	}
	return math.Abs(actual-expected) <= math.Max(1e-12, math.Abs(expected)*1e-12)
}

func totalActionSamples(actions []monitor.ActionSnapshot) int64 {
	var total int64
	for _, action := range actions {
		total += action.SampleCount
	}
	return total
}

func histogramHasValue(histogram monitor.HistogramSnapshot) bool {
	return histogram.MinMs != nil || histogram.MaxMs != nil || histogram.AvgMs != nil ||
		histogram.P50Ms != nil || histogram.P90Ms != nil || histogram.P95Ms != nil || histogram.P99Ms != nil
}

func validHistogramValues(histogram monitor.HistogramSnapshot) bool {
	values := []*float64{
		histogram.MinMs, histogram.P50Ms, histogram.P90Ms, histogram.P95Ms,
		histogram.P99Ms, histogram.AvgMs, histogram.MaxMs,
	}
	for _, value := range values {
		if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
			return false
		}
	}
	min, max := *histogram.MinMs, *histogram.MaxMs
	if min > *histogram.P50Ms || *histogram.P50Ms > *histogram.P90Ms ||
		*histogram.P90Ms > *histogram.P95Ms || *histogram.P95Ms > *histogram.P99Ms ||
		*histogram.P99Ms > max || *histogram.AvgMs < min || *histogram.AvgMs > max {
		return false
	}
	expectedAverage := float64(histogram.SumNs) / float64(histogram.Count) / float64(time.Millisecond)
	return math.Abs(*histogram.AvgMs-expectedAverage) <= math.Max(1e-12, math.Abs(expectedAverage)*1e-12)
}

func validateCumulativeMonotonic(previous, current *monitor.CollectorSnapshot) error {
	if current.TotalActions < previous.TotalActions || current.InvalidMetricSamples < previous.InvalidMetricSamples {
		return fmt.Errorf("顶层累计计数变小")
	}
	currentByName := make(map[string]monitor.ActionSnapshot, len(current.Actions))
	for _, action := range current.Actions {
		currentByName[action.Name] = action
	}
	for _, old := range previous.Actions {
		cur, ok := currentByName[old.Name]
		if !ok {
			return fmt.Errorf("动作 %q 消失", old.Name)
		}
		if cur.SampleCount < old.SampleCount || cur.SuccessCount < old.SuccessCount || cur.FailureCount < old.FailureCount ||
			cur.TimeoutCount < old.TimeoutCount || cur.CanceledCount < old.CanceledCount || cur.RTT.Count < old.RTT.Count ||
			cur.ListenWait.Count < old.ListenWait.Count || cur.TotalDuration.Count < old.TotalDuration.Count ||
			cur.TotalSendBytes < old.TotalSendBytes || cur.TotalRecvBytes < old.TotalRecvBytes ||
			cur.TimeoutTotalNs < old.TimeoutTotalNs || cur.ClientCostSumNs < old.ClientCostSumNs ||
			cur.BuildCostSumNs < old.BuildCostSumNs || cur.EncodeCostSumNs < old.EncodeCostSumNs ||
			cur.SendCostSumNs < old.SendCostSumNs || cur.DecodeWaitSumNs < old.DecodeWaitSumNs ||
			cur.DecodeCostSumNs < old.DecodeCostSumNs || cur.DispatchWaitSumNs < old.DispatchWaitSumNs ||
			cur.ParseStoreSumNs < old.ParseStoreSumNs {
			return fmt.Errorf("动作 %q 累计计数变小", old.Name)
		}
	}
	return nil
}

func (s *MetricsWindowStore) PendingHistoryCount(taskID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := len(s.pendingHistory[taskID])
	if batch := s.historyInFlight[taskID]; batch != nil {
		count += len(batch.Windows)
	}
	return count
}

func (s *MetricsWindowStore) PeekHistory(taskID string) (MetricHistoryBatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batch := s.historyInFlight[taskID]; batch != nil {
		return cloneMetricHistoryBatch(*batch), true
	}
	windows := s.pendingHistory[taskID]
	if len(windows) == 0 {
		return MetricHistoryBatch{}, false
	}
	batch := &MetricHistoryBatch{
		Token:   metricHistoryBatchToken(windows),
		Windows: append([]MetricHistoryWindow(nil), windows...),
	}
	s.pendingHistory[taskID] = nil
	s.historyInFlight[taskID] = batch
	return cloneMetricHistoryBatch(*batch), true
}

func (s *MetricsWindowStore) AckHistory(taskID string, token [sha256.Size]byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := s.historyInFlight[taskID]
	if batch == nil || batch.Token != token {
		return false
	}
	delete(s.historyInFlight, taskID)
	s.cleanupTerminalTaskLocked(taskID)
	return true
}

// MarkTaskTerminal releases a completed task after every accepted window has
// either been persisted or is no longer in flight.
func (s *MetricsWindowStore) MarkTaskTerminal(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminalTasks[taskID] = struct{}{}
	s.cleanupTerminalTaskLocked(taskID)
}

// DropTask releases a completed task when history persistence is disabled.
func (s *MetricsWindowStore) DropTask(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropTaskLocked(taskID)
}

func (s *MetricsWindowStore) cleanupTerminalTaskLocked(taskID string) {
	if _, terminal := s.terminalTasks[taskID]; !terminal {
		return
	}
	if len(s.pendingHistory[taskID]) != 0 || s.historyInFlight[taskID] != nil {
		return
	}
	s.dropTaskLocked(taskID)
}

func (s *MetricsWindowStore) dropTaskLocked(taskID string) {
	delete(s.byTask, taskID)
	delete(s.pendingHistory, taskID)
	delete(s.historyInFlight, taskID)
	delete(s.terminalTasks, taskID)
}

func cloneMetricHistoryBatch(batch MetricHistoryBatch) MetricHistoryBatch {
	batch.Windows = append([]MetricHistoryWindow(nil), batch.Windows...)
	return batch
}

func metricHistoryBatchToken(windows []MetricHistoryWindow) [sha256.Size]byte {
	type sequenceRange struct {
		first uint64
		last  uint64
	}
	ranges := make(map[string]sequenceRange)
	for _, item := range windows {
		sequence := item.Window.Sequence
		current, ok := ranges[item.AgentID]
		if !ok || sequence < current.first {
			current.first = sequence
		}
		if !ok || sequence > current.last {
			current.last = sequence
		}
		ranges[item.AgentID] = current
	}
	agents := make([]string, 0, len(ranges))
	for agentID := range ranges {
		agents = append(agents, agentID)
	}
	sort.Strings(agents)
	hash := sha256.New()
	var encoded [16]byte
	for _, agentID := range agents {
		hash.Write([]byte(agentID))
		hash.Write([]byte{0})
		rangeValue := ranges[agentID]
		binary.BigEndian.PutUint64(encoded[:8], rangeValue.first)
		binary.BigEndian.PutUint64(encoded[8:], rangeValue.last)
		hash.Write(encoded[:])
	}
	var token [sha256.Size]byte
	copy(token[:], hash.Sum(nil))
	return token
}

func (s *MetricsWindowStore) AgentState(taskID, agentID string) (AgentMetricState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	taskStates := s.byTask[taskID]
	if taskStates == nil || taskStates[agentID] == nil {
		return AgentMetricState{}, false
	}
	return *taskStates[agentID], true
}

func (s *MetricsWindowStore) States(taskID string) []AgentMetricState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	taskStates := s.byTask[taskID]
	states := make([]AgentMetricState, 0, len(taskStates))
	for _, state := range taskStates {
		states = append(states, *state)
	}
	return states
}
