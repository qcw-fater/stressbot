// Package command 实现控制面下发命令的顺序执行与结果留存：
// 命令经队列串行处理，结果进入待确认 outbox，直到 Admin 确认接收后才可淘汰。
package command

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"stressbot/agent/bundle"
	agenttask "stressbot/agent/task"
	controlpb "stressbot/controlplane/pb"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type commandRequest struct {
	ctx    context.Context
	client controlpb.AgentBundleServiceClient
	cmd    *controlpb.Command
}

// Host 是命令执行器对 Agent 生命周期的最小依赖。
type Host interface {
	AgentID() string
	RuntimeState() (controlpb.AgentRuntimeStatus, string, int)
	CancelTask(expectedTaskID, reason string) (string, bool)
	ReserveTask(task *agenttask.Assignment) (context.Context, context.CancelCauseFunc, error)
	LaunchReservedTask(context.Context, context.CancelCauseFunc, *agenttask.Assignment, func(func()) error) error
	ReleaseReservedTask(*agenttask.Assignment, context.CancelCauseFunc, error)
	PrepareShutdown(commandID string, sequence uint64)
	CancelControlPlane()
}

var errTaskSessionInterrupted = errors.New("启动任务时控制会话中断")

// Executor 按顺序执行控制面下发的命令，并保留待确认结果。
type Executor struct {
	host     Host
	cache    *bundle.Cache
	outcomes *OutcomeOutbox
	queue    chan commandRequest
	mu       sync.Mutex
	inflight map[string]struct{}
	deferred map[string]commandRequest
}

// NewExecutor 创建命令执行器。
func NewExecutor(host Host, cache *bundle.Cache) *Executor {
	return &Executor{host: host, cache: cache, outcomes: NewOutcomeOutbox(4096), queue: make(chan commandRequest, 64),
		inflight: make(map[string]struct{}), deferred: make(map[string]commandRequest)}
}

// Start 启动命令消费循环（提交到协程池）：从队列逐条取出命令执行，
// 并将结果写入 outbox；ctx 结束后循环退出。
func (e *Executor) Start(ctx context.Context) error {
	return workpool.Default().Submit(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case request := <-e.queue:
				ack := e.execute(request)
				e.finish(request, ack)
			}
		}
	})
}

