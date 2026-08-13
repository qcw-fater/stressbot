package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	flowdef "stressbot/flow"
	"strings"
	"time"

	"stressbot/errcode"
	"stressbot/internal/stresslog"
	"stressbot/monitor"

	"go.uber.org/zap"
)

// errBreak / errContinue 是循环控制的内部信号，通过 error 冒泡直到被 executeLoop 捕获。
// errSkip 是 onError.strategy=skip 的内部信号：被 sequence/loop/boolean/switch/weighted 捕获后，
// 视为当前分支完成，不继续传播为失败。
var (
	errBreak                = errors.New("break")
	errContinue             = errors.New("continue")
	errSkip                 = errors.New("skip")
	errConditionNotPrepared = errors.New("条件表达式尚未准备")
)

// Executor 流程执行器，每个 Robot 持有一个独立实例。
type Executor struct {
	flow           *flowdef.TaskFlow
	handler        ActionHandler
	defaultDelayMs int    // 解析自 flow.DefaultDelayMs，初始化后只读
	caller         string // 调用方标识，用于日志追踪
}

// ActionHandler 动作执行委托接口。
// 由 Robot 层实现，负责具体的网络请求、Lua 脚本执行、条件判断和推送监听注册。
type ActionHandler interface {
	// ExecuteAction 执行声明式动作或 Lua 脚本，返回 nil 表示成功。
	// ctx 来自当前流程执行上下文，供声明式 / Lua 动作外壳共享同一取消语义。
	ExecuteAction(ctx context.Context, actionDef *flowdef.ActionDef) error
	// ExecuteCondition 执行加载期已经准备的 state 或 Lua 条件。
	ExecuteCondition(condition *flowdef.CompiledCondition) bool
	// RegisterListen 批量注册持久化推送监听（注册本身不阻塞流程）。推送到达后的处理分两路，
	// 均不在网络 pump goroutine 内碰业务 LState：① script 回调——pump 只把回调**投递到 Robot
	// 任务队列**，由执行器 goroutine 在 await / 等待窗口的 select 内就地**串行**执行；② 声明式
	// store——pump 直接写线程安全的 state.Store（纯 Go，无 LState）。
	RegisterListen(refs []flowdef.ListenRef) error
	// CooperativeSleep 协作式休眠 d（节点延迟 / wait 节点用）。与裸 time.After 的区别：
	// 等待期间持续 drain 任务队列（跑 listen 回调等），不让「会等待」的点饿死推送回调——
	// 这是 actor 运行时「任何阻塞点都不裸阻塞」的统一约束。d<=0 立即返回。
	// ctx 取消时返回 ctx.Err()，供 wait 节点向上传播取消。
	CooperativeSleep(ctx context.Context, d time.Duration) error
}

// NewExecutor 创建流程执行器。
// caller 用于日志中标识调用方（如机器人账号），便于追踪问题。
func NewExecutor(flow *flowdef.TaskFlow, handler ActionHandler, caller string) *Executor {
	// defaultDelayMs 兜底：flow 未配置（0）时按引擎默认 1000ms，兑现 DefaultDelayMs 字段注释承诺的
	// 「0=引擎默认(1000ms)」。保证漏配的流程每个 action 节点仍有 drain 窗口（listen 回调靠节点延迟
	// drain，不在节点入口单独 drain）。
	defaultDelayMs := flow.DefaultDelayMs
	if defaultDelayMs == 0 {
		defaultDelayMs = 1000
	}
	return &Executor{
		flow:           flow,
		handler:        handler,
		defaultDelayMs: defaultDelayMs,
		caller:         caller,
	}
}

