package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"stressbot/controlplane/controlv1"
	"stressbot/robot"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const controlProtocolVersion = 1

type grpcControlService struct {
	controlv1.UnimplementedAgentControlServiceServer
	server *AdminServer
}

func (svc *grpcControlService) Session(stream controlv1.AgentControlService_SessionServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.FailedPrecondition, "Session 第一帧必须是 Hello")
	}
	if hello.ProtocolVersion != controlProtocolVersion {
		return status.Errorf(codes.FailedPrecondition, "不支持的协议版本 %d", hello.ProtocolVersion)
	}
	if err := requireAgentID(hello.AgentId); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	session, err := svc.server.sessions.Attach(stream.Context(), hello.AgentId)
	if err != nil {
		return status.Error(codes.Unavailable, "服务器正在关闭")
	}
	defer func() {
		session.cancel()
		svc.server.sessions.Detach(session.agentID, session.generation)
		svc.server.telemetry.DropGeneration(session.agentID, session.generation)
	}()
	if err := svc.server.sessions.WithCurrent(session.agentID, session.generation, func() error {
		if err := svc.server.agents.Register(agentNodeFromHello(hello)); err != nil {
			return err
		}
		if hello.CurrentTaskId != "" && !svc.server.agentOwnsActiveTask(hello.AgentId, hello.CurrentTaskId) {
			ctx, cancel := commandContext()
			err := svc.server.scheduleStopCommands(ctx, hello.CurrentTaskId, []string{hello.AgentId}, "Admin 不再持有该任务，停止孤儿压测")
			cancel()
			if err != nil {
				return err
			}
			session.orphanStop.Store(true)
		}
		return nil
	}); err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	lease := svc.server.leaseDuration()
	if err := stream.Send(&controlv1.AdminEvent{Event: &controlv1.AdminEvent_Welcome{Welcome: &controlv1.Welcome{
		ProtocolVersion:        controlProtocolVersion,
		Generation:             session.generation,
		HeartbeatIntervalNanos: int64(svc.server.heartbeatInterval()),
		LeaseDurationNanos:     int64(lease),
		ServerTimeUnixNano:     time.Now().UnixNano(),
	}}}); err != nil {
		return err
	}
	if err := svc.server.commandBus.Replay(session.ctx, session); err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	if err := utils.GetWorkPool().Submit(func() {
		if sendErr := svc.sendLoop(session, stream); sendErr != nil {
			stresslog.Debug("[ADMIN] gRPC 会话发送循环结束", zap.String("agentID", session.agentID), zap.Error(sendErr))
			session.cancel()
		}
	}); err != nil {
		return status.Error(codes.ResourceExhausted, "无法启动会话发送循环")
	}
	// gRPC already owns this handler goroutine; it is the sole Recv owner.
	return normalizeSessionError(svc.recvLoop(session, stream))
}

func (svc *grpcControlService) sendLoop(session *agentSession, stream controlv1.AgentControlService_SessionServer) error {
	for {
		if !svc.server.sessions.Current(session.agentID, session.generation) {
			return context.Canceled
		}
		select {
		case event := <-session.control:
			if err := stream.Send(event); err != nil {
				return err
			}
		default:
		}
		select {
		case <-session.ctx.Done():
			return session.ctx.Err()
		case event := <-session.control:
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-session.commands:
			commands, err := svc.server.commandBus.PendingBatch(session.ctx, session)
			if err != nil {
				return err
			}
			for _, command := range commands {
				if !svc.server.sessions.Current(session.agentID, session.generation) {
					return context.Canceled
				}
				if command.GetStartTask() != nil && !svc.server.agentOwnsActiveTask(session.agentID, command.TaskId) {
					_, err := svc.server.commandBus.Acknowledge(session.ctx, &controlv1.CommandAck{CommandId: command.CommandId, Sequence: command.Sequence,
						AgentId: command.AgentId, TaskId: command.TaskId, Status: controlv1.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED,
						Reason: "任务已不再活跃", AcknowledgedAtUnixNano: time.Now().UnixNano()})
					if err != nil {
						return err
					}
					session.markCommandAcknowledged(command.CommandId)
					continue
				}
				command.DeliveryGeneration = session.generation
				if err := stream.Send(&controlv1.AdminEvent{Event: &controlv1.AdminEvent_Command{Command: command}}); err != nil {
					return err
				}
			}
		case ack := <-session.heartbeat:
			if err := stream.Send(&controlv1.AdminEvent{Event: &controlv1.AdminEvent_HeartbeatAck{HeartbeatAck: ack}}); err != nil {
				return err
			}
		}
	}
}

