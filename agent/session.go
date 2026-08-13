package agent

import (
	"context"
	"time"

	"stressbot/agent/metrics"
	"stressbot/agent/session"
	"stressbot/controlplane/pb"
	"stressbot/internal/workpool"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxControlMessageSize = 16 << 20
)

func newSessionSender(reports *session.ReportOutbox) *session.Sender {
	return session.NewSender(reports)
}

func (a *Agent) runConnection(parent context.Context, conn *grpc.ClientConn) error {
	client := controlpb.NewAgentControlServiceClient(conn)
	stream, err := client.Session(parent)
	if err != nil {
		return err
	}
	statusValue, taskID, bots := a.runtimeState()
	hello := &controlpb.Hello{ProtocolVersion: session.ProtocolVersion, AgentId: a.id, Name: a.cfg.Name, AppVersion: a.cfg.AppVersion,
		MaxBots: int32(a.cfg.MaxBots), MetricsIntervalNanos: int64(a.cfg.MetricsInterval), Status: statusValue,
		CurrentTaskId: taskID, CurrentBots: int32(bots),
		StaticInfo: metrics.StaticInfoToProto(a.sysmon.Static())}
	if err := stream.Send(&controlpb.AgentEvent{Event: &controlpb.AgentEvent_Hello{Hello: hello}}); err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	welcome := first.GetWelcome()
	if welcome == nil || welcome.ProtocolVersion != session.ProtocolVersion {
		return status.Error(codes.FailedPrecondition, "Admin 未返回有效 Welcome")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	a.leaseDeadline.Store(time.Now().Add(time.Duration(welcome.LeaseDurationNanos)).UnixNano())
	sender := newSessionSender(a.reportOutbox)
	a.reportOutbox.Wake()
	a.executor.Outcomes().Wake()
	bundleClient := controlpb.NewAgentBundleServiceClient(conn)
	metricsClient := controlpb.NewAgentMetricsServiceClient(conn)
	sendDone, recvDone, metricsDone := make(chan error, 1), make(chan error, 1), make(chan error, 1)
	if err := workpool.GetWorkPool().Submit(func() { sendDone <- a.sessionSendLoop(ctx, sender, stream) }); err != nil {
		return err
	}
	if err := workpool.GetWorkPool().Submit(func() { recvDone <- a.sessionRecvLoop(ctx, stream, bundleClient, welcome.Generation) }); err != nil {
		return err
	}
	if err := workpool.GetWorkPool().Submit(func() { metricsDone <- a.metrics.SendLoop(ctx, metricsClient, a.id, welcome.Generation) }); err != nil {
		return err
	}
	if err := workpool.GetWorkPool().Submit(func() { a.heartbeatProducer(ctx, sender, welcome) }); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-sendDone:
		return err
	case err := <-recvDone:
		return err
	case err := <-metricsDone:
		return err
	}
}

func (a *Agent) sessionSendLoop(ctx context.Context, sender *session.Sender, stream controlpb.AgentControlService_SessionClient) error {
	return session.SendLoop(ctx, sender, stream, a.executor.Outcomes())
}

func (a *Agent) sessionRecvLoop(ctx context.Context, stream controlpb.AgentControlService_SessionClient, bundleClient controlpb.AgentBundleServiceClient, generation uint64) error {
	return session.ReceiveLoop(ctx, stream, bundleClient, generation, session.ReceiveCallbacks{
		EnqueueCommand:    a.executor.Enqueue,
		UpdateLease:       func(remaining time.Duration) { a.leaseDeadline.Store(time.Now().Add(remaining).UnixNano()) },
		AcknowledgeReport: a.reportOutbox.Acknowledge,
		ConfirmCommand:    a.executor.Outcomes().Confirm,
		ConfirmShutdown:   a.confirmShutdown,
	})
}

func (a *Agent) heartbeatProducer(ctx context.Context, sender *session.Sender, welcome *controlpb.Welcome) {
	session.HeartbeatProducer(ctx, sender, welcome, a.cfg.HeartbeatInterval, a.id, a.cfg.AppVersion, a.runtimeState)
}

func (a *Agent) runtimeState() (controlpb.AgentRuntimeStatus, string, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.currentTask != nil {
		return controlpb.AgentRuntimeStatus_AGENT_RUNTIME_STATUS_BUSY, a.currentTask.TaskID, a.currentTask.TotalBots
	}
	return controlpb.AgentRuntimeStatus_AGENT_RUNTIME_STATUS_IDLE, "", 0
}

// RuntimeState 实现内部命令执行器所需的当前任务状态查询。
func (a *Agent) RuntimeState() (controlpb.AgentRuntimeStatus, string, int) {
	return a.runtimeState()
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
	return session.NormalizeError(err)
}
