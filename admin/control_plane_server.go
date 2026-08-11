package admin

import (
	"net"
	"net/http"
	"strconv"
)

// registerControlPlaneRoutes 只暴露 Agent 上行端点。
func (s *AdminServer) registerControlPlaneRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /sbot/agent/register", s.handleAgentRegister)
	mux.HandleFunc("POST /sbot/agent/{id}/heartbeat", s.handleAgentHeartbeat)
	mux.HandleFunc("POST /sbot/agent/{id}/deregister", s.handleAgentDeregister)
	mux.HandleFunc("POST /sbot/agent/stress", s.handleAgentStressReport)
	mux.HandleFunc("POST /sbot/agent/system", s.handleAgentSystemReport)
	mux.HandleFunc("POST /sbot/agent/{id}/task/{tid}/done", s.handleAgentTaskDone)
	mux.HandleFunc("GET /sbot/agent/{id}/pending-task", s.handleAgentPendingTask)
	return recoverMiddleware(mux)
}

func (s *AdminServer) newControlPlaneServer() *http.Server {
	return &http.Server{
		Addr: net.JoinHostPort(
			s.cfg.ControlPlane.ListenHost,
			strconv.Itoa(s.cfg.ControlPlane.Port),
		),
		Handler:           s.registerControlPlaneRoutes(),
		ReadHeaderTimeout: controlPlaneReadHeaderTimeout,
		IdleTimeout:       controlPlaneIdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}
