package metrics

import (
	"time"

	"stressbot/admin/agent"
	adminhttp "stressbot/admin/apierror"
	admintask "stressbot/admin/task"
	"stressbot/internal/stresslog"

	"go.uber.org/zap"
)

type Registry = agent.Registry
type Node = agent.Node
type SystemSnapshot = agent.SystemSnapshot
type Task = admintask.Task
type TaskConfig = admintask.Config
type RobotConfig = admintask.RobotConfig

const (
	Idle = agent.Idle
	Busy = agent.Busy
)

func newTestAgentRegistry(nodes ...*Node) *Registry {
	if stresslog.GetLogger() == nil {
		stresslog.ReplaceLogger(zap.NewNop())
	}
	registry := agent.NewRegistry(agent.Config{
		UnhealthyThreshold: 30 * time.Second,
		OfflineThreshold:   60 * time.Second,
		NotFoundError:      adminhttp.ErrAgentNotFound,
	}, nil)
	for _, node := range nodes {
		if err := registry.Register(node); err != nil {
			panic(err)
		}
	}
	return registry
}
