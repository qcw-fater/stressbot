package errcode

import "fmt"

// ActionError carries the unified framework/business error code and context.
type ActionError struct {
	Code   ErrorCode
	Detail string
	cause  error
}

// NewActionError 用错误码与明细构造 ActionError，可选第一个 cause 作为被包装的底层错误
// （Unwrap 暴露给 errors.Is/As，Error 输出形如 "[code] detail: cause"）。
func NewActionError(code ErrorCode, detail string, cause ...error) *ActionError {
	err := &ActionError{Code: code, Detail: detail}
	if len(cause) > 0 {
		err.cause = cause[0]
	}
	return err
}

func (e *ActionError) Error() string {
	message := fmt.Sprintf("[%d]", e.Code)
	if e.Detail != "" {
		message += " " + e.Detail
	}
	if e.cause != nil {
		message += ": " + e.cause.Error()
	}
	return message
}

func (e *ActionError) Unwrap() error { return e.cause }

// ErrorCode 返回数值错误码，实现 monitor.CodedError 接口供按 code 聚合。
func (e *ActionError) ErrorCode() uint64 { return uint64(e.Code) }

// ErrorDetail 返回错误的上下文明细（如 "action=xxx field=yyy"），实现 monitor.CodedError 接口。
func (e *ActionError) ErrorDetail() string { return e.Detail }
