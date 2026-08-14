package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	json "stressbot/internal/jsonx"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"

	"go.uber.org/zap"
)

// RegisterHandlers 将 /metrics 和 /metrics/summary 注册到 http.DefaultServeMux。
// pprof 由 config.StartPprofServer 独立管理，不与此模块耦合。
func RegisterHandlers(c *MetricsCollector) {
	http.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		snap := c.Snapshot(nil, 0).PublicCopy()
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(snap)
	})
	http.HandleFunc("/metrics/summary", func(w http.ResponseWriter, _ *http.Request) {
		snap := c.Snapshot(nil, 0)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "uptime: %s\n", snap.Uptime.Round(time.Second))
		_, _ = fmt.Fprintf(w, "robots: started=%d running=%d stopped=%d errored=%d\n",
			snap.Robots.Started, snap.Robots.Running, snap.Robots.Stopped, snap.Robots.Errored)
		for _, a := range snap.Actions {
			_, _ = fmt.Fprintf(w, "%s: samples=%d success=%d timeout=%d failure=%d avg=%.1fms p99=%.1fms apdex=%.3f qps=%.2f\n",
				a.Name, a.SampleCount, a.SuccessCount, a.TimeoutCount, a.FailureCount,
				histogramValue(a.RTT.AvgMs), histogramValue(a.RTT.P99Ms), a.RTTApdex, a.AvgQPS)
		}
	})
}

// StartHTTPServer 启动 HTTP 服务（非阻塞），返回优雅关闭函数。
func StartHTTPServer(port int) (stop func(), err error) {
	return startHTTPServerWithSubmit(port, workpool.Default().Submit)
}

func startHTTPServerWithSubmit(port int, submit func(func()) error) (stop func(), err error) {
	addr := fmt.Sprintf(":%d", port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           http.DefaultServeMux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := submit(func() {
		stresslog.Info("[MONITOR] HTTP 指标服务启动", zap.String("addr", addr),
			zap.String("metrics", "http://localhost"+addr+"/metrics"))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			stresslog.Error("[MONITOR] HTTP 服务异常退出", zap.String("addr", addr), zap.Error(err))
		}
	}); err != nil {
		return nil, fmt.Errorf("提交监控 HTTP 服务任务失败: %w", err)
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.SetKeepAlivesEnabled(false)
		if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			stresslog.Warn("[MONITOR] 关闭 HTTP 指标服务失败", zap.String("addr", addr), zap.Error(err))
		}
	}, nil
}
