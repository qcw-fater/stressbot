package monitor

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
)

// ExportCSV 将快照写入 CSV 文件。
func ExportCSV(c *MetricsCollector, path string) (err error) {
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
	defer func() {
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}()

	w := csv.NewWriter(f)

	// 新增"网络样本数 / 客户端开销"两列，对应 latency 拆分后的 monitor 模型。
	// 网络延迟列保留原名，但语义已改为"纯网络往返"，不再含客户端构建/解析。
	header := []string{
		"接口名", "样本数", "成功次数", "超时次数", "错误次数",
		"网络样本数", "成功率",
		"平均网络往返(ms)", "客户端开销(ms)",
		"最小网络往返(ms)", "最大网络往返(ms)",
		"P50(ms)", "P90(ms)", "P95(ms)", "P99(ms)",
		"Apdex", "平均发送字节", "平均接收字节",
		"平均QPS", "压测时长(s)",
	}
	if err := w.Write(header); err != nil {
		return fmt.Errorf("写入 CSV 表头失败: %w", err)
	}

	uptimeSec := snap.UptimeSec
	for _, a := range snap.Actions {
		if err := w.Write([]string{
			a.Name,
			fmt.Sprintf("%d", a.SampleCount),
			fmt.Sprintf("%d", a.SuccessCount),
			fmt.Sprintf("%d", a.TimeoutCount),
			fmt.Sprintf("%d", a.FailureCount),
			fmt.Sprintf("%d", a.RTTSampleCount),
			fmt.Sprintf("%.4f", a.SuccessRate),
			formatHistogramCSV(a.RTT.AvgMs),
			fmt.Sprintf("%.2f", a.ClientAvgMs),
			formatHistogramCSV(a.RTT.MinMs),
			formatHistogramCSV(a.RTT.MaxMs),
			formatHistogramCSV(a.RTT.P50Ms),
			formatHistogramCSV(a.RTT.P90Ms),
			formatHistogramCSV(a.RTT.P95Ms),
			formatHistogramCSV(a.RTT.P99Ms),
			fmt.Sprintf("%.4f", a.RTTApdex),
			fmt.Sprintf("%.1f", a.AvgSendBytes),
			fmt.Sprintf("%.1f", a.AvgRecvBytes),
			fmt.Sprintf("%.2f", a.AvgQPS),
			fmt.Sprintf("%.1f", uptimeSec),
		}); err != nil {
			return fmt.Errorf("写入 CSV 数据失败: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("刷新 CSV 数据失败: %w", err)
	}
	return nil
}

func formatHistogramCSV(value *float64) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%.2f", *value)
}
