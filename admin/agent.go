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

// Register 注册 Agent。
func (r *AgentRegistry) Register(node *AgentNode) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, exists := r.agents[node.ID]
	if exists {
		// 同名 offline 可覆盖
		if existing.Status != AgentOffline {
			return ErrAgentBusy.WithMessage("agent already registered and not offline")
		}
	}

	from := AgentStatus("")
	if exists {
		from = existing.Status
	}
	r.agents[node.ID] = node

	if r.onChange != nil && exists && from != node.Status {
		r.onChange(node.ID, from, node.Status)
	}

	stresslog.Info("agent 注册",
		zap.String("agentId", node.ID),
		zap.String("name", node.Name),
		zap.String("address", node.Address))
	return nil
}

// Heartbeat 处理心跳。
func (r *AgentRegistry) Heartbeat(agentID string, req HeartbeatRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	node, ok := r.agents[agentID]
	if !ok {
		return ErrAgentNotFound
	}

	node.LastHeartbeatAt = time.Now()
	node.AppVersion = req.AppVersion

	// 当 agent 切到新任务（含"上一任务结束 → 进入空闲"和"空闲 → 接到新任务"两种场景）时，
	// 立刻把 LatestStress 清成 nil。否则会出现：
	//   - 任务 A 结束、agent 心跳把 CurrentTaskID 清成 ""，但 LatestStress 还是 A 的快照；
	//   - 用户秒启动任务 B，agent 心跳把 CurrentTaskID 改成 taskB；
	//   - 此刻 agent 还没产生 B 的第一拍 stress（reporter 还没到 tick），
	//     LatestStress 仍是 A 的；
	//   - 前端 polling /api/metrics → AggregateStress(taskB) 命中
	//     `agent.CurrentTaskID == taskB && agent.LatestStress != nil` → 直接返回 A 的快照；
	//   - 前端节点和动作面板瞬间显示上一次任务的数据，过几秒被新数据覆盖 ⇒ 用户感知"残留"。
	// 在 CurrentTaskID 变化点统一清理 LatestStress，从源头杜绝跨任务串数据。
	// LatestSystem 不动，系统指标和具体任务无关。
	if node.CurrentTaskID != req.CurrentTaskID {
		node.LatestStress = nil
		node.StressUpdatedAt = time.Time{}
	}
	node.CurrentTaskID = req.CurrentTaskID
	node.CurrentBots = req.CurrentBots

	// 心跳恢复：如果之前是 unhealthy/offline，恢复业务状态
	if node.Status == AgentUnhealthy || node.Status == AgentOffline {
		from := node.Status
		if req.CurrentTaskID != "" {
			node.Status = AgentBusy
		} else {
			node.Status = AgentIdle
		}
		if r.onChange != nil {
			r.onChange(agentID, from, node.Status)
		}
		stresslog.Warn("agent 心跳恢复",
			zap.String("agentId", agentID),
			zap.String("status", string(node.Status)))
	}
	return nil
}

// Deregister 注销 Agent。
func (r *AgentRegistry) Deregister(agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.agents[agentID]; !ok {
		return ErrAgentNotFound
	}
	delete(r.agents, agentID)
	stresslog.Info("agent 注销", zap.String("agentId", agentID))
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
func (r *AgentRegistry) UpdateStress(agentID string, snap monitor.CollectorSnapshot, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if node, ok := r.agents[agentID]; ok {
		node.LatestStress = &snap
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
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.scanAndMarkStatus()
			}
		}
	}()
}

func (r *AgentRegistry) scanAndMarkStatus() {
	r.mu.Lock()
	defer r.mu.Unlock()

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
			// 心跳正常，保持业务状态
			continue
		}

		if node.Status == newStatus {
			continue
		}

		from := node.Status
		node.Status = newStatus

		if r.onChange != nil {
			r.onChange(node.ID, from, newStatus)
		}
		stresslog.Warn("agent 状态变更",
			zap.String("agentId", node.ID),
			zap.String("from", string(from)),
			zap.String("to", string(newStatus)),
			zap.Duration("lag", lag))
	}
}
