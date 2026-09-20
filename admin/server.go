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

// 管理面 HTTP 超时：
//   - ReadHeaderTimeout 防止慢客户端攻击型连接占住 server goroutine 不放，
//     避免堆积导致 accept queue 满；
//   - IdleTimeout 让长期空闲的 keep-alive 连接主动关闭，
//     防止 agent → admin 的 client-side 池保留大量已不再使用的连接；
//   - ReadTimeout/WriteTimeout 故意不设：history 导出可能很长。
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
		return fmt.Errorf("管理面监听失败: %w", err)
	}
	controlPlaneAddr := net.JoinHostPort(s.cfg.ControlPlane.ListenHost, strconv.Itoa(s.cfg.ControlPlane.Port))
	controlPlaneListener, err := listenConfig.Listen(ctx, "tcp", controlPlaneAddr)
	if err != nil {
		_ = managementListener.Close()
		return fmt.Errorf("控制面监听失败: %w", err)
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
	serve("管理面", s.managementSrv, managementListener)
	workpool.Default().Go(func() {
		err := s.grpcSrv.Serve(controlPlaneListener)
		if errors.Is(err, grpc.ErrServerStopped) {
			err = nil
		}
		results <- adminServeResult{name: "gRPC 控制面", err: err}
	})

	select {
	case <-ctx.Done():
		stresslog.Info("收到退出请求，开始关闭...", zap.Error(ctx.Err()))
		return s.Shutdown(context.Background())
	case result := <-results:
		shutdownErr := s.Shutdown(context.Background())
		if result.err != nil {
			return fmt.Errorf("%s 服务异常退出: %w", result.name, result.err)
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
