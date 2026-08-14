package httpapi

import (
	"context"
	"net/http"
	"time"

	"stressbot/admin/agent"
	"stressbot/admin/history"
	"stressbot/admin/metrics"
	admintask "stressbot/admin/task"
	"stressbot/admin/template"
	"stressbot/state/shared"
)

type Dependencies struct {
	StaticDir       string
	Redis           *shared.RedisConfig
	Tasks           *admintask.Store
	Agents          *agent.Registry
	Aggregator      *metrics.Aggregator
	Assigner        *admintask.Assigner
	History         *history.Store
	Sampler         *metrics.Sampler
	Flows           *template.FlowTemplateStore
	ActionTemplates *template.ActionTemplateStore
	ListenTemplates *template.ListenTemplateStore
	NextID          func() string

	ScheduleStart             func(context.Context, *admintask.Task, []admintask.Assignment) ([]string, error)
	ScheduleStop              func(context.Context, string, []string, string) error
	ScheduleShutdown          func(context.Context, []string, string) error
	FinishTaskIfFullyReported func(string)
	SynthesizeOfflineReports  func(string) bool
	StartStopTimeout          func(string)
}

type Handler struct {
	staticDir       string
	redis           *shared.RedisConfig
	tasks           *admintask.Store
	agents          *agent.Registry
	aggregator      *metrics.Aggregator
	assigner        *admintask.Assigner
	history         *history.Store
	sampler         *metrics.Sampler
	flows           *template.FlowTemplateStore
	actionTemplates *template.ActionTemplateStore
	listenTemplates *template.ListenTemplateStore
	nextID          func() string

	scheduleStart             func(context.Context, *admintask.Task, []admintask.Assignment) ([]string, error)
	scheduleStop              func(context.Context, string, []string, string) error
	scheduleShutdown          func(context.Context, []string, string) error
	finishTaskIfFullyReported func(string)
	synthesizeOfflineReports  func(string) bool
	startStopTimeout          func(string)
}

func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		staticDir: deps.StaticDir, redis: deps.Redis, tasks: deps.Tasks, agents: deps.Agents,
		aggregator: deps.Aggregator, assigner: deps.Assigner, history: deps.History,
		sampler: deps.Sampler, flows: deps.Flows, actionTemplates: deps.ActionTemplates,
		listenTemplates: deps.ListenTemplates, nextID: deps.NextID,
		scheduleStart: deps.ScheduleStart, scheduleStop: deps.ScheduleStop,
		scheduleShutdown:          deps.ScheduleShutdown,
		finishTaskIfFullyReported: deps.FinishTaskIfFullyReported,
		synthesizeOfflineReports:  deps.SynthesizeOfflineReports,
		startStopTimeout:          deps.StartStopTimeout,
	}
}

func (s *Handler) Routes() http.Handler { return s.registerManagementRoutes() }

func (s *Handler) redisEnabled() bool { return s.redis != nil && s.redis.Enabled() }

func commandContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func taskUsesShare(task *admintask.Task) bool {
	if task == nil {
		return false
	}
	for _, content := range task.Config.LuaScripts {
		if shared.UsesShare(string(content)) {
			return true
		}
	}
	return false
}

func (s *Handler) scheduleStartCommands(ctx context.Context, task *admintask.Task, assignments []admintask.Assignment) ([]string, error) {
	return s.scheduleStart(ctx, task, assignments)
}

func (s *Handler) scheduleStopCommands(ctx context.Context, taskID string, agentIDs []string, reason string) error {
	return s.scheduleStop(ctx, taskID, agentIDs, reason)
}

func (s *Handler) scheduleShutdownCommands(ctx context.Context, agentIDs []string, reason string) error {
	return s.scheduleShutdown(ctx, agentIDs, reason)
}

func writeJSON(w http.ResponseWriter, status int, value any) { WriteJSON(w, status, value) }
func writeError(w http.ResponseWriter, err error)            { WriteError(w, err) }

const defaultHistoryTimeseriesMaxPoints = history.DefaultTimeseriesMaxPoints

func taskSystemAgentIDs(task *admintask.Task) []string {
	if task == nil {
		return nil
	}
	ids := make([]string, 0, len(task.Assignments))
	for _, assignment := range task.Assignments {
		ids = append(ids, assignment.AgentID)
	}
	return ids
}

func systemSnapshotFreshFor(interval string) time.Duration {
	return metrics.SystemSnapshotFreshFor(interval)
}

func validPercent(value *float64) *float64 { return metrics.ValidPercent(value) }

func validHostMemory(snapshot *agent.SystemSnapshot) (uint64, uint64, float64, bool) {
	return metrics.ValidHostMemory(snapshot)
}
