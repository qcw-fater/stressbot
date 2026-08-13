package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
	"stressbot/api/admin"
)

var (
	managementSpecOnce sync.Once
	managementSpec     *openapi3.T
	managementSpecErr  error
)

// Wrap installs the management API schema validator and panic boundary.
// Response formatting and logging stay injectable so this transport package
// does not depend on Admin's domain error model.
func Wrap(next http.Handler, schemaError func(http.ResponseWriter, string, int), panicError func(http.ResponseWriter, *http.Request, any, []byte)) http.Handler {
	validated := Validator(next, schemaError)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				panicError(w, r, rec, debug.Stack())
			}
		}()
		validated.ServeHTTP(w, r)
	})
}

// Validator validates management requests against the embedded OpenAPI spec.
func Validator(next http.Handler, schemaError func(http.ResponseWriter, string, int)) http.Handler {
	spec, err := managementOpenAPISpec()
	if err != nil {
		panic(fmt.Sprintf("加载管理面 OpenAPI 失败: %v", err))
	}
	return nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{MultiError: true},
		Skipper: func(r *http.Request) bool {
			return !strings.HasPrefix(r.URL.Path, "/sbot/") || isDocumentationPath(r.URL.Path) || isTaskConfigCatchAll(r.URL.Path)
		},
		ErrorHandler: schemaError,
	})(next)
}

func isDocumentationPath(requestPath string) bool {
	return requestPath == "/sbot/docs" || requestPath == "/sbot/openapi.yaml"
}

func managementOpenAPISpec() (*openapi3.T, error) {
	managementSpecOnce.Do(func() {
		managementSpec, managementSpecErr = openapi3.NewLoader().LoadFromData(adminapi.AdminSpec())
		if managementSpecErr == nil {
			managementSpecErr = managementSpec.Validate(context.Background())
		}
	})
	return managementSpec, managementSpecErr
}

func isTaskConfigCatchAll(requestPath string) bool {
	const prefix = "/sbot/tasks/"
	if !strings.HasPrefix(requestPath, prefix) {
		return false
	}
	rest := strings.TrimPrefix(requestPath, prefix)
	return strings.Contains(rest, "/config/")
}
