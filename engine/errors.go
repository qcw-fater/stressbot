package engine

import (
	"context"
	"errors"

	"stressbot/errcode"
)

// 流程配置错误哨兵。这些是 flow.json 配置错误，不是运行时动作错误。
var (
	ErrNodeNotFound     = errors.New("节点不存在")
	ErrUnknownNodeType  = errors.New("未知节点类型")
	ErrActionNotFound   = errors.New("动作不存在")
	ErrWeightedNoBranch = errors.New("weighted 节点权重之和 ≤ 0，无分支可选")
)

// isCanceledError 判断一个动作错误是否表示「客户端主动取消」（流程 ctx 取消 / 连接被本地关闭）。
// 取消判据全包只此一处：normalizeActionCancel（onError 短路）与 execRequest（是否记
// frustrated 样本）共用，避免两处判据漂移。
//
// 取消与超时必须分得开：两者都是「没等到响应」，但超时是服务端表现，取消是压测端自己
// 停机；混为一谈会让每次停止任务都在监控面记一批超时失败。
func isCanceledError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if actionErr, ok := errors.AsType[*errcode.ActionError](err); ok {
		return actionErr.Code == errcode.ErrActionCanceled
	}
	return false
}
