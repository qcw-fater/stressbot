package agent

import (
	"context"
	"net/http"

	adminapi "stressbot/admin/api"
	"stressbot/controlplane"
)

// agentControlPlaneAPI 把生成的 transport DTO 适配到现有 Agent 领域 handler。
type agentControlPlaneAPI struct {
	controlplane.UnimplementedStrictServer
	agent *Agent
}

func (a *agentControlPlaneAPI) AssignTask(ctx context.Context, request adminapi.AssignTaskRequestObject) (adminapi.AssignTaskResponseObject, error) {
	return a.invoke(ctx, request.Body, a.agent.handleTaskAssign)
}

func (a *agentControlPlaneAPI) StopTask(ctx context.Context, request adminapi.StopTaskRequestObject) (adminapi.StopTaskResponseObject, error) {
	return a.invoke(ctx, request.Body, a.agent.handleStop)
}

func (a *agentControlPlaneAPI) ShutdownAgent(ctx context.Context, _ adminapi.ShutdownAgentRequestObject) (adminapi.ShutdownAgentResponseObject, error) {
	return a.invoke(ctx, nil, a.agent.handleShutdown)
}

func (a *agentControlPlaneAPI) GetAgentVersion(ctx context.Context, _ adminapi.GetAgentVersionRequestObject) (adminapi.GetAgentVersionResponseObject, error) {
	return a.invoke(ctx, nil, a.agent.handleVersion)
}

func (a *agentControlPlaneAPI) GetAgentStatus(ctx context.Context, _ adminapi.GetAgentStatusRequestObject) (adminapi.GetAgentStatusResponseObject, error) {
	return a.invoke(ctx, nil, a.agent.handleStatus)
}

func (a *agentControlPlaneAPI) QueryAgentLogs(ctx context.Context, _ adminapi.QueryAgentLogsRequestObject) (adminapi.QueryAgentLogsResponseObject, error) {
	return a.invoke(ctx, nil, a.agent.handleLogs)
}

func (a *agentControlPlaneAPI) ListAgentLogFiles(ctx context.Context, _ adminapi.ListAgentLogFilesRequestObject) (adminapi.ListAgentLogFilesResponseObject, error) {
	return a.invoke(ctx, nil, a.agent.handleListLogFiles)
}

func (a *agentControlPlaneAPI) DownloadAgentLogFile(ctx context.Context, _ adminapi.DownloadAgentLogFileRequestObject) (adminapi.DownloadAgentLogFileResponseObject, error) {
	return a.invoke(ctx, nil, a.agent.handleDownloadLogFile)
}

func (a *agentControlPlaneAPI) GetAgentHealth(ctx context.Context, _ adminapi.GetAgentHealthRequestObject) (adminapi.GetAgentHealthResponseObject, error) {
	return a.invoke(ctx, nil, a.agent.handleHealth)
}

func (a *agentControlPlaneAPI) invoke(ctx context.Context, body any, handler http.HandlerFunc) (controlplane.LegacyResponse, error) {
	req, err := controlplane.LegacyRequest(ctx, body)
	if err != nil {
		return controlplane.LegacyResponse{}, err
	}
	return controlplane.InvokeLegacy(handler, req), nil
}
