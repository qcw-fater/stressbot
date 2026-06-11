package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"stressbot/errcode"
	"stressbot/monitor"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// errBreak / errContinue 是循环控制的内部信号，通过 error 冒泡直到被 executeLoop 捕获。
// errSkip 是 skip 错误策略的内部信号：跳出当前所在的 sequence/loop，完成当前节点的执行。
var (
	errBreak    = errors.New("break")
	errContinue = errors.New("continue")
	errSkip     = errors.New("skip")
)

// Executor 流程执行器，每个 Robot 持有一个独立实例。
type Executor struct {
	flow           *TaskFlow
	handler        ActionHandler
	defaultDelayMs int    // 解析自 flow.DefaultDelayMs，初始化后只读
	caller         string // 调用方标识，用于日志追踪
}

// ActionHandler 动作执行委托接口。
// 由 Robot 层实现，负责具体的网络请求、Lua 脚本执行、条件判断和推送监听注册。
type ActionHandler interface {
	// ExecuteAction 执行声明式动作或 Lua 脚本，返回 nil 表示成功。
	ExecuteAction(actionDef *ActionDef) error
	// ExecuteBoolean 对条件表达式求值，返回 true/false。
	// 表达式支持 state: 前缀（从 StateStore 比较）和 lua: 前缀（调用 Lua 脚本）。
	ExecuteBoolean(expression string) bool
	// RegisterListen 批量注册持久化推送监听，回调在后台触发，不阻塞流程。
	RegisterListen(refs []ListenRef) error
}

// NewExecutor 创建流程执行器。
// caller 用于日志中标识调用方（如机器人账号），便于追踪问题。
func NewExecutor(flow *TaskFlow, handler ActionHandler, caller string) *Executor {
	return &Executor{
		flow:           flow,
		handler:        handler,
		defaultDelayMs: flow.DefaultDelayMs,
		caller:         caller,
	}
}

// Flow 返回流程图定义
func (e *Executor) Flow() *TaskFlow {
	return e.flow
}

// Run 从 main 节点开始执行流程（类比编程语言的 main 函数入口）。
// 阻塞直到流程结束或 context 取消。
//
// 退出日志分级：
//   - 正常结束（err == nil）→ debug
//   - ctx 取消（任务停止 / robot.Close）→ debug，这是预期生命周期事件
//     不打 warn 避免 500 robot 停止时 500 条警告刷屏掩盖真实问题
//   - 其它错误 → warn，业务/工具异常需要排查
func (e *Executor) Run(ctx context.Context) error {
	stresslog.Debug("[ENGINE] 开始执行流程", zap.String("caller", e.caller))
	err := e.executeNode(ctx, "main")
	switch {
	case err == nil:
		stresslog.Debug("[ENGINE] 流程正常结束", zap.String("caller", e.caller))
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		stresslog.Debug("[ENGINE] 流程因取消退出", zap.String("caller", e.caller), zap.Error(err))
	default:
		stresslog.Warn("[ENGINE] 流程异常退出", zap.String("caller", e.caller), zap.Error(err))
	}
	return err
}

// executeNode 执行单个节点（按 nodeID 查找）。
func (e *Executor) executeNode(ctx context.Context, nodeID string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	node, ok := e.flow.Nodes[nodeID]
	if !ok {
		return fmt.Errorf("%w: %s (caller=%s)", ErrNodeNotFound, nodeID, e.caller)
	}

	stresslog.Debug("[ENGINE] 执行节点", zap.String("caller", e.caller), zap.String("node", nodeID), zap.String("type", node.Type))

	switch node.Type {
	case NodeSequence:
		return e.executeSequence(ctx, node)
	case NodeAction:
		return e.executeAction(ctx, node)
	case NodeLoop:
		return e.executeLoop(ctx, node)
	case NodeBoolean:
		return e.executeBoolean(ctx, node)
	case NodeWeighted:
		return e.executeWeighted(ctx, node)
	case NodeWait:
		return e.executeWait(ctx, node)
	case NodeBreak:
		return errBreak
	case NodeContinue:
		return errContinue
	default:
		return fmt.Errorf("%w: %s (node=%s, caller=%s)", ErrUnknownNodeType, node.Type, nodeID, e.caller)
	}
}

// executeSequence 顺序节点：按顺序依次执行所有子节点。
// 捕获 errSkip 时跳过剩余子节点（视为本 sequence 正常完成）。
// 透传其他错误和信号（含 errBreak / errContinue）。
//
// ctx 取消处理：当 sequence 因 ctx 取消而中断时，给剩余的 action 子节点补记
// canceled 样本（见 reportRemainingCanceled）。避免出现"上一节点 500 样本、
// 下一节点 481 样本"的监控盲区——19 个 robot 在节点间断流但面板无法呈现。
func (e *Executor) executeSequence(ctx context.Context, node *Node) error {
	for i, childID := range node.Next {
		if ctx.Err() != nil {
			e.reportRemainingCanceled(node.Next[i:])
			return ctx.Err()
		}
		if err := e.executeNode(ctx, childID); err != nil {
			if errors.Is(err, errSkip) {
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				e.reportRemainingCanceled(node.Next[i+1:])
			}
			return err
		}
	}
	return nil
}

