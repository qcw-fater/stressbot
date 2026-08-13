package engine

import (
	"errors"
)

// 流程配置错误哨兵。这些是 flow.json 配置错误，不是运行时动作错误。
var (
	ErrNodeNotFound    = errors.New("节点不存在")
	ErrUnknownNodeType = errors.New("未知节点类型")
	ErrActionNotFound  = errors.New("动作不存在")
)
