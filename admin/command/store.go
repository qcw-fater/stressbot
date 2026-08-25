package command

import (
	"context"
	"errors"

	controlpb "stressbot/controlplane/pb"
)

const commandReplayBatchSize = 256

// 命令日志的哨兵错误：ErrCommandNotFound 表示按 ID 未命中（读路径与
// ACK 路径共用），ErrCommandStoreFull 表示容量腾挪后仍装不下新批次。
var (
	ErrCommandNotFound  = errors.New("命令不存在")
	ErrCommandStoreFull = errors.New("内存命令日志已满")
)

// Store 是 Admin 侧命令日志的持久化接口：批量写入、按 ID 读取、按 Agent
// 与 Sequence 游标拉取未决命令（limit 为单批上限）、按 ACK 状态确认落终态。
type Store interface {
	CreateBatch(context.Context, []*controlpb.Command) error
	Get(context.Context, string) (*controlpb.Command, error)
	Pending(context.Context, string, uint64, int) ([]*controlpb.Command, error)
	Acknowledge(context.Context, string, controlpb.CommandAckStatus, string) error
}

func commandKind(command *controlpb.Command) (string, error) {
	switch command.GetBody().(type) {
	case *controlpb.Command_StartTask:
		return "start", nil
	case *controlpb.Command_StopTask:
		return "stop", nil
	case *controlpb.Command_Shutdown:
		return "shutdown", nil
	default:
		return "", errors.New("命令缺少有效 body")
	}
}

func commandState(status controlpb.CommandAckStatus) (string, error) {
	switch status {
	case controlpb.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED,
		controlpb.CommandAckStatus_COMMAND_ACK_STATUS_DUPLICATE:
		return "acked", nil
	case controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED:
		return "rejected", nil
	default:
		return "", errors.New("命令 ACK 状态无效")
	}
}
