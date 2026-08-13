package command

import (
	"context"
	"fmt"
	"strconv"

	"stressbot/admin/bundle"
	"stressbot/admin/grpcapi"
	admintask "stressbot/admin/task"
	"stressbot/controlplane/pb"
	"stressbot/internal/stresslog"
	"stressbot/state/shared"

	"go.uber.org/zap"
)

type Dispatcher struct {
	bundles *bundle.Store
	bus     *Bus
	redis   *shared.RedisConfig
}

func NewDispatcher(bundles *bundle.Store, bus *Bus, redis *shared.RedisConfig) *Dispatcher {
	return &Dispatcher{bundles: bundles, bus: bus, redis: redis}
}

func (d *Dispatcher) ScheduleStart(ctx context.Context, task *admintask.Task, assignments []admintask.Assignment) ([]string, error) {
	descriptor, err := d.bundles.Build(&task.Config)
	if err != nil {
		return nil, err
	}
	robotConfig := task.Config.RobotConfig
	concurrencyByAgent := admintask.SplitGlobalValues(robotConfig.Concurrency, task.TotalBots, assignments)
	var sharedRuntime *admintask.SharedRuntimeAssignment
	if task.SharedUsed && d.redis != nil && d.redis.Enabled() {
		runID := task.SharedRunID
		if runID == "" {
			runID = task.ID
		}
		sharedRuntime = &admintask.SharedRuntimeAssignment{RunID: runID, Redis: *d.redis}
	}

	commands := make([]*controlpb.Command, 0, len(assignments))
	startNumber := assignments[0].StartNumber
	for _, assignment := range assignments {
		domain := admintask.TaskAssignment{
			TaskID: task.ID, TaskName: task.Name, StartNumber: startNumber,
			StartIndex: admintask.AssignmentStartIndex(assignment, startNumber), TotalBots: assignment.TotalBots,
			AccountPrefix: stringOr(robotConfig.AccountPrefix, "bot_", "robotConfig.accountPrefix"),
			MainService:   robotConfig.MainService, StateExtra: robotConfig.StateExtra,
			ConcurrentNum:     concurrencyByAgent[assignment.AgentID],
			HeartbeatInterval: secondsOr(robotConfig.HeartbeatSec, 5, "robotConfig.heartbeatSec"),
			TCPTimeout:        secondsOr(robotConfig.TimeoutSec, 60, "robotConfig.timeoutSec"),
			HTTPTimeout:       secondsOr(robotConfig.HTTPTimeoutSec, 10, "robotConfig.httpTimeoutSec"),
			ApdexT:            intOr(robotConfig.ApdexT, 100, "robotConfig.apdexT"), LogLevel: robotConfig.LogLevel,
			RampUp: admintask.ScaleRampUp(robotConfig.RampUp, task.TotalBots, assignment.TotalBots, assignments, assignment.AgentID),
			Shared: sharedRuntime,
		}
		transport, err := grpcapi.TaskAssignmentToProto(domain)
		if err != nil {
			return nil, err
		}
		commands = append(commands, &controlpb.Command{
			AgentId: assignment.AgentID, TaskId: task.ID,
			Body: &controlpb.Command_StartTask{StartTask: &controlpb.StartTask{
				Assignment: transport,
				Bundle:     &controlpb.BundleDescriptor{Sha256: append([]byte(nil), descriptor.Digest[:]...), Size: descriptor.Size},
			}},
		})
	}
	if err := d.bus.CreateBatch(ctx, commands); err != nil {
		return nil, err
	}
	agentIDs := make([]string, 0, len(commands))
	for _, command := range commands {
		agentIDs = append(agentIDs, command.AgentId)
	}
	return agentIDs, nil
}

func (d *Dispatcher) ScheduleStop(ctx context.Context, taskID string, agentIDs []string, reason string) error {
	commands := make([]*controlpb.Command, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		commands = append(commands, &controlpb.Command{AgentId: agentID, TaskId: taskID,
			Body: &controlpb.Command_StopTask{StopTask: &controlpb.StopTask{Reason: reason}}})
	}
	return d.bus.CreateBatch(ctx, commands)
}

func (d *Dispatcher) ScheduleShutdown(ctx context.Context, agentIDs []string, reason string) error {
	commands := make([]*controlpb.Command, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		commands = append(commands, &controlpb.Command{AgentId: agentID,
			Body: &controlpb.Command_Shutdown{Shutdown: &controlpb.Shutdown{Reason: reason}}})
	}
	if len(commands) == 0 {
		return fmt.Errorf("没有可关闭的在线节点")
	}
	return d.bus.CreateBatch(ctx, commands)
}

func stringOr(value, fallback, label string) string {
	if value != "" {
		return value
	}
	stresslog.Warn("[CONFIG] 配置未填写，使用默认值", zap.String("key", label), zap.String("default", fallback))
	return fallback
}

func intOr(value, fallback int, label string) int {
	if value > 0 {
		return value
	}
	stresslog.Warn("[CONFIG] 配置未填写，使用默认值", zap.String("key", label), zap.Int("default", fallback))
	return fallback
}

func secondsOr(value, fallback int, label string) string {
	return strconv.Itoa(intOr(value, fallback, label)) + "s"
}
