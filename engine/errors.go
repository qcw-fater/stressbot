package engine

import (
	"errors"
	"fmt"

	"stressbot/errcode"
)

// 流程配置错误哨兵。这些是 flow.json 配置错误，不是运行时动作错误。
var (
	ErrNodeNotFound    = errors.New("节点不存在")
	ErrUnknownNodeType = errors.New("未知节点类型")
	ErrActionNotFound  = errors.New("动作不存在")
)

// ActionError 携带错误码的结构化错误。单一 code 唯一标识（< 100 框架 / ≥ 100 业务）。
type ActionError struct {
	Code   errcode.ErrorCode
	Detail string
	cause  error
}

// NewActionError 创建结构化错误（框架码与业务码统一入口）。
// 可选 cause 参数用于包装下层 error（如 factory.Create 失败）。
func NewActionError(code errcode.ErrorCode, detail string, cause ...error) *ActionError {
	e := &ActionError{Code: code, Detail: detail}
	if len(cause) > 0 {
		e.cause = cause[0]
	}
	return e
}

// Error 格式：[1] service=logic: cause 或 [1004] desc: route=CreateTeam。
func (e *ActionError) Error() string {
	s := fmt.Sprintf("[%d]", e.Code)
	if e.Detail != "" {
		s += " " + e.Detail
	}
	if e.cause != nil {
		s += ": " + e.cause.Error()
	}
	return s
}

// Unwrap 返回被包装的下层错误，支持 errors.Is 链式判断。
func (e *ActionError) Unwrap() error { return e.cause }

// ErrorCode 返回数值错误码，供 monitor 通过接口提取。
func (e *ActionError) ErrorCode() uint64 { return uint64(e.Code) }

// ErrorDetail 返回错误上下文描述，供 monitor 通过接口提取。
func (e *ActionError) ErrorDetail() string { return e.Detail }
