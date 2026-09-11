package command

import (
	"testing"

	controlpb "stressbot/controlplane/pb"
	"stressbot/internal/stresslog"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestFinishLogsCommandOutcome(t *testing.T) {
	tests := []struct {
		name    string
		status  controlpb.CommandAckStatus
		reason  string
		message string
		level   zapcore.Level
	}{
		{name: "执行完成", status: controlpb.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED, message: "[AGENT] 控制命令执行完成", level: zapcore.InfoLevel},
		{name: "执行被拒绝", status: controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, reason: "任务状态不允许", message: "[AGENT] 控制命令执行被拒绝", level: zapcore.WarnLevel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := stresslog.GetLogger()
			core, logs := observer.New(zapcore.DebugLevel)
			stresslog.ReplaceLogger(zap.New(core))
			t.Cleanup(func() { stresslog.ReplaceLogger(original) })

			executor := &Executor{outcomes: NewOutcomeOutbox(8)}
			command := &controlpb.Command{
				CommandId: "cmd-001", TaskId: "task-001", Sequence: 3,
				Body: &controlpb.Command_StopTask{StopTask: &controlpb.StopTask{}},
			}
			executor.finish(commandRequest{cmd: command}, &controlpb.CommandAck{
				CommandId: command.CommandId, AgentId: "agent-001", TaskId: command.TaskId, Sequence: command.Sequence,
				Status: tt.status, Reason: tt.reason,
			})

			entries := logs.FilterMessage(tt.message).All()
			if len(entries) != 1 {
				t.Fatalf("%q 日志数量 = %d，期望 1；全部日志: %v", tt.message, len(entries), logs.All())
			}
			if entries[0].Level != tt.level {
				t.Fatalf("日志级别 = %s，期望 %s", entries[0].Level, tt.level)
			}
			fields := entries[0].ContextMap()
			wantStatus := "applied"
			if tt.status == controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED {
				wantStatus = "rejected"
			}
			if fields["agentID"] != "agent-001" || fields["commandType"] != "stopTask" || fields["status"] != wantStatus {
				t.Fatalf("命令结果日志字段不完整: %v", fields)
			}
		})
	}
}
