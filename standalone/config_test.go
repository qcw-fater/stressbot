package standalone

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalConfig = `
[standalone]
[standalone.bot]
mainService = "logic"
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stressbot.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	return path
}

// monitor.reportInterval 时长契约：留空回退默认；非法或 <=0 启动报错，不静默保持 5s。
func TestLoadConfigMonitorReportIntervalStrict(t *testing.T) {
	t.Run("留空回退默认", func(t *testing.T) {
		if _, err := LoadConfig(writeConfig(t, minimalConfig+"\n[monitor]\nreportInterval = \"\"\n")); err != nil {
			t.Fatalf("留空应回退默认: %v", err)
		}
	})

	t.Run("非法文本报错", func(t *testing.T) {
		_, err := LoadConfig(writeConfig(t, minimalConfig+"\n[monitor]\nreportInterval = \"abc\"\n"))
		if err == nil || !strings.Contains(err.Error(), "monitor.reportInterval") {
			t.Fatalf("期望 reportInterval 报错，得到 %v", err)
		}
	})

	t.Run("零值报错", func(t *testing.T) {
		if _, err := LoadConfig(writeConfig(t, minimalConfig+"\n[monitor]\nreportInterval = \"0s\"\n")); err == nil {
			t.Fatal("reportInterval=0s 应报错（0 会退化成立即重打）")
		}
	})
}

// [network] 段解析进 cfg.Network，未填项保持零值由引擎侧回退默认。
func TestLoadConfigNetworkSection(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, minimalConfig+"\n[network]\nreadBufferCap = 65536\nnumEventLoop = 4\n"))
	if err != nil {
		t.Fatalf("携带 [network] 段应正常加载: %v", err)
	}
	if cfg.Network.ReadBufferCap != 65536 {
		t.Fatalf("readBufferCap = %d, want 65536", cfg.Network.ReadBufferCap)
	}
	if cfg.Network.NumEventLoop != 4 {
		t.Fatalf("numEventLoop = %d, want 4", cfg.Network.NumEventLoop)
	}
	if cfg.Network.WriteBufferCap != 0 || cfg.Network.MaxConcurrentDials != 0 || cfg.Network.MaxBodyLen != 0 {
		t.Fatalf("未填项应保持零值（引擎侧回退默认）: %+v", cfg.Network)
	}
}
