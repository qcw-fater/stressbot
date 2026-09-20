package config

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	// 注册标准 pprof 处理器到 http.DefaultServeMux。
	_ "net/http/pprof"
	"sync"
	"time"

	// 注册 stressbot 专有的调优观测端点到 http.DefaultServeMux。
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

// pprofReadHeaderTimeout 读取请求头的上限。
// 裸 http.Server 不设任何超时，一个只发半行请求头就不动的连接能永久占住一个
// 读协程（slowloris，gosec G112）；调试口同样对外监听，必须设上限。
// 只限制请求头：pprof 的 profile/trace 端点按 ?seconds= 长时间流式响应，
// 设 WriteTimeout 会把正常的 30s profile 采集直接掐断。
const pprofReadHeaderTimeout = 10 * time.Second

// StartPprofServer 在指定端口启动 pprof HTTP 服务（非阻塞）。
// 任何模式（standalone / agent / admin）均可通过配置启用。
// 返回的函数用于优雅关闭服务。
//
// Handler 留空即用 http.DefaultServeMux：除 net/http/pprof 外，debughttp、
// protox、codec、network、script 各包都在 init 里把 /debug/* 观测端点挂到默认
// mux 上，由本服务统一对外暴露（见 AGENTS.md「调优观测端点」）。换成私有 mux
// 会让这些端点整体消失，故此处刻意共用默认 mux。
func StartPprofServer(ctx context.Context, port int) (stop func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	addr := fmt.Sprintf(":%d", port)
	srv := &http.Server{Addr: addr, ReadHeaderTimeout: pprofReadHeaderTimeout}
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
