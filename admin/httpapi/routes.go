package httpapi

import (
	"fmt"
	"net/http"

	"stressbot/admin/apierror"
	"stressbot/internal/stresslog"

	"go.uber.org/zap"
)

var baselineResources = newBaselineResources("conf")

// registerManagementRoutes 注册浏览器管理 API 与静态资源路由。
//
// 顶层用 recoverMiddleware 包裹，确保 handler panic 不会断开连接，
// 而是返回标准 500 JSON 并把 stack trace 写入应用日志。
func (s *Handler) registerManagementRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sbot/openapi.yaml", serveOpenAPIDocument)
	mux.HandleFunc("GET /sbot/docs", serveSwaggerUI)

	// ── 前端-资源基线 ──
	// ── 前端-任务 ──
	mux.HandleFunc("POST /sbot/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /sbot/tasks", s.handleListTasks)
	mux.HandleFunc("GET /sbot/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /sbot/tasks/{id}/config/{path...}", s.handleGetTaskConfig)
	mux.HandleFunc("POST /sbot/tasks/{id}/start", s.handleStartTask)
	mux.HandleFunc("POST /sbot/tasks/{id}/stop", s.handleStopTask)
	mux.HandleFunc("DELETE /sbot/tasks/{id}", s.handleDeleteTask)

	// ── 前端-Agent ──
	mux.HandleFunc("GET /sbot/agents", s.handleListAgents)
	mux.HandleFunc("GET /sbot/agents/{id}", s.handleGetAgent)
	mux.HandleFunc("DELETE /sbot/agents/{id}", s.handleDeleteAgent)
	mux.HandleFunc("POST /sbot/agents/{id}/shutdown", s.handleShutdownAgent)
	mux.HandleFunc("POST /sbot/agents/shutdown-all", s.handleShutdownAllAgents)

	// ── 前端-指标 ──
	mux.HandleFunc("GET /sbot/metrics", s.handleGetMetrics)
	mux.HandleFunc("GET /sbot/metrics/summary", s.handleGetMetricsSummary)
	mux.HandleFunc("GET /sbot/metrics/agents", s.handleGetAgentMetrics)
	mux.HandleFunc("GET /sbot/metrics/agents/{id}", s.handleGetSingleAgentMetrics)
	mux.HandleFunc("GET /sbot/system", s.handleGetSystem)
	mux.HandleFunc("GET /sbot/system/agents", s.handleGetSystemAgents)
	mux.HandleFunc("GET /sbot/system/agents/{id}", s.handleGetSystemAgent)

	// ── 历史归档 ──
	mux.HandleFunc("GET /sbot/history", s.handleListHistory)
	mux.HandleFunc("GET /sbot/history/tags", s.handleGetHistoryTags)
	mux.HandleFunc("GET /sbot/history/{id}", s.handleGetHistory)
	mux.HandleFunc("PUT /sbot/history/{id}", s.handleUpdateHistory)
	mux.HandleFunc("DELETE /sbot/history/{id}", s.handleDeleteHistory)
	mux.HandleFunc("GET /sbot/history/{id}/agents", s.handleGetHistoryAgents)
	mux.HandleFunc("GET /sbot/history/{id}/config", s.handleGetHistoryConfig)
	mux.HandleFunc("GET /sbot/history/{id}/config/archive", s.handleGetHistoryConfigArchive)
	mux.HandleFunc("GET /sbot/history/{id}/timeseries", s.handleGetHistoryTimeseries)
	mux.HandleFunc("POST /sbot/history/{id}/clone", s.handleCloneHistory)
	mux.HandleFunc("GET /sbot/history/compare", s.handleCompareHistory)

	// ── 基线资源读取 ──
	mux.HandleFunc("GET /sbot/baseline/proto/index.json", s.handleBaselineProtoIndex)
	mux.HandleFunc("GET /sbot/baseline/proto/{name}", s.handleBaselineProtoFile)
	mux.HandleFunc("GET /sbot/baseline/scripts/index.json", s.handleBaselineScriptIndex)
	mux.HandleFunc("GET /sbot/baseline/scripts/{name}", s.handleBaselineScriptFile)
	// adapter 基线按文件名透传（支持多 *_codec.json + errors.json）。
	mux.HandleFunc("GET /sbot/baseline/adapter/index.json", s.handleBaselineCodecIndex)
	mux.HandleFunc("GET /sbot/baseline/adapter/{name}", s.handleBaselineCodecFile)
	mux.HandleFunc("GET /sbot/baseline/flow/flow.json", s.handleBaselineFlow)

	// ── 错误码 ──
	mux.HandleFunc("GET /sbot/api/error-codes", s.handleErrorCodeIndex)

	// ── 流程模板库 ──
	mux.HandleFunc("GET /sbot/flows", s.handleListFlows)
	mux.HandleFunc("POST /sbot/flows", s.handleCreateFlow)
	mux.HandleFunc("GET /sbot/flows/snapshot", s.handleGetFlowSnapshot)
	mux.HandleFunc("PUT /sbot/flows/snapshot", s.handleReplaceFlowSnapshot)
	mux.HandleFunc("GET /sbot/flows/{id}", s.handleGetFlow)
	mux.HandleFunc("PUT /sbot/flows/{id}", s.handleUpdateFlow)
	mux.HandleFunc("DELETE /sbot/flows/{id}", s.handleDeleteFlow)

	// ── Action/Listen 模板库 ──
	mux.HandleFunc("GET /sbot/action-templates", s.handleListActionTemplates)
	mux.HandleFunc("POST /sbot/action-templates", s.handleCreateActionTemplate)
	mux.HandleFunc("GET /sbot/action-templates/snapshot", s.handleGetActionTemplateSnapshot)
	mux.HandleFunc("PUT /sbot/action-templates/snapshot", s.handleReplaceActionTemplateSnapshot)
	mux.HandleFunc("GET /sbot/action-templates/{id}", s.handleGetActionTemplate)
	mux.HandleFunc("PUT /sbot/action-templates/{id}", s.handleUpdateActionTemplate)
	mux.HandleFunc("DELETE /sbot/action-templates/{id}", s.handleDeleteActionTemplate)
	mux.HandleFunc("GET /sbot/listen-templates", s.handleListListenTemplates)
	mux.HandleFunc("POST /sbot/listen-templates", s.handleCreateListenTemplate)
	mux.HandleFunc("GET /sbot/listen-templates/snapshot", s.handleGetListenTemplateSnapshot)
	mux.HandleFunc("PUT /sbot/listen-templates/snapshot", s.handleReplaceListenTemplateSnapshot)
	mux.HandleFunc("GET /sbot/listen-templates/{id}", s.handleGetListenTemplate)
	mux.HandleFunc("PUT /sbot/listen-templates/{id}", s.handleUpdateListenTemplate)
	mux.HandleFunc("DELETE /sbot/listen-templates/{id}", s.handleDeleteListenTemplate)

	// ── 服务器能力 ──
	mux.HandleFunc("GET /sbot/capabilities", s.handleCapabilities)

	// ── Codec 预览/算法元数据（T4.2，纯计算，供前端 codec 编辑器调用）──
	mux.HandleFunc("POST /sbot/codec/preview", s.handleCodecPreview)
	mux.HandleFunc("GET /sbot/codec/algorithms", s.handleCodecAlgorithms)

	// ── 静态资源 ──
	fs := http.FileServer(http.Dir(s.staticDir))
	mux.Handle("/", fs)

	return Wrap(mux,
		func(w http.ResponseWriter, message string, statusCode int) {
			writeJSON(w, statusCode, &apierror.Error{Code: "REQUEST_SCHEMA_INVALID", HTTPStatus: statusCode, Message: message})
		},
		func(w http.ResponseWriter, r *http.Request, rec any, stack []byte) {
			stresslog.Error("[ADMIN] HTTP handler panic",
				zap.String("path", r.URL.Path),
				zap.String("method", r.Method),
				zap.Any("panic", rec),
				zap.String("stack", string(stack)))
			writeError(w, apierror.ErrInternal.WithMessage("internal server error"))
		},
	)
}

