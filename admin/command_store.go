package admin

import (
	"context"
	"errors"

	"stressbot/controlplane/controlv1"
)

const commandReplayBatchSize = 256

var (
	ErrCommandNotFound  = errors.New("命令不存在")
	ErrCommandStoreFull = errors.New("内存命令日志已满")
)

type CommandStore interface {
	CreateBatch(context.Context, []*controlv1.Command) error
	Get(context.Context, string) (*controlv1.Command, error)
	Pending(context.Context, string, uint64, int) ([]*controlv1.Command, error)
	Acknowledge(context.Context, string, controlv1.CommandAckStatus, string) error
}

func commandKind(command *controlv1.Command) (string, error) {
	switch command.GetBody().(type) {
	case *controlv1.Command_StartTask:
		return "start", nil
	case *controlv1.Command_StopTask:
		return "stop", nil
	case *controlv1.Command_Shutdown:
		return "shutdown", nil
	default:
		return "", errors.New("命令缺少有效 body")
	}
}

func commandState(status controlv1.CommandAckStatus) (string, error) {
	switch status {
	case controlv1.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED,
		controlv1.CommandAckStatus_COMMAND_ACK_STATUS_DUPLICATE:
		return "acked", nil
	case controlv1.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED:
		return "rejected", nil
	default:
		return "", errors.New("命令 ACK 状态无效")
	}
}
