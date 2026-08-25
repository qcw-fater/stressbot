package grpcapi

import (
	"errors"
	"fmt"
	"time"

	"stressbot/admin/agent"
	admintask "stressbot/admin/task"
	"stressbot/controlplane"
	controlpb "stressbot/controlplane/pb"
	"stressbot/robot"
)

// TaskAssignmentToProto 把任务分派配置转换为 proto TaskAssignment：校验
// 任务身份与分片参数，HeartbeatInterval/TCPTimeout/HTTPTimeout 以 duration
// 字符串解析为纳秒（必须大于 0）。
func TaskAssignmentToProto(src admintask.Dispatch) (*controlpb.TaskAssignment, error) {
	if src.TaskID == "" || src.StartIndex < 0 || src.TotalBots <= 0 {
		return nil, errors.New("任务分配参数无效")
	}
	out := &controlpb.TaskAssignment{
		TaskId:        src.TaskID,
		TaskName:      src.TaskName,
		StartNumber:   int32(src.StartNumber),
		StartIndex:    int32(src.StartIndex),
		TotalBots:     int32(src.TotalBots),
		AccountPrefix: src.AccountPrefix,
		ConcurrentNum: int32(src.ConcurrentNum),
		MainService:   src.MainService,
		StateExtra:    cloneStringMapAdmin(src.StateExtra),
		ApdexTMillis:  int32(src.ApdexT),
		LogLevel:      src.LogLevel,
		RampUp:        rampUpToProto(src.RampUp),
		Shared:        sharedAssignmentToProto(src.Shared),
	}
	var err error
	if out.HeartbeatIntervalNanos, err = durationNanos(src.HeartbeatInterval); err != nil {
		return nil, fmt.Errorf("解析 heartbeatInterval: %w", err)
	}
	if out.TcpTimeoutNanos, err = durationNanos(src.TCPTimeout); err != nil {
		return nil, fmt.Errorf("解析 tcpTimeout: %w", err)
	}
	if out.HttpTimeoutNanos, err = durationNanos(src.HTTPTimeout); err != nil {
		return nil, fmt.Errorf("解析 httpTimeout: %w", err)
	}
	return out, nil
}

func rampUpToProto(src *admintask.RampUpConfig) *controlpb.RampUpConfig {
	if src == nil {
		return nil
	}
	out := &controlpb.RampUpConfig{Stages: make([]*controlpb.RampUpStage, 0, len(src.Stages))}
	for _, stage := range src.Stages {
		out.Stages = append(out.Stages, &controlpb.RampUpStage{
			Count:       int32(stage.Count),
			Concurrency: int32(stage.Concurrency),
			HoldSeconds: int32(stage.HoldSec),
			Reset_:      stage.Reset,
		})
	}
	return out
}

func sharedAssignmentToProto(src *admintask.SharedRuntimeAssignment) *controlpb.SharedRuntimeAssignment {
	if src == nil {
		return nil
	}
	return &controlpb.SharedRuntimeAssignment{
		RunId: src.RunID,
		Redis: &controlpb.RedisConfig{
			Host:                  src.Redis.Host,
			Port:                  int32(src.Redis.Port),
			Username:              src.Redis.Username,
			Password:              src.Redis.Password,
			KeyPrefix:             src.Redis.KeyPrefix,
			DefaultClaimTtl:       src.Redis.DefaultClaimTTL,
			OperationTimeout:      src.Redis.OpTimeout,
			DialTimeout:           src.Redis.Pool.DialTimeout,
			ReadTimeout:           src.Redis.Pool.ReadTimeout,
			WriteTimeout:          src.Redis.Pool.WriteTimeout,
			MaxOpenConnections:    int32(src.Redis.Pool.MaxOpenConns),
			MaxIdleConnections:    int32(src.Redis.Pool.MaxIdleConns),
			ConnectionMaxLifetime: src.Redis.Pool.ConnMaxLifetime,
		},
	}
}

func staticInfoFromProto(src *controlpb.StaticInfo) agent.StaticInfo {
	if src == nil {
		return agent.StaticInfo{}
	}
	return agent.StaticInfo{
		Hostname:      src.Hostname,
		OS:            src.Os,
		Arch:          src.Arch,
		NumCPU:        int(src.CpuCount),
		MemTotalBytes: src.MemoryTotalBytes,
		GoVersion:     src.GoVersion,
		KernelVer:     src.KernelVersion,
		StartedAt:     timeFromUnixNanoAdmin(src.StartedAtUnixNano),
	}
}

