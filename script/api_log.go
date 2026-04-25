package script

import (
	"fmt"
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

// logPrefix 生成日志前缀（含机器人 id 和 account）
func logPrefix(id int, account string) string {
	return fmt.Sprintf(" id=%d account=%s", id, account)
}

// logAtLevel 通用日志函数，避免 4 个函数的重复代码。
func logAtLevel(L *lua.LState, logFn func(string, ...zap.Field)) {
	ctx := GetContext(L)
	prefix := ""
	if ctx != nil {
		prefix = logPrefix(ctx.RobotID, ctx.Account)
	}
	logFn("[SCRIPT]"+prefix, zap.String("msg", L.CheckString(1)))
}

func logDebug(L *lua.LState) int { logAtLevel(L, stresslog.Debug); return 0 }
func logInfo(L *lua.LState) int  { logAtLevel(L, stresslog.Info); return 0 }
func logWarn(L *lua.LState) int  { logAtLevel(L, stresslog.Warn); return 0 }
func logError(L *lua.LState) int { logAtLevel(L, stresslog.Error); return 0 }
