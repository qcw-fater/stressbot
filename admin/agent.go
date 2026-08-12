package admin

import (
	"context"
	"fmt"
	"sync"
	"time"

	"stressbot/monitor"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// statusChange 状态变更事件，锁外触发 onChange。
type statusChange struct {
	agentID string
	from    AgentStatus
	to      AgentStatus
}

// AgentRegistry Agent 注册表。
type AgentRegistry struct {
	mu        sync.RWMutex
	agents    map[string]*AgentNode
	instances map[string]agentInstance
	cfg       RegistryConfig
	onChange  func(agentID string, from, to AgentStatus)
	onRestart func(agentID, taskID string)

	unhealthyThreshold time.Duration
	offlineThreshold   time.Duration
}

func NewAgentRegistry(cfg RegistryConfig, onChange func(string, AgentStatus, AgentStatus)) *AgentRegistry {
	unhealthy := utils.ParseDurationDefault(cfg.UnhealthyAfter, 30*time.Second, "agentRegistry.unhealthyAfter")
	offline := utils.ParseDurationDefault(cfg.OfflineAfter, 60*time.Second, "agentRegistry.offlineAfter")

	return &AgentRegistry{
		agents:             make(map[string]*AgentNode),
		instances:          make(map[string]agentInstance),
		cfg:                cfg,
		onChange:           onChange,
		unhealthyThreshold: unhealthy,
		offlineThreshold:   offline,
	}
}

// fireOnChange 在锁外触发回调，防止死锁。
func (r *AgentRegistry) fireOnChange(changes []statusChange) {
	if r.onChange == nil {
		return
	}
	for _, c := range changes {
		r.onChange(c.agentID, c.from, c.to)
	}
}

// Register 注册 Agent。
//
//   - 同 ID 重新注册：旧 entry 覆盖为新的 idle 节点；
//   - 旧节点关联的运行任务由调度层通过 onAgentStatusChange + 心跳超时安全网处理。
func (r *AgentRegistry) Register(node *AgentNode) error {
	r.mu.Lock()
	existing, exists := r.agents[node.ID]
	previousInstance, hadInstance := r.instances[node.ID]
	from := AgentStatus("")
	if exists {
		from = existing.Status
	}
	r.agents[node.ID] = node
	r.instances[node.ID] = agentInstance{startedAt: node.StaticInfo.StartedAt, taskID: node.CurrentTaskID}
	needCallback := exists && r.onChange != nil && from != node.Status
	restartedTaskID := ""
	if hadInstance && !previousInstance.startedAt.IsZero() && !node.StaticInfo.StartedAt.IsZero() &&
		!previousInstance.startedAt.Equal(node.StaticInfo.StartedAt) && previousInstance.taskID != "" {
		restartedTaskID = previousInstance.taskID
	}
	onRestart := r.onRestart

	if exists {
		stresslog.Warn("agent 重新注册",
			zap.String("agentID", node.ID),
			zap.String("agentName", node.Name),
			zap.String("addr", node.Address),
			zap.String("appVersion", node.AppVersion),
			zap.Int("maxBots", node.MaxBots),
			zap.String("currentTaskID", node.CurrentTaskID),
			zap.String("previousStatus", string(from)),
			zap.String("status", string(node.Status)))
	} else {
		stresslog.Info("agent 注册",
			zap.String("agentID", node.ID),
			zap.String("agentName", node.Name),
			zap.String("addr", node.Address),
			zap.String("appVersion", node.AppVersion),
			zap.Int("maxBots", node.MaxBots),
			zap.String("status", string(node.Status)))
	}
	r.mu.Unlock()

	if needCallback {
		r.onChange(node.ID, from, node.Status)
	}
	if restartedTaskID != "" && onRestart != nil {
		onRestart(node.ID, restartedTaskID)
	}
	return nil
}

// Heartbeat 处理心跳。
func (r *AgentRegistry) Heartbeat(agentID string, req HeartbeatRequest) error {
	r.mu.Lock()
	node, ok := r.agents[agentID]
	if !ok {
		r.mu.Unlock()
		return ErrAgentNotFound
	}

	if node.CurrentTaskID != req.CurrentTaskID {
		node.LatestStress = nil
		node.StressUpdatedAt = time.Time{}
	}
	node.CurrentTaskID = req.CurrentTaskID
	node.CurrentBots = req.CurrentBots
	requested, err := heartbeatAgentStatus(req.Status, req.CurrentTaskID)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	from := node.Status
	node.Status = requested
	r.instances[agentID] = agentInstance{startedAt: node.StaticInfo.StartedAt, taskID: req.CurrentTaskID}
	change := r.touchLocked(node, req.AppVersion)
	if change == nil && from != node.Status {
		change = &statusChange{agentID: node.ID, from: from, to: node.Status}
	}
	r.mu.Unlock()

	if change != nil {
		r.fireOnChange([]statusChange{*change})
	}
	return nil
}

type agentInstance struct {
	startedAt time.Time
	taskID    string
}

func heartbeatAgentStatus(value, taskID string) (AgentStatus, error) {
	switch value {
	case "busy":
		if taskID == "" {
			return "", fmt.Errorf("busy 心跳缺少 currentTaskId")
		}
		return AgentBusy, nil
	case "idle":
		if taskID != "" {
			return "", fmt.Errorf("idle 心跳不能携带 currentTaskId")
		}
		return AgentIdle, nil
	default:
		return "", fmt.Errorf("未知节点状态 %q", value)
	}
}

func (r *AgentRegistry) SetOnRestart(fn func(agentID, taskID string)) {
	r.mu.Lock()
	r.onRestart = fn
	r.mu.Unlock()
}

// StartTask synchronizes the registry as soon as a Start ACK is committed, so
// the first telemetry window does not have to wait for the next heartbeat.
func (r *AgentRegistry) StartTask(agentID, taskID string, bots int) error {
	r.mu.Lock()
	node, ok := r.agents[agentID]
	if !ok {
		r.mu.Unlock()
		return ErrAgentNotFound
	}
	from := node.Status
	node.Status, node.CurrentTaskID, node.CurrentBots = AgentBusy, taskID, bots
	r.instances[agentID] = agentInstance{startedAt: node.StaticInfo.StartedAt, taskID: taskID}
	node.LastHeartbeatAt = time.Now()
	r.mu.Unlock()
	if from != AgentBusy {
		r.fireOnChange([]statusChange{{agentID: agentID, from: from, to: AgentBusy}})
	}
	return nil
}

// CompleteTask 仅在 taskID 仍是节点当前任务时清理任务状态。
// 返回 false 表示完成报告已经迟到，节点可能已开始执行其他任务。
func (r *AgentRegistry) CompleteTask(agentID, taskID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	node, ok := r.agents[agentID]
	if !ok {
		return false, ErrAgentNotFound
	}
	if node.CurrentTaskID != taskID {
		return false, nil
	}

	node.CurrentTaskID = ""
	node.CurrentBots = 0
	node.Status = AgentIdle
	r.instances[agentID] = agentInstance{startedAt: node.StaticInfo.StartedAt}
	node.LatestStress = nil
	node.StressUpdatedAt = time.Time{}
	return true, nil
}

// Touch 用于"任何 Agent 请求"路径上刷新心跳时间。
func (r *AgentRegistry) Touch(agentID, appVersion string) {
	r.mu.Lock()
	node, ok := r.agents[agentID]
	if !ok {
		r.mu.Unlock()
		return
	}
	change := r.touchLocked(node, appVersion)
	r.mu.Unlock()

	if change != nil {
		r.fireOnChange([]statusChange{*change})
	}
}

// touchLocked 更新心跳时间、appVersion，并处理 unhealthy/offline → 在线 的恢复。
// 调用方必须已持有 r.mu。返回状态变更事件（锁外触发）。
func (r *AgentRegistry) touchLocked(node *AgentNode, appVersion string) *statusChange {
	node.LastHeartbeatAt = time.Now()
	if appVersion != "" {
		node.AppVersion = appVersion
	}

	if node.Status == AgentUnhealthy || node.Status == AgentOffline {
		from := node.Status
		if node.CurrentTaskID != "" {
			node.Status = AgentBusy
		} else {
			node.Status = AgentIdle
		}
		stresslog.Warn("agent 状态恢复",
			zap.String("agentID", node.ID),
			zap.String("agentName", node.Name),
			zap.String("addr", node.Address),
			zap.String("currentTaskID", node.CurrentTaskID),
			zap.String("from", string(from)),
			zap.String("to", string(node.Status)))
		return &statusChange{agentID: node.ID, from: from, to: node.Status}
	}
	return nil
}

// Deregister 注销 Agent。
func (r *AgentRegistry) Deregister(agentID string) error {
	r.mu.Lock()
	node, ok := r.agents[agentID]
	if !ok {
		r.mu.Unlock()
		return ErrAgentNotFound
	}

	var change *statusChange
	if node.CurrentTaskID != "" {
		change = &statusChange{agentID: agentID, from: node.Status, to: AgentOffline}
	}
	delete(r.agents, agentID)
	stresslog.Info("agent 注销",
		zap.String("agentID", agentID),
		zap.String("agentName", node.Name),
		zap.String("addr", node.Address),
		zap.String("currentTaskID", node.CurrentTaskID),
		zap.String("status", string(node.Status)))
	r.mu.Unlock()

	if change != nil {
		r.fireOnChange([]statusChange{*change})
	}
	return nil
}

// Get 获取 Agent 的读安全副本（无则返回 false）。
func (r *AgentRegistry) Get(agentID string) (*AgentNode, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	node, ok := r.agents[agentID]
	if !ok {
		return nil, false
	}
	return cloneNodeForRead(node), true
}

// List 列出所有 Agent 的读安全副本。
func (r *AgentRegistry) List() []*AgentNode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*AgentNode, 0, len(r.agents))
	for _, n := range r.agents {
		out = append(out, cloneNodeForRead(n))
	}
	return out
}

