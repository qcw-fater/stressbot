package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3filter"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
	adminapi "stressbot/admin/api"
)

type requestContextKey struct{}

// NewOpenAPIHandler 为生成的 strict server 增加入站 OpenAPI 校验。
// mTLS 已由外层 listener 在进入 HTTP handler 前完成，这里只确认 spec 的 security requirement。
func NewOpenAPIHandler(server adminapi.StrictServerInterface, allow func(*http.Request) bool) http.Handler {
	spec, err := adminapi.GetSwagger()
	if err != nil {
		panic(fmt.Sprintf("加载内嵌控制面 OpenAPI 失败: %v", err))
	}
	if err := spec.Validate(context.Background()); err != nil {
		panic(fmt.Sprintf("校验内嵌控制面 OpenAPI 失败: %v", err))
	}

	strict := adminapi.NewStrictHandlerWithOptions(
		server,
		[]adminapi.StrictMiddlewareFunc{captureHTTPRequest},
		adminapi.StrictHTTPServerOptions{
			RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
				writeOpenAPIError(w, http.StatusBadRequest, "CONTROL_PLANE_REQUEST_INVALID", err.Error())
			},
			ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
				writeOpenAPIError(w, http.StatusInternalServerError, "CONTROL_PLANE_RESPONSE_INVALID", err.Error())
			},
		},
	)
	mux := http.NewServeMux()
	generated := adminapi.HandlerFromMux(strict, mux)
	validated := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
			MultiError:         true,
		},
		ErrorHandler: func(w http.ResponseWriter, message string, statusCode int) {
			writeOpenAPIError(w, statusCode, "CONTROL_PLANE_REQUEST_INVALID", message)
		},
	})(generated)

	if allow == nil {
		return validated
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allow(r) {
			http.NotFound(w, r)
			return
		}
		validated.ServeHTTP(w, r)
	})
}

func captureHTTPRequest(next adminapi.StrictHandlerFunc, _ string) adminapi.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		return next(context.WithValue(ctx, requestContextKey{}, r), w, r, request)
	}
}

// LegacyRequest 取得 strict server 已匹配的原始请求，并用生成 DTO 重建已被消费的 JSON body。
func LegacyRequest(ctx context.Context, body any) (*http.Request, error) {
	raw, ok := ctx.Value(requestContextKey{}).(*http.Request)
	if !ok || raw == nil {
		return nil, fmt.Errorf("控制面请求上下文缺少原始 HTTP request")
	}
	req := raw.Clone(ctx)
	if body == nil {
		return req, nil
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化控制面 DTO: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// InvokeLegacy 把生成 DTO 交给现有领域 handler，并保留其状态码、响应头与响应体。
func InvokeLegacy(handler http.HandlerFunc, req *http.Request) LegacyResponse {
	recorder := &legacyRecorder{header: make(http.Header)}
	handler(recorder, req)
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	return LegacyResponse{
		StatusCode: status,
		Header:     recorder.header.Clone(),
		Body:       bytes.Clone(recorder.body.Bytes()),
	}
}

type legacyRecorder struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *legacyRecorder) Header() http.Header { return r.header }

func (r *legacyRecorder) WriteHeader(statusCode int) {
	if r.status == 0 {
		r.status = statusCode
	}
}

func (r *legacyRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}

// LegacyResponse 实现所有生成的 response-object 接口，让迁移期领域 handler 保持既有 HTTP 语义。
// 新接口完成领域层拆分后可逐个替换为生成的强类型 response。
type LegacyResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (r LegacyResponse) write(w http.ResponseWriter) error {
	for key, values := range r.Header {
		w.Header()[key] = append([]string(nil), values...)
	}
	w.WriteHeader(r.StatusCode)
	_, err := w.Write(r.Body)
	return err
}

func (r LegacyResponse) VisitQueryAgentLogsResponse(w http.ResponseWriter) error { return r.write(w) }
func (r LegacyResponse) VisitListAgentLogFilesResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r LegacyResponse) VisitDownloadAgentLogFileResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r LegacyResponse) VisitShutdownAgentResponse(w http.ResponseWriter) error   { return r.write(w) }
func (r LegacyResponse) VisitGetAgentStatusResponse(w http.ResponseWriter) error  { return r.write(w) }
func (r LegacyResponse) VisitStopTaskResponse(w http.ResponseWriter) error        { return r.write(w) }
func (r LegacyResponse) VisitAssignTaskResponse(w http.ResponseWriter) error      { return r.write(w) }
func (r LegacyResponse) VisitGetAgentVersionResponse(w http.ResponseWriter) error { return r.write(w) }
func (r LegacyResponse) VisitGetAgentHealthResponse(w http.ResponseWriter) error  { return r.write(w) }
func (r LegacyResponse) VisitRegisterAgentResponse(w http.ResponseWriter) error   { return r.write(w) }
func (r LegacyResponse) VisitReportAgentStressResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r LegacyResponse) VisitReportAgentSystemResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r LegacyResponse) VisitDeregisterAgentResponse(w http.ResponseWriter) error { return r.write(w) }
func (r LegacyResponse) VisitHeartbeatAgentResponse(w http.ResponseWriter) error  { return r.write(w) }
func (r LegacyResponse) VisitGetPendingAgentTaskResponse(w http.ResponseWriter) error {
	return r.write(w)
}
func (r LegacyResponse) VisitCompleteAgentTaskResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func writeOpenAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(adminapi.ApiError{Code: code, Message: message})
}

