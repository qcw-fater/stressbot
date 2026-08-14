package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"stressbot/admin/agent"
	"stressbot/admin/bundle"
	"stressbot/admin/metrics"
	admintask "stressbot/admin/task"
	"stressbot/controlplane/pb"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"
	"stressbot/robot"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const controlProtocolVersion = 1

type grpcControlService struct {
	controlpb.UnimplementedAgentControlServiceServer
	deps Dependencies
}

// Dependencies contains the Admin capabilities used by the gRPC transport.
// Cross-module task policy stays in the application layer and is injected as
// callbacks, keeping this package focused on protocol/session handling.
type Dependencies struct {
	Sessions               *SessionRegistry
	Agents                 *agent.Registry
	Commands               CommandBus
	Bundles                *bundle.Store
	Metrics                *metrics.Ingestor
	HeartbeatInterval      time.Duration
	LeaseDuration          time.Duration
	OwnsActiveTask         func(agentID, taskID string) bool
	ScheduleStop           func(context.Context, string, []string, string) error
	AcceptTaskReport       func(admintask.CompletionReport) error
	IsPermanentReportError func(error) bool
}

type CommandBus interface {
	Replay(context.Context, *Session) error
	PendingBatch(context.Context, *Session) ([]*controlpb.Command, error)
	Acknowledge(context.Context, *controlpb.CommandAck) (*controlpb.Command, error)
}

func commandContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func (svc *grpcControlService) Session(stream controlpb.AgentControlService_SessionServer) error {
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
	session, err := svc.deps.Sessions.Attach(stream.Context(), hello.AgentId)
	if err != nil {
		return status.Error(codes.Unavailable, "服务器正在关闭")
	}
	defer func() {
		session.Cancel()
		svc.deps.Sessions.Detach(session.AgentID(), session.Generation())
		svc.deps.Metrics.DropGeneration(session.AgentID(), session.Generation())
	}()
	if err := svc.deps.Sessions.WithCurrent(session.AgentID(), session.Generation(), func() error {
		if err := svc.deps.Agents.Register(agentNodeFromHello(hello)); err != nil {
			return err
		}
		if hello.CurrentTaskId != "" && !svc.deps.OwnsActiveTask(hello.AgentId, hello.CurrentTaskId) {
			ctx, cancel := commandContext()
			err := svc.deps.ScheduleStop(ctx, hello.CurrentTaskId, []string{hello.AgentId}, "Admin 不再持有该任务，停止孤儿压测")
			cancel()
			if err != nil {
				return err
			}
			session.TryMarkOrphanStop()
		}
		return nil
	}); err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	lease := svc.deps.LeaseDuration
	if err := stream.Send(&controlpb.AdminEvent{Event: &controlpb.AdminEvent_Welcome{Welcome: &controlpb.Welcome{
		ProtocolVersion:        controlProtocolVersion,
		Generation:             session.Generation(),
		HeartbeatIntervalNanos: int64(svc.deps.HeartbeatInterval),
		LeaseDurationNanos:     int64(lease),
		ServerTimeUnixNano:     time.Now().UnixNano(),
	}}}); err != nil {
		return err
	}
	if err := svc.deps.Commands.Replay(session.Context(), session); err != nil {
		return status.Error(codes.Unavailable, err.Error())
	}
	if err := workpool.Default().Submit(func() {
		if sendErr := svc.sendLoop(session, stream); sendErr != nil {
			stresslog.Debug("[ADMIN] gRPC 会话发送循环结束", zap.String("agentID", session.AgentID()), zap.Error(sendErr))
			session.Cancel()
		}
	}); err != nil {
		return status.Error(codes.ResourceExhausted, "无法启动会话发送循环")
	}
	// gRPC already owns this handler goroutine; it is the sole Recv owner.
	return normalizeSessionError(svc.recvLoop(session, stream))
}

