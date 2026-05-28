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
	_ = w.Write(header) // CSV 写入错误不影响主流程

	uptimeSec := snap.UptimeSec
	for _, a := range snap.Actions {
		_ = w.Write([]string{ // 同上
			a.Name,
			fmt.Sprintf("%d", a.SampleCount),
			fmt.Sprintf("%d", a.SuccessCount),
			fmt.Sprintf("%d", a.TimeoutCount),
			fmt.Sprintf("%d", a.FailureCount),
			fmt.Sprintf("%d", a.RTTSampleCount),
			fmt.Sprintf("%.4f", a.SuccessRate),
			fmt.Sprintf("%.2f", a.RTT.AvgMs),
			fmt.Sprintf("%.2f", a.ClientAvgMs),
			fmt.Sprintf("%.2f", a.RTT.MinMs),
			fmt.Sprintf("%.2f", a.RTT.MaxMs),
			fmt.Sprintf("%.2f", a.RTT.P50Ms),
			fmt.Sprintf("%.2f", a.RTT.P90Ms),
			fmt.Sprintf("%.2f", a.RTT.P95Ms),
			fmt.Sprintf("%.2f", a.RTT.P99Ms),
			fmt.Sprintf("%.4f", a.Apdex),
			fmt.Sprintf("%.1f", a.AvgSendBytes),
			fmt.Sprintf("%.1f", a.AvgRecvBytes),
			fmt.Sprintf("%.2f", a.AvgQPS),
			fmt.Sprintf("%.1f", uptimeSec),
		})
	}
	return nil
}
