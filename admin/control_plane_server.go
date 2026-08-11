package admin

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"stressbot/controlplane"
)

// registerControlPlaneRoutes 只暴露 Agent 上行端点。
func (s *AdminServer) registerControlPlaneRoutes() http.Handler {
	handler := controlplane.NewOpenAPIHandler(&adminControlPlaneAPI{server: s}, func(r *http.Request) bool {
		return strings.HasPrefix(r.URL.Path, "/sbot/agent/")
	})
	return recoverMiddleware(handler)
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