func (svc *grpcControlService) sendLoop(session *Session, stream controlpb.AgentControlService_SessionServer) error {
	for {
		if !svc.deps.Sessions.Current(session.AgentID(), session.Generation()) {
			return context.Canceled
		}
		select {
		case event := <-session.Control():
			if err := stream.Send(event); err != nil {
				return err
			}
		default:
		}
		select {
		case <-session.Context().Done():
			return session.Context().Err()
		case event := <-session.Control():
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-session.Commands():
			commands, err := svc.deps.Commands.PendingBatch(session.Context(), session)
			if err != nil {
				return err
			}
			for _, command := range commands {
				if !svc.deps.Sessions.Current(session.AgentID(), session.Generation()) {
					return context.Canceled
				}
				if command.GetStartTask() != nil && !svc.deps.OwnsActiveTask(session.AgentID(), command.TaskId) {
					_, err := svc.deps.Commands.Acknowledge(session.Context(), &controlpb.CommandAck{CommandId: command.CommandId, Sequence: command.Sequence,
						AgentId: command.AgentId, TaskId: command.TaskId, Status: controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED,
						Reason: "任务已不再活跃", AcknowledgedAtUnixNano: time.Now().UnixNano()})
					if err != nil {
						return err
					}
					session.MarkCommandAcknowledged(command.CommandId)
					continue
				}
				command.DeliveryGeneration = session.Generation()
				if err := stream.Send(&controlpb.AdminEvent{Event: &controlpb.AdminEvent_Command{Command: command}}); err != nil {
					return err
				}
			}
		case ack := <-session.Heartbeat():
			if err := stream.Send(&controlpb.AdminEvent{Event: &controlpb.AdminEvent_HeartbeatAck{HeartbeatAck: ack}}); err != nil {
				return err
			}
		}
	}
}

func (svc *grpcControlService) recvLoop(session *Session, stream controlpb.AgentControlService_SessionServer) error {
	for {
		event, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := svc.deps.Sessions.WithCurrent(session.AgentID(), session.Generation(), func() error {
			return svc.handleEvent(session, event)
		}); err != nil {
			if errors.Is(err, context.Canceled) {
				return status.Error(codes.Aborted, "会话已被新连接替代")
			}
			return err
		}
	}
}

