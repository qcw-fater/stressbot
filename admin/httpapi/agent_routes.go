package httpapi

import (
	"net/http"
	"time"

	adminagent "stressbot/admin/agent"
	"stressbot/admin/apierror"
	"stressbot/admin/metrics"
	"stressbot/internal/stresslog"

	"go.uber.org/zap"
)

func (s *Handler) handleListAgents(w http.ResponseWriter, _ *http.Request) {
	agents := s.agents.List()
	now := time.Now()
	items := make([]metrics.AgentListItem, 0, len(agents))
	for _, a := range agents {
		items = append(items, buildAgentListItem(a, now))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

func buildAgentListItem(agent *adminagent.Node, now time.Time) metrics.AgentListItem {
	item := metrics.AgentListItem{
		AgentID:         agent.ID,
		Name:            agent.Name,
		Address:         agent.Address,
		AppVersion:      agent.AppVersion,
		MaxBots:         agent.MaxBots,
		Status:          agent.Status,
		CurrentTaskID:   agent.CurrentTaskID,
		CurrentBots:     agent.CurrentBots,
		StaticInfo:      agent.StaticInfo,
		LastHeartbeatAt: agent.LastHeartbeatAt,
	}
	if !agent.StressUpdatedAt.IsZero() {
		item.StressUpdatedAt = pointer(agent.StressUpdatedAt)
	}
	if agent.LatestSystem == nil || agent.SystemUpdatedAt.IsZero() {
		return item
	}

	item.SystemUpdatedAt = pointer(agent.SystemUpdatedAt)
	age := now.Sub(agent.SystemUpdatedAt)
	if age >= 0 {
		item.SystemSnapshotAgeSeconds = pointer(age.Seconds())
	}
	fresh := (agent.Status == adminagent.Idle || agent.Status == adminagent.Busy) &&
		age >= 0 && age <= systemSnapshotFreshFor(agent.SystemInterval)
	item.SystemStale = !fresh
	if !fresh {
		return item
	}

	snapshot := agent.LatestSystem
	item.HostCPUPercent = validPercent(snapshot.HostCPUPercent)
	if _, _, percent, ok := validHostMemory(snapshot); ok {
		item.HostMemPercent = pointer(percent)
	}
	item.ProcessCPUPercent = validPercent(snapshot.ProcessCPUPercent)
	if snapshot.ProcessRSSBytes != nil {
		item.ProcessRSSBytes = pointer(*snapshot.ProcessRSSBytes)
	}
	item.ProcessGoroutines = pointer(snapshot.ProcessGoroutines)
	return item
}

func pointer[T any](value T) *T { return &value }

func (s *Handler) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.agents.Get(id)
	if !ok {
		writeError(w, apierror.ErrAgentNotFound)
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

func (s *Handler) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.agents.Get(id)
	if !ok {
		writeError(w, apierror.ErrAgentNotFound)
		return
	}
	if agent.Status != adminagent.Offline {
		writeError(w, apierror.ErrAgentBusy.WithMessage("can only delete offline agents"))
		return
	}
	if err := s.agents.Deregister(id); err != nil {
		writeError(w, err)
		return
	}
	stresslog.Info("[ADMIN] 节点已删除", zap.String("agentID", id), zap.String("agentName", agent.Name))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Handler) handleShutdownAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.agents.Get(id)
	if !ok {
		writeError(w, apierror.ErrAgentNotFound)
		return
	}
	if agent.Status == adminagent.Offline {
		writeError(w, apierror.ErrAgentOffline.WithMessage("agent is offline, cannot send shutdown"))
		return
	}
	ctx, cancel := commandContext()
	err := s.scheduleShutdownCommands(ctx, []string{id}, "管理员关闭节点")
	cancel()
	if err != nil {
		stresslog.Warn("关闭命令发送失败", zap.String("agentID", id), zap.Error(err))
		writeError(w, apierror.ErrInternal.WithMessage("创建关闭命令失败: "+err.Error()))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "shutdown_sent"})
}

func (s *Handler) handleShutdownAllAgents(w http.ResponseWriter, _ *http.Request) {
	all := s.agents.List()
	var succeeded, failed []string
	succeeded = make([]string, 0)
	failed = make([]string, 0)
	for _, a := range all {
		if a.Status == adminagent.Offline {
			continue
		}
		succeeded = append(succeeded, a.ID)
	}
	ctx, cancel := commandContext()
	err := s.scheduleShutdownCommands(ctx, succeeded, "管理员批量关闭节点")
	cancel()
	if err != nil {
		failed = append(failed, succeeded...)
		succeeded = succeeded[:0]
		stresslog.Warn("批量关闭命令创建失败", zap.Error(err))
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "shutdown_sent",
		"succeeded": succeeded,
		"failed":    failed,
	})
}

// ── 前端-指标 ──
