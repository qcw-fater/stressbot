package admin

import (
	"errors"
	"fmt"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

type permanentTaskReportError struct{ err error }

func (e *permanentTaskReportError) Error() string { return e.err.Error() }
func (e *permanentTaskReportError) Unwrap() error { return e.err }

func isPermanentTaskReportError(err error) bool {
	var target *permanentTaskReportError
	return errors.As(err, &target) || errors.Is(err, ErrTaskNotFound)
}

func (s *AdminServer) acceptTaskReport(report TaskCompletionReport) error {
	s.agents.Touch(report.AgentID, "")
	isFinal := report.StageIndex <= 0
	var transition TaskState
	memberValid := false
	err := s.tasks.Update(report.TaskID, func(task *Task) {
		expected := taskExpectedAgents(task)
		if _, ok := expected[report.AgentID]; !ok {
			return
		}
		memberValid = true
		if !isFinal {
			for i := range task.StageReports {
				if task.StageReports[i].AgentID == report.AgentID && task.StageReports[i].StageIndex == report.StageIndex {
					task.StageReports[i] = report
					return
				}
			}
			task.StageReports = append(task.StageReports, report)
			return
		}
		if task.Reports == nil {
			task.Reports = make(map[string]TaskCompletionReport)
		}
		task.Reports[report.AgentID] = report
		completed := 0
		for id := range task.Reports {
			if _, ok := expected[id]; ok {
				completed++
			}
		}
		if len(expected) > 0 && completed >= len(expected) {
			task.CleanupSummary = aggregateTaskCleanup(task)
			if task.State == TaskRunning || task.State == TaskStopping {
				transition = task.State
			}
		}
	})
	if err != nil {
		return err
	}
	if !memberValid {
		return &permanentTaskReportError{err: fmt.Errorf("节点不属于任务 %s", report.TaskID)}
	}
	if isFinal {
		if _, err := s.agents.CompleteTask(report.AgentID, report.TaskID); err != nil {
			stresslog.Warn("[ADMIN] 清理已完成任务的节点状态失败", zap.String("taskID", report.TaskID), zap.String("agentID", report.AgentID), zap.Error(err))
		}
	}
	if transition == TaskRunning {
		_, err = s.tasks.Transition(report.TaskID, TaskRunning, TaskStopped)
	} else if transition == TaskStopping {
		_, err = s.tasks.Transition(report.TaskID, TaskStopping, TaskStopped)
	}
	return err
}

func (s *AdminServer) acceptStressReport(report StressReport) error {
	return s.acceptStressReportWithDrops(report, 0)
}

func (s *AdminServer) acceptStressReportWithDrops(report StressReport, droppedIntervals uint64) error {
	s.agents.Touch(report.AgentID, "")
	agent, ok := s.agents.Get(report.AgentID)
	if !ok {
		return ErrAgentNotFound
	}
	if agent.CurrentTaskID == "" || agent.CurrentTaskID != report.TaskID {
		return nil
	}
	expectedEvery, err := time.ParseDuration(agent.StressInterval)
	if err != nil || expectedEvery <= 0 {
		return fmt.Errorf("节点指标上报周期无效")
	}
	active := s.tasks.ActiveTask()
	if active == nil || active.ID != report.TaskID {
		return nil
	}
	apdexT := active.Config.RobotConfig.ApdexT
	if apdexT <= 0 {
		apdexT = 100
	}
	if _, err := s.metricsWindows.AcceptWithDrops(report, report.TaskID, expectedEvery, time.Duration(apdexT)*time.Millisecond, droppedIntervals); err != nil {
		return err
	}
	state, _ := s.metricsWindows.AgentState(report.TaskID, report.AgentID)
	s.agents.UpdateStress(report.AgentID, report.Snapshot, state.ReceivedAt)
	return nil
}