func (svc *grpcControlService) handleEvent(session *Session, event *controlpb.AgentEvent) error {
	switch body := event.GetEvent().(type) {
	case *controlpb.AgentEvent_Heartbeat:
		hb := body.Heartbeat
		if hb.AgentId != session.AgentID() || hb.Generation != session.Generation() {
			return status.Error(codes.PermissionDenied, "心跳会话身份无效")
		}
		if err := svc.deps.Agents.Heartbeat(session.AgentID(), agent.HeartbeatRequest{
			AgentID: session.AgentID(), Status: runtimeStatusString(hb.Status), CurrentTaskID: hb.CurrentTaskId,
			CurrentBots: int(hb.CurrentBots), AppVersion: hb.AppVersion,
		}); err != nil {
			return status.Error(codes.NotFound, err.Error())
		}
		if svc.deps.OwnsActiveTask(session.AgentID(), hb.CurrentTaskId) || hb.CurrentTaskId == "" {
			now := time.Now()
			session.OfferHeartbeat(&controlpb.HeartbeatAck{Generation: session.Generation(), ServerTimeUnixNano: now.UnixNano(), LeaseDeadlineUnixNano: now.Add(svc.deps.LeaseDuration).UnixNano()})
		} else if session.TryMarkOrphanStop() {
			agentID, taskID := session.AgentID(), hb.CurrentTaskId
			workpool.Default().Go(func() {
				ctx, cancel := commandContext()
				err := svc.deps.ScheduleStop(ctx, taskID, []string{agentID}, "Admin 不再持有该任务，停止孤儿压测")
				cancel()
				if err != nil {
					session.ClearOrphanStop()
					stresslog.Warn("[ADMIN] 创建孤儿任务停止命令失败", zap.String("agentID", agentID), zap.String("taskID", taskID), zap.Error(err))
				}
			})
		}
	case *controlpb.AgentEvent_CommandAck:
		ack := body.CommandAck
		if ack.AgentId != session.AgentID() {
			return status.Error(codes.PermissionDenied, "命令 ACK 身份无效")
		}
		command, err := svc.deps.Commands.Acknowledge(session.Context(), ack)
		if err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		session.MarkCommandAcknowledged(ack.CommandId)
		if ack.Status == controlpb.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED && command.GetStartTask() != nil {
			if svc.deps.OwnsActiveTask(session.AgentID(), command.TaskId) {
				assignment := command.GetStartTask().GetAssignment()
				if err := svc.deps.Agents.StartTask(session.AgentID(), command.TaskId, int(assignment.TotalBots)); err != nil {
					return status.Error(codes.Unavailable, err.Error())
				}
			}
		}
		if ack.Status == controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED {
			if command.GetStartTask() != nil || command.GetStopTask() != nil {
				message := "节点拒绝启动命令，未进入清理阶段"
				if command.GetStopTask() != nil {
					message = "节点拒绝停止命令，无法等待该节点的完成报告"
				}
				cleanup := robot.UnknownCleanupStatus(robot.CleanupReasonStopWaitTimeout, message)
				reportErr := svc.deps.AcceptTaskReport(admintask.CompletionReport{
					AgentID: session.AgentID(), TaskID: command.TaskId, Result: admintask.ResultFailed, ErrorMsg: ack.Reason,
					FinishedAt: time.Now(), CleanupStatus: &cleanup,
				})
				if reportErr != nil {
					if !svc.deps.IsPermanentReportError(reportErr) {
						return status.Error(codes.Unavailable, reportErr.Error())
					}
					stresslog.Warn("[ADMIN] 记录被拒绝命令的任务结果失败", zap.String("commandID", ack.CommandId), zap.Error(reportErr))
				}
			}
		}
		if !session.OfferControl(&controlpb.AdminEvent{Event: &controlpb.AdminEvent_CommandReceipt{CommandReceipt: &controlpb.CommandReceipt{
			CommandId: ack.CommandId, Sequence: ack.Sequence,
		}}}) {
			return status.Error(codes.ResourceExhausted, "会话控制队列已满")
		}
	case *controlpb.AgentEvent_FinalReport:
		report := body.FinalReport
		if report.AgentId != session.AgentID() {
			return status.Error(codes.PermissionDenied, "最终报告身份无效")
		}
		domain, err := finalReportFromProto(report)
		if err == nil {
			err = svc.deps.AcceptTaskReport(domain)
		} else {
			err = &permanentReportError{err: err}
		}
		if err != nil && !svc.deps.IsPermanentReportError(err) && !isPermanentReportError(err) {
			// Do not terminally NACK transient persistence failures: closing the
			// stream keeps the Agent outbox entry for replay after reconnect.
			return status.Error(codes.Unavailable, err.Error())
		}
		ack := &controlpb.ReportAck{ReportId: report.ReportId, TaskId: report.TaskId, StageIndex: report.StageIndex, Accepted: err == nil}
		if err != nil {
			ack.Reason = err.Error()
		}
		if !session.OfferControl(&controlpb.AdminEvent{Event: &controlpb.AdminEvent_ReportAck{ReportAck: ack}}) {
			return status.Error(codes.ResourceExhausted, "会话控制队列已满")
		}
	case *controlpb.AgentEvent_Hello:
		return status.Error(codes.FailedPrecondition, "同一会话不能重复发送 Hello")
	default:
		return status.Error(codes.InvalidArgument, "未知 AgentEvent")
	}
	return nil
}

func agentNodeFromHello(hello *controlpb.Hello) *agent.Node {
	return &agent.Node{ID: hello.AgentId, Name: hello.Name, AppVersion: hello.AppVersion, MaxBots: int(hello.MaxBots),
		StressInterval: time.Duration(hello.MetricsIntervalNanos).String(), SystemInterval: time.Duration(hello.MetricsIntervalNanos).String(),
		StaticInfo: staticInfoFromProto(hello.StaticInfo), Status: runtimeStatusAdmin(hello.Status), LastHeartbeatAt: time.Now(),
		CurrentTaskID: hello.CurrentTaskId, CurrentBots: int(hello.CurrentBots)}
}

func runtimeStatusAdmin(statusValue controlpb.AgentRuntimeStatus) agent.Status {
	if statusValue == controlpb.AgentRuntimeStatus_AGENT_RUNTIME_STATUS_BUSY {
		return agent.Busy
	}
	return agent.Idle
}

func runtimeStatusString(statusValue controlpb.AgentRuntimeStatus) string {
	if statusValue == controlpb.AgentRuntimeStatus_AGENT_RUNTIME_STATUS_BUSY {
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

type permanentReportError struct{ err error }

func (e *permanentReportError) Error() string { return e.err.Error() }
func (e *permanentReportError) Unwrap() error { return e.err }

func isPermanentReportError(err error) bool {
	_, ok := errors.AsType[*permanentReportError](err)
	return ok
}
