package script

import (
	stresslog "stressbot/utils/log"

	lua "github.com/yuin/gopher-lua"
	"go.uber.org/zap"
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

// logFields 生成机器人上下文的 zap 结构化字段
func logFields(ctx *Context) []zap.Field {
	if ctx == nil {
		return nil
	}
	return []zap.Field{zap.Int("robotID", ctx.RobotID), zap.String("account", ctx.Account)}
}

// logAtLevel 通用日志函数，避免 4 个函数的重复代码。
func logAtLevel(L *lua.LState, logFn func(string, ...zap.Field)) {
	ctx := GetContext(L)
	fields := logFields(ctx)
	fields = append(fields, zap.String("msg", L.CheckString(1)))
	logFn("[SCRIPT]", fields...)
}

func logDebug(L *lua.LState) int { logAtLevel(L, stresslog.Debug); return 0 }
func logInfo(L *lua.LState) int  { logAtLevel(L, stresslog.Info); return 0 }
func logWarn(L *lua.LState) int  { logAtLevel(L, stresslog.Warn); return 0 }
func logError(L *lua.LState) int { logAtLevel(L, stresslog.Error); return 0 }
