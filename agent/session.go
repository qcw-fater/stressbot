package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"stressbot/controlplane/controlv1"
	"stressbot/utils"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxControlMessageSize  = 16 << 20
	controlProtocolVersion = 1
)

type sessionSender struct {
	outgoing  chan *controlv1.AgentEvent
	heartbeat chan *controlv1.Heartbeat
	reports   *ReportOutbox
}

func newSessionSender(reports *ReportOutbox) *sessionSender {
	return &sessionSender{outgoing: make(chan *controlv1.AgentEvent, 64), heartbeat: make(chan *controlv1.Heartbeat, 1), reports: reports}
}

func (s *sessionSender) offer(event *controlv1.AgentEvent) error {
	select {
	case s.outgoing <- event:
		return nil
	default:
		return fmt.Errorf("会话发送队列已满")
	}
}

func (s *sessionSender) offerHeartbeat(heartbeat *controlv1.Heartbeat) {
	select {
	case s.heartbeat <- heartbeat:
	default:
		select {
		case <-s.heartbeat:
		default:
		}
		select {
		case s.heartbeat <- heartbeat:
		default:
		}
	}
}

func (a *Agent) runConnection(parent context.Context, conn *grpc.ClientConn) error {
	client := controlv1.NewAgentControlServiceClient(conn)
	stream, err := client.Session(parent)
	if err != nil {
		return err
	}
	statusValue, taskID, bots := a.runtimeState()
	hello := &controlv1.Hello{ProtocolVersion: controlProtocolVersion, AgentId: a.id, Name: a.cfg.Name, AppVersion: a.cfg.AppVersion,
		MaxBots: int32(a.cfg.MaxBots), MetricsIntervalNanos: int64(a.cfg.MetricsInterval), Status: statusValue,
		CurrentTaskId: taskID, CurrentBots: int32(bots),
		StaticInfo: staticInfoToProto(a.sysmon.Static())}
	if err := stream.Send(&controlv1.AgentEvent{Event: &controlv1.AgentEvent_Hello{Hello: hello}}); err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	welcome := first.GetWelcome()
	if welcome == nil || welcome.ProtocolVersion != controlProtocolVersion {
		return status.Error(codes.FailedPrecondition, "Admin 未返回有效 Welcome")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	a.leaseDeadline.Store(time.Now().Add(time.Duration(welcome.LeaseDurationNanos)).UnixNano())
	sender := newSessionSender(a.reportOutbox)
	a.reportOutbox.Wake()
	a.executor.Outcomes().Wake()
	bundleClient := controlv1.NewAgentBundleServiceClient(conn)
	telemetryClient := controlv1.NewAgentTelemetryServiceClient(conn)
	sendDone, recvDone, telemetryDone := make(chan error, 1), make(chan error, 1), make(chan error, 1)
	if err := utils.GetWorkPool().Submit(func() { sendDone <- a.sessionSendLoop(ctx, sender, stream) }); err != nil {
		return err
	}
	if err := utils.GetWorkPool().Submit(func() { recvDone <- a.sessionRecvLoop(ctx, sender, stream, bundleClient, welcome.Generation) }); err != nil {
		return err
	}
	if err := utils.GetWorkPool().Submit(func() { telemetryDone <- a.telemetry.sendLoop(ctx, telemetryClient, a.id, welcome.Generation) }); err != nil {
		return err
	}
	if err := utils.GetWorkPool().Submit(func() { a.heartbeatProducer(ctx, sender, welcome) }); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-sendDone:
		return err
	case err := <-recvDone:
		return err
	case err := <-telemetryDone:
		return err
	}
}

