package admin

import (
	"context"
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
	mu       sync.RWMutex
	agents   map[string]*AgentNode
	cfg      RegistryConfig
	onChange func(agentID string, from, to AgentStatus)

	unhealthyThreshold time.Duration
	offlineThreshold   time.Duration
}

func NewAgentRegistry(cfg RegistryConfig, onChange func(string, AgentStatus, AgentStatus)) *AgentRegistry {
	unhealthy := utils.ParseDurationDefault(cfg.UnhealthyAfter, 30*time.Second, "agentRegistry.unhealthyAfter")
	offline := utils.ParseDurationDefault(cfg.OfflineAfter, 60*time.Second, "agentRegistry.offlineAfter")

	return &AgentRegistry{
		agents:             make(map[string]*AgentNode),
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
	from := AgentStatus("")
	if exists {
		from = existing.Status
	}
	r.agents[node.ID] = node
	needCallback := exists && r.onChange != nil && from != node.Status

	if exists {
		stresslog.Warn("agent 重新注册",
			zap.String("agentId", node.ID),
			zap.String("name", node.Name),
			zap.String("address", node.Address),
			zap.String("previousStatus", string(from)))
	} else {
		stresslog.Info("agent 注册",
			zap.String("agentId", node.ID),
			zap.String("name", node.Name),
			zap.String("address", node.Address))
	}
	r.mu.Unlock()

	if needCallback {
		r.onChange(node.ID, from, node.Status)
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
	change := r.touchLocked(node, req.AppVersion)
	r.mu.Unlock()

	if change != nil {
		r.fireOnChange([]statusChange{*change})
	}
	return nil
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
			zap.String("agentId", node.ID),
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
	stresslog.Info("agent 注销", zap.String("agentId", agentID),
		zap.String("currentTaskId", node.CurrentTaskID))
	r.mu.Unlock()

	if change != nil {
		r.fireOnChange([]statusChange{*change})
	}
	return nil
}

// Get 获取 Agent。
func (r *AgentRegistry) Get(agentID string) (*AgentNode, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	node, ok := r.agents[agentID]
	return node, ok
}

// List 列出所有 Agent。
func (r *AgentRegistry) List() []*AgentNode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*AgentNode, 0, len(r.agents))
	for _, n := range r.agents {
		out = append(out, n)
	}
	return out
}

// ListByStatus 按状态列出 Agent。
func (r *AgentRegistry) ListByStatus(status AgentStatus) []*AgentNode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*AgentNode
	for _, n := range r.agents {
		if n.Status == status {
			out = append(out, n)
		}
	}
	return out
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
				zap.String("agentId", node.ID),
				zap.String("from", string(from)),
				zap.String("to", string(newStatus)),
				zap.Duration("lag", lag))
		}

		// offline → 直接删除（任务已通过 onChange 回调处理）
		if node.Status == AgentOffline {
			delete(r.agents, node.ID)
			stresslog.Info("agent 已离线，自动清理",
				zap.String("agentId", node.ID),
				zap.String("name", node.Name))
		}
	}
	r.mu.Unlock()

	r.fireOnChange(changes)
}
