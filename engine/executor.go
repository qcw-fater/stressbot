package engine

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// DefaultNodeDelayMs 动作/布尔叶节点执行完后的默认延迟（毫秒）。
// 与旧 Robot 工具保持一致（Robot/agent/execute.go DefaultDelayMs = 1000），
// 用于控制压测节点的节奏。节点可通过 delayMs 字段覆盖：
//   - delayMs > 0: 使用配置值
//   - delayMs == 0: 使用 DefaultNodeDelayMs
//   - delayMs < 0: 禁用延迟
const DefaultNodeDelayMs = 1000

// Executor 流程执行器。
// 遍历 TaskFlow 中的节点图，根据节点类型执行对应逻辑。
// 每个 Robot 拥有独立的 Executor 实例。
type Executor struct {
	flow    *TaskFlow
	handler ActionHandler // 动作执行委托
}

// ActionHandler 动作执行委托接口。
// 由调用方（Robot）实现，负责具体的网络请求、Lua 脚本执行等。
type ActionHandler interface {
	// ExecuteAction 执行声明式动作
	ExecuteAction(actionDef *ActionDef) error
	// ExecuteBoolean 执行条件判断
	ExecuteBoolean(expression string) bool
	// RegisterListen 注册持久化监听
	RegisterListen(refs []ListenRef) error
	// OnNodeError 节点执行出错时的回调
	OnNodeError(node *Node, err error)
}

// NewExecutor 创建流程执行器
func NewExecutor(flow *TaskFlow, handler ActionHandler) *Executor {
	return &Executor{
		flow:    flow,
		handler: handler,
	}
}

// GetFlow 返回流程图定义
func (e *Executor) GetFlow() *TaskFlow {
	return e.flow
}

// Run 从起始节点开始执行流程。
// 阻塞直到流程结束或 context 取消。
func (e *Executor) Run(ctx context.Context) error {
	startNode, ok := e.flow.GetNode(e.flow.StartNode)
	if !ok {
		return fmt.Errorf("起始节点不存在: %s", e.flow.StartNode)
	}

	return e.executeNode(ctx, startNode)
}

// executeNode 执行单个节点
func (e *Executor) executeNode(ctx context.Context, node *Node) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	stresslog.Debug("[ENGINE] 执行节点", zap.String("id", node.ID), zap.String("type", node.Type))

	switch node.Type {
	case "start":
		return e.executeStart(ctx, node)
	case "sequence":
		return e.executeSequence(ctx, node)
	case "action":
		return e.executeAction(ctx, node)
	case "loop":
		return e.executeLoop(ctx, node)
	case "boolean":
		return e.executeBoolean(ctx, node)
	case "weighted":
		return e.executeWeighted(ctx, node)
	case "wait":
		return e.executeWait(ctx, node)
	default:
		return fmt.Errorf("未知节点类型: %s (node=%s)", node.Type, node.ID)
	}
}

// executeStart 起始节点：委托 executeSequence 顺序执行所有 next 节点
func (e *Executor) executeStart(ctx context.Context, node *Node) error {
	return e.executeSequence(ctx, node)
}

// executeSequence 顺序节点：按顺序依次执行所有 next 节点
func (e *Executor) executeSequence(ctx context.Context, node *Node) error {
	for _, nn := range node.Next {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		child, ok := e.flow.GetNode(nn.Node)
		if !ok {
			return fmt.Errorf("节点不存在: %s", nn.Node)
		}
		if err := e.executeNode(ctx, child); err != nil {
			return err
		}
	}
	return nil
}

// executeAction 动作节点：执行指定 action。
// 先执行动作（创建连接），再注册监听回调。
func (e *Executor) executeAction(ctx context.Context, node *Node) error {
	if node.Action == "" {
		return nil
	}

	actionDef, ok := e.flow.GetAction(node.Action)
	if !ok {
		return fmt.Errorf("动作不存在: %s (node=%s)", node.Action, node.ID)
	}

	// 执行动作
	err := e.handler.ExecuteAction(actionDef)
	if err != nil {
		e.handler.OnNodeError(node, err)
		if node.BreakOff {
			return fmt.Errorf("动作执行失败: %s (node=%s): %w", node.Action, node.ID, err)
		}
		// 非中断模式，记录错误但继续
		stresslog.Warn("[ENGINE] 动作执行失败但继续",
			zap.String("action", node.Action),
			zap.String("node", node.ID),
			zap.Error(err),
		)
		return nil
	}

	// 如果节点有监听回调，在动作执行后注册（连接已在动作中创建）
	if len(node.ListenCallbacks) > 0 {
		if err := e.handler.RegisterListen(node.ListenCallbacks); err != nil {
			return fmt.Errorf("注册监听失败: %w", err)
		}
	}

	// 节点级默认延迟（对齐旧 Robot 工具 delaySleep）
	nodeDelay(ctx, node)
	return nil
}

