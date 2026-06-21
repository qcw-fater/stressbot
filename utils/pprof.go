package utils

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// StartPprofServer 在指定端口启动 pprof HTTP 服务（非阻塞）。
// 任何模式（standalone / agent / admin）均可通过配置启用。
// 返回的函数用于优雅关闭服务。
func StartPprofServer(port int) (stop func()) {
	addr := fmt.Sprintf(":%d", port)
	srv := &http.Server{Addr: addr}
	GetWorkPool().Go(func() {
		stresslog.Info("[DEBUG] pprof 服务启动",
			zap.String("endpoint", "http://localhost"+addr+"/debug/pprof/"))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			stresslog.Error("[DEBUG] pprof 服务异常退出", zap.Error(err))
		}
	})
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.SetKeepAlivesEnabled(false)
		_ = srv.Shutdown(ctx)
	}
}