// Flow 返回流程图定义
func (e *Executor) Flow() *flowdef.TaskFlow {
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

	// 节点入口不 drain 任务队列——listen 回调的 drain 收敛到空闲窗口（action 节点的 nodeDelay →
	// CooperativeSleep、wait 节点、Lua await），由调度器在 wait 的 select 内就地处理。
	node, ok := e.flow.Nodes[nodeID]
	if !ok {
		return fmt.Errorf("%w: %s (caller=%s)", ErrNodeNotFound, nodeID, e.caller)
	}

	stresslog.Debug("[ENGINE] 执行节点", zap.String("caller", e.caller), zap.String("node", nodeID), zap.String("type", node.Type))

	switch node.Type {
	case flowdef.NodeSequence:
		return e.executeSequence(ctx, node)
	case flowdef.NodeAction:
		return e.executeAction(ctx, node)
	case flowdef.NodeLoop:
		return e.executeLoop(ctx, node)
	case flowdef.NodeBoolean:
		return e.executeBoolean(ctx, node)
	case flowdef.NodeSwitch:
		return e.executeSwitch(ctx, node)
	case flowdef.NodeWeighted:
		return e.executeWeighted(ctx, node)
	case flowdef.NodeWait:
		return e.executeWait(ctx, node)
	case flowdef.NodeBreak:
		return errBreak
	case flowdef.NodeContinue:
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
func (e *Executor) executeSequence(ctx context.Context, node *flowdef.Node) error {
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
//   - 控制类节点（boolean/switch/weighted/loop/wait/break/continue）本身不算 action，无样本概念
func (e *Executor) reportRemainingCanceled(remaining []string) {
	mc := monitor.Global()
	if mc == nil {
		return
	}
	for _, id := range remaining {
		n, ok := e.flow.Nodes[id]
		if !ok || n.Type != flowdef.NodeAction || n.Action == "" {
			continue
		}
		mc.RecordPendingCanceled(n.Action)
	}
}

// executeLoop 循环节点：循环执行单个 body 节点。
// 支持次数控制、前置条件、后置条件、break/continue 信号捕获。
func (e *Executor) executeLoop(ctx context.Context, node *flowdef.Node) error {
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
			if !e.evaluatePreparedCondition(node.Condition, node.PreparedCondition()) {
				stresslog.Debug("[ENGINE] loop 前置条件不满足，退出循环",
					zap.String("caller", e.caller), zap.Int("iteration", i))
				break
			}
		}

		// 后置退出条件检查（对应 Go: do { } while !breakCondition）。
		// 提成闭包：continue 分支也必须评估，否则依赖 breakCondition 退出的无限 loop
		// 一旦命中 continue 就跳过退出判断 → 永不退出。
		checkBreak := func() bool {
			return node.BreakCondition != "" &&
				e.evaluatePreparedCondition(node.BreakCondition, node.PreparedBreakCondition())
		}

		// 执行循环体（单个节点）
		err := e.executeNode(ctx, node.Body)

		if errors.Is(err, errBreak) || errors.Is(err, errSkip) {
			break
		}
		if errors.Is(err, errContinue) {
			// continue 语义对齐 do-while：跳过本轮剩余逻辑，但仍要判断后置退出条件。
			if checkBreak() {
				break
			}
			continue
		}
		if err != nil {
			return err
		}

		if checkBreak() {
			break
		}
	}
	return nil
}

// executeAction 动作节点：执行指定 action。
func (e *Executor) executeAction(ctx context.Context, node *flowdef.Node) error {
	if node.Action == "" {
		return nil
	}

	actionDef, ok := e.flow.Action(node.Action)
	if !ok {
		return fmt.Errorf("%w: %s", ErrActionNotFound, node.Action)
	}

	retriesUsed := 0
	maxRetries := onErrorMaxRetries(node)
	for {
		err := e.handler.ExecuteAction(ctx, actionDef)
		if err == nil {
			return e.finishActionSuccess(ctx, node, actionDef)
		}

		if cancelErr := normalizeActionCancel(err); cancelErr != nil {
			if errors.Is(cancelErr, context.Canceled) && stresslog.DebugEnabled() {
				stresslog.Debug("[ENGINE] 动作在停止阶段被取消",
					zap.String("caller", e.caller), zap.String("action", node.Action), zap.Error(err))
			}
			return cancelErr
		}

		if isIgnoredActionError(node, err) {
			return e.finishActionAccepted(ctx, node, actionDef, err)
		}

		e.logActionFailure("[ENGINE] 动作执行失败", node, actionDef, retriesUsed, maxRetries, err)
		if err := e.executeOnErrorHandler(ctx, node); err != nil {
			if errors.Is(err, errSkip) {
				// handler 内部的 skip 表示 handler 子流程正常收束，不作为原 action 错误继续传播。
			} else {
				return err
			}
		}

		if retriesUsed < maxRetries {
			retriesUsed++
			e.logActionRetry(node, actionDef, retriesUsed, maxRetries, err)
			if err := e.retryDelay(ctx, node.OnError.Retry); err != nil {
				return err
			}
			continue
		}

		// 非取消类失败最终收束前执行节点延迟，避免错误路径下节点推进速度超过配置预期。
		if err := e.nodeDelay(ctx, node); err != nil {
			return err
		}
		return applyOnErrorStrategy(onErrorStrategy(node), func() error {
			return errcode.NewActionError(errcode.ErrExecFailed, "action="+node.Action, err)
		})
	}
}

func (e *Executor) finishActionSuccess(ctx context.Context, node *flowdef.Node, actionDef *flowdef.ActionDef) error {
	if err := e.registerActionListens(ctx, node, actionDef); err != nil {
		return err
	}
	if stresslog.DebugEnabled() {
		stresslog.Debug("[ENGINE] 执行动作成功",
			zap.String("caller", e.caller),
			zap.String("action", node.Action),
			zap.String("pattern", actionDef.Pattern),
			zap.Int("listens", len(node.ListenRefs)))
	}
	if err := e.nodeDelay(ctx, node); err != nil {
		return err
	}
	return nil
}

func (e *Executor) finishActionAccepted(ctx context.Context, node *flowdef.Node, actionDef *flowdef.ActionDef, err error) error {
	fields := []zap.Field{
		zap.String("caller", e.caller),
		zap.String("action", node.Action),
		zap.String("pattern", actionDef.Pattern),
		zap.Int("ignoreCodeCount", len(node.OnError.IgnoreCodes)),
		zap.Error(err),
	}
	fields = append(fields, actionErrorLogFields(err)...)
	stresslog.Warn("[ENGINE] 动作错误码已忽略，流程继续", fields...)
	if listenErr := e.registerActionListens(ctx, node, actionDef); listenErr != nil {
		return listenErr
	}
	if err := e.nodeDelay(ctx, node); err != nil {
		return err
	}
	return nil
}

func (e *Executor) registerActionListens(ctx context.Context, node *flowdef.Node, actionDef *flowdef.ActionDef) error {
	if len(node.ListenRefs) == 0 {
		return nil
	}
	if err := e.handler.RegisterListen(node.ListenRefs); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		e.logActionFailure("[ENGINE] 注册监听失败", node, actionDef, 0, onErrorMaxRetries(node), err, zap.Int("listenCount", len(node.ListenRefs)))
		// 动作本体已成功但监听注册失败，不重试原 action，按最终错误路径执行节点延迟。
		if err := e.nodeDelay(ctx, node); err != nil {
			return err
		}
		return applyOnErrorStrategy(onErrorStrategy(node), func() error {
			return errcode.NewActionError(errcode.ErrListenRegister, "action="+node.Action, err)
		})
	}
	return nil
}

