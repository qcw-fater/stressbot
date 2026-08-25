// Package metrics 实现 Admin 侧节点指标接入：受理 Agent 上报的压测/系统指标，
// 完成窗口校验与留存、时序采样归档、跨节点聚合与最新状态查询。
package metrics

import (
	"errors"
	"time"

	"stressbot/admin/agent"
	"stressbot/admin/apierror"
	admintask "stressbot/admin/task"
)

// AcceptanceService 压测指标上报的接入校验服务：确认节点与任务归属后，
// 将窗口交给 WindowStore 校验提交，并把最新累计状态回写到节点注册表。
type AcceptanceService struct {
	agents  *agent.Registry
	tasks   *admintask.Store
	windows *WindowStore
}

// NewAcceptanceService 创建压测指标接入服务。
func NewAcceptanceService(agents *agent.Registry, tasks *admintask.Store, windows *WindowStore) *AcceptanceService {
	return &AcceptanceService{agents: agents, tasks: tasks, windows: windows}
}

// Accept 处理一帧压测指标上报：刷新节点心跳并校验节点存在、任务归属与上报周期，
// 通过后按任务配置的 ApdexT 提交窗口并更新节点最新指标；
// 任务已切换或不属于当前活跃任务时静默丢弃（返回 nil），校验失败才返回错误。
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
		return errors.New("节点指标上报周期无效")
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
