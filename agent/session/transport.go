package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"stressbot/controlplane/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ProtocolVersion = 1

type Sender struct {
	outgoing  chan *controlpb.AgentEvent
	heartbeat chan *controlpb.Heartbeat
	reports   *ReportOutbox
}

func NewSender(reports *ReportOutbox) *Sender {
	return &Sender{outgoing: make(chan *controlpb.AgentEvent, 64), heartbeat: make(chan *controlpb.Heartbeat, 1), reports: reports}
}

func (s *Sender) Offer(event *controlpb.AgentEvent) error {
	select {
	case s.outgoing <- event:
		return nil
	default:
		return fmt.Errorf("会话发送队列已满")
	}
}

func (s *Sender) OfferHeartbeat(heartbeat *controlpb.Heartbeat) {
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

type OutcomeOutbox interface {
	Notifications() <-chan struct{}
	Snapshot() []*controlpb.CommandAck
	Confirm(*controlpb.CommandReceipt) bool
}

func SendLoop(ctx context.Context, sender *Sender, stream controlpb.AgentControlService_SessionClient, outcomes OutcomeOutbox) error {
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
		case <-outcomes.Notifications():
			for _, ack := range outcomes.Snapshot() {
				if err := stream.Send(&controlpb.AgentEvent{Event: &controlpb.AgentEvent_CommandAck{CommandAck: ack}}); err != nil {
					return err
				}
			}
		case heartbeat := <-sender.heartbeat:
			if err := stream.Send(&controlpb.AgentEvent{Event: &controlpb.AgentEvent_Heartbeat{Heartbeat: heartbeat}}); err != nil {
				return err
			}
		case <-sender.reports.Notifications():
			for _, report := range sender.reports.Snapshot() {
				if err := stream.Send(&controlpb.AgentEvent{Event: &controlpb.AgentEvent_FinalReport{FinalReport: report}}); err != nil {
					return err
				}
			}
		}
	}
}

type ReceiveCallbacks struct {
	EnqueueCommand    func(context.Context, controlpb.AgentBundleServiceClient, *controlpb.Command) error
	UpdateLease       func(time.Duration)
	AcknowledgeReport func(*controlpb.ReportAck)
	ConfirmCommand    func(*controlpb.CommandReceipt) bool
	ConfirmShutdown   func(string, uint64)
}

func ReceiveLoop(ctx context.Context, stream controlpb.AgentControlService_SessionClient, bundleClient controlpb.AgentBundleServiceClient, generation uint64, callbacks ReceiveCallbacks) error {
	for {
		event, err := stream.Recv()
		if err != nil {
			return err
		}
		switch body := event.GetEvent().(type) {
		case *controlpb.AdminEvent_Command:
			if body.Command.DeliveryGeneration != generation {
				return status.Error(codes.Aborted, "命令 delivery generation 不匹配")
			}
			if err := callbacks.EnqueueCommand(ctx, bundleClient, body.Command); err != nil {
				return err
			}
		case *controlpb.AdminEvent_HeartbeatAck:
			if body.HeartbeatAck.Generation != generation {
				return status.Error(codes.Aborted, "心跳 ACK generation 不匹配")
			}
			remaining := time.Duration(body.HeartbeatAck.LeaseDeadlineUnixNano - body.HeartbeatAck.ServerTimeUnixNano)
			if remaining < 0 {
				remaining = 0
			}
			callbacks.UpdateLease(remaining)
		case *controlpb.AdminEvent_ReportAck:
			callbacks.AcknowledgeReport(body.ReportAck)
		case *controlpb.AdminEvent_CommandReceipt:
			if callbacks.ConfirmCommand(body.CommandReceipt) {
				callbacks.ConfirmShutdown(body.CommandReceipt.CommandId, body.CommandReceipt.Sequence)
			}
		case *controlpb.AdminEvent_ServerClosing:
			return status.Error(codes.Unavailable, body.ServerClosing.Reason)
		case *controlpb.AdminEvent_ProtocolError:
			return status.Error(codes.FailedPrecondition, body.ProtocolError.Message)
		case *controlpb.AdminEvent_Welcome:
			return status.Error(codes.FailedPrecondition, "重复 Welcome")
		default:
			return status.Error(codes.InvalidArgument, "未知 AdminEvent")
		}
	}
}

func HeartbeatProducer(ctx context.Context, sender *Sender, welcome *controlpb.Welcome, fallbackInterval time.Duration, agentID, appVersion string, state func() (controlpb.AgentRuntimeStatus, string, int)) {
	interval := time.Duration(welcome.HeartbeatIntervalNanos)
	if interval <= 0 {
		interval = fallbackInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			statusValue, taskID, bots := state()
			sender.OfferHeartbeat(&controlpb.Heartbeat{AgentId: agentID, Generation: welcome.Generation, SentAtUnixNano: now.UnixNano(), Status: statusValue, CurrentTaskId: taskID, CurrentBots: int32(bots), AppVersion: appVersion})
		}
	}
}

func NormalizeError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