// ListByStatus 按状态列出 Agent 的读安全副本。
func (r *AgentRegistry) ListByStatus(status AgentStatus) []*AgentNode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*AgentNode
	for _, n := range r.agents {
		if n.Status == status {
			out = append(out, cloneNodeForRead(n))
		}
	}
	return out
}

// cloneNodeForRead 返回 AgentNode 的读安全浅拷贝。
//
// Register/Heartbeat/Touch/UpdateStress/scanAndMarkStatus 均在 r.mu 写锁内就地改写 *AgentNode
// 的标量字段（Status/CurrentTaskID/CurrentBots/LastHeartbeatAt 等），若 Get/List 直接返回内部
// 指针，调用方在锁外读取这些字段会与写入并发触发 data race。指针字段 LatestStress/LatestSystem
// 以「整体重新赋值」方式更新（不就地改写指向的快照对象），浅拷贝持有的指针即为一致快照。
// 调用方必须持有 r.mu。
func cloneNodeForRead(n *AgentNode) *AgentNode {
	cp := *n
	return &cp
}

// UpdateStress 更新 Agent 压测指标快照。
func (r *AgentRegistry) UpdateStress(agentID string, snap *monitor.CollectorSnapshot, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if node, ok := r.agents[agentID]; ok {
		node.LatestStress = snap
		node.StressUpdatedAt = at
	}
}