func (svc *grpcControlService) recvLoop(session *agentSession, stream controlv1.AgentControlService_SessionServer) error {
	for {
		event, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := svc.server.sessions.WithCurrent(session.agentID, session.generation, func() error {
			return svc.handleEvent(session, event)
		}); err != nil {
			if errors.Is(err, context.Canceled) {
				return status.Error(codes.Aborted, "会话已被新连接替代")
			}
			return err
		}
	}
}

func (svc *grpcControlService) handleEvent(session *agentSession, event *controlv1.AgentEvent) error {
	switch body := event.GetEvent().(type) {
	case *controlv1.AgentEvent_Heartbeat:
		hb := body.Heartbeat
		if hb.AgentId != session.agentID || hb.Generation != session.generation {
			return status.Error(codes.PermissionDenied, "心跳会话身份无效")
		}
		if err := svc.server.agents.Heartbeat(session.agentID, HeartbeatRequest{
			AgentID: session.agentID, Status: runtimeStatusString(hb.Status), CurrentTaskID: hb.CurrentTaskId,
			CurrentBots: int(hb.CurrentBots), AppVersion: hb.AppVersion,
		}); err != nil {
			return status.Error(codes.NotFound, err.Error())
		}
		if svc.server.agentOwnsActiveTask(session.agentID, hb.CurrentTaskId) || hb.CurrentTaskId == "" {
			now := time.Now()
			session.offerHeartbeat(&controlv1.HeartbeatAck{Generation: session.generation, ServerTimeUnixNano: now.UnixNano(), LeaseDeadlineUnixNano: now.Add(svc.server.leaseDuration()).UnixNano()})
		} else if session.orphanStop.CompareAndSwap(false, true) {
			agentID, taskID := session.agentID, hb.CurrentTaskId
			utils.GetWorkPool().Go(func() {
				ctx, cancel := commandContext()
				err := svc.server.scheduleStopCommands(ctx, taskID, []string{agentID}, "Admin 不再持有该任务，停止孤儿压测")
				cancel()
				if err != nil {
					session.orphanStop.Store(false)
					stresslog.Warn("[ADMIN] 创建孤儿任务停止命令失败", zap.String("agentID", agentID), zap.String("taskID", taskID), zap.Error(err))
				}
			})
		}
	case *controlv1.AgentEvent_CommandAck:
		ack := body.CommandAck
		if ack.AgentId != session.agentID {
			return status.Error(codes.PermissionDenied, "命令 ACK 身份无效")
		}
		command, err := svc.server.commandBus.Acknowledge(session.ctx, ack)
		if err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		session.markCommandAcknowledged(ack.CommandId)
		if ack.Status == controlv1.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED && command.GetStartTask() != nil {
			if svc.server.agentOwnsActiveTask(session.agentID, command.TaskId) {
				assignment := command.GetStartTask().GetAssignment()
				if err := svc.server.agents.StartTask(session.agentID, command.TaskId, int(assignment.TotalBots)); err != nil {
					return status.Error(codes.Unavailable, err.Error())
				}
			}
		}
		if ack.Status == controlv1.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED {
			if command.GetStartTask() != nil || command.GetStopTask() != nil {
				message := "节点拒绝启动命令，未进入清理阶段"
				if command.GetStopTask() != nil {
					message = "节点拒绝停止命令，无法等待该节点的完成报告"
				}
				cleanup := robot.UnknownCleanupStatus(robot.CleanupReasonStopWaitTimeout, message)
				reportErr := svc.server.acceptTaskReport(TaskCompletionReport{
					AgentID: session.agentID, TaskID: command.TaskId, Result: ResultFailed, ErrorMsg: ack.Reason,
					FinishedAt: time.Now(), CleanupStatus: &cleanup,
				})
				if reportErr != nil {
					if !isPermanentTaskReportError(reportErr) {
						return status.Error(codes.Unavailable, reportErr.Error())
					}
					stresslog.Warn("[ADMIN] 记录被拒绝命令的任务结果失败", zap.String("commandID", ack.CommandId), zap.Error(reportErr))
				}
			}
		}
		if !session.offerControl(&controlv1.AdminEvent{Event: &controlv1.AdminEvent_CommandReceipt{CommandReceipt: &controlv1.CommandReceipt{
			CommandId: ack.CommandId, Sequence: ack.Sequence,
		}}}) {
			return status.Error(codes.ResourceExhausted, "会话控制队列已满")
		}
	case *controlv1.AgentEvent_FinalReport:
		report := body.FinalReport
		if report.AgentId != session.agentID {
			return status.Error(codes.PermissionDenied, "最终报告身份无效")
		}
		domain, err := finalReportFromProto(report)
		if err == nil {
			err = svc.server.acceptTaskReport(domain)
		} else {
			err = &permanentTaskReportError{err: err}
		}
		if err != nil && !isPermanentTaskReportError(err) {
			// Do not terminally NACK transient persistence failures: closing the
			// stream keeps the Agent outbox entry for replay after reconnect.
			return status.Error(codes.Unavailable, err.Error())
		}
		ack := &controlv1.ReportAck{ReportId: report.ReportId, TaskId: report.TaskId, StageIndex: report.StageIndex, Accepted: err == nil}
		if err != nil {
			ack.Reason = err.Error()
		}
		if !session.offerControl(&controlv1.AdminEvent{Event: &controlv1.AdminEvent_ReportAck{ReportAck: ack}}) {
			return status.Error(codes.ResourceExhausted, "会话控制队列已满")
		}
	case *controlv1.AgentEvent_Hello:
		return status.Error(codes.FailedPrecondition, "同一会话不能重复发送 Hello")
	default:
		return status.Error(codes.InvalidArgument, "未知 AgentEvent")
	}
	return nil
}

