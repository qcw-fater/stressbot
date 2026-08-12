package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"stressbot/controlplane/controlv1"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type commandRequest struct {
	ctx    context.Context
	client controlv1.AgentBundleServiceClient
	cmd    *controlv1.Command
}

type CommandExecutor struct {
	agent    *Agent
	cache    *BundleCache
	outcomes *CommandOutcomeOutbox
	queue    chan commandRequest
	mu       sync.Mutex
	inflight map[string]struct{}
	deferred map[string]commandRequest
}

func NewCommandExecutor(agent *Agent, cache *BundleCache) *CommandExecutor {
	return &CommandExecutor{agent: agent, cache: cache, outcomes: NewCommandOutcomeOutbox(4096), queue: make(chan commandRequest, 64),
		inflight: make(map[string]struct{}), deferred: make(map[string]commandRequest)}
}

func (e *CommandExecutor) Start(ctx context.Context) error {
	return utils.GetWorkPool().Submit(func() {
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

func (e *CommandExecutor) Enqueue(ctx context.Context, client controlv1.AgentBundleServiceClient, command *controlv1.Command) error {
	request := commandRequest{ctx: ctx, client: client, cmd: command}
	select {
	case e.queue <- request:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *CommandExecutor) execute(request commandRequest) *controlv1.CommandAck {
	command := request.cmd
	ack := &controlv1.CommandAck{CommandId: command.CommandId, Sequence: command.Sequence, AgentId: e.agent.id,
		TaskId: command.TaskId, Status: controlv1.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED, AcknowledgedAtUnixNano: time.Now().UnixNano()}
	if command.AgentId != e.agent.id {
		ack.Status, ack.Reason = controlv1.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, "命令目标节点不匹配"
		return ack
	}
	if previous := e.outcomes.FindAndReplay(command.CommandId); previous != nil {
		return nil
	}
	if body, ok := command.GetBody().(*controlv1.Command_StartTask); ok {
		e.beginStart(request, ack, body.StartTask)
		return nil
	}
	var err error
	switch body := command.GetBody().(type) {
	case *controlv1.Command_StopTask:
		current, canceled := e.agent.cancelTask(command.TaskId, body.StopTask.Reason)
		if !canceled && current != "" {
			err = fmt.Errorf("当前任务为 %s", current)
		}
	case *controlv1.Command_Shutdown:
		// Process exit is deferred until Admin durably commits this ACK and
		// returns CommandReceipt.
	default:
		err = fmt.Errorf("未知命令类型")
	}
	if err != nil {
		if isTransientCommandError(err) {
			return nil
		}
		ack.Status, ack.Reason = controlv1.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, err.Error()
	}
	return ack
}

func (e *CommandExecutor) beginStart(request commandRequest, ack *controlv1.CommandAck, start *controlv1.StartTask) {
	command := request.cmd
	e.mu.Lock()
	if _, exists := e.inflight[command.CommandId]; exists {
		e.deferred[command.CommandId] = request
		e.mu.Unlock()
		return
	}
	e.inflight[command.CommandId] = struct{}{}
	e.mu.Unlock()

	_, currentTaskID, _ := e.agent.runtimeState()
	if currentTaskID == command.TaskId && currentTaskID != "" {
		ack.Status = controlv1.CommandAckStatus_COMMAND_ACK_STATUS_DUPLICATE
		e.completeStart(request, ack)
		return
	}
	if currentTaskID != "" {
		ack.Status, ack.Reason = controlv1.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, fmt.Sprintf("已有任务运行: %s", currentTaskID)
		e.completeStart(request, ack)
		return
	}
	domain, err := taskAssignmentFromProto(start.GetAssignment(), start.GetBundle())
	if err != nil {
		ack.Status, ack.Reason = controlv1.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, err.Error()
		e.completeStart(request, ack)
		return
	}
	taskCtx, taskCancel, err := e.agent.reserveTask(domain)
	if err != nil {
		ack.Status, ack.Reason = controlv1.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, err.Error()
		e.completeStart(request, ack)
		return
	}
	if err := utils.GetWorkPool().Submit(func() {
		sessionCanceled := make(chan struct{})
		stopSessionCancel := context.AfterFunc(request.ctx, func() {
			taskCancel(errTaskSessionInterrupted)
			close(sessionCanceled)
		})
		domain.BundleDir, err = e.cache.Ensure(taskCtx, request.client, e.agent.id, domain.BundleDigest, domain.BundleSize)
		if !stopSessionCancel() {
			<-sessionCanceled
		}
		if err == nil && context.Cause(taskCtx) == nil {
			err = e.agent.launchReservedTask(taskCtx, taskCancel, domain, utils.GetWorkPool().Submit)
		}
		if err != nil || context.Cause(taskCtx) != nil {
			cause := context.Cause(taskCtx)
			if cause == nil {
				cause = err
			}
			e.agent.releaseReservedTask(domain, taskCancel, cause)
			switch {
			case errors.Is(cause, errTaskSessionInterrupted), isTransientCommandError(cause):
				e.completeStart(request, nil)
			default:
				ack.Status, ack.Reason = controlv1.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, cause.Error()
				e.completeStart(request, ack)
			}
			return
		}
		e.completeStart(request, ack)
	}); err != nil {
		e.agent.releaseReservedTask(domain, taskCancel, err)
		ack.Status, ack.Reason = controlv1.CommandAckStatus_COMMAND_ACK_STATUS_REJECTED, err.Error()
		e.completeStart(request, ack)
	}
}

func (e *CommandExecutor) completeStart(request commandRequest, ack *controlv1.CommandAck) {
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

func (e *CommandExecutor) finish(request commandRequest, ack *controlv1.CommandAck) {
	if ack == nil {
		return
	}
	shutdown := request.cmd.GetShutdown() != nil && ack.Status == controlv1.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED
	if shutdown {
		e.agent.prepareShutdown(request.cmd.CommandId, request.cmd.Sequence)
	}
	if err := e.outcomes.Record(ack); err != nil {
		stresslog.Error("[AGENT] 命令结果进入待确认队列失败", zap.String("commandID", ack.CommandId), zap.Error(err))
		if e.agent.cancel != nil {
			e.agent.cancel()
		}
		return
	}
}

func (e *CommandExecutor) Outcomes() *CommandOutcomeOutbox { return e.outcomes }

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