// UpdateSystem 更新 Agent 系统指标快照。
func (r *AgentRegistry) UpdateSystem(agentID string, snap *SystemSnapshot, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if node, ok := r.agents[agentID]; ok {
		if snap == nil || (node.LatestSystem != nil && snap.Sequence <= node.LatestSystem.Sequence) {
			return
		}
		node.LatestSystem = snap
		node.SystemUpdatedAt = at
	}
}

// StartHealthChecker 启动心跳超时检测协程。
func (r *AgentRegistry) StartHealthChecker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	utils.GetWorkPool().GoWithStop(func(stopCh <-chan struct{}) {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopCh:
				return
			case <-ticker.C:
				r.scanAndMarkStatus()
			}
		}
	})
}

func (r *AgentRegistry) scanAndMarkStatus() {
	var changes []statusChange

	r.mu.Lock()
	now := time.Now()
	for _, node := range r.agents {
		lag := now.Sub(node.LastHeartbeatAt)
		var newStatus AgentStatus

		switch {
		case lag >= r.offlineThreshold:
			newStatus = AgentOffline
		case lag >= r.unhealthyThreshold:
			newStatus = AgentUnhealthy
		default:
			continue
		}

		if node.Status != newStatus {
			from := node.Status
			node.Status = newStatus
			changes = append(changes, statusChange{agentID: node.ID, from: from, to: newStatus})
			stresslog.Warn("agent 状态变更",
				zap.String("agentID", node.ID),
				zap.String("agentName", node.Name),
				zap.String("addr", node.Address),
				zap.String("currentTaskID", node.CurrentTaskID),
				zap.Int("currentBots", node.CurrentBots),
				zap.String("from", string(from)),
				zap.String("to", string(newStatus)),
				zap.Duration("lag", lag))
		}

		// offline → 直接删除（任务已通过 onChange 回调处理）
		if node.Status == AgentOffline {
			delete(r.agents, node.ID)
			stresslog.Info("agent 已离线，自动清理",
				zap.String("agentID", node.ID),
				zap.String("agentName", node.Name),
				zap.String("addr", node.Address),
				zap.String("currentTaskID", node.CurrentTaskID),
				zap.Duration("lag", lag))
		}
	}
	r.mu.Unlock()

	r.fireOnChange(changes)
}
