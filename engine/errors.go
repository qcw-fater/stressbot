package engine

import (
	"errors"
	"fmt"

	"stressbot/errcode"
)

// ErrTimeout 表示动作超时。
// 被 NewTimeoutError 包装后，通过 errors.Is(err, ErrTimeout) 识别。
var ErrTimeout = errors.New("action timeout")

// 流程配置错误哨兵。这些是 flow.json 配置错误，不是运行时动作错误。
var (
	ErrNodeNotFound   = errors.New("节点不存在")
	ErrUnknownNodeType = errors.New("未知节点类型")
	ErrActionNotFound  = errors.New("动作不存在")
)

// ActionError 携带错误码与来源类别的结构化错误。
// (Kind, Code) 二元组唯一标识一类错误：
//   - 框架错误：Kind=KindFramework, Code=errcode.Err*
//   - 服务端错误：Kind=KindServer,   Code=headerErr 原值
type ActionError struct {
	Kind   errcode.Kind      // 错误来源类别：KindFramework 或 KindServer
	Code   errcode.ErrorCode // 错误码，与 Kind 组成唯一标识
	Detail string            // 上下文描述（service / route / elapsed 等），不含 [code] 前缀
	cause  error             // 可选下层错误，用于 errors.Is 链式判断（如 ErrTimeout）
}

// NewActionError 创建框架错误（最常用入口）。
// 可选 cause 参数用于包装下层 error（如 factory.Create 失败）。
func NewActionError(code errcode.ErrorCode, detail string, cause ...error) *ActionError {
	e := &ActionError{Kind: errcode.KindFramework, Code: code, Detail: detail}
	if len(cause) > 0 {
		e.cause = cause[0]
	}
	return e
}

// NewTimeoutError 创建带 ErrTimeout cause 的超时错误。
// classifyResult 通过 errors.Is(err, ErrTimeout) 识别并归类为 ResultTimeout。
func NewTimeoutError(code errcode.ErrorCode, detail string) *ActionError {
	return &ActionError{Kind: errcode.KindFramework, Code: code, Detail: detail, cause: ErrTimeout}
}

// NewServerError 包装服务端 headerErr 为 ActionError。
// Kind 显式标为 KindServer，便于 monitor/前端区分。
func NewServerError(serverCode uint64, detail string) *ActionError {
	return &ActionError{Kind: errcode.KindServer, Code: errcode.ErrorCode(serverCode), Detail: detail}
}

// Error 格式：[framework/1] service=logic 或 [server/1004] desc: route=CreateTeam。
func (e *ActionError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("[%s/%d] %s", e.Kind, e.Code, e.Detail)
	}
	return fmt.Sprintf("[%s/%d]", e.Kind, e.Code)
}

// Unwrap 返回被包装的下层错误，支持 errors.Is 链式判断。
func (e *ActionError) Unwrap() error { return e.cause }

// IsServerError 判断是否为服务端错误（基于 Kind，而非数值区间）。
func (e *ActionError) IsServerError() bool { return e.Kind == errcode.KindServer }

// ErrorKind 返回错误来源类别，供 monitor 通过接口提取，避免循环依赖。
func (e *ActionError) ErrorKind() errcode.Kind { return e.Kind }

// ErrorCode 返回数值错误码，供 monitor 通过接口提取。
func (e *ActionError) ErrorCode() uint64 { return uint64(e.Code) }

// ErrorDetail 返回错误上下文描述，供 monitor 通过接口提取。
func (e *ActionError) ErrorDetail() string { return e.Detail }
