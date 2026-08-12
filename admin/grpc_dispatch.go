package admin

import (
	"context"
	"fmt"
	"time"

	"stressbot/controlplane/controlv1"
)

func (s *AdminServer) scheduleStartCommands(ctx context.Context, task *Task, assignments []Assignment) ([]string, error) {
	descriptor, err := s.bundles.Build(&task.Config)
	if err != nil {
		return nil, err
	}
	rc := task.Config.RobotConfig
	concurrencyByAgent := splitGlobalValues(rc.Concurrency, task.TotalBots, assignments)
	var shared *SharedRuntimeAssignment
	if task.SharedUsed && s.cfg.RedisEnabled() {
		runID := task.SharedRunID
		if runID == "" {
			runID = task.ID
		}
		shared = &SharedRuntimeAssignment{RunID: runID, Redis: *s.cfg.Redis}
	}
	commands := make([]*controlv1.Command, 0, len(assignments))
	startNumber := assignments[0].StartNumber
	for _, assignment := range assignments {
		domain := TaskAssignment{
			TaskID: task.ID, TaskName: task.Name, StartNumber: startNumber,
			StartIndex: assignmentStartIndex(assignment, startNumber), TotalBots: assignment.TotalBots,
			AccountPrefix: stringOr(rc.AccountPrefix, "bot_", "robotConfig.accountPrefix"), MainService: rc.MainService,
			StateExtra: rc.StateExtra, ConcurrentNum: concurrencyByAgent[assignment.AgentID],
			HeartbeatInterval: secsOr(rc.HeartbeatSec, 5, "robotConfig.heartbeatSec"),
			TCPTimeout:        secsOr(rc.TimeoutSec, 60, "robotConfig.timeoutSec"),
			HTTPTimeout:       secsOr(rc.HTTPTimeoutSec, 10, "robotConfig.httpTimeoutSec"),
			ApdexT:            intOr(rc.ApdexT, 100, "robotConfig.apdexT"), LogLevel: rc.LogLevel,
			RampUp: scaleRampUp(rc.RampUp, task.TotalBots, assignment.TotalBots, assignments, assignment.AgentID), Shared: shared,
		}
		transport, err := taskAssignmentToProto(domain)
		if err != nil {
			return nil, err
		}
		commands = append(commands, &controlv1.Command{AgentId: assignment.AgentID, TaskId: task.ID,
			Body: &controlv1.Command_StartTask{StartTask: &controlv1.StartTask{Assignment: transport,
				Bundle: &controlv1.BundleDescriptor{Sha256: append([]byte(nil), descriptor.Digest[:]...), Size: descriptor.Size}}}})
	}
	if err := s.commandBus.CreateBatch(ctx, commands); err != nil {
		return nil, err
	}
	agents := make([]string, 0, len(commands))
	for _, command := range commands {
		agents = append(agents, command.AgentId)
	}
	return agents, nil
}

func (s *AdminServer) scheduleStopCommands(ctx context.Context, taskID string, agentIDs []string, reason string) error {
	commands := make([]*controlv1.Command, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		commands = append(commands, &controlv1.Command{AgentId: agentID, TaskId: taskID,
			Body: &controlv1.Command_StopTask{StopTask: &controlv1.StopTask{Reason: reason}}})
	}
	return s.commandBus.CreateBatch(ctx, commands)
}

func (s *AdminServer) scheduleShutdownCommands(ctx context.Context, agentIDs []string, reason string) error {
	commands := make([]*controlv1.Command, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		commands = append(commands, &controlv1.Command{AgentId: agentID,
			Body: &controlv1.Command_Shutdown{Shutdown: &controlv1.Shutdown{Reason: reason}}})
	}
	if len(commands) == 0 {
		return fmt.Errorf("没有可关闭的在线节点")
	}
	return s.commandBus.CreateBatch(ctx, commands)
}

func commandContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (s *AdminServer) finishTaskIfFullyReported(taskID string) {
	task, ok := s.tasks.Get(taskID)
	if !ok || task.State != TaskRunning {
		return
	}
	expected := taskExpectedAgents(task)
	if len(expected) == 0 {
		return
	}
	for agentID := range expected {
		if _, reported := task.Reports[agentID]; !reported {
			return
		}
	}
	_ = s.tasks.Update(taskID, func(current *Task) { current.CleanupSummary = aggregateTaskCleanup(current) })
	_, _ = s.tasks.Transition(taskID, TaskRunning, TaskStopped)
}
