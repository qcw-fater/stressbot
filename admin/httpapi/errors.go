package httpapi

import (
	"errors"
	"net/http"

	"stressbot/admin/apierror"
	json "stressbot/internal/jsonx"
)

// WriteJSON 以 JSON 编码写出响应体：设置 Content-Type 与状态码，编码
// 失败静默忽略（首包已发出，无法回写错误）。
func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// WriteError 把错误写成标准 JSON 响应：apierror.Error 按其自带 HTTP 状态
// 码原样输出，其余错误统一包装为 500 INTERNAL_ERROR。
func WriteError(w http.ResponseWriter, err error) {
	if apiErr, ok := errors.AsType[*apierror.Error](err); ok {
		WriteJSON(w, apiErr.HTTPStatus, apiErr)
		return
	}
	WriteJSON(w, http.StatusInternalServerError, &apierror.Error{
		Code: "INTERNAL_ERROR", HTTPStatus: http.StatusInternalServerError, Message: err.Error(),
	})
}
