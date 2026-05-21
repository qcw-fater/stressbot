package monitor

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
)

// ExportCSV 将快照写入 CSV 文件。
func ExportCSV(c *MetricsCollector, path string) error {
	snap := c.Snapshot(nil, 0)
	if len(snap.Actions) == 0 {
		return nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	header := []string{
		"接口名", "样本数", "成功次数", "超时次数", "错误次数",
		"成功率", "平均响应(ms)", "最小响应(ms)", "最大响应(ms)",
		"P50(ms)", "P90(ms)", "P95(ms)", "P99(ms)",
		"Apdex", "平均发送字节", "平均接收字节",
		"平均QPS", "压测时长(s)",
	}
	_ = w.Write(header) // CSV 写入错误不影响主流程

	uptimeSec := snap.UptimeSec
	for _, a := range snap.Actions {
		_ = w.Write([]string{ // 同上
			a.Name,
			fmt.Sprintf("%d", a.SampleCount),
			fmt.Sprintf("%d", a.SuccessCount),
			fmt.Sprintf("%d", a.TimeoutCount),
			fmt.Sprintf("%d", a.FailureCount),
			fmt.Sprintf("%.4f", a.SuccessRate),
			fmt.Sprintf("%.2f", a.Latency.AvgMs),
			fmt.Sprintf("%.2f", a.Latency.MinMs),
			fmt.Sprintf("%.2f", a.Latency.MaxMs),
			fmt.Sprintf("%.2f", a.Latency.P50Ms),
			fmt.Sprintf("%.2f", a.Latency.P90Ms),
			fmt.Sprintf("%.2f", a.Latency.P95Ms),
			fmt.Sprintf("%.2f", a.Latency.P99Ms),
			fmt.Sprintf("%.4f", a.Apdex),
			fmt.Sprintf("%.1f", a.AvgSendBytes),
			fmt.Sprintf("%.1f", a.AvgRecvBytes),
			fmt.Sprintf("%.2f", a.AvgQPS),
			fmt.Sprintf("%.1f", uptimeSec),
		})
	}
	return nil
}
