package standalone

import (
	"os"
	"path/filepath"
	"strconv"
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

// standalone.bot.defaultDelayMs 节点执行间隔契约：不写或 0 = 引擎默认 1000ms；
// -1 与正数合法；-2 与超上限值启动报错，不静默回退。
func TestLoadConfigBotDefaultDelayMs(t *testing.T) {
	t.Run("不写保持零值", func(t *testing.T) {
		cfg, err := LoadConfig(writeConfig(t, minimalConfig))
		if err != nil {
			t.Fatalf("不配置 defaultDelayMs 应正常加载: %v", err)
		}
		if got := cfg.Standalone.Bot.DefaultDelayMs; got != 0 {
			t.Fatalf("defaultDelayMs = %d, want 0", got)
		}
	})

	t.Run("合法值原样解析", func(t *testing.T) {
		for _, raw := range []string{"-1", "0", "500", "600000"} {
			cfg, err := LoadConfig(writeConfig(t, minimalConfig+"defaultDelayMs = "+raw+"\n"))
			if err != nil {
				t.Fatalf("defaultDelayMs = %s 应合法: %v", raw, err)
			}
			want, convErr := strconv.Atoi(raw)
			if convErr != nil {
				t.Fatalf("测试用例 %s 不是整数: %v", raw, convErr)
			}
			if got := cfg.Standalone.Bot.DefaultDelayMs; got != want {
				t.Fatalf("defaultDelayMs = %d, want %d", got, want)
			}
		}
	})

	t.Run("非法值报错", func(t *testing.T) {
		for _, raw := range []string{"-2", "600001"} {
			_, err := LoadConfig(writeConfig(t, minimalConfig+"defaultDelayMs = "+raw+"\n"))
			if err == nil || !strings.Contains(err.Error(), "defaultDelayMs") {
				t.Fatalf("defaultDelayMs = %s 应报错, got %v", raw, err)
			}
		}
	})
}