// nodeDelay 执行节点级默认延迟（1 秒，与旧 Robot 工具一致）。
// - node.DelayMs > 0: 使用配置值
// - node.DelayMs == 0: 使用 DefaultNodeDelayMs
// - node.DelayMs < 0: 禁用延迟
func nodeDelay(ctx context.Context, node *Node) {
	ms := node.DelayMs
	if ms < 0 {
		return
	}
	if ms == 0 {
		ms = DefaultNodeDelayMs
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(ms) * time.Millisecond):
	}
}

// executeLoop 循环节点：重复执行 next 节点
func (e *Executor) executeLoop(ctx context.Context, node *Node) error {
	count := node.LoopCount
	for i := 0; count < 0 || i < count; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.executeNextList(ctx, node.Next); err != nil {
			return err
		}
	}
	return nil
}

// executeBoolean 条件分支节点
// 兼容两种写法：
//   - 新式: { condition: "lua:xxx.lua", trueNext: "...", falseNext: "..." }
//   - 旧式: { action: "actionName"/"lua:xxx.lua", trueBranch: "...", falseBranch: "..." }
func (e *Executor) executeBoolean(ctx context.Context, node *Node) error {
	expr := node.Condition
	if expr == "" {
		expr = node.Action
	}
	result := e.handler.ExecuteBoolean(expr)

	// 节点级默认延迟（对齐旧 Robot 工具 delaySleep）
	nodeDelay(ctx, node)

	var targetID string
	if result {
		targetID = node.TrueNext
	} else {
		targetID = node.FalseNext
	}

	if targetID == "" {
		return nil
	}

	child, ok := e.flow.GetNode(targetID)
	if !ok {
		return fmt.Errorf("条件分支目标节点不存在: %s", targetID)
	}
	return e.executeNode(ctx, child)
}

// executeWeighted 加权随机节点：按权重随机选择一个 next 节点执行
func (e *Executor) executeWeighted(ctx context.Context, node *Node) error {
	if len(node.Next) == 0 {
		return nil
	}

	total := 0
	for _, nn := range node.Next {
		total += nn.Weight
	}
	if total == 0 {
		return nil
	}

	r := rand.Intn(total)
	cumulative := 0
	for _, nn := range node.Next {
		cumulative += nn.Weight
		if r < cumulative {
			child, ok := e.flow.GetNode(nn.Node)
			if !ok {
				return fmt.Errorf("加权节点不存在: %s", nn.Node)
			}
			return e.executeNode(ctx, child)
		}
	}

	// 不会到达，但保险起见
	last := node.Next[len(node.Next)-1]
	child, ok := e.flow.GetNode(last.Node)
	if !ok {
		return fmt.Errorf("加权节点不存在: %s", last.Node)
	}
	return e.executeNode(ctx, child)
}

// executeWait 等待节点：暂停指定时间
func (e *Executor) executeWait(ctx context.Context, node *Node) error {
	d := time.Duration(node.WaitSeconds * float64(time.Second))
	if d <= 0 {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// executeNextList 执行 next 节点列表（并行）
func (e *Executor) executeNextList(ctx context.Context, nextList []NextNode) error {
	if len(nextList) == 0 {
		return nil
	}
	if len(nextList) == 1 {
		child, ok := e.flow.GetNode(nextList[0].Node)
		if !ok {
			return fmt.Errorf("节点不存在: %s", nextList[0].Node)
		}
		return e.executeNode(ctx, child)
	}

	// 多个 next 并行执行
	errCh := make(chan error, len(nextList))
	for _, nn := range nextList {
		nn := nn
		go func() {
			child, ok := e.flow.GetNode(nn.Node)
			if !ok {
				errCh <- fmt.Errorf("节点不存在: %s", nn.Node)
				return
			}
			errCh <- e.executeNode(ctx, child)
		}()
	}

	// 等待所有完成，返回第一个错误
	var firstErr error
	for i := 0; i < len(nextList); i++ {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