// reportRemainingCanceled 给 sequence 中尚未执行的 action 子节点补记 canceled 样本。
//
// 设计背景：robot 在节点 A 完成后、节点 B 调度前因 ctx 取消（如服务端断连）退出时，
// 监控面板会看到"节点 A 500 样本、节点 B 仅 481 样本"的人数对不上现象——19 个 robot
// 在两节点之间"凭空消失"。补记 canceled 让数据连续，告知用户"这些 robot 在 A 之后、
// B 调度前断流"，便于定位握手 / 心跳 / 服务端 RST 时序问题。
//
// 仅遍历当前 sequence 层的 action 子节点（不深入嵌套）：
//   - 嵌套 sequence 内的 action 已在它们各自的 sequence 里被同样处理
//   - 控制类节点（boolean/weighted/loop/wait）本身不算 action，无样本概念
func (e *Executor) reportRemainingCanceled(remaining []string) {
	mc := monitor.Global()
	if mc == nil {
		return
	}
	for _, id := range remaining {
		n, ok := e.flow.Nodes[id]
		if !ok || n.Type != NodeAction || n.Action == "" {
			continue
		}
		// RecordAction 内部 executing-- ，需要先 RecordActionStart 让 executing++ 配对，
		// 避免 executing 计数漂成负数
		mc.RecordActionStart(n.Action)
		mc.RecordAction(n.Action, monitor.ResultCanceled, monitor.ActionTiming{}, 0, 0, 0, nil)
	}
}

// executeLoop 循环节点：循环执行单个 body 节点。
// 支持次数控制、前置条件、后置条件、break/continue 信号捕获。
func (e *Executor) executeLoop(ctx context.Context, node *Node) error {
	if node.LoopCount == 0 {
		stresslog.Debug("[ENGINE] loop 节点 loopCount=0，跳过循环体")
		return nil
	}

	for i := 0; node.LoopCount < 0 || i < node.LoopCount; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// 前置条件检查（对应 Go: for condition { }）
		if node.Condition != "" {
			if !e.handler.ExecuteBoolean(node.Condition) {
				stresslog.Debug("[ENGINE] loop 前置条件不满足，退出循环",
					zap.String("caller", e.caller), zap.Int("iteration", i))
				break
			}
		}

		// 执行循环体（单个节点）
		err := e.executeNode(ctx, node.Body)

		if errors.Is(err, errBreak) || errors.Is(err, errSkip) {
			break
		}
		if errors.Is(err, errContinue) {
			continue
		}
		if err != nil {
			return err
		}

		// 后置条件检查（对应 Go: do { } while !breakCondition）
		if node.BreakCondition != "" {
			if e.handler.ExecuteBoolean(node.BreakCondition) {
				break
			}
		}
	}
	return nil
}

// executeAction 动作节点：执行指定 action。
func (e *Executor) executeAction(ctx context.Context, node *Node) error {
	if node.Action == "" {
		return nil
	}

	actionDef, ok := e.flow.Action(node.Action)
	if !ok {
		return fmt.Errorf("%w: %s", ErrActionNotFound, node.Action)
	}

	err := e.handler.ExecuteAction(actionDef)
	if err != nil {
		// ctx 取消优先：任务级停止不走 errorStrategy，也不执行节点延迟。
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		// ACTION_CANCELED 是"任务停止过程中的副作用"，monitor 已经按 Canceled 归类，
		// 这里不再当真 error 处理：不打 error 日志、不走 errorStrategy，也不执行节点延迟。
		// 历史一次 4 分钟任务 stop 阶段刷出 60+ 条 match_succeed/connect_battle_tcp 假 error。
		if actionErr, ok := errors.AsType[*ActionError](err); ok && actionErr.Code == errcode.ErrActionCanceled {
			stresslog.Debug("[ENGINE] 动作在停止阶段被取消",
				zap.String("caller", e.caller), zap.String("action", node.Action), zap.Error(err))
			return context.Canceled
		}
		stresslog.Error("[ENGINE] 动作执行失败",
			zap.String("caller", e.caller), zap.String("action", node.Action), zap.Error(err))
		// 非取消类失败同样执行节点延迟，避免错误路径下节点推进速度超过配置预期。
		e.nodeDelay(ctx, node)
		return applyErrorStrategy(node.ErrorStrategy, func() error {
			return NewActionError(errcode.ErrExecFailed, "action="+node.Action, err)
		})
	}

	// 注册监听（连接已在动作中创建）
	if len(node.ListenRefs) > 0 {
		if err := e.handler.RegisterListen(node.ListenRefs); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			stresslog.Error("[ENGINE] 注册监听失败",
				zap.String("caller", e.caller), zap.Error(err))
			// 动作本体已成功但监听注册失败，仍按失败动作执行节点延迟。
			e.nodeDelay(ctx, node)
			return applyErrorStrategy(node.ErrorStrategy, func() error {
				return NewActionError(errcode.ErrListenRegister, "action="+node.Action, err)
			})
		}
	}

	stresslog.Info("[ENGINE] 执行动作成功", zap.String("caller", e.caller), zap.String("action", node.Action), zap.Int("listens", len(node.ListenRefs)))
	e.nodeDelay(ctx, node)
	return nil
}

