package admin

import (
	"net/http"

	json "stressbot/utils/jsonx"
)

// Error 统一 API 错误类型。
type Error struct {
	Code       string         `json:"code"`
	HTTPStatus int            `json:"-"`
	Message    string         `json:"message"`
	Details    map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string {
	return e.Message
}

// NewError 创建预定义错误。
func NewError(code string, httpStatus int) *Error {
	return &Error{Code: code, HTTPStatus: httpStatus, Message: code}
}

// WithMessage 返回附带消息的错误副本。
func (e *Error) WithMessage(msg string) *Error {
	cp := *e
	cp.Message = msg
	return &cp
}

// WithDetails 返回附带详情的错误副本。
func (e *Error) WithDetails(details map[string]any) *Error {
	cp := *e
	cp.Details = details
	return &cp
}

// 预定义错误。
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

// writeJSON 向 response writer 写入 JSON。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError 向 response writer 写入错误响应。
func writeError(w http.ResponseWriter, err error) {
	if apiErr, ok := err.(*Error); ok {
		writeJSON(w, apiErr.HTTPStatus, apiErr)
		return
	}
	writeJSON(w, http.StatusInternalServerError, &Error{
		Code:       "INTERNAL_ERROR",
		HTTPStatus: http.StatusInternalServerError,
		Message:    err.Error(),
	})
}
