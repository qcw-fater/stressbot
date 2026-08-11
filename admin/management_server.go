package admin

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

const (
	controlPlaneReadHeaderTimeout = 10 * time.Second
	controlPlaneIdleTimeout       = 120 * time.Second
)

func (s *AdminServer) newManagementServer() *http.Server {
	return &http.Server{
		Addr:              net.JoinHostPort(s.cfg.ListenHost, strconv.Itoa(s.cfg.Port)),
		Handler:           s.registerManagementRoutes(),
		ReadHeaderTimeout: controlPlaneReadHeaderTimeout,
		IdleTimeout:       controlPlaneIdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}

type adminServeResult struct {
	name string
	err  error
}

func (s *AdminServer) serveHTTPServers(sigCh <-chan os.Signal) error {
	managementListener, err := net.Listen("tcp", s.managementSrv.Addr)
	if err != nil {
		return fmt.Errorf("management server listen: %w", err)
	}
	controlPlaneListener, err := net.Listen("tcp", s.controlPlaneSrv.Addr)
	if err != nil {
		_ = managementListener.Close()
		return fmt.Errorf("control plane server listen: %w", err)
	}
	if s.controlPlaneTLS != nil {
		controlPlaneListener = tls.NewListener(controlPlaneListener, s.controlPlaneTLS)
	}

	results := make(chan adminServeResult, 2)
	serve := func(name string, server *http.Server, listener net.Listener) {
		utils.GetWorkPool().Go(func() {
			err := server.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			results <- adminServeResult{name: name, err: err}
		})
	}
	serve("management", s.managementSrv, managementListener)
	serve("control plane", s.controlPlaneSrv, controlPlaneListener)

	select {
	case sig := <-sigCh:
		stresslog.Info("收到退出信号，开始关闭...", zap.String("signal", sig.String()))
		return s.Shutdown(context.Background())
	case result := <-results:
		shutdownErr := s.Shutdown(context.Background())
		if result.err != nil {
			return fmt.Errorf("%s server: %w", result.name, result.err)
		}
		return shutdownErr
	}
}

func (s *AdminServer) shutdownHTTPServers(ctx context.Context) error {
	var shutdownErr error
	for _, server := range []*http.Server{s.managementSrv, s.controlPlaneSrv} {
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
