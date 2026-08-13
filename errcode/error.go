package errcode

import "fmt"

// ActionError carries the unified framework/business error code and context.
type ActionError struct {
	Code   ErrorCode
	Detail string
	cause  error
}

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

func (e *ActionError) Unwrap() error       { return e.cause }
func (e *ActionError) ErrorCode() uint64   { return uint64(e.Code) }
func (e *ActionError) ErrorDetail() string { return e.Detail }
