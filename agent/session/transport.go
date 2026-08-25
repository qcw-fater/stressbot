package session

import (
	"context"
	"errors"
	"io"
	"time"

	controlpb "stressbot/controlplane/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ProtocolVersion 是 Agent 控制会话协议版本：Hello 携带上报，Admin 回以 Welcome，
// 双方不一致时会话无法建立。
const ProtocolVersion = 1

// Sender 汇聚会话待发送的事件通道：一般事件（有界缓冲、满则丢弃并报错）、
// 心跳（仅保留最新一条）与最终报告待确认队列。
type Sender struct {
	outgoing  chan *controlpb.AgentEvent
	heartbeat chan *controlpb.Heartbeat
	reports   *ReportOutbox
}

// NewSender 创建会话发送器，事件缓冲 64 条、心跳缓冲 1 条。
func NewSender(reports *ReportOutbox) *Sender {
	return &Sender{outgoing: make(chan *controlpb.AgentEvent, 64), heartbeat: make(chan *controlpb.Heartbeat, 1), reports: reports}
}

// Offer 将一条事件放入发送缓冲，缓冲已满时立即返回错误。
func (s *Sender) Offer(event *controlpb.AgentEvent) error {
	select {
	case s.outgoing <- event:
		return nil
	default:
		return errors.New("会话发送队列已满")
	}
}

// OfferHeartbeat 提交一条心跳：缓冲已有旧值时先丢弃再写入，保证通道内始终只有最新一条心跳。
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

// OutcomeOutbox 是 SendLoop 对命令结果待确认队列的最小依赖：
// 等待发送通知、取待确认快照重放、确认 Admin 已持久接收。
type OutcomeOutbox interface {
	Notifications() <-chan struct{}
	Snapshot() []*controlpb.CommandAck
	Confirm(*controlpb.CommandReceipt) bool
}

// SendLoop 驱动会话发送侧（建议提交到协程池）：优先发送缓冲事件，随后在
// ctx 结束、新事件、命令结果通知（重放全部待确认 ack）、心跳与最终报告通知
// （重放全部未确认报告）之间循环选择发送；stream 发送出错即返回。
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

// ReceiveCallbacks 是 ReceiveLoop 分发 Admin 下行事件所需的宿主回调集合。
type ReceiveCallbacks struct {
	EnqueueCommand    func(context.Context, controlpb.AgentBundleServiceClient, *controlpb.Command) error
	UpdateLease       func(time.Duration)
	AcknowledgeReport func(*controlpb.ReportAck)
	ConfirmCommand    func(*controlpb.CommandReceipt) bool
	ConfirmShutdown   func(string, uint64)
}

// ReceiveLoop 阻塞接收并分发 Admin 下行事件（建议提交到协程池）：命令经校验
// delivery generation 后入队，心跳 ACK 校验 generation 并更新剩余租约，报告确认、
// 命令回执分别走对应回调；generation 不匹配、服务端关闭、协议错误及未知事件
// 均以 gRPC 错误终止会话。
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

// NormalizeError 归一化会话循环的退出错误：nil、io.EOF 与主动取消视为正常退出返回 nil，其余原样返回。
func NormalizeError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
