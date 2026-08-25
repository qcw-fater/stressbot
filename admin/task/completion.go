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

// CompletionService 汇总各 Agent 的任务完成汇报：接收阶段/最终报告，
// 全部预期 Agent 汇报完毕后聚合清理摘要并把任务推进到 Stopped。
type CompletionService struct {
	tasks  *Store
	agents *agent.Registry
}

// NewCompletionService 创建完成汇报服务，tasks 提供任务存储，agents 提供节点注册表。
func NewCompletionService(tasks *Store, agents *agent.Registry) *CompletionService {
	return &CompletionService{tasks: tasks, agents: agents}
}

// PermanentReportError 标记不可重试的汇报错误（如节点不属于该任务）：调用方应终态 NACK
// 丢弃，不能按瞬时故障重放，否则 Agent 会无限重发同一份报告。
type PermanentReportError struct{ Err error }

func (e *PermanentReportError) Error() string { return e.Err.Error() }
func (e *PermanentReportError) Unwrap() error { return e.Err }

// IsPermanentReportError 判断汇报错误是否不可重试：PermanentReportError 或任务已不存在。
func IsPermanentReportError(err error) bool {
	_, ok := errors.AsType[*PermanentReportError](err)
	return ok || errors.Is(err, apierror.ErrTaskNotFound)
}

// OwnsActiveTask 判断 agentID 是否仍是 taskID 活跃任务的待汇报成员：
// 任务须为当前活跃任务、该 Agent 在预期名单中且尚未提交最终报告（用于会话接管与心跳路由判定）。
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

// Accept 接收一份 Agent 汇报：StageIndex>0 视为阶段报告（同 Agent 同阶段幂等覆盖），
// 否则为最终报告；全员汇报完毕时聚合清理摘要并按当前状态流转到 Stopped。
// 汇报者不在预期名单时返回 PermanentReportError。
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

// FinishIfFullyReported 补偿收口：若任务处于 Running 且所有预期 Agent 均已提交最终报告，
// 则补写聚合清理摘要并流转到 Stopped；任一条件不满足时静默返回。
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

// AggregateCleanup 汇总全部 Agent 的最终清理状态：未上报者按停止等待超时补 unknown，
// 再经 MergeCleanupStatus 以 AdminStop 为主因合并成单份任务级清理摘要；无报告时返回 nil。
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
