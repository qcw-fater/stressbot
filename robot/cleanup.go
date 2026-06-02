package robot

import "time"

// CleanupState 表示 Robot/Manager 资源清理结果。
type CleanupState string

const (
	CleanupOK      CleanupState = "ok"
	CleanupPartial CleanupState = "partial"
	CleanupTimeout CleanupState = "timeout"
	CleanupUnknown CleanupState = "unknown"
)

// CleanupReason 表示触发清理的原因。
type CleanupReason string

const (
	CleanupReasonNatural         CleanupReason = "natural"
	CleanupReasonAdminStop       CleanupReason = "admin_stop"
	CleanupReasonAgentShutdown   CleanupReason = "agent_shutdown"
	CleanupReasonRampReset       CleanupReason = "ramp_reset"
	CleanupReasonDurationStop    CleanupReason = "duration_stop"
	CleanupReasonOfflineSynthetic CleanupReason = "offline_synthetic"
	CleanupReasonStopWaitTimeout CleanupReason = "stop_wait_timeout"
)

// CleanupIssue 单个 Robot 清理异常样本。
type CleanupIssue struct {
	RobotID      int    `json:"robotId,omitempty"`
	Account      string `json:"account,omitempty"`
	Phase        string `json:"phase,omitempty"`
	WaitDone     bool   `json:"waitDone,omitempty"`
	CloseAllDone bool   `json:"closeAllDone,omitempty"`
	Message      string `json:"message,omitempty"`
}

// CleanupStatus 表示一次清理汇总，会上报给 Admin/前端。
type CleanupStatus struct {
	Status        CleanupState   `json:"status"`
	Reason        CleanupReason  `json:"reason,omitempty"`
	Message       string         `json:"message,omitempty"`
	DurationMs    int64          `json:"durationMs,omitempty"`
	TotalRobots   int            `json:"totalRobots,omitempty"`
	CleanedRobots int            `json:"cleanedRobots,omitempty"`
	TimeoutRobots int            `json:"timeoutRobots,omitempty"`
	LuaReturned   int            `json:"luaReturned,omitempty"`
	LuaSkipped    int            `json:"luaSkipped,omitempty"`
	Issues        []CleanupIssue `json:"issues,omitempty"`
}

func cleanupOK(reason CleanupReason, duration time.Duration) CleanupStatus {
	return CleanupStatus{
		Status:        CleanupOK,
		Reason:        reason,
		Message:       "资源清理完成，Lua 运行时已归还",
		DurationMs:    duration.Milliseconds(),
		TotalRobots:   1,
		CleanedRobots: 1,
		LuaReturned:   1,
	}
}

func cleanupTimeout(reason CleanupReason, duration time.Duration, issue CleanupIssue) CleanupStatus {
	return CleanupStatus{
		Status:        CleanupTimeout,
		Reason:        reason,
		Message:       "清理超时，Lua 运行时已隔离未归还，避免复用可能仍在使用的运行时",
		DurationMs:    duration.Milliseconds(),
		TotalRobots:   1,
		TimeoutRobots: 1,
		LuaSkipped:    1,
		Issues:        []CleanupIssue{issue},
	}
}

func emptyCleanupSummary(reason CleanupReason) CleanupStatus {
	return CleanupStatus{
		Status:  CleanupOK,
		Reason:  reason,
		Message: "没有需要清理的机器人",
	}
}

// MergeCleanupStatus 聚合多个清理结果，保留有限异常样本。
func MergeCleanupStatus(reason CleanupReason, statuses ...CleanupStatus) CleanupStatus {
	out := CleanupStatus{Status: CleanupOK, Reason: reason, Message: "资源清理完成，Lua 运行时已全部归还"}
	for _, s := range statuses {
		out.TotalRobots += s.TotalRobots
		out.CleanedRobots += s.CleanedRobots
		out.TimeoutRobots += s.TimeoutRobots
		out.LuaReturned += s.LuaReturned
		out.LuaSkipped += s.LuaSkipped
		// 各机器人是并发关闭的，整体耗时取最大值而非累加，避免高估。
		if s.DurationMs > out.DurationMs {
			out.DurationMs = s.DurationMs
		}
		if len(out.Issues) < 20 {
			remain := 20 - len(out.Issues)
			if len(s.Issues) < remain {
				remain = len(s.Issues)
			}
			out.Issues = append(out.Issues, s.Issues[:remain]...)
		}
		switch s.Status {
		case CleanupTimeout:
			out.Status = CleanupTimeout
		case CleanupPartial:
			if out.Status == CleanupOK {
				out.Status = CleanupPartial
			}
		case CleanupUnknown:
			if out.Status == CleanupOK {
				out.Status = CleanupUnknown
			}
		}
	}
	if out.TotalRobots == 0 {
		return emptyCleanupSummary(reason)
	}
	switch out.Status {
	case CleanupTimeout:
		out.Message = "部分机器人清理超时，Lua 运行时已隔离未归还"
	case CleanupPartial:
		out.Message = "部分机器人清理异常"
	case CleanupUnknown:
		out.Message = "部分节点未上报清理结果，清理状态未知"
	default:
		out.Message = "资源清理完成，Lua 运行时已全部归还"
	}
	return out
}

// UnknownCleanupStatus 构造 Admin 合成报告使用的未知清理状态。
func UnknownCleanupStatus(reason CleanupReason, message string) CleanupStatus {
	return CleanupStatus{Status: CleanupUnknown, Reason: reason, Message: message}
}