// applyErrorStrategy 根据 errorStrategy 配置决定如何处理错误。
// abortErr 函数在 strategy 为 "abort" 时调用，用于构造带有上下文信息的 ActionError。
func applyErrorStrategy(strategy string, abortErr func() error) error {
	switch strategy {
	case StrategyAbort:
		return abortErr()
	case StrategySkip:
		return errSkip
	default:
		return nil
	}
}

// executeBoolean 条件分支节点。
func (e *Executor) executeBoolean(ctx context.Context, node *Node) error {
	result := e.handler.ExecuteBoolean(node.Condition)

	stresslog.Debug("[ENGINE] boolean 条件判断",
		zap.String("caller", e.caller), zap.Bool("result", result), zap.String("next", func() string {
			if result {
				return node.TrueNext
			}
			return node.FalseNext
		}()))

	var targetID string
	if result {
		targetID = node.TrueNext
	} else {
		targetID = node.FalseNext
	}

	if targetID == "" {
		return nil
	}

	err := e.executeNode(ctx, targetID)
	if errors.Is(err, errSkip) {
		return nil
	}
	return err
}

// executeWeighted 加权随机节点：按权重随机选择一个 option 执行。
func (e *Executor) executeWeighted(ctx context.Context, node *Node) error {
	if len(node.Options) == 0 {
		return nil
	}

	total := 0
	for _, opt := range node.Options {
		w := opt.Weight
		if w < 0 {
			stresslog.Warn("[ENGINE] weighted 选项权重为负，视为 0",
				zap.String("node", opt.Node), zap.Int("weight", w))
			w = 0
		}
		total += w
	}
	if total <= 0 {
		stresslog.Warn("[ENGINE] weighted 所有选项权重之和 ≤ 0，跳过")
		return nil
	}

	r := rand.Intn(total)
	cumulative := 0
	for _, opt := range node.Options {
		w := max(opt.Weight, 0)
		cumulative += w
		if r < cumulative {
			err := e.executeNode(ctx, opt.Node)
			if errors.Is(err, errSkip) {
				return nil
			}
			return err
		}
	}

	err := e.executeNode(ctx, node.Options[len(node.Options)-1].Node)
	if errors.Is(err, errSkip) {
		return nil
	}
	return err
}

// executeWait 等待节点：暂停指定时间。支持固定和随机两种模式。
func (e *Executor) executeWait(ctx context.Context, node *Node) error {
	var ms int

	if node.WaitMin > 0 && node.WaitMax > 0 {
		if node.WaitMin >= node.WaitMax {
			stresslog.Warn("[ENGINE] wait 节点 waitMin >= waitMax，使用 waitMin",
				zap.Int("waitMin", node.WaitMin), zap.Int("waitMax", node.WaitMax))
			ms = node.WaitMin
		} else {
			ms = rand.Intn(node.WaitMax-node.WaitMin+1) + node.WaitMin
		}
	} else if node.WaitMin > 0 || node.WaitMax > 0 {
		stresslog.Warn("[ENGINE] wait 节点 waitMin/waitMax 必须同时 > 0",
			zap.Int("waitMin", node.WaitMin), zap.Int("waitMax", node.WaitMax))
		return nil
	} else if node.WaitMs > 0 {
		ms = node.WaitMs
	} else {
		if node.WaitMs < 0 {
			stresslog.Warn("[ENGINE] wait 节点 waitMs < 0，跳过", zap.Int("waitMs", node.WaitMs))
		}
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return nil
	}
}

// nodeDelay 执行节点级延迟，仅在 action 节点执行完后调用。
// 延迟值优先级：node.DelayMs > e.defaultDelayMs
func (e *Executor) nodeDelay(ctx context.Context, node *Node) {
	ms := node.DelayMs
	if ms == 0 {
		ms = e.defaultDelayMs
	}
	if ms < 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(ms) * time.Millisecond):
	}
}