// SystemSnapshotFromProto 把 Agent 上报的系统指标 proto 快照（主机与
// 进程两级 CPU/内存/网络指标）转换为领域模型 agent.SystemSnapshot。
func SystemSnapshotFromProto(src *controlpb.SystemMetricSnapshot) *agent.SystemSnapshot {
	if src == nil {
		return nil
	}
	return &agent.SystemSnapshot{
		Timestamp:              timeFromUnixNanoAdmin(src.TimestampUnixNano),
		Sequence:               src.Sequence,
		HostCPUPercent:         cloneFloat64Admin(src.HostCpuPercent),
		HostMemTotalBytes:      cloneUint64Admin(src.HostMemoryTotalBytes),
		HostMemUsedBytes:       cloneUint64Admin(src.HostMemoryUsedBytes),
		HostMemPercent:         cloneFloat64Admin(src.HostMemoryPercent),
		HostNetSendBytesPerSec: cloneFloat64Admin(src.HostNetworkSendBytesPerSecond),
		HostNetRecvBytesPerSec: cloneFloat64Admin(src.HostNetworkReceiveBytesPerSecond),
		ProcessCPUPercent:      cloneFloat64Admin(src.ProcessCpuPercent),
		ProcessRSSBytes:        cloneUint64Admin(src.ProcessRssBytes),
		ProcessHeapBytes:       src.ProcessHeapBytes,
		ProcessGoroutines:      int(src.ProcessGoroutines),
		ProcessThreads:         cloneInt32Admin(src.ProcessThreads),
		ProcessFDs:             cloneInt32Admin(src.ProcessFileDescriptors),
	}
}

func cleanupStatusFromProto(src *controlpb.CleanupStatus) *robot.CleanupStatus {
	if src == nil {
		return nil
	}
	out := &robot.CleanupStatus{
		Status:        robot.CleanupState(src.Status),
		Reason:        robot.CleanupReason(src.Reason),
		Message:       src.Message,
		DurationMs:    src.DurationMillis,
		TotalRobots:   int(src.TotalRobots),
		CleanedRobots: int(src.CleanedRobots),
		TimeoutRobots: int(src.TimeoutRobots),
		LuaReturned:   int(src.LuaReturned),
		LuaSkipped:    int(src.LuaSkipped),
		Issues:        make([]robot.CleanupIssue, 0, len(src.Issues)),
	}
	for _, issue := range src.Issues {
		if issue == nil {
			continue
		}
		out.Issues = append(out.Issues, robot.CleanupIssue{
			RobotID:      int(issue.RobotId),
			Account:      issue.Account,
			Phase:        issue.Phase,
			WaitDone:     issue.WaitDone,
			CloseAllDone: issue.CloseAllDone,
			Message:      issue.Message,
		})
	}
	return out
}

func finalReportFromProto(src *controlpb.FinalReport) (admintask.CompletionReport, error) {
	if src == nil || src.AgentId == "" || src.TaskId == "" || src.ReportId == "" {
		return admintask.CompletionReport{}, errors.New("最终报告缺少身份字段")
	}
	result, err := taskResultFromProto(src.Result)
	if err != nil {
		return admintask.CompletionReport{}, err
	}
	return admintask.CompletionReport{
		AgentID:       src.AgentId,
		TaskID:        src.TaskId,
		Result:        result,
		ErrorMsg:      src.ErrorMessage,
		FinishedAt:    timeFromUnixNanoAdmin(src.FinishedAtUnixNano),
		FinalSnapshot: controlplane.FromProtoCollectorSnapshot(src.Snapshot),
		StageIndex:    int(src.StageIndex),
		CleanupStatus: cleanupStatusFromProto(src.CleanupStatus),
	}, nil
}

func taskResultFromProto(src controlpb.TaskResultCode) (admintask.Result, error) {
	switch src {
	case controlpb.TaskResultCode_TASK_RESULT_CODE_COMPLETED:
		return admintask.ResultCompleted, nil
	case controlpb.TaskResultCode_TASK_RESULT_CODE_STOPPED:
		return admintask.ResultStopped, nil
	case controlpb.TaskResultCode_TASK_RESULT_CODE_FAILED:
		return admintask.ResultFailed, nil
	default:
		return "", fmt.Errorf("未知任务结果 %s", src.String())
	}
}

func durationNanos(raw string) (int64, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, errors.New("duration 必须大于 0")
	}
	return int64(duration), nil
}

func cloneStringMapAdmin(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func cloneFloat64Admin(src *float64) *float64 {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func cloneUint64Admin(src *uint64) *uint64 {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func cloneInt32Admin(src *int32) *int32 {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}

func timeFromUnixNanoAdmin(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value)
}
