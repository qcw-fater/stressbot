package task

import (
	"errors"
	"fmt"

	"stressbot/admin/agent"
	"stressbot/admin/apierror"
	"stressbot/internal/stresslog"
	"stressbot/robot"

	"go.uber.org/zap"
)

type CompletionService struct {
	tasks  *Store
	agents *agent.Registry
}

func NewCompletionService(tasks *Store, agents *agent.Registry) *CompletionService {
	return &CompletionService{tasks: tasks, agents: agents}
}

type PermanentReportError struct{ Err error }

func (e *PermanentReportError) Error() string { return e.Err.Error() }
func (e *PermanentReportError) Unwrap() error { return e.Err }

func IsPermanentReportError(err error) bool {
	_, ok := errors.AsType[*PermanentReportError](err)
	return ok || errors.Is(err, apierror.ErrTaskNotFound)
}

func (s *CompletionService) OwnsActiveTask(agentID, taskID string) bool {
	if taskID == "" {
		return false
	}
	task := s.tasks.ActiveTask()
	if task == nil || task.ID != taskID {
		return false
	}
	if _, finished := task.Reports[agentID]; finished {
		return false
	}
	_, ok := ExpectedAgents(task)[agentID]
	return ok
}

func (s *CompletionService) Accept(report CompletionReport) error {
	s.agents.Touch(report.AgentID, "")
	isFinal := report.StageIndex <= 0
	var transition State
	memberValid := false
	err := s.tasks.Update(report.TaskID, func(task *Task) {
		expected := ExpectedAgents(task)
		if _, ok := expected[report.AgentID]; !ok {
			return
		}
		memberValid = true
		if !isFinal {
			for index := range task.StageReports {
				if task.StageReports[index].AgentID == report.AgentID && task.StageReports[index].StageIndex == report.StageIndex {
					task.StageReports[index] = report
					return
				}
			}
			task.StageReports = append(task.StageReports, report)
			return
		}
		if task.Reports == nil {
			task.Reports = make(map[string]CompletionReport)
		}
		task.Reports[report.AgentID] = report
		completed := 0
		for agentID := range task.Reports {
			if _, ok := expected[agentID]; ok {
				completed++
			}
		}
		if len(expected) > 0 && completed >= len(expected) {
			task.CleanupSummary = AggregateCleanup(task)
			if task.State == Running || task.State == Stopping {
				transition = task.State
			}
		}
	})
	if err != nil {
		return err
	}
	if !memberValid {
		return &PermanentReportError{Err: fmt.Errorf("节点不属于任务 %s", report.TaskID)}
	}
	if isFinal {
		if _, err := s.agents.CompleteTask(report.AgentID, report.TaskID); err != nil {
			stresslog.Warn("[ADMIN] 清理已完成任务的节点状态失败",
				zap.String("taskID", report.TaskID), zap.String("agentID", report.AgentID), zap.Error(err))
		}
	}
	switch transition {
	case Running:
		_, err = s.tasks.Transition(report.TaskID, Running, Stopped)
	case Stopping:
		_, err = s.tasks.Transition(report.TaskID, Stopping, Stopped)
	}
	return err
}

func (s *CompletionService) FinishIfFullyReported(taskID string) {
	task, ok := s.tasks.Get(taskID)
	if !ok || task.State != Running {
		return
	}
	expected := ExpectedAgents(task)
	if len(expected) == 0 {
		return
	}
	for agentID := range expected {
		if _, reported := task.Reports[agentID]; !reported {
			return
		}
	}
	_ = s.tasks.Update(taskID, func(current *Task) { current.CleanupSummary = AggregateCleanup(current) })
	_, _ = s.tasks.Transition(taskID, Running, Stopped)
}

func AggregateCleanup(task *Task) *robot.CleanupStatus {
	if task == nil || len(task.Reports) == 0 {
		return nil
	}
	statuses := make([]robot.CleanupStatus, 0, len(task.Reports))
	for _, report := range task.Reports {
		if report.CleanupStatus != nil {
			statuses = append(statuses, *report.CleanupStatus)
		} else {
			statuses = append(statuses, robot.UnknownCleanupStatus(robot.CleanupReasonStopWaitTimeout, "节点未上报清理结果"))
		}
	}
	merged := robot.MergeCleanupStatus(robot.CleanupReasonAdminStop, statuses...)
	return &merged
}