func (a *Agent) sessionSendLoop(ctx context.Context, sender *sessionSender, stream controlv1.AgentControlService_SessionClient) error {
	for {
		select {
		case event := <-sender.outgoing:
			if err := stream.Send(event); err != nil {
				return err
			}
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-sender.outgoing:
			if err := stream.Send(event); err != nil {
				return err
			}
		case <-a.executor.Outcomes().notify:
			for _, ack := range a.executor.Outcomes().Snapshot() {
				if err := stream.Send(&controlv1.AgentEvent{Event: &controlv1.AgentEvent_CommandAck{CommandAck: ack}}); err != nil {
					return err
				}
			}
		case heartbeat := <-sender.heartbeat:
			if err := stream.Send(&controlv1.AgentEvent{Event: &controlv1.AgentEvent_Heartbeat{Heartbeat: heartbeat}}); err != nil {
				return err
			}
		case <-sender.reports.notify:
			for _, report := range sender.reports.Snapshot() {
				if err := stream.Send(&controlv1.AgentEvent{Event: &controlv1.AgentEvent_FinalReport{FinalReport: report}}); err != nil {
					return err
				}
			}
		}
	}
}

func (a *Agent) sessionRecvLoop(ctx context.Context, sender *sessionSender, stream controlv1.AgentControlService_SessionClient, bundleClient controlv1.AgentBundleServiceClient, generation uint64) error {
	for {
		event, err := stream.Recv()
		if err != nil {
			return err
		}
		switch body := event.GetEvent().(type) {
		case *controlv1.AdminEvent_Command:
			if body.Command.DeliveryGeneration != generation {
				return status.Error(codes.Aborted, "命令 delivery generation 不匹配")
			}
			if err := a.executor.Enqueue(ctx, bundleClient, body.Command); err != nil {
				return err
			}
		case *controlv1.AdminEvent_HeartbeatAck:
			if body.HeartbeatAck.Generation != generation {
				return status.Error(codes.Aborted, "心跳 ACK generation 不匹配")
			}
			remaining := time.Duration(body.HeartbeatAck.LeaseDeadlineUnixNano - body.HeartbeatAck.ServerTimeUnixNano)
			if remaining < 0 {
				remaining = 0
			}
			a.leaseDeadline.Store(time.Now().Add(remaining).UnixNano())
		case *controlv1.AdminEvent_ReportAck:
			a.reportOutbox.Acknowledge(body.ReportAck)
		case *controlv1.AdminEvent_CommandReceipt:
			if a.executor.Outcomes().Confirm(body.CommandReceipt) {
				a.confirmShutdown(body.CommandReceipt.CommandId, body.CommandReceipt.Sequence)
			}
		case *controlv1.AdminEvent_ServerClosing:
			return status.Error(codes.Unavailable, body.ServerClosing.Reason)
		case *controlv1.AdminEvent_ProtocolError:
			return status.Error(codes.FailedPrecondition, body.ProtocolError.Message)
		case *controlv1.AdminEvent_Welcome:
			return status.Error(codes.FailedPrecondition, "重复 Welcome")
		default:
			return status.Error(codes.InvalidArgument, "未知 AdminEvent")
		}
	}
}

func (a *Agent) heartbeatProducer(ctx context.Context, sender *sessionSender, welcome *controlv1.Welcome) {
	interval := time.Duration(welcome.HeartbeatIntervalNanos)
	if interval <= 0 {
		interval = a.cfg.HeartbeatInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			statusValue, taskID, bots := a.runtimeState()
			sender.offerHeartbeat(&controlv1.Heartbeat{AgentId: a.id, Generation: welcome.Generation, SentAtUnixNano: now.UnixNano(),
				Status: statusValue, CurrentTaskId: taskID, CurrentBots: int32(bots), AppVersion: a.cfg.AppVersion})
		}
	}
}

func (a *Agent) runtimeState() (controlv1.AgentRuntimeStatus, string, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.currentTask != nil {
		return controlv1.AgentRuntimeStatus_AGENT_RUNTIME_STATUS_BUSY, a.currentTask.TaskID, a.currentTask.TotalBots
	}
	return controlv1.AgentRuntimeStatus_AGENT_RUNTIME_STATUS_IDLE, "", 0
}

func (a *Agent) leaseLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			deadline := a.leaseDeadline.Load()
			if deadline > 0 && now.UnixNano() > deadline && a.Status() == StatusBusy {
				a.cancelCurrentTask("Admin 控制面租约已过期")
				a.leaseDeadline.Store(0)
			}
		}
	}
}

func normalizeClientSessionError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