// Enqueue 将一条控制面命令放入执行队列，队列已满时阻塞；
// ctx 先结束则放弃入队并返回其错误。
func (e *Executor) Enqueue(ctx context.Context, client controlpb.AgentBundleServiceClient, command *controlpb.Command) error {
	request := commandRequest{ctx: ctx, client: client, cmd: command}
	select {
	case e.queue <- request:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Executor) execute(request commandRequest) *controlpb.CommandAck {
	command := request.cmd
	ack := &controlpb.CommandAck{CommandId: command.CommandId, Sequence: command.Sequence, AgentId: e.host.AgentID(),
		TaskId: command.TaskId, Status: controlpb.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED, AcknowledgedAtUnixNano: time.Now().UnixNano()}
	if command.AgentId != e.host.AgentID() {
		ack.Status, ack.Reason = controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, "命令目标节点不匹配"
		return ack
	}
	if previous := e.outcomes.FindAndReplay(command.CommandId); previous != nil {
		return nil
	}
	if body, ok := command.GetBody().(*controlpb.Command_StartTask); ok {
		e.beginStart(request, ack, body.StartTask)
		return nil
	}
	var err error
	switch body := command.GetBody().(type) {
	case *controlpb.Command_StopTask:
		current, canceled := e.host.CancelTask(command.TaskId, body.StopTask.Reason)
		if !canceled && current != "" {
			err = fmt.Errorf("当前任务为 %s", current)
		}
	case *controlpb.Command_Shutdown:
		// Process exit is deferred until Admin durably commits this ACK and
		// returns CommandReceipt.
	default:
		err = errors.New("未知命令类型")
	}
	if err != nil {
		if isTransientCommandError(err) {
			return nil
		}
		ack.Status, ack.Reason = controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, err.Error()
	}
	return ack
}

func (e *Executor) beginStart(request commandRequest, ack *controlpb.CommandAck, start *controlpb.StartTask) {
	command := request.cmd
	e.mu.Lock()
	if _, exists := e.inflight[command.CommandId]; exists {
		e.deferred[command.CommandId] = request
		e.mu.Unlock()
		return
	}
	e.inflight[command.CommandId] = struct{}{}
	e.mu.Unlock()

	_, currentTaskID, _ := e.host.RuntimeState()
	if currentTaskID == command.TaskId && currentTaskID != "" {
		ack.Status = controlpb.CommandAckStatus_COMMAND_ACK_STATUS_DUPLICATE
		e.completeStart(request, ack)
		return
	}
	if currentTaskID != "" {
		ack.Status, ack.Reason = controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, "已有任务运行: "+currentTaskID
		e.completeStart(request, ack)
		return
	}
	domain, err := agenttask.FromProto(start.GetAssignment(), start.GetBundle())
	if err != nil {
		ack.Status, ack.Reason = controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, err.Error()
		e.completeStart(request, ack)
		return
	}
	taskCtx, taskCancel, err := e.host.ReserveTask(domain)
	if err != nil {
		ack.Status, ack.Reason = controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, err.Error()
		e.completeStart(request, ack)
		return
	}
	if err := workpool.Default().Submit(func() {
		sessionCanceled := make(chan struct{})
		stopSessionCancel := context.AfterFunc(request.ctx, func() {
			taskCancel(errTaskSessionInterrupted)
			close(sessionCanceled)
		})
		domain.BundleDir, err = e.cache.Ensure(taskCtx, request.client, e.host.AgentID(), domain.BundleDigest, domain.BundleSize)
		if !stopSessionCancel() {
			<-sessionCanceled
		}
		if err == nil && context.Cause(taskCtx) == nil {
			err = e.host.LaunchReservedTask(taskCtx, taskCancel, domain, workpool.Default().Submit)
		}
		if err != nil || context.Cause(taskCtx) != nil {
			cause := context.Cause(taskCtx)
			if cause == nil {
				cause = err
			}
			e.host.ReleaseReservedTask(domain, taskCancel, cause)
			switch {
			case errors.Is(cause, errTaskSessionInterrupted), isTransientCommandError(cause):
				e.completeStart(request, nil)
			default:
				ack.Status, ack.Reason = controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, cause.Error()
				e.completeStart(request, ack)
			}
			return
		}
		e.completeStart(request, ack)
	}); err != nil {
		e.host.ReleaseReservedTask(domain, taskCancel, err)
		ack.Status, ack.Reason = controlpb.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, err.Error()
		e.completeStart(request, ack)
	}
}

func (e *Executor) completeStart(request commandRequest, ack *controlpb.CommandAck) {
	if ack != nil {
		// Publish the exact outcome before making the command non-inflight; a
		// concurrently replayed delivery can then only replay this outcome.
		e.finish(request, ack)
		e.mu.Lock()
		delete(e.deferred, request.cmd.CommandId)
		delete(e.inflight, request.cmd.CommandId)
		e.mu.Unlock()
		return
	}
	e.mu.Lock()
	retry := e.deferred[request.cmd.CommandId]
	delete(e.deferred, request.cmd.CommandId)
	delete(e.inflight, request.cmd.CommandId)
	e.mu.Unlock()
	if retry.cmd != nil {
		select {
		case e.queue <- retry:
		case <-retry.ctx.Done():
		}
	}
}

func (e *Executor) finish(request commandRequest, ack *controlpb.CommandAck) {
	if ack == nil {
		return
	}
	shutdown := request.cmd.GetShutdown() != nil && ack.Status == controlpb.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED
	if shutdown {
		e.host.PrepareShutdown(request.cmd.CommandId, request.cmd.Sequence)
	}
	if err := e.outcomes.Record(ack); err != nil {
		stresslog.Error("[AGENT] 命令结果进入待确认队列失败", zap.String("commandID", ack.CommandId), zap.Error(err))
		e.host.CancelControlPlane()
		return
	}
}

// Outcomes 返回命令结果的待确认 outbox，供控制会话读取快照、接收通知与确认。
func (e *Executor) Outcomes() *OutcomeOutbox { return e.outcomes }

func isTransientCommandError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.Canceled, codes.DeadlineExceeded, codes.Unavailable:
		return true
	default:
		return false
	}
}
