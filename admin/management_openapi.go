package admin

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
	openapispec "stressbot/api/openapi"
)

var (
	managementSpecOnce sync.Once
	managementSpec     *openapi3.T
	managementSpecErr  error
)

// managementOpenAPIValidator 在进入领域 handler 前校验管理面请求。
// 任务配置下载是 net/http 的 {path...} 捕获路由，OpenAPI 3.0 无法表达跨斜杠参数，故仅该路由保留 handler 自校验。
func managementOpenAPIValidator(next http.Handler) http.Handler {
	spec, err := managementOpenAPISpec()
	if err != nil {
		panic(fmt.Sprintf("加载管理面 OpenAPI 失败: %v", err))
	}
	return nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{MultiError: true},
		Skipper: func(r *http.Request) bool {
			return !strings.HasPrefix(r.URL.Path, "/sbot/") || isTaskConfigCatchAll(r.URL.Path)
		},
		ErrorHandler: func(w http.ResponseWriter, message string, statusCode int) {
			writeJSON(w, statusCode, &Error{
				Code:       "REQUEST_SCHEMA_INVALID",
				HTTPStatus: statusCode,
				Message:    message,
			})
		},
	})(next)
}

func managementOpenAPISpec() (*openapi3.T, error) {
	managementSpecOnce.Do(func() {
		managementSpec, managementSpecErr = openapi3.NewLoader().LoadFromData(openapispec.AdminSpec())
		if managementSpecErr == nil {
			managementSpecErr = managementSpec.Validate(context.Background())
		}
	})
	return managementSpec, managementSpecErr
}

func isTaskConfigCatchAll(path string) bool {
	const prefix = "/sbot/tasks/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := strings.TrimPrefix(path, prefix)
	return strings.Contains(rest, "/config/")
}
