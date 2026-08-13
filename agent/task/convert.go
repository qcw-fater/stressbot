package task

import (
	"fmt"
	"time"

	"stressbot/config"
	"stressbot/controlplane/pb"
	"stressbot/state/shared"
)

// FromProto 校验并转换控制面任务分配与资源包描述。
func FromProto(src *controlpb.TaskAssignment, bundle *controlpb.BundleDescriptor) (*TaskAssignment, error) {
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
		TaskID: src.TaskId, TaskName: src.TaskName, StartNumber: int(src.StartNumber), StartIndex: &startIndex,
		TotalBots: int(src.TotalBots), AccountPrefix: src.AccountPrefix, ConcurrentNum: int(src.ConcurrentNum),
		MainService: src.MainService, StateExtra: cloneStringMap(src.StateExtra),
		HeartbeatInterval: time.Duration(src.HeartbeatIntervalNanos).String(),
		TCPTimeout:        time.Duration(src.TcpTimeoutNanos).String(), HTTPTimeout: time.Duration(src.HttpTimeoutNanos).String(),
		ApdexT: int(src.ApdexTMillis), LogLevel: src.LogLevel,
		BundleDigest: append([]byte(nil), bundle.Sha256...), BundleSize: bundle.Size,
		RampUp: rampUpFromProto(src.RampUp), Shared: sharedAssignmentFromProto(src.Shared),
	}
	return out, out.Validate()
}

func rampUpFromProto(src *controlpb.RampUpConfig) *RampUpConfig {
	if src == nil {
		return nil
	}
	out := &RampUpConfig{Stages: make([]RampUpStage, 0, len(src.Stages))}
	for _, stage := range src.Stages {
		if stage == nil {
			continue
		}
		out.Stages = append(out.Stages, RampUpStage{Count: int(stage.Count), Concurrency: int(stage.Concurrency), HoldSec: int(stage.HoldSeconds), Reset: stage.Reset_})
	}
	return out
}

func sharedAssignmentFromProto(src *controlpb.SharedRuntimeAssignment) *SharedRuntimeAssignment {
	if src == nil || src.Redis == nil {
		return nil
	}
	return &SharedRuntimeAssignment{RunID: src.RunId, Redis: shared.RedisConfig{
		Host: src.Redis.Host, Port: int(src.Redis.Port), Username: src.Redis.Username, Password: src.Redis.Password,
		KeyPrefix: src.Redis.KeyPrefix, DefaultClaimTTL: src.Redis.DefaultClaimTtl, OpTimeout: src.Redis.OperationTimeout,
		Pool: config.ConnPoolConfig{DialTimeout: src.Redis.DialTimeout, ReadTimeout: src.Redis.ReadTimeout,
			WriteTimeout: src.Redis.WriteTimeout, MaxOpenConns: int(src.Redis.MaxOpenConnections),
			MaxIdleConns: int(src.Redis.MaxIdleConnections), ConnMaxLifetime: src.Redis.ConnectionMaxLifetime},
	}}
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