// CapabilitiesResponse 服务器能力查询响应。
type CapabilitiesResponse struct {
	// SharedState 是否已配置共享状态（Redis）。前端据此提示脚本是否可用 share。
	SharedState bool `json:"sharedState"`
	// SharedAddr Redis 地址。仅当 SharedState=true 时有值。
	SharedAddr string `json:"sharedAddr,omitempty"`
	// FlowLibrary 是否已启用服务器流程库。
	FlowLibrary bool `json:"flowLibrary"`
	// TemplateLibrary 是否已启用共享 Action/Listen 模板库；两类存储均可用时才为 true。
	TemplateLibrary bool `json:"templateLibrary"`
}

// handleCapabilities 返回服务器能力（当前仅共享状态可用性），供前端展示与校验提示。
// 出于安全考虑，不返回原始 Redis 地址，只返回脱敏后的展示地址。
func (s *Handler) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	resp := CapabilitiesResponse{
		SharedState:     s.redisEnabled(),
		FlowLibrary:     s.flows != nil,
		TemplateLibrary: s.actionTemplates != nil && s.listenTemplates != nil,
	}
	if resp.SharedState {
		if resolved, err := s.redis.Resolve(); err == nil {
			resp.SharedAddr = fmt.Sprintf("%s:%d", resolved.Host, resolved.Port)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
