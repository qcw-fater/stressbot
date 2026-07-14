package robot

import (
	"os"
	"path/filepath"
	"testing"

	"stressbot/monitor"
	"stressbot/utils"

	stresslog "stressbot/utils/log"
)

// TestMain 初始化全局日志 + monitor collector + WorkPool。
//
// robot 包部分测试（如 dial_resolver_test 的 ConnectTCP/UDP fail-loud）会触达
// network.NewConnection（stresslog.Debug）与 monitor.Global().ConnFailed()，未初始化时
// logger/collector 为 nil 会 panic。写入临时文件，级别 error 保持测试输出静默。
// WorkPool 在 lifecycle 测试中由 Robot.Start 使用（GoWithStop/Go），需提前初始化。
func TestMain(m *testing.M) {
	stresslog.InitLog(filepath.Join(os.TempDir(), "stressbot_robot_test.log"), "test",
		&stresslog.Config{PrintConsole: false, LogLevel: "error"}, "")
	monitor.Init(monitor.CollectorConfig{})
	utils.InitWorkPool(nil)
	os.Exit(m.Run())
}
