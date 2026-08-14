package admin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"stressbot/admin/httpapi"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"

	"go.uber.org/zap"
	"google.golang.org/grpc"
)

const (
	managementReadHeaderTimeout = 10 * time.Second
	managementIdleTimeout       = 120 * time.Second
)

func (s *Server) newManagementServer() *http.Server {
	return &http.Server{
		Addr: net.JoinHostPort(s.cfg.Server.ListenHost, strconv.Itoa(s.cfg.Server.Port)),
		Handler: httpapi.NewHandler(httpapi.Dependencies{
			StaticDir: s.cfg.Server.StaticDir, Redis: s.cfg.Redis,
			Tasks: s.tasks, Agents: s.agents, Aggregator: s.aggregator, Assigner: s.assigner,
			History: s.history, Sampler: s.sampler, Flows: s.flows,
			ActionTemplates: s.actionTemplates, ListenTemplates: s.listenTemplates, NextID: generateID,
			ScheduleStart: s.commandDispatch.ScheduleStart, ScheduleStop: s.commandDispatch.ScheduleStop,
			ScheduleShutdown:          s.commandDispatch.ScheduleShutdown,
			FinishTaskIfFullyReported: s.completion.FinishIfFullyReported,
			SynthesizeOfflineReports:  s.synthesizeOfflineReports,
			StartStopTimeout:          s.startStopTimeout,
		}).Routes(),
		ReadHeaderTimeout: managementReadHeaderTimeout,
		IdleTimeout:       managementIdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}

type adminServeResult struct {
	name string
	err  error
}

func (s *Server) serveServers(ctx context.Context) error {
	var listenConfig net.ListenConfig
	managementListener, err := listenConfig.Listen(ctx, "tcp", s.managementSrv.Addr)
	if err != nil {
		return fmt.Errorf("management server listen: %w", err)
	}
	controlPlaneAddr := net.JoinHostPort(s.cfg.ControlPlane.ListenHost, strconv.Itoa(s.cfg.ControlPlane.Port))
	controlPlaneListener, err := listenConfig.Listen(ctx, "tcp", controlPlaneAddr)
	if err != nil {
		_ = managementListener.Close()
		return fmt.Errorf("control plane server listen: %w", err)
	}

	results := make(chan adminServeResult, 2)
	serve := func(name string, server *http.Server, listener net.Listener) {
		workpool.Default().Go(func() {
			err := server.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			results <- adminServeResult{name: name, err: err}
		})
	}
	serve("management", s.managementSrv, managementListener)
	workpool.Default().Go(func() {
		err := s.grpcSrv.Serve(controlPlaneListener)
		if errors.Is(err, grpc.ErrServerStopped) {
			err = nil
		}
		results <- adminServeResult{name: "gRPC control plane", err: err}
	})

	select {
	case <-ctx.Done():
		stresslog.Info("收到退出请求，开始关闭...", zap.Error(ctx.Err()))
		return s.Shutdown(context.Background())
	case result := <-results:
		shutdownErr := s.Shutdown(context.Background())
		if result.err != nil {
			return fmt.Errorf("%s server: %w", result.name, result.err)
		}
		return shutdownErr
	}
}

func (s *Server) shutdownServers(ctx context.Context) error {
	var shutdownErr error
	if s.sessions != nil {
		s.sessions.Close("Admin 正在关闭")
	}
	if s.grpcSrv != nil {
		done := make(chan struct{})
		workpool.Default().Go(func() {
			s.grpcSrv.GracefulStop()
			close(done)
		})
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			s.grpcSrv.Stop()
		}
	}
	for _, server := range []*http.Server{s.managementSrv} {
		if server == nil {
			continue
		}
		server.SetKeepAlivesEnabled(false)
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := server.Shutdown(shutdownCtx)
		cancel()
		if err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	return shutdownErr
}
