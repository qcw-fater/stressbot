package httpapi

import (
	"errors"
	"net/http"

	"stressbot/admin/apierror"
	json "stressbot/internal/jsonx"
)

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteError(w http.ResponseWriter, err error) {
	if apiErr, ok := errors.AsType[*apierror.Error](err); ok {
		WriteJSON(w, apiErr.HTTPStatus, apiErr)
		return
	}
	WriteJSON(w, http.StatusInternalServerError, &apierror.Error{
		Code: "INTERNAL_ERROR", HTTPStatus: http.StatusInternalServerError, Message: err.Error(),
	})
}
