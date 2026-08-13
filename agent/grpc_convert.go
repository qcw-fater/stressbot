package agent

import (
	agenttask "stressbot/agent/task"
	"time"

	"stressbot/controlplane"
	"stressbot/controlplane/pb"
	"stressbot/robot"
)

func cleanupStatusToProto(src *robot.CleanupStatus) *controlpb.CleanupStatus {
	if src == nil {
		return nil
	}
	out := &controlpb.CleanupStatus{
		Status:         string(src.Status),
		Reason:         string(src.Reason),
		Message:        src.Message,
		DurationMillis: src.DurationMs,
		TotalRobots:    int32(src.TotalRobots),
		CleanedRobots:  int32(src.CleanedRobots),
		TimeoutRobots:  int32(src.TimeoutRobots),
		LuaReturned:    int32(src.LuaReturned),
		LuaSkipped:     int32(src.LuaSkipped),
		Issues:         make([]*controlpb.CleanupIssue, 0, len(src.Issues)),
	}
	for _, issue := range src.Issues {
		out.Issues = append(out.Issues, &controlpb.CleanupIssue{
			RobotId:      int32(issue.RobotID),
			Account:      issue.Account,
			Phase:        issue.Phase,
			WaitDone:     issue.WaitDone,
			CloseAllDone: issue.CloseAllDone,
			Message:      issue.Message,
		})
	}
	return out
}

func taskResultToProto(src agenttask.TaskResult) controlpb.TaskResultCode {
	switch src {
	case agenttask.TaskCompleted:
		return controlpb.TaskResultCode_TASK_RESULT_CODE_COMPLETED
	case agenttask.TaskStopped:
		return controlpb.TaskResultCode_TASK_RESULT_CODE_STOPPED
	case agenttask.TaskFailed:
		return controlpb.TaskResultCode_TASK_RESULT_CODE_FAILED
	default:
		return controlpb.TaskResultCode_TASK_RESULT_CODE_UNSPECIFIED
	}
}

func finalReportToProto(reportID string, src agenttask.TaskCompletionReport) *controlpb.FinalReport {
	return &controlpb.FinalReport{
		ReportId:           reportID,
		AgentId:            src.AgentID,
		TaskId:             src.TaskID,
		Result:             taskResultToProto(src.Result),
		ErrorMessage:       src.ErrorMsg,
		FinishedAtUnixNano: unixNanoAgent(src.FinishedAt),
		StageIndex:         int32(src.StageIndex),
		Snapshot:           controlplane.ToProtoCollectorSnapshot(src.FinalSnapshot),
		CleanupStatus:      cleanupStatusToProto(src.CleanupStatus),
	}
}

func unixNanoAgent(src time.Time) int64 {
	if src.IsZero() {
		return 0
	}
	return src.UnixNano()
}