func (e *Executor) executeOnErrorHandler(ctx context.Context, node *flowdef.Node) error {
	if node.OnError == nil || node.OnError.Handler == "" {
		return nil
	}
	return e.executeNode(ctx, node.OnError.Handler)
}

func (e *Executor) retryDelay(ctx context.Context, retry *flowdef.RetryDef) error {
	if retry == nil || retry.RetryDelayMs <= 0 {
		return nil
	}
	return e.handler.CooperativeSleep(ctx, time.Duration(retry.RetryDelayMs)*time.Millisecond)
}

func actionErrorLogFields(err error) []zap.Field {
	actionErr, ok := errors.AsType[*errcode.ActionError](err)
	if !ok || actionErr == nil {
		return nil
	}
	return []zap.Field{
		zap.Uint64("errorCode", uint64(actionErr.Code)),
		zap.String("errorDetail", actionErr.Detail),
	}
}

func (e *Executor) logActionFailure(msg string, node *flowdef.Node, actionDef *flowdef.ActionDef, retriesUsed, maxRetries int, err error, extraFields ...zap.Field) {
	fields := []zap.Field{
		zap.String("caller", e.caller),
		zap.String("action", node.Action),
		zap.String("pattern", actionDef.Pattern),
		zap.String("onErrorStrategy", onErrorStrategy(node)),
		zap.String("handler", onErrorHandler(node)),
		zap.Int("retryUsed", retriesUsed),
		zap.Int("maxRetries", maxRetries),
		zap.Error(err),
	}
	fields = append(fields, extraFields...)
	fields = append(fields, actionErrorLogFields(err)...)
	if onErrorStrategy(node) == flowdef.StrategyAbort {
		stresslog.Error(msg, fields...)
		return
	}
	stresslog.Warn(msg, fields...)
}

