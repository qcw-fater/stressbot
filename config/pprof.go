package config

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	// Register the standard pprof handlers on http.DefaultServeMux.
	_ "net/http/pprof"
	"sync"
	"time"

	// Register stressbot-specific diagnostic handlers on http.DefaultServeMux.
	_ "stressbot/internal/debughttp"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"

	"go.uber.org/zap"
)

// PprofConfig pprof 调试服务配置。
// 指针为 nil 时表示不启用。admin / agent / stressbot 三种模式共用。
type PprofConfig struct {
	Port int `toml:"port" json:"port"` // pprof 监听端口（默认 6060）
}

// StartPprofServer 在指定端口启动 pprof HTTP 服务（非阻塞）。
// 任何模式（standalone / agent / admin）均可通过配置启用。
// 返回的函数用于优雅关闭服务。
func StartPprofServer(ctx context.Context, port int) (stop func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	addr := fmt.Sprintf(":%d", port)
	srv := &http.Server{Addr: addr}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", addr)
	if err != nil {
		stresslog.Error("[DEBUG] pprof 服务启动失败", zap.String("addr", addr), zap.Error(err))
		return func() {}
	}

	var stopOnce sync.Once
	stopServer := func() {
		stopOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			srv.SetKeepAlivesEnabled(false)
			_ = srv.Shutdown(ctx)
			_ = listener.Close()
		})
	}
	pool := workpool.Default()
	pool.Go(func() {
		stresslog.Info("[DEBUG] pprof 服务启动",
			zap.String("endpoint", "http://localhost"+addr+"/debug/pprof/"))
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			stresslog.Error("[DEBUG] pprof 服务异常退出", zap.Error(err))
		}
	})
	// 即使命令入口尚未来得及显式关闭 pprof，全局协程池关闭时也会先
	// 关闭监听器，让 Serve worker 退出，避免 Shutdown 误报泄漏任务。
	pool.GoWithStop(func(stopCh <-chan struct{}) {
		select {
		case <-ctx.Done():
		case <-stopCh:
		}
		stopServer()
	})
	return stopServer
}
