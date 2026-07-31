package script

import (
	stresslog "stressbot/utils/log"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// loadLogModule 加载 log 命名空间模块。
// Lua 用法：
//
//	local log = require("log")
//	log.debug("message")
//	log.info("message")
//	log.warn("message")
//	log.error("message")
func loadLogModule(L *lua.LState) int {
	mod := L.NewTable()
	L.SetField(mod, "debug", L.NewFunction(logDebug))
	L.SetField(mod, "info", L.NewFunction(logInfo))
	L.SetField(mod, "warn", L.NewFunction(logWarn))
	L.SetField(mod, "error", L.NewFunction(logError))
	L.Push(mod)
	return 1
}

// logAtLevel 通用日志函数，避免 4 个函数的重复代码。
//
// 等级判断必须在构造字段之前：交给 zap 内部去判断意味着字段切片已经分配完才被丢弃。
// 旧实现分配两次（logFields 建 len=2 切片，append 追加 msg 时再扩容一次），
// 现在等级关闭直接返回、开启时按变参一次成型。
//
// 能省的只有 Go 侧。脚本里 log.info("roleId=" .. tostring(id)) 的 tostring 与拼接
// 发生在调用本函数之前，由 Lua VM 承担，无从在此干预——要省那部分只能改脚本。
func logAtLevel(L *lua.LState, lvl zapcore.Level, logFn func(string, ...zap.Field)) {
	if !stresslog.LevelEnabled(lvl) {
		return
	}
	msg := L.CheckString(1)
	ctx := GetContext(L)
	if ctx == nil {
		logFn("[SCRIPT]", zap.String("msg", msg))
		return
	}
	logFn("[SCRIPT]",
		zap.Int("robotID", ctx.RobotID),
		zap.String("account", ctx.Account),
		zap.String("msg", msg))
}

func logDebug(L *lua.LState) int { logAtLevel(L, zapcore.DebugLevel, stresslog.Debug); return 0 }
func logInfo(L *lua.LState) int  { logAtLevel(L, zapcore.InfoLevel, stresslog.Info); return 0 }
func logWarn(L *lua.LState) int  { logAtLevel(L, zapcore.WarnLevel, stresslog.Warn); return 0 }
func logError(L *lua.LState) int { logAtLevel(L, zapcore.ErrorLevel, stresslog.Error); return 0 }