func (s *AdminServer) agentOwnsActiveTask(agentID, taskID string) bool {
	if taskID == "" {
		return false
	}
	task := s.tasks.ActiveTask()
	if task == nil || task.ID != taskID {
		return false
	}
	if _, finished := task.Reports[agentID]; finished {
		return false
	}
	_, ok := taskExpectedAgents(task)[agentID]
	return ok
}

func agentNodeFromHello(hello *controlv1.Hello) *AgentNode {
	return &AgentNode{ID: hello.AgentId, Name: hello.Name, AppVersion: hello.AppVersion, MaxBots: int(hello.MaxBots),
		StressInterval: time.Duration(hello.MetricsIntervalNanos).String(), SystemInterval: time.Duration(hello.MetricsIntervalNanos).String(),
		StaticInfo: staticInfoFromProto(hello.StaticInfo), Status: runtimeStatusAdmin(hello.Status), LastHeartbeatAt: time.Now(),
		CurrentTaskID: hello.CurrentTaskId, CurrentBots: int(hello.CurrentBots)}
}

func runtimeStatusAdmin(statusValue controlv1.AgentRuntimeStatus) AgentStatus {
	if statusValue == controlv1.AgentRuntimeStatus_AGENT_RUNTIME_STATUS_BUSY {
		return AgentBusy
	}
	return AgentIdle
}

func runtimeStatusString(statusValue controlv1.AgentRuntimeStatus) string {
	if statusValue == controlv1.AgentRuntimeStatus_AGENT_RUNTIME_STATUS_BUSY {
		return "busy"
	}
	return "idle"
}

func normalizeSessionError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	stresslog.Debug("[ADMIN] gRPC 会话结束", zap.Error(err))
	return fmt.Errorf("会话结束: %w", err)
}