func (e *Executor) logActionRetry(node *flowdef.Node, actionDef *flowdef.ActionDef, retriesUsed, maxRetries int, err error) {
	fields := []zap.Field{
		zap.String("caller", e.caller),
		zap.String("action", node.Action),
		zap.String("pattern", actionDef.Pattern),
		zap.String("handler", onErrorHandler(node)),
		zap.Int("retryUsed", retriesUsed),
		zap.Int("maxRetries", maxRetries),
		zap.Int("retryDelayMs", node.OnError.Retry.RetryDelayMs),
		zap.Error(err),
	}
	fields = append(fields, actionErrorLogFields(err)...)
	stresslog.Warn("[ENGINE] 动作失败后准备重试", fields...)
}

func onErrorHandler(node *flowdef.Node) string {
	if node == nil || node.OnError == nil {
		return ""
	}
	return node.OnError.Handler
}

func onErrorStrategy(node *flowdef.Node) string {
	if node == nil || node.OnError == nil || node.OnError.Strategy == "" {
		return flowdef.StrategyResume
	}
	return node.OnError.Strategy
}

func onErrorMaxRetries(node *flowdef.Node) int {
	if node == nil || node.OnError == nil || node.OnError.Retry == nil || node.OnError.Retry.MaxRetries <= 0 {
		return 0
	}
	return node.OnError.Retry.MaxRetries
}

func actionErrorCode(err error) (errcode.ErrorCode, bool) {
	actionErr, ok := errors.AsType[*errcode.ActionError](err)
	if !ok || actionErr == nil {
		return 0, false
	}
	return actionErr.Code, true
}

func isIgnoredActionError(node *flowdef.Node, err error) bool {
	if node == nil || node.OnError == nil || len(node.OnError.IgnoreCodes) == 0 {
		return false
	}
	code, ok := actionErrorCode(err)
	if !ok {
		return false
	}
	return slices.Contains(node.OnError.IgnoreCodes, code)
}

func normalizeActionCancel(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if actionErr, ok := errors.AsType[*errcode.ActionError](err); ok && actionErr.Code == errcode.ErrActionCanceled {
		return context.Canceled
	}
	return nil
}

// applyOnErrorStrategy 根据 onError.strategy 配置决定如何处理错误。
// abortErr 函数在 strategy 为 "abort" 时调用，用于构造带有上下文信息的 ActionError。
func applyOnErrorStrategy(strategy string, abortErr func() error) error {
	switch strategy {
	case flowdef.StrategyAbort:
		return abortErr()
	case flowdef.StrategySkip:
		return errSkip
	default:
		return nil
	}
}

