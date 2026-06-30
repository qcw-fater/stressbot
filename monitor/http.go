package monitor

import (
	"fmt"
	"net/http"
	"time"

	"stressbot/utils"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// RegisterHandlers 将 /metrics 和 /metrics/summary 注册到 http.DefaultServeMux。
// pprof 由 utils.StartPprofServer 独立管理，不与此模块耦合。
func RegisterHandlers(c *MetricsCollector) {
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		snap := c.Snapshot(nil, 0)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(snap)
	})
	http.HandleFunc("/metrics/summary", func(w http.ResponseWriter, r *http.Request) {
		snap := c.Snapshot(nil, 0)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "uptime: %s\n", snap.Uptime.Round(time.Second))
		fmt.Fprintf(w, "robots: started=%d running=%d stopped=%d errored=%d\n",
			snap.Robots.Started, snap.Robots.Running, snap.Robots.Stopped, snap.Robots.Errored)
		for _, a := range snap.Actions {
			fmt.Fprintf(w, "%s: samples=%d success=%d timeout=%d failure=%d avg=%.1fms p99=%.1fms apdex=%.3f qps=%.2f\n",
				a.Name, a.SampleCount, a.SuccessCount, a.TimeoutCount, a.FailureCount,
				a.RTT.AvgMs, a.RTT.P99Ms, a.RTTApdex, a.AvgQPS)
		}
	})
}

// StartHTTPServer 启动 HTTP 服务（非阻塞）。
func StartHTTPServer(port int) {
	addr := fmt.Sprintf(":%d", port)
	utils.GetWorkPool().Go(func() {
		stresslog.Info("[MONITOR] HTTP 指标服务启动", zap.String("addr", addr),
			zap.String("metrics", "http://localhost"+addr+"/metrics"))
		if err := http.ListenAndServe(addr, nil); err != nil {
			stresslog.Error("[MONITOR] HTTP 服务异常退出", zap.String("addr", addr), zap.Error(err))
		}
	})
}
