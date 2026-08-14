package metrics

import (
	"fmt"
	"time"

	"stressbot/admin/agent"
	"stressbot/admin/apierror"
	admintask "stressbot/admin/task"
)

type AcceptanceService struct {
	agents  *agent.Registry
	tasks   *admintask.Store
	windows *WindowStore
}

func NewAcceptanceService(agents *agent.Registry, tasks *admintask.Store, windows *WindowStore) *AcceptanceService {
	return &AcceptanceService{agents: agents, tasks: tasks, windows: windows}
}

func (s *AcceptanceService) Accept(report StressReport, droppedIntervals uint64) error {
	s.agents.Touch(report.AgentID, "")
	agent, ok := s.agents.Get(report.AgentID)
	if !ok {
		return apierror.ErrAgentNotFound
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
	if _, err := s.windows.AcceptWithDrops(report, report.TaskID, expectedEvery, time.Duration(apdexT)*time.Millisecond, droppedIntervals); err != nil {
		return err
	}
	state, _ := s.windows.AgentState(report.TaskID, report.AgentID)
	s.agents.UpdateStress(report.AgentID, report.Snapshot, state.ReceivedAt)
	return nil
}