// executeBoolean 条件分支节点。
func (e *Executor) executeBoolean(ctx context.Context, node *flowdef.Node) error {
	result := e.evaluatePreparedCondition(node.Condition, node.PreparedCondition())

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

// executeSwitch 多条件分支节点：按顺序执行第一条命中的 case。
// 日志只在"决策点"打（命中哪条 / 走默认 / 静默结束），不逐 case 打，避免噪音：
//   - 命中 case / 走 default / 命中空 next：debug（正常控制流决策）
//   - 无 case 命中且无 default：warn（分支静默结束，压测里易藏 bug，默认可见；补 default 可消除）
//
// 子流程内的失败由 executeAction 自行 warn，此处不重复。
func (e *Executor) executeSwitch(ctx context.Context, node *flowdef.Node) error {
	for i := range node.Cases {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conditionCase := &node.Cases[i]
		if !e.evaluatePreparedCondition(conditionCase.Condition, conditionCase.PreparedCondition()) {
			continue
		}
		// 命中第 i 条 case
		if conditionCase.Next == "" {
			stresslog.Debug("[ENGINE] switch 命中 case 但 next 为空，结束分支",
				zap.String("caller", e.caller), zap.Int("case", i), zap.String("condition", conditionCase.Condition))
			return nil
		}
		stresslog.Debug("[ENGINE] switch 命中 case",
			zap.String("caller", e.caller), zap.Int("case", i),
			zap.String("condition", conditionCase.Condition), zap.String("next", conditionCase.Next))
		err := e.executeNode(ctx, conditionCase.Next)
		if errors.Is(err, errSkip) {
			return nil
		}
		return err
	}

	// 无 case 命中
	if node.DefaultNext == "" {
		// 未配置 default 且全部 case 未命中：分支静默结束（机器人此处什么都没做）。
		// 这是压测里最易藏 bug 的场景（路由落空），升 warn 默认可见；若属预期 fall-through，补一个 default 即可消除。
		stresslog.Warn("[ENGINE] switch 无 case 命中且未配置 default，分支静默结束",
			zap.String("caller", e.caller), zap.Int("cases", len(node.Cases)))
		return nil
	}
	stresslog.Debug("[ENGINE] switch 无 case 命中，执行默认分支",
		zap.String("caller", e.caller), zap.Int("cases", len(node.Cases)), zap.String("next", node.DefaultNext))
	err := e.executeNode(ctx, node.DefaultNext)
	if errors.Is(err, errSkip) {
		return nil
	}
	return err
}

func (e *Executor) evaluatePreparedCondition(source string, condition *flowdef.CompiledCondition) bool {
	if strings.TrimSpace(source) == "" {
		return true
	}
	if condition == nil {
		stresslog.Error("[ENGINE] 条件表达式尚未准备",
			zap.String("caller", e.caller), zap.String("condition", source),
			zap.Error(errConditionNotPrepared))
		return false
	}
	return e.handler.ExecuteCondition(condition)
}

// executeWeighted 加权随机节点：按权重随机选择一个 option 执行。
func (e *Executor) executeWeighted(ctx context.Context, node *flowdef.Node) error {
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
func (e *Executor) executeWait(ctx context.Context, node *flowdef.Node) error {
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
	} else if node.WaitMs > 0 {
		ms = node.WaitMs
	} else {
		if node.WaitMs < 0 {
			stresslog.Warn("[ENGINE] wait 节点 waitMs < 0，跳过", zap.Int("waitMs", node.WaitMs))
		}
	}

	if ms > 0 {
		// wait 节点同样走协作式休眠：等待期间 drain 任务队列；ctx 取消时向上传播。
		if err := e.handler.CooperativeSleep(ctx, time.Duration(ms)*time.Millisecond); err != nil {
			return err
		}
	}
	if node.Then == "" {
		return nil
	}
	return e.executeNode(ctx, node.Then)
}

// nodeDelay 执行节点级延迟，仅在 action 节点执行完后调用。
// 延迟值优先级：node.DelayMs > e.defaultDelayMs。
// 使用协作式休眠，等待期间继续 drain 任务队列；ctx 取消时向上传播取消。
func (e *Executor) nodeDelay(ctx context.Context, node *flowdef.Node) error {
	ms := node.DelayMs
	if ms == 0 {
		ms = e.defaultDelayMs
	}
	if ms < 0 {
		return nil
	}
	return e.handler.CooperativeSleep(ctx, time.Duration(ms)*time.Millisecond)
}
