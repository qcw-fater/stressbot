package httpapi

import (
	"net/http"

	"stressbot/admin/apierror"
)

// managementOpenAPIValidator is kept as the Admin-facing adapter used by
// focused transport tests; validation itself lives in admin/httpapi.
func managementOpenAPIValidator(next http.Handler) http.Handler {
	return Validator(next, func(w http.ResponseWriter, message string, statusCode int) {
		writeJSON(w, statusCode, &apierror.Error{Code: "REQUEST_SCHEMA_INVALID", HTTPStatus: statusCode, Message: message})
	})
}