// UnimplementedStrictServer 供 Admin/Agent 各自只覆盖本进程实际暴露的 operation。
type UnimplementedStrictServer struct{}

func unimplemented() LegacyResponse {
	body, _ := json.Marshal(adminapi.ApiError{Code: "CONTROL_PLANE_OPERATION_UNAVAILABLE", Message: "operation unavailable on this process"})
	return LegacyResponse{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, Body: body}
}

func (UnimplementedStrictServer) QueryAgentLogs(context.Context, adminapi.QueryAgentLogsRequestObject) (adminapi.QueryAgentLogsResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) ListAgentLogFiles(context.Context, adminapi.ListAgentLogFilesRequestObject) (adminapi.ListAgentLogFilesResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) DownloadAgentLogFile(context.Context, adminapi.DownloadAgentLogFileRequestObject) (adminapi.DownloadAgentLogFileResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) ShutdownAgent(context.Context, adminapi.ShutdownAgentRequestObject) (adminapi.ShutdownAgentResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) GetAgentStatus(context.Context, adminapi.GetAgentStatusRequestObject) (adminapi.GetAgentStatusResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) StopTask(context.Context, adminapi.StopTaskRequestObject) (adminapi.StopTaskResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) AssignTask(context.Context, adminapi.AssignTaskRequestObject) (adminapi.AssignTaskResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) GetAgentVersion(context.Context, adminapi.GetAgentVersionRequestObject) (adminapi.GetAgentVersionResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) GetAgentHealth(context.Context, adminapi.GetAgentHealthRequestObject) (adminapi.GetAgentHealthResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) RegisterAgent(context.Context, adminapi.RegisterAgentRequestObject) (adminapi.RegisterAgentResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) ReportAgentStress(context.Context, adminapi.ReportAgentStressRequestObject) (adminapi.ReportAgentStressResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) ReportAgentSystem(context.Context, adminapi.ReportAgentSystemRequestObject) (adminapi.ReportAgentSystemResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) DeregisterAgent(context.Context, adminapi.DeregisterAgentRequestObject) (adminapi.DeregisterAgentResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) HeartbeatAgent(context.Context, adminapi.HeartbeatAgentRequestObject) (adminapi.HeartbeatAgentResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) GetPendingAgentTask(context.Context, adminapi.GetPendingAgentTaskRequestObject) (adminapi.GetPendingAgentTaskResponseObject, error) {
	return unimplemented(), nil
}
func (UnimplementedStrictServer) CompleteAgentTask(context.Context, adminapi.CompleteAgentTaskRequestObject) (adminapi.CompleteAgentTaskResponseObject, error) {
	return unimplemented(), nil
}
