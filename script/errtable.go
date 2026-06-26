package script

import (
	"errors"
	"strings"

	"stressbot/engine"
	"stressbot/errcode"

	lua "github.com/yuin/gopher-lua"
)

// newErrTable 构造 err table {code, detail}，不压栈。
func newErrTable(L *lua.LState, code int, detail string) *lua.LTable {
	tb := L.CreateTable(0, 2)
	tb.RawSetString("code", lua.LNumber(code))
	tb.RawSetString("detail", lua.LString(detail))
	return tb
}

// pushErr 构造 err table 并压栈，返回 1。供单返回值 API（connect/send）使用。
func pushErr(L *lua.LState, code int, detail string) int {
	L.Push(newErrTable(L, code, detail))
	return 1
}

// pushResult 压 err + data 两值，返回 2。err 为 lua.LNil（成功）或 table（失败）。
func pushResult(L *lua.LState, err lua.LValue, data lua.LValue) int {
	L.Push(err)
	L.Push(data)
	return 2
}

// parseErrTable 解析栈顶值是否为 err table。ok=true 失败；ok=false 为 nil（成功）或非法值。
func parseErrTable(v lua.LValue) (code int, detail string, ok bool) {
	tb, isTable := v.(*lua.LTable)
	if !isTable {
		return 0, "", false
	}
	codeVal, codeOK := tb.RawGetString("code").(lua.LNumber)
	if !codeOK || codeVal == 0 {
		return 0, "", false
	}
	detailVal, detailOK := tb.RawGetString("detail").(lua.LString)
	if !detailOK {
		return 0, "", false
	}
	return int(codeVal), string(detailVal), true
}

// buildActionError 由 code+detail 构造 *engine.ActionError，补 script= 上下文。
func buildActionError(code int, detail, scriptName string) error {
	full := detail
	if !strings.Contains(full, "script=") {
		if full != "" {
			full += " "
		}
		full += "script=" + scriptName
	}
	return engine.NewActionError(errcode.ErrorCode(code), full)
}

// errTableFromActionErr 从 *engine.ActionError 提取 code+detail 构造 err table（不压栈）。
// 供“网络层已有完整 ActionError”的分支使用。
func errTableFromActionErr(L *lua.LState, err error) *lua.LTable {
	var ae *engine.ActionError
	if errors.As(err, &ae) {
		return newErrTable(L, int(ae.ErrorCode()), ae.ErrorDetail())
	}
	return newErrTable(L, int(errcode.ErrSendFailed), err.Error())
}
