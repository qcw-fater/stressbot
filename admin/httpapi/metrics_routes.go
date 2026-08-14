package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	adminagent "stressbot/admin/agent"
	"stressbot/admin/apierror"
	"stressbot/admin/metrics"
	"stressbot/internal/stresslog"
	"stressbot/monitor"

	"go.uber.org/zap"
)

func (s *Handler) handleGetMetrics(w http.ResponseWriter, _ *http.Request) {
	active := s.tasks.ActiveTask()
	if active == nil {
		writeJSON(w, http.StatusOK, &metrics.StressAggregate{
			Snapshot: &monitor.CollectorSnapshot{TimingDetail: monitor.TimingRTTOnly, Actions: []monitor.ActionSnapshot{}},
			AsOf:     time.Now(),
		})
		return
	}
	agg, err := s.aggregator.AggregateStress(active.ID)
	if err != nil {
		stresslog.Error("聚合压测指标失败", zap.String("taskID", active.ID), zap.Error(err))
		writeError(w, apierror.ErrInternal.WithMessage("压测指标聚合失败"))
		return
	}
	public := *agg
	public.Snapshot = agg.Snapshot.PublicCopy()
	writeJSON(w, http.StatusOK, &public)
}

// handleGetMetricsSummary 文本摘要。
func (s *Handler) handleGetMetricsSummary(w http.ResponseWriter, _ *http.Request) {
	active := s.tasks.ActiveTask()
	if active == nil {
		writeJSON(w, http.StatusOK, map[string]string{"summary": "no active task"})
		return
	}

	agg, err := s.aggregator.AggregateStress(active.ID)
	if err != nil {
		stresslog.Error("聚合压测指标摘要失败", zap.String("taskID", active.ID), zap.Error(err))
		writeError(w, apierror.ErrInternal.WithMessage("压测指标聚合失败"))
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "admintask.Task: %s (%s)\n", active.Name, active.ID)
	fmt.Fprintf(&b, "Total Actions: %d\n", agg.Snapshot.TotalActions)
	if len(agg.Snapshot.Actions) > 0 {
		for _, a := range agg.Snapshot.Actions {
			p50, p99 := "—", "—"
			if a.RTT.P50Ms != nil {
				p50 = fmt.Sprintf("%.1fms", *a.RTT.P50Ms)
			}
			if a.RTT.P99Ms != nil {
				p99 = fmt.Sprintf("%.1fms", *a.RTT.P99Ms)
			}
			fmt.Fprintf(&b, "  %s: count=%d success=%.1f%% p50=%s p99=%s\n",
				a.Name, a.SampleCount, a.SuccessRate*100, p50, p99)
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func (s *Handler) handleGetAgentMetrics(w http.ResponseWriter, _ *http.Request) {
	agents := s.agents.List()
	items := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		if a.LatestStress != nil {
			items = append(items, map[string]any{
				"agentId":   a.ID,
				"agentName": a.Name,
				"snapshot":  a.LatestStress.PublicCopy(),
				"updatedAt": a.StressUpdatedAt,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

func (s *Handler) handleGetSingleAgentMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.agents.Get(id)
	if !ok {
		writeError(w, apierror.ErrAgentNotFound)
		return
	}
	if agent.LatestStress == nil {
		writeJSON(w, http.StatusOK, map[string]string{"message": "no stress data"})
		return
	}
	writeJSON(w, http.StatusOK, agent.LatestStress.PublicCopy())
}

func (s *Handler) handleGetSystem(w http.ResponseWriter, _ *http.Request) {
	active := s.tasks.ActiveTask()
	snap := s.aggregator.AggregateSystem(taskSystemAgentIDs(active))
	writeJSON(w, http.StatusOK, snap)
}

func (s *Handler) handleGetSystemAgents(w http.ResponseWriter, _ *http.Request) {
	agents := s.agents.List()
	now := time.Now()
	items := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		if a.Status == adminagent.Offline {
			continue
		}
		item := map[string]any{
			"agentId":   a.ID,
			"agentName": a.Name,
			"status":    a.Status,
		}
		if a.LatestSystem != nil {
			item["snapshot"] = a.LatestSystem
			item["updatedAt"] = a.SystemUpdatedAt
			age := now.Sub(a.SystemUpdatedAt)
			item["isStale"] = age < 0 || age > systemSnapshotFreshFor(a.SystemInterval)
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
	})
}

func (s *Handler) handleGetSystemAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agent, ok := s.agents.Get(id)
	if !ok {
		writeError(w, apierror.ErrAgentNotFound)
		return
	}
	if agent.LatestSystem == nil {
		writeJSON(w, http.StatusOK, map[string]string{"message": "no system data"})
		return
	}
	writeJSON(w, http.StatusOK, agent.LatestSystem)
}

// ── 工具函数 ──

func parseIntOrDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func parseBoolOrDefault(s string, def bool) bool {
	if s == "" {
		return def
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return b
}

func parseTimeOrDefault(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return def
	}
	return t
}

func parseTagsFromQuery(r *http.Request, key string) []string {
	var tags []string
	for _, t := range r.URL.Query()[key] {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}
