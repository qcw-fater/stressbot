package admin

import (
	"context"
	"net/http"

	adminapi "stressbot/admin/api"
	"stressbot/controlplane"
)

// adminControlPlaneAPI 把生成的 transport DTO 适配到现有 Admin 领域 handler。
// 迁移期通过 JSON 边界重建请求，避免生成 DTO 穿透到调度、指标和存储领域模型。
type adminControlPlaneAPI struct {
	controlplane.UnimplementedStrictServer
	server *AdminServer
}

func (a *adminControlPlaneAPI) RegisterAgent(ctx context.Context, request adminapi.RegisterAgentRequestObject) (adminapi.RegisterAgentResponseObject, error) {
	return a.invoke(ctx, request.Body, a.server.handleAgentRegister)
}

func (a *adminControlPlaneAPI) HeartbeatAgent(ctx context.Context, request adminapi.HeartbeatAgentRequestObject) (adminapi.HeartbeatAgentResponseObject, error) {
	return a.invoke(ctx, request.Body, a.server.handleAgentHeartbeat)
}

func (a *adminControlPlaneAPI) DeregisterAgent(ctx context.Context, _ adminapi.DeregisterAgentRequestObject) (adminapi.DeregisterAgentResponseObject, error) {
	return a.invoke(ctx, nil, a.server.handleAgentDeregister)
}

func (a *adminControlPlaneAPI) ReportAgentStress(ctx context.Context, request adminapi.ReportAgentStressRequestObject) (adminapi.ReportAgentStressResponseObject, error) {
	return a.invoke(ctx, request.Body, a.server.handleAgentStressReport)
}

func (a *adminControlPlaneAPI) ReportAgentSystem(ctx context.Context, request adminapi.ReportAgentSystemRequestObject) (adminapi.ReportAgentSystemResponseObject, error) {
	return a.invoke(ctx, request.Body, a.server.handleAgentSystemReport)
}

func (a *adminControlPlaneAPI) CompleteAgentTask(ctx context.Context, request adminapi.CompleteAgentTaskRequestObject) (adminapi.CompleteAgentTaskResponseObject, error) {
	return a.invoke(ctx, request.Body, a.server.handleAgentTaskDone)
}

func (a *adminControlPlaneAPI) GetPendingAgentTask(ctx context.Context, _ adminapi.GetPendingAgentTaskRequestObject) (adminapi.GetPendingAgentTaskResponseObject, error) {
	return a.invoke(ctx, nil, a.server.handleAgentPendingTask)
}

func (a *adminControlPlaneAPI) invoke(ctx context.Context, body any, handler http.HandlerFunc) (controlplane.LegacyResponse, error) {
	req, err := controlplane.LegacyRequest(ctx, body)
	if err != nil {
		return controlplane.LegacyResponse{}, err
	}
	return controlplane.InvokeLegacy(handler, req), nil
}
