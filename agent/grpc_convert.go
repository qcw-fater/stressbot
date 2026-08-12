package agent

import (
	"fmt"
	"time"

	"stressbot/controlplane"
	"stressbot/controlplane/controlv1"
	"stressbot/robot"
	"stressbot/sharedstate"
)

func taskAssignmentFromProto(src *controlv1.TaskAssignment, bundle *controlv1.BundleDescriptor) (*TaskAssignment, error) {
	if src == nil {
		return nil, fmt.Errorf("任务分配不能为空")
	}
	if src.TaskId == "" {
		return nil, fmt.Errorf("任务分配缺少 taskId")
	}
	if src.StartIndex < 0 || src.TotalBots <= 0 || src.ConcurrentNum < 0 {
		return nil, fmt.Errorf("任务分配包含无效数量")
	}
	if bundle == nil || len(bundle.Sha256) != 32 || bundle.Size <= 0 {
		return nil, fmt.Errorf("任务资源包描述无效")
	}
	startIndex := int(src.StartIndex)
	out := &TaskAssignment{
		TaskID:            src.TaskId,
		TaskName:          src.TaskName,
		StartNumber:       int(src.StartNumber),
		StartIndex:        &startIndex,
		TotalBots:         int(src.TotalBots),
		AccountPrefix:     src.AccountPrefix,
		ConcurrentNum:     int(src.ConcurrentNum),
		MainService:       src.MainService,
		StateExtra:        cloneStringMap(src.StateExtra),
		HeartbeatInterval: time.Duration(src.HeartbeatIntervalNanos).String(),
		TCPTimeout:        time.Duration(src.TcpTimeoutNanos).String(),
		HTTPTimeout:       time.Duration(src.HttpTimeoutNanos).String(),
		ApdexT:            int(src.ApdexTMillis),
		LogLevel:          src.LogLevel,
		BundleDigest:      append([]byte(nil), bundle.Sha256...),
		BundleSize:        bundle.Size,
		RampUp:            rampUpFromProto(src.RampUp),
		Shared:            sharedAssignmentFromProto(src.Shared),
	}
	return out, out.Validate()
}

func rampUpFromProto(src *controlv1.RampUpConfig) *RampUpConfig {
	if src == nil {
		return nil
	}
	out := &RampUpConfig{Stages: make([]RampUpStage, 0, len(src.Stages))}
	for _, stage := range src.Stages {
		if stage == nil {
			continue
		}
		out.Stages = append(out.Stages, RampUpStage{
			Count:       int(stage.Count),
			Concurrency: int(stage.Concurrency),
			HoldSec:     int(stage.HoldSeconds),
			Reset:       stage.Reset_,
		})
	}
	return out
}

func sharedAssignmentFromProto(src *controlv1.SharedRuntimeAssignment) *SharedRuntimeAssignment {
	if src == nil || src.Redis == nil {
		return nil
	}
	return &SharedRuntimeAssignment{
		RunID: src.RunId,
		Redis: sharedstate.RedisConfig{
			Host:            src.Redis.Host,
			Port:            int(src.Redis.Port),
			Username:        src.Redis.Username,
			Password:        src.Redis.Password,
			KeyPrefix:       src.Redis.KeyPrefix,
			DefaultClaimTTL: src.Redis.DefaultClaimTtl,
			OpTimeout:       src.Redis.OperationTimeout,
			DialTimeout:     src.Redis.DialTimeout,
			ReadTimeout:     src.Redis.ReadTimeout,
			WriteTimeout:    src.Redis.WriteTimeout,
			MaxOpenConns:    int(src.Redis.MaxOpenConnections),
			MaxIdleConns:    int(src.Redis.MaxIdleConnections),
			ConnMaxLifetime: src.Redis.ConnectionMaxLifetime,
		},
	}
}

func staticInfoToProto(src StaticInfo) *controlv1.StaticInfo {
	return &controlv1.StaticInfo{
		Hostname:          src.Hostname,
		Os:                src.OS,
		Arch:              src.Arch,
		CpuCount:          int32(src.NumCPU),
		MemoryTotalBytes:  src.MemTotalBytes,
		GoVersion:         src.GoVersion,
		KernelVersion:     src.KernelVer,
		StartedAtUnixNano: unixNanoAgent(src.StartedAt),
	}
}

func systemSnapshotToProto(src SystemSnapshot) *controlv1.SystemMetricSnapshot {
	return &controlv1.SystemMetricSnapshot{
		TimestampUnixNano:                unixNanoAgent(src.Timestamp),
		Sequence:                         src.Sequence,
		HostCpuPercent:                   cloneFloat64Agent(src.HostCPUPercent),
		HostMemoryTotalBytes:             cloneUint64Agent(src.HostMemTotalBytes),
		HostMemoryUsedBytes:              cloneUint64Agent(src.HostMemUsedBytes),
		HostMemoryPercent:                cloneFloat64Agent(src.HostMemPercent),
		HostNetworkSendBytesPerSecond:    cloneFloat64Agent(src.HostNetSendBytesPerSec),
		HostNetworkReceiveBytesPerSecond: cloneFloat64Agent(src.HostNetRecvBytesPerSec),
		ProcessCpuPercent:                cloneFloat64Agent(src.ProcessCPUPercent),
		ProcessRssBytes:                  cloneUint64Agent(src.ProcessRSSBytes),
		ProcessHeapBytes:                 src.ProcessHeapBytes,
		ProcessGoroutines:                int32(src.ProcessGoroutines),
		ProcessThreads:                   cloneInt32Agent(src.ProcessThreads),
		ProcessFileDescriptors:           cloneInt32Agent(src.ProcessFDs),
	}
}

func cleanupStatusToProto(src *robot.CleanupStatus) *controlv1.CleanupStatus {
	if src == nil {
		return nil
	}
	out := &controlv1.CleanupStatus{
		Status:         string(src.Status),
		Reason:         string(src.Reason),
		Message:        src.Message,
		DurationMillis: src.DurationMs,
		TotalRobots:    int32(src.TotalRobots),
		CleanedRobots:  int32(src.CleanedRobots),
		TimeoutRobots:  int32(src.TimeoutRobots),
		LuaReturned:    int32(src.LuaReturned),
		LuaSkipped:     int32(src.LuaSkipped),
		Issues:         make([]*controlv1.CleanupIssue, 0, len(src.Issues)),
	}
	for _, issue := range src.Issues {
		out.Issues = append(out.Issues, &controlv1.CleanupIssue{
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

func taskResultToProto(src TaskResult) controlv1.TaskResultCode {
	switch src {
	case TaskCompleted:
		return controlv1.TaskResultCode_TASK_RESULT_CODE_COMPLETED
	case TaskStopped:
		return controlv1.TaskResultCode_TASK_RESULT_CODE_STOPPED
	case TaskFailed:
		return controlv1.TaskResultCode_TASK_RESULT_CODE_FAILED
	default:
		return controlv1.TaskResultCode_TASK_RESULT_CODE_UNSPECIFIED
	}
}

func finalReportToProto(reportID string, src TaskCompletionReport) *controlv1.FinalReport {
	return &controlv1.FinalReport{
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

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func cloneFloat64Agent(src *float64) *float64 {
	if src == nil {
		return nil
	}
	return new(*src)
}

func cloneUint64Agent(src *uint64) *uint64 {
	if src == nil {
		return nil
	}
	return new(*src)
}

func cloneInt32Agent(src *int32) *int32 {
	if src == nil {
		return nil
	}
	return new(*src)
}

func unixNanoAgent(src time.Time) int64 {
	if src.IsZero() {
		return 0
	}
	return src.UnixNano()
}
