package engine

import (
	"context"
	"errors"
	flowdef "stressbot/flow"
	"testing"
)

func newWeightedExecutor(options []flowdef.WeightedOption, handler *executorActionTestHandler) *Executor {
	return NewExecutor(&flowdef.TaskFlow{
		Nodes: map[string]*flowdef.Node{
			"pick": {Type: flowdef.NodeWeighted, Options: options},
			"a":    {Type: flowdef.NodeAction, Action: "a", DelayMs: -1},
			"b":    {Type: flowdef.NodeAction, Action: "cleanup", DelayMs: -1},
		},
		Actions: map[string]*flowdef.ActionDef{
			"a":       {Name: "a", Pattern: flowdef.PatternClearState},
			"cleanup": {Name: "cleanup", Pattern: flowdef.PatternClearState},
		},
	}, handler, "test", -1)
}

// 权重之和 ≤ 0 时没有任何分支可选：这只可能是配置写错（全 0 / 全负），
// 静默返回 nil 会让整条支路在压测里凭空消失，必须 fail loud。
func TestExecutorWeightedZeroTotalFailsLoud(t *testing.T) {
	for name, options := range map[string][]flowdef.WeightedOption{
		"全零权重": {{Node: "a", Weight: 0}, {Node: "b", Weight: 0}},
		"全负权重": {{Node: "a", Weight: -1}, {Node: "b", Weight: -3}},
	} {
		t.Run(name, func(t *testing.T) {
			handler := &executorActionTestHandler{}
			ex := newWeightedExecutor(options, handler)

			err := ex.executeNode(context.Background(), "pick")
			if !errors.Is(err, ErrWeightedNoBranch) {
				t.Fatalf("err=%v，期望 ErrWeightedNoBranch", err)
			}
			if handler.actionCalls != 0 {
				t.Fatalf("无分支可选时不应执行任何动作，实际 actionCalls=%d", handler.actionCalls)
			}
		})
	}
}

// 正权重必然命中某一路：负权重按 0 处理后不参与抽样，权重为 0 的选项永不被选中。
func TestExecutorWeightedAlwaysPicksPositiveWeightOption(t *testing.T) {
	handler := &executorActionTestHandler{}
	ex := newWeightedExecutor([]flowdef.WeightedOption{
		{Node: "a", Weight: 3},
		{Node: "b", Weight: 0},
		{Node: "b", Weight: -5},
	}, handler)

	for range 64 {
		if err := ex.executeNode(context.Background(), "pick"); err != nil {
			t.Fatalf("weighted 执行失败: %v", err)
		}
	}
	if got := countActionCalls(handler.actionNames, "a"); got != 64 {
		t.Fatalf("唯一正权重选项应每次命中，实际 a calls=%d", got)
	}
	if got := countActionCalls(handler.actionNames, "cleanup"); got != 0 {
		t.Fatalf("零/负权重选项不应被选中，实际 cleanup calls=%d", got)
	}
}

// 无 options 的 weighted 节点保持既有语义：什么都不做，返回 nil（不是配置错误面）。
func TestExecutorWeightedNoOptionsReturnsNil(t *testing.T) {
	handler := &executorActionTestHandler{}
	ex := newWeightedExecutor(nil, handler)

	if err := ex.executeNode(context.Background(), "pick"); err != nil {
		t.Fatalf("无 options 应返回 nil，实际 %v", err)
	}
}
