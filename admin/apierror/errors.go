// Package apierror 定义 Admin 管理 API 的稳定 JSON 错误契约与全量错误码。
package apierror

import "net/http"

// Error is the stable JSON error contract exposed by the Admin management API.
type Error struct {
	Code       string         `json:"code"`
	HTTPStatus int            `json:"-"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// NewError creates an API error with the supplied stable code and HTTP status.
func NewError(code string, httpStatus int) *Error {
	return &Error{Code: code, HTTPStatus: httpStatus, Message: code}
}

// WithMessage returns a copy carrying a user-facing message.
func (e *Error) WithMessage(message string) *Error {
	clone := *e
	clone.Message = message
	return &clone
}

// WithDetails returns a copy carrying structured error details.
func (e *Error) WithDetails(details map[string]any) *Error {
	clone := *e
	clone.Details = details
	return &clone
}

// 管理 API 的稳定错误码集合（code 与 HTTP 状态一一映射），覆盖任务状态机、
// 节点可用性、容量与参数校验、历史归档、共享状态、流程库与三类模板库。
// Message 默认取 code，可经 WithMessage/WithDetails 细化。
var (
	ErrTaskNotFound             = NewError("TASK_NOT_FOUND", http.StatusNotFound)
	ErrTaskConflict             = NewError("TASK_CONFLICT", http.StatusConflict)
	ErrTaskInvalidState         = NewError("TASK_INVALID_STATE", http.StatusConflict)
	ErrAgentNotFound            = NewError("AGENT_NOT_FOUND", http.StatusNotFound)
	ErrAgentBusy                = NewError("AGENT_BUSY", http.StatusConflict)
	ErrAgentOffline             = NewError("AGENT_OFFLINE", http.StatusConflict)
	ErrCapacityExceeded         = NewError("CAPACITY_EXCEEDED", http.StatusBadRequest)
	ErrInvalidArgument          = NewError("INVALID_ARGUMENT", http.StatusBadRequest)
	ErrHistoryDisabled          = NewError("HISTORY_DISABLED", http.StatusServiceUnavailable)
	ErrHistoryNotFound          = NewError("HISTORY_NOT_FOUND", http.StatusNotFound)
	ErrInternal                 = NewError("INTERNAL_ERROR", http.StatusInternalServerError)
	ErrStarredProtected         = NewError("HISTORY_STARRED", http.StatusConflict)
	ErrSharedUnavailable        = NewError("SHARED_STATE_UNAVAILABLE", http.StatusBadRequest)
	ErrFlowLibraryDisabled      = NewError("FLOW_LIBRARY_DISABLED", http.StatusServiceUnavailable)
	ErrFlowTemplateNotFound     = NewError("FLOW_TEMPLATE_NOT_FOUND", http.StatusNotFound)
	ErrFlowSnapshotConflict     = NewError("FLOW_SNAPSHOT_CONFLICT", http.StatusConflict)
	ErrTemplateLibraryDisabled  = NewError("TEMPLATE_LIBRARY_DISABLED", http.StatusServiceUnavailable)
	ErrActionTemplateNotFound   = NewError("ACTION_TEMPLATE_NOT_FOUND", http.StatusNotFound)
	ErrListenTemplateNotFound   = NewError("LISTEN_TEMPLATE_NOT_FOUND", http.StatusNotFound)
	ErrTemplateNameConflict     = NewError("TEMPLATE_NAME_CONFLICT", http.StatusConflict)
	ErrTemplateSnapshotConflict = NewError("TEMPLATE_SNAPSHOT_CONFLICT", http.StatusConflict)
)
