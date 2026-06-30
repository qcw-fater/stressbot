# Lua 错误返回统一为 err table 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 lua action 的错误传递从"int code 返回值 + `lastActionError` 旁路 + 对账"双轨制,统一为"err table 一等返回值"单轨,与声明式 action 的"`执行 → 结构化 ActionError`"同构。

**Architecture:** 两个层面的契约同时改变:
- **层 1 — network.* API 返回值**:从 `code`(number)改为 `err`(nil 或 err table)。出错时网络 API 直接把结构化错误 table 压回 Lua 栈。
- **层 2 — action 脚本顶层返回**:`execute(r)` 从 `return code` 改为 `return nil`(成功)/ `return err table`(失败)。脚本透传 network API 给的同一个 table,或用 `robot.error(code, detail)` 自构。

同一个 table 对象从产生点(网络 API)经脚本一路流到消费点(`RunActionScript` → `executeLuaAction`),中间不被拆成标量。删掉 `errToCode` / `luaCodeToActionErr` / `lastActionError` 三件套 / `remember*Err` / 对账分支等全部"塌缩-重建-旁路"机制。

**Tech Stack:** Go(gopher-lua `github.com/yuin/gopher-lua`,标准库 `testing`,无 testify)+ Lua 5.1 脚本 + React/TS 前端。

## Global Constraints

- **禁止兼容性兜底**(`feedback_no_compat_hacks`):不写迁移函数、不留 `return code` 旧式 fallback。`RunActionScript` 收到非 nil 非 table 的返回值必须 **fail loud**(报错)。新旧契约**不能并存运行**。
- **新字段/新契约全链路一致**:err table 的 schema(`{code, detail}`)在 Go 构造点、Lua 脚本、Go 解析点、**前端 API 文档**四处必须一字不差。
- **Go 字段名与 JSON/Lua key 一致**(项目约定):table 字段用 `code` / `detail`。
- **日志和错误信息用中文**(项目约定)。
- **测试用标准库 `testing` + `errors.As`**,断言范式抄 `engine/action_map_test.go:86-97`;Lua 返回值断言抄 `script/api_json_test.go`。
- **module path**:`stressbot`;lua import 别名 `lua`;errcode 包 `stressbot/errcode`。
- **复用现有 `TestMain`**(`script/runtime_cache_test.go:14`),不重写。
- **commit 用 conventional commits**(项目惯例:`feat:` / `refactor:` / `docs:` + 中文描述)。
- **影响面已确认收敛**:`agent/monitor/admin/engine/network/protox/adapter/codec/state/sharedstate/utils` 对待删符号**零引用**;`codec.lua/error.lua` oracle、`errors.json` 不受影响;boolean 脚本(`check_role/has_guild/is_guild_*`)与 listen 脚本(`onMessage`)内部零 network API 调用。本计划覆盖的全部影响点见 File Structure。

## 设计决定(已定,执行时遵循)

1. **err table schema**:`{code=number, detail=string}`。**不**显式带 `kind`——`kind` 由 `classifyCode(code)` 确定性推导(`code >= 100` → `KindServer`,否则 `KindFramework`),与现有 errcode 约定一致(框架码 <100,服务端码 ≥100)。脚本构造入口 `robot.error(code, detail)` 只需两参。
2. **network.* API 返回值改 err table(层 1)**:这是本次改动的核心,不是只改 action 顶层。前端 `luaApiSpec.ts` 的 network.* 文档**必须跟着改**(曾有一个判断说不用改,那是混淆了层 1 和层 2,错误)。
3. **http_request 纳入改造**:返回值从 `(status, body)` 改为 `(err, status, body)` 三值。框架传输错误 → `err` table;成功拿到响应(任意 status)→ `err=nil` + `status` + `body`。HTTP status 是结果而非框架错误,保留为 number。
4. **不改的 API**:`close_tcp/udp`、`ensure_tcp/udp_listener`(0 返回值且 `NetSender.CloseTCP/EnsureTCPListener` 不返回 error);`set/get_tcp/udp_secret_key`(纯 setter/getter)。
5. **`try_tcp/udp_listen` 的"队列空"**:返回 `(nil, nil)`(成功无消息),不走 err table。只有 codec 未映射 / headerErr 才走 err table。
6. **boolean / listen 脚本不动**:本次只改 action 脚本(`execute(r)` 返回 err/nil)。boolean(`return true/false`)和 listen(`onMessage` 无返回)的返回契约不变,且其内部不调 network API(已排查确认)。
7. **`L.RaiseError` 保留策略**:"network not available" / 参数缺失 / serialize 失败这类**编程错误**保留异常语义;只有"执行了但失败"(网络层错误)走 err table。
8. **保留 54 + 重命名 + 迁移细分三类**:`return 54` 的内容是**三类不同错误**被脚本图省事塞进同一码。垃圾桶的病根是"被滥用 + 语义模糊",不是"码不该存在"——删了只会让第三类(脚本断言失败)无处安放、污染 12 等其他码(换垃圾桶)。正确解法:
   - **保留数值 54**,重命名常量 `ErrLuaExitCode` → `ErrLuaScriptCheck`;registry 名称 `LUA_EXIT_CODE` → `LUA_SCRIPT_CHECK`,描述改为"脚本校验失败(字段缺失/值不符等业务断言)"。原"非零退出码"语义误导,重命名后语义清晰。
   - **迁移细分三类**:① 协议/proto 结构错误 → `ErrParseFailed`(12);② 脚本从响应拿到服务端码的业务失败 → 透传该服务端码(≥100);③ 纯脚本断言失败(字段缺失/值校验,既非协议错误也非服务端码)→ `ErrLuaScriptCheck`(54)。
   - 结果:54 从"什么都塞的大垃圾桶"变成"只放脚本断言的小专桶",12 回归纯协议语义不被污染,服务端业务错误回到服务端码体系。每个码只承载自己的语义。详见 Task 3.3。
9. **迁移原子性**:阶段 3(RunActionScript 契约改 + 脚本迁移 + 测试夹具)必须作为一个提交单元,中间状态不可运行。

## File Structure

**新增(Go):**
- `script/errtable.go` — err table 构造/解析/classifyCode helper
- `script/errtable_test.go`
- `script/api_network_test.go` — network API 错误返回单测(项目原本无)
- `script/api_robot_test.go` — `robot.error` 单测
- `script/fake_netsender_test.go` — 测试用 fake `NetSender` / `CodecResolver`

**修改(Go):**
- `script/api_network.go` — 17 个错误型函数改返回 err table;删 `errToCode`/`rememberActionErr`/`rememberFrameworkErr`/`rememberHeaderErr`/`pushRequestResult`;`resolveDescribeError` 保留
- `script/api_robot.go` — 新增 `robot.error`(注册 `loadRobotModule` + `robotIndex` 两处)
- `script/runtime.go` — `Context` 删 `lastActionError` 及 Set/Last/Clear;`resetMetrics` 删相关行;`RunActionScript` 改解析 err table、签名改返回 `error`
- `script/runtime_cache_test.go` — 3 处 `RunActionScript` caller 适配新签名(:46/:74/:96);2 处内嵌 lua `return 0` 改 `return nil`(:38-39/:66-68)
- `robot/robot.go` — `executeLuaAction` 删对账分支,简化;删 `luaCodeToActionErr`
- `errcode/codes.go` — 重命名 `ErrLuaExitCode`→`ErrLuaScriptCheck`(:54,数值 54 保留);registry 名称 `LUA_EXIT_CODE`→`LUA_SCRIPT_CHECK` + 描述"脚本校验失败"(:97)

**修改(脚本):**
- `conf/scripts/*.lua` — 约 30 个 action 脚本迁移(`return code`→`return err`,`return 0`→`return nil`,`return 54`→按三类细分见 Task 3.3:协议错误归 12、服务端码透传、脚本断言用 54)

**修改(前端 TS):**
- `cmd/web/src/components/FlowEditor/lua/luaApiSpec.ts` — robotModule 补 `error` 条目;network.* 全部函数 returns/detail 改 err table;http_request 改 3 返回值
- `cmd/web/src/components/FlowEditor/editors/ActionEditor/LuaForm.tsx` — `TEMPLATE.action` 骨架(:43-56)、Alert banner(:353-358)、文件头注释(:5-10)
- `cmd/web/src/components/FlowEditor/editors/shared/LuaScriptField.tsx` — `DEFAULT_HELP.action`(:27-33)、文件头注释(:5)
- `cmd/web/src/components/FlowEditor/lua/luaSyntaxWorker.ts` — 文件头注释(:5,可选)

**修改(文档):**
- `CLAUDE.md:69` — "返回 0 表示成功" → "返回 nil/err table"
- `README.md:210` — 动作 pattern 表 lua 行;`:394` — 条件表达式 lua 描述
- `docs/visual-flow-editor.md:657-661` — LuaForm mode 表 action 行
- `docs/flow-node-system.md:622-627` — lua 动作返回码描述
- `docs/monitoring-system.md:1165` — "脚本只返回 code"
- `docs/error-code-system.md:141,402` — ErrLuaExitCode 描述;`:413,416` — network API code 返回模型

**删除(阶段 3 内逐个移除):**
- `script/runtime.go`:`lastActionError` 字段、`SetLastActionError`、`LastActionError`、`ClearLastActionError`
- `script/api_network.go`:`errToCode`、`rememberActionErr`、`rememberFrameworkErr`、`rememberHeaderErr`、`pushRequestResult`
- `robot/robot.go`:`luaCodeToActionErr`

---

## 阶段 1:基础设施(纯新增,自洽可测)

本阶段只新增 helper 和测试,**不触碰**现有 network 函数 / RunActionScript / 脚本。完成后旧路径完全不变,新 helper 尚未被调用。

### Task 1.1:err table helper(`script/errtable.go`)

**Files:**
- Create: `script/errtable.go`
- Test: `script/errtable_test.go`

**Interfaces:**
- Produces:`newErrTable` / `pushErr` / `pushResult` / `parseErrTable` / `classifyCode` / `buildActionError` / `errTableFromActionErr`

- [ ] **Step 1: 写失败测试 `script/errtable_test.go`**

```go
package script

import (
	"errors"
	"strings"
	"testing"

	"stressbot/engine"
	"stressbot/errcode"

	lua "github.com/yuin/gopher-lua"
)

func TestNewErrTable(t *testing.T) {
	L := lua.NewState()
	defer L.Close()
	tb := newErrTable(L, int(errcode.ErrEncodeFailed), "service=logic route=1:2 codec 未映射")
	if got := tb.RawGetString("code"); got != lua.LNumber(int(errcode.ErrEncodeFailed)) {
		t.Fatalf("code = %v, want %d", got, int(errcode.ErrEncodeFailed))
	}
	if got := lua.LVAsString(tb.RawGetString("detail")); got != "service=logic route=1:2 codec 未映射" {
		t.Fatalf("detail = %q", got)
	}
}

func TestParseErrTable(t *testing.T) {
	L := lua.NewState()
	defer L.Close()
	tb := newErrTable(L, 4, "recv 超时")
	if code, detail, ok := parseErrTable(tb); !ok || code != 4 || detail != "recv 超时" {
		t.Fatalf("parse table: ok=%v code=%d detail=%q", ok, code, detail)
	}
	if _, _, ok := parseErrTable(lua.LNil); ok {
		t.Fatalf("LNil 不应解析为 err table")
	}
	if _, _, ok := parseErrTable(lua.LNumber(54)); ok {
		t.Fatalf("LNumber 不应解析为 err table（fail-loud 旧式返回）")
	}
}

func TestClassifyCode(t *testing.T) {
	if classifyCode(4) != errcode.KindFramework { t.Fatal("code=4 应 framework") }
	if classifyCode(54) != errcode.KindFramework { t.Fatal("code=54 应 framework") }
	if classifyCode(1004) != errcode.KindServer { t.Fatal("code=1004 应 server") }
}

func TestBuildActionError(t *testing.T) {
	// 框架码
	err := buildActionError(int(errcode.ErrRecvTimeout), "service=logic route=1:2", "match_succeed.lua")
	var ae *engine.ActionError
	if !errors.As(err, &ae) {
		t.Fatalf("err 不是 *ActionError: %T", err)
	}
	if ae.Kind != errcode.KindFramework { t.Fatalf("kind=%v want framework", ae.Kind) }
	if ae.Code != errcode.ErrRecvTimeout { t.Fatalf("code=%v want %v", ae.Code, errcode.ErrRecvTimeout) }
	if !strings.Contains(ae.Detail, "script=match_succeed.lua") { t.Fatalf("detail 缺 script=: %q", ae.Detail) }
	// 服务端码
	err = buildActionError(1004, "队伍已满: route=CreateTeam", "guild_join.lua")
	if !errors.As(err, &ae) { t.Fatal("server err 不是 *ActionError") }
	if ae.Kind != errcode.KindServer { t.Fatalf("server kind=%v want server", ae.Kind) }
	if ae.Code != errcode.ErrorCode(1004) { t.Fatalf("code=%v want 1004", ae.Code) }
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `go test ./script/ -run TestNewErrTable -v`
Expected: 编译失败(`newErrTable` undefined)

- [ ] **Step 3: 实现 `script/errtable.go`**

```go
package script

import (
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
	code = int(lua.LVAsNumber(tb.RawGetString("code")))
	detail = lua.LVAsString(tb.RawGetString("detail"))
	return code, detail, true
}

// classifyCode 由错误码推导 Kind。框架码 <100，服务端码 >=100。
func classifyCode(code int) errcode.Kind {
	if code >= 100 {
		return errcode.KindServer
	}
	return errcode.KindFramework
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
	if classifyCode(code) == errcode.KindServer {
		return engine.NewServerError(uint64(code), full)
	}
	return engine.NewActionError(errcode.ErrorCode(code), full)
}

// errTableFromActionErr 从 *engine.ActionError 提取 code+detail 构造 err table（不压栈）。
// 供"网络层已有完整 ActionError"的分支使用（替代 rememberActionErr）。
func errTableFromActionErr(L *lua.LState, err error) *lua.LTable {
	if ae, ok := err.(*engine.ActionError); ok {
		return newErrTable(L, int(ae.ErrorCode()), ae.ErrorDetail())
	}
	return newErrTable(L, int(errcode.ErrSendFailed), err.Error())
}
```

- [ ] **Step 4: 跑测试验证通过**

Run: `go test ./script/ -run "TestNewErrTable|TestParseErrTable|TestClassifyCode|TestBuildActionError" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add script/errtable.go script/errtable_test.go
git commit -m "feat(script): 新增 err table 构造/解析 helper"
```

---

### Task 1.2:测试 fixture——fake NetSender / CodecResolver(`script/fake_netsender_test.go`)

**Files:**
- Create: `script/fake_netsender_test.go`

**Interfaces:** 测试内部:`fakeNetSender`(实现 `engine.NetSender`)、`fakeResolver`(实现 `adapter.CodecResolver`)、`newTestState` helper

- [ ] **Step 1: 实现 fixture**

```go
package script

import (
	"context"
	"time"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/state"

	lua "github.com/yuin/gopher-lua"
)

type fakeResolver struct {
	adp adapter.SchemaAdapter
}

func (r *fakeResolver) Resolve(serverKey string) adapter.SchemaAdapter { return r.adp }

type fakeNetSender struct {
	tcpReqExchange *engine.NetExchange
	tcpReqErr      error
	udpReqExchange *engine.NetExchange
	udpReqErr      error
	tcpSendErr     error
	udpSendErr     error
	connectErr     error
	httpExchange   *engine.HTTPExchange
	httpErr        error
	listenResp     *engine.NetExchange
	tcpKey         []byte
	udpKey         []byte
}

func (f *fakeNetSender) TCPSend(string, []byte) (int, error)             { return 0, f.tcpSendErr }
func (f *fakeNetSender) UDPSend(string, []byte) (int, error)             { return 0, f.udpSendErr }
func (f *fakeNetSender) TCPRequest(string, []byte, string, ...time.Duration) (*engine.NetExchange, error) {
	return f.tcpReqExchange, f.tcpReqErr
}
func (f *fakeNetSender) UDPRequest(string, []byte, string, ...time.Duration) (*engine.NetExchange, error) {
	return f.udpReqExchange, f.udpReqErr
}
func (f *fakeNetSender) ConnectTCP(string, string) error                             { return f.connectErr }
func (f *fakeNetSender) ConnectUDP(string, string) error                             { return f.connectErr }
func (f *fakeNetSender) HTTPRequest(string, string, string, []byte) (*engine.HTTPExchange, error) {
	return f.httpExchange, f.httpErr
}
func (f *fakeNetSender) CloseTCP(string)                                            {}
func (f *fakeNetSender) CloseUDP(string)                                            {}
func (f *fakeNetSender) GetTCPListenResp(string, string) *engine.NetExchange        { return f.listenResp }
func (f *fakeNetSender) GetUDPListenResp(string, string) *engine.NetExchange        { return f.listenResp }
func (f *fakeNetSender) EnsureTCPListener(string, string, int)                      {}
func (f *fakeNetSender) EnsureUDPListener(string, string, int)                      {}
func (f *fakeNetSender) RegisterHeartbeat(engine.HeartbeatActionConfig) error       { return nil }
func (f *fakeNetSender) GetTCPSecretKey(string) []byte                              { return f.tcpKey }
func (f *fakeNetSender) SetTCPSecretKey(string, []byte)                             {}
func (f *fakeNetSender) GetUDPSecretKey(string) []byte                              { return f.udpKey }
func (f *fakeNetSender) SetUDPSecretKey(string, []byte)                             {}

// newTestState 注册全部模块 + 注入 fake Context。
func newTestState(t interface{ Helper() }, ctx context.Context, ns engine.NetSender, resolver adapter.CodecResolver) *lua.LState {
	t.Helper()
	L := lua.NewState()
	registerAPIs(L)
	c := &Context{
		RobotID:               1,
		Account:               "test",
		Store:                 state.NewStore(),
		Resolver:              resolver,
		NetSender:             ns,
		Ctx:                   ctx,
		DefaultRequestTimeout: 10 * time.Second,
	}
	SetContext(L, c)
	return L
}

var _ engine.NetSender = (*fakeNetSender)(nil)
var _ adapter.CodecResolver = (*fakeResolver)(nil)
```

> 注:`state.NewStore()` 按 `state` 包实际构造函数核对(可能是 `state.NewStore()` 或 `state.New()`);`Factory` 字段此处不设(测错误分支多数不触达 proto 解析),若测试需 proto 解析再注入 `protox.Factory`。

- [ ] **Step 2: 编译验证**

Run: `go vet ./script/`
Expected: 无 vet 错误

- [ ] **Step 3: Commit**

```bash
git add script/fake_netsender_test.go
git commit -m "test(script): 新增 fake NetSender/Resolver 测试 fixture"
```

---

### Task 1.3:`robot.error` API(`script/api_robot.go`)

**Files:**
- Modify: `script/api_robot.go`(`loadRobotModule:34-47` + `robotIndex:55-88`)
- Test: `script/api_robot_test.go`

**Interfaces:** `robot.error(code, detail)` → err table;Go 侧 `robotError(L) int`

- [ ] **Step 1: 写失败测试 `script/api_robot_test.go`**

```go
package script

import (
	"context"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

func TestRobotError(t *testing.T) {
	L := newTestState(t, context.Background(), &fakeNetSender{}, nil)
	defer L.Close()
	if err := L.DoString(`
		local robot = require("robot")
		err = robot.error(54, "battleId 缺失")
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	tb, ok := L.GetGlobal("err").(*lua.LTable)
	if !ok {
		t.Fatalf("err 不是 table: %T", L.GetGlobal("err"))
	}
	if got := tb.RawGetString("code"); got != lua.LNumber(54) {
		t.Fatalf("code=%v want 54", got)
	}
	if got := lua.LVAsString(tb.RawGetString("detail")); got != "battleId 缺失" {
		t.Fatalf("detail=%q", got)
	}
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `go test ./script/ -run TestRobotError -v`
Expected: FAIL(`robot.error` undefined)

- [ ] **Step 3: 实现 `robotError` 并两处注册**

在 `script/api_robot.go`(`robotKeys` 后)新增:

```go
// robotError 构造 err table 供脚本 return。
// Lua: robot.error(code, detail) → {code=number, detail=string}
func robotError(L *lua.LState) int {
	code := L.CheckInt(1)
	detail := L.CheckString(2)
	L.Push(newErrTable(L, code, detail))
	return 1
}
```

**改 `loadRobotModule`**(api_robot.go:34-47 的 `L.SetField` 区块,`get_context` 后加):

```go
	L.SetField(mod, "error", L.NewFunction(robotError))
```

**改 `robotIndex`**(api_robot.go:55-88 的 switch,`case "get_context":` 后、`default:` 前加):

```go
	case "error":
		L.Push(L.NewFunction(robotError))
```

- [ ] **Step 4: 跑测试验证通过**

Run: `go test ./script/ -run TestRobotError -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add script/api_robot.go script/api_robot_test.go
git commit -m "feat(script): 新增 robot.error 构造 err table"
```

---

## 阶段 2:network API 改造(每个函数 Go 单测自洽)

逐个改造 network 函数,每个配 Go 单测(用 fake NetSender,**不依赖脚本**)。

> **本阶段每个 task 完成后,旧脚本调用这些函数会运行出错(拿到 table 当 number 用),这是预期——脚本迁移在阶段 3。本阶段只保证 Go 单测通过 + `go build` 通过。**
> **本阶段不删 `errToCode`/`remember*Err`/`pushRequestResult`**(阶段 3 删;本阶段随改随删该函数内的调用即可)。
> **`L.RaiseError`("network not available" / 参数缺失 / serialize 失败)保留**(编程错误,设计决定 7)。

### Task 2.1:改造 `doTCPRequest` / `doUDPRequest`(request 系列)

**Files:**
- Modify: `script/api_network.go:420-490`(doTCPRequest)、`545-617`(doUDPRequest)、删 `pushRequestResult:84-88`
- Test: `script/api_network_test.go`

**Interfaces:** `doTCPRequest/doUDPRequest` 返回 `(err, data)` 两值,err 为 `lua.LNil`(成功)或 err table(失败)

- [ ] **Step 1: 写失败测试 `script/api_network_test.go`**

```go
package script

import (
	"context"
	"testing"

	lua "github.com/yuin/gopher-lua"
)

func TestTCPRequest_EncodeFailure_ReturnsErrTable(t *testing.T) {
	// resolver nil → buildPacket/Resolve 命中 encode 失败分支
	ns := &fakeNetSender{}
	L := newTestState(t, context.Background(), ns, nil) // resolver=nil
	defer L.Close()
	if err := L.DoString(`
		local network = require("network")
		e, d = network.tcp_request("logic", {cmd=1,act=1}, nil, "Game.X", 5)
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	errLV := L.GetGlobal("e")
	tb, ok := errLV.(*lua.LTable)
	if !ok {
		t.Fatalf("err 不是 table（resolver nil 应命中 encode 失败）: %T", errLV)
	}
	if int(lua.LVAsNumber(tb.RawGetString("code"))) == 0 {
		t.Fatalf("code 不应为 0")
	}
}
```

> **测试范围说明**:fake `SchemaAdapter` 的 encode 实现较重,本 task 只测 `ErrEncodeFailed` 分支(resolver nil)证明 err table 压栈链路通。recv 超时 / headerErr / parse 分支留给阶段 6 端到端验证覆盖(codec 同源已在声明式侧测过)。执行者若要补 recv 分支,需新增 `fakeSchemaAdapter`(实现 `EncodeTCP`/`ExpectedRouteKey`/`DescribeError`)注入。

- [ ] **Step 2: 跑测试验证失败**

Run: `go test ./script/ -run TestTCPRequest -v`
Expected: FAIL(仍返回 number)

- [ ] **Step 3: 删旧 `pushRequestResult`(api_network.go:84-88),重写 `doTCPRequest`**

```go
func doTCPRequest(L *lua.LState, ctx *Context, service string, requestRoute, responseRoute lua.LValue, msg proto.Message, s2cProto string, timeout int) int {
	msgData, err := serializeMsg(ctx, msg)
	if err != nil {
		L.RaiseError("serialize failed: %v", err) // 编程错误，保留
		return 0
	}

	var encodeStart time.Time
	if ctx.TimingLevel >= engine.TimingLevelCodec {
		encodeStart = time.Now()
	}
	packet := buildPacket(ctx, service, requestRoute, msgData)
	if ctx.TimingLevel >= engine.TimingLevelCodec && !encodeStart.IsZero() {
		ctx.recordClientTiming(engine.ClientTiming{EncodeCost: time.Since(encodeStart)})
	}
	if packet == nil {
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), "service="+service+" codec 未映射"), lua.LNil)
	}

	tcpAdp := ctx.Resolver.Resolve("tcp:" + service)
	if tcpAdp == nil {
		return pushResult(L, newErrTable(L, int(errcode.ErrEncodeFailed), "service="+service+" routeKey 解析失败（codec 未映射）"), lua.LNil)
	}
	routeKey := tcpAdp.ExpectedRouteKey(luaValueToRoute(responseRoute))
	pktLen := len(packet)

	exchange, reqErr := ctx.NetSender.TCPRequest(service, packet, routeKey, time.Duration(timeout)*time.Second)
	if exchange == nil {
		exchange = &engine.NetExchange{SendWireBytes: pktLen}
	}
	ctx.recordRequest(exchange.Timing)
	ctx.recordBytes(exchange.SendWireBytes, exchange.RecvWireBytes)
	respBody := exchange.Body

	if ctx.Ctx != nil && ctx.Ctx.Err() != nil {
		return pushResult(L, newErrTable(L, int(errcode.ErrActionCanceled), "service="+service+" route="+routeKey), lua.LNil)
	}
	if reqErr != nil {
		return pushResult(L, errTableFromActionErr(L, reqErr), lua.LNil)
	}
	if exchange.HeaderErr != 0 {
		desc := resolveDescribeError(ctx, "tcp:"+service, exchange.HeaderErr)
		detail := "service=" + service + " route=" + routeKey
		if desc != "" {
			detail = desc + ": " + detail
		}
		return pushResult(L, newErrTable(L, int(exchange.HeaderErr), detail), lua.LString(string(respBody)))
	}

	if s2cProto != "" && ctx.Factory != nil && len(respBody) > 0 {
		var parseStart time.Time
		if ctx.TimingLevel >= engine.TimingLevelFull {
			parseStart = time.Now()
		}
		respMsg, err := ctx.Factory.Parse(s2cProto, respBody)
		if ctx.TimingLevel >= engine.TimingLevelFull && !parseStart.IsZero() {
			ctx.recordClientTiming(engine.ClientTiming{ParseStoreCost: time.Since(parseStart)})
		}
		if err != nil {
			return pushResult(L, newErrTable(L, int(errcode.ErrParseFailed), "service="+service+" route="+routeKey), lua.LString(string(respBody)))
		}
		return pushResult(L, lua.LNil, wrapProtoMessage(L, respMsg))
	}

	return pushResult(L, lua.LNil, lua.LString(string(respBody)))
}
```

**`doUDPRequest`(api_network.go:545-617)同模式改造**:逐分支替换(encode 失败×2 / cancel / reqErr / headerErr / parse / 成功×2),结构对称。

- [ ] **Step 4: 跑测试验证通过**

Run: `go test ./script/ -run TestTCPRequest -v && go build ./...`
Expected: PASS + 编译通过

- [ ] **Step 5: Commit**

```bash
git add script/api_network.go script/api_network_test.go script/errtable.go script/errtable_test.go
git commit -m "refactor(script): tcp/udp request 改返回 err table"
```

---

### Task 2.2:改造 `networkTCPSend` / `networkUDPSend`(单返回值)

**Files:**
- Modify: `script/api_network.go:710-760`(TCP)、`768-822`(UDP)
- Test: `script/api_network_test.go`

**Interfaces:** `tcp_send/udp_send` 返回 1 值:`lua.LNil`(成功)或 err table(失败)

- [ ] **Step 1: 写失败测试**(类似 Task 2.1,`network.tcp_send(...)`,resolver nil 命中 encode,断言 e 是 table)
- [ ] **Step 2: 跑测试验证失败**

- [ ] **Step 3: 改造 `networkTCPSend`(api_network.go:710-760),对照原函数逐点替换**

| 原行 | 原代码 | 替换为 |
|---|---|---|
| 713/719/725 | `L.RaiseError(...)` | **保留**(编程错误) |
| 738-740 encode 失败 | `rememberFrameworkErr(...); L.Push(LNumber(ErrEncodeFailed))` | `return pushErr(L, int(errcode.ErrEncodeFailed), "service="+service+" codec 未映射")` |
| 753 成功 | `L.Push(lua.LNumber(0))` | `L.Push(lua.LNil)`(后续 `return 1` 不变) |
| 756-757 send 失败 | `rememberActionErr(ctx, err); L.Push(LNumber(errToCode(err)))` | `return pushErr(L, int(errcode.ErrSendFailed), err.Error())` |

`networkUDPSend`(api_network.go:768-822)同模式:codec 未映射(786-788)+ packet nil(801-803)→ `pushErr`;成功(815)`L.Push(0)`→`L.Push(LNil)`;send 失败(818-819)→ `pushErr`。

- [ ] **Step 4: 跑测试验证通过**

Run: `go test ./script/ -run TestTCPSend -v && go build ./...`
Expected: PASS + 编译通过

- [ ] **Step 5: Commit**

```bash
git add script/api_network.go script/api_network_test.go
git commit -m "refactor(script): tcp/udp send 改返回 err table"
```

---

### Task 2.3:改造 `networkListen`(阻塞监听,2 返回值)

**Files:**
- Modify: `script/api_network.go:845-955`
- Test: `script/api_network_test.go`

**Interfaces:** `tcp_listen/udp_listen` 返回 `(err, data)` 两值

- [ ] **Step 1: 写失败测试**(`network.tcp_listen("logic",{cmd=1,act=1},"Game.X",1,50)`,listenResp nil 超时,断言 e 是 table)
- [ ] **Step 2: 跑测试验证失败**

- [ ] **Step 3: 改造 `networkListen`(api_network.go:845-955),逐分支替换**

| 原行 | 替换为 |
|---|---|
| 848 "network not available" | 保留 RaiseError |
| 857-860 encode 失败 | `return pushResult(L, newErrTable(L, int(ErrEncodeFailed), "service="+service+" codec 未映射"), LNil)` |
| 917-920 cancel | `return pushResult(L, newErrTable(L, int(ErrActionCanceled), "service="+service), LNil)` |
| 926-930 listen 超时 | `return pushResult(L, newErrTable(L, int(ErrListenTimeout), "service="+service+" route="+routeKey), LNil)` |
| 933-936 headerErr | `resolveDescribeError` 拼 detail;`return pushResult(L, newErrTable(L, int(HeaderErr), detail), LString(respBody))` |
| 942-945 parse 失败 | `return pushResult(L, newErrTable(L, int(ErrParseFailed), "service="+service), LNil)` |
| 947-948 / 952-953 成功 | `return pushResult(L, lua.LNil, wrapProtoMessage/​LString)` |

`select { time.After / ctx.Done }` 轮询结构保留(它的 ctx 响应性比声明式好)。

- [ ] **Step 4: 跑测试验证通过**

Run: `go test ./script/ -run TestTCPListen -v && go build ./...`
Expected: PASS + 编译通过

- [ ] **Step 5: Commit**

```bash
git add script/api_network.go script/api_network_test.go
git commit -m "refactor(script): tcp/udp listen 改返回 err table"
```

---

### Task 2.4:改造 `networkTryListen`(非阻塞,2 返回值)

**Files:**
- Modify: `script/api_network.go:982-1030`
- Test: `script/api_network_test.go`

**Interfaces:** `try_tcp/udp_listen` 返回 `(err, data)`。**队列空**返回 `(nil, nil)`(设计决定 5)

- [ ] **Step 1-5 同前模式**:
  - 995-998 encode 失败 → `pushResult(L, newErrTable(...), LNil)`
  - **1012-1014 队列空**(原不调 remember)→ `pushResult(L, lua.LNil, lua.LNil)`(**成功无消息**)
  - 1021-1024 headerErr → `pushResult(L, newErrTable(L, int(HeaderErr), detail), LString(respBody))`
  - 1027-1028 成功 → `pushResult(L, lua.LNil, LString(respBody))`

```bash
git add script/api_network.go script/api_network_test.go
git commit -m "refactor(script): try_tcp/udp_listen 改返回 err table"
```

---

### Task 2.5:改造 `networkConnectTCP` / `networkConnectUDP`(单返回值)

**Files:**
- Modify: `script/api_network.go:271-297`(TCP)、`304-330`(UDP)
- Test: `script/api_network_test.go`

**Interfaces:** `connect_tcp/udp` 返回 1 值:`lua.LNil` 或 err table

- [ ] **Step 1-5**:
  - 280-282 / 286-288 cancel → `return pushErr(L, int(ErrActionCanceled), "service="+service+" address="+address)`
  - 291-292 `rememberActionErr(err) + L.Push(LNumber(errToCode(err)))` → `L.Push(errTableFromActionErr(L, err)); return 1`
  - 成功 294 `L.Push(LNumber(0))` → `L.Push(lua.LNil); return 1`
  - "network not available" RaiseError 保留

connectUDP 同模式。

```bash
git add script/api_network.go script/api_network_test.go
git commit -m "refactor(script): connect_tcp/udp 改返回 err table"
```

---

### Task 2.6:改造 `networkHTTPRequest`(3 返回值)

**Files:**
- Modify: `script/api_network.go:625-698`
- Test: `script/api_network_test.go`

**Interfaces:** `http_request` 返回 `(err, status, body)` 三值

- [ ] **Step 1: 写失败测试**

```go
func TestHTTPRequest_Success_ReturnsNilErrAndStatus(t *testing.T) {
	ns := &fakeNetSender{httpExchange: &engine.HTTPExchange{StatusCode: 404, Body: "not found"}}
	L := newTestState(t, context.Background(), ns, nil)
	defer L.Close()
	if err := L.DoString(`
		local network = require("network")
		e, s, b = network.http_request("http://x/notice", "POST", "form", "a=1")
	`); err != nil {
		t.Fatalf("lua error: %v", err)
	}
	if L.GetGlobal("e") != lua.LNil { t.Fatalf("成功 err 应 nil: %T", L.GetGlobal("e")) }
	if got := L.GetGlobal("s"); got != lua.LNumber(404) { t.Fatalf("status=%v want 404", got) }
	if got := lua.LVAsString(L.GetGlobal("b")); got != "not found" { t.Fatalf("body=%q", got) }
}
```

- [ ] **Step 2: 跑验证失败 → Step 3 改造**
  - 成功(695-696):`L.Push(lua.LNil); L.Push(lua.LNumber(status)); L.Push(lua.LString(body)); return 3`
  - 框架错误(689-692):`L.Push(errTableFromActionErr(L, err)); L.Push(lua.LNumber(0)); L.Push(lua.LString("")); return 3`
  - url 缺失 / json marshal RaiseError 保留

- [ ] **Step 4: 跑测试验证通过**

Run: `go test ./script/ -run TestHTTPRequest -v && go build ./...`
Expected: PASS + 编译通过

- [ ] **Step 5: Commit**

```bash
git add script/api_network.go script/api_network_test.go
git commit -m "refactor(script): http_request 改返回 (err, status, body)"
```

---

## 阶段 3:执行链改造 + 脚本迁移 + 测试夹具(原子)

> **原子单元**:`RunActionScript` 契约改 + `executeLuaAction` 简化 + 脚本迁移 + 测试夹具迁移 + 删除旧机制,一起完成。中间不可运行。

### Task 3.1:改造 `RunActionScript`(解析 err table)

**Files:**
- Modify: `script/runtime.go:390-416`
- Test: `script/runtime_cache_test.go`

**Interfaces:** `RunActionScript` 签名从 `(code, send, recv int, timing, err)` 改为 `(send, recv int, timing, err)`

- [ ] **Step 1: 在 `script/runtime_cache_test.go` 末尾补测试**

```go
func TestRunActionScript_ErrTableBecomesActionError(t *testing.T) {
	rp := newPoolWithScript(t, "fail.lua", `
		local robot = require("robot")
		function execute(r)
			return robot.error(54, "battleId 缺失")
		end
	`)
	L := rp.Acquire()
	defer rp.Release(L)
	_, _, _, err := rp.RunActionScript(L, "fail.lua")
	if err == nil { t.Fatal("want error") }
	var ae *engine.ActionError
	if !errors.As(err, &ae) { t.Fatalf("err 不是 *ActionError: %T", err) }
	if ae.Code != errcode.ErrLuaScriptCheck { t.Fatalf("code=%v want 54", ae.Code) }
	if !strings.Contains(ae.Detail, "battleId 缺失") { t.Fatalf("detail 缺原因: %q", ae.Detail) }
	if !strings.Contains(ae.Detail, "script=fail.lua") { t.Fatalf("detail 缺 script=: %q", ae.Detail) }
}

func TestRunActionScript_NilIsSuccess(t *testing.T) {
	rp := newPoolWithScript(t, "ok.lua", `function execute(r) return nil end`)
	L := rp.Acquire()
	defer rp.Release(L)
	if _, _, _, err := rp.RunActionScript(L, "ok.lua"); err != nil {
		t.Fatalf("want nil err, got %v", err)
	}
}

func TestRunActionScript_LegacyReturnCodeFailsLoud(t *testing.T) {
	rp := newPoolWithScript(t, "legacy.lua", `function execute(r) return 0 end`)
	L := rp.Acquire()
	defer rp.Release(L)
	if _, _, _, err := rp.RunActionScript(L, "legacy.lua"); err == nil {
		t.Fatal("旧式 return code 必须 fail loud")
	}
}
```

> 注:`rp.Acquire()`/`rp.Release(L)` 按 `RuntimePool` 实际 API 核对(参考 `runtime_cache_test.go` 现有用法);import 补 `errors`/`strings`/`stressbot/engine`/`stressbot/errcode`。

- [ ] **Step 2: 跑测试验证失败**

Run: `go test ./script/ -run TestRunActionScript -v`
Expected: FAIL(签名不匹配)

- [ ] **Step 3: 重写 `RunActionScript`(runtime.go:390-416)**

```go
// RunActionScript 执行动作脚本。
// 脚本 function execute(r) 返回：nil（成功）/ err table {code, detail}（失败）。
// 旧式 return code（number）已废弃，收到时 fail loud（禁止兼容兜底）。
func (rp *RuntimePool) RunActionScript(L *lua.LState, scriptName string) (send, recv int, timing engine.ActionTiming, err error) {
	ctx := GetContext(L)
	if ctx != nil {
		ctx.resetMetrics()
		defer func() { send, recv, timing = ctx.metrics() }()
	}

	savedTop := L.GetTop()
	defer L.SetTop(savedTop)

	executeFn, lerr := rp.loadScriptFn(L, scriptName, "execute")
	if lerr != nil {
		return 0, 0, engine.ActionTiming{}, lerr
	}

	robotUD := createRobotUserData(L)
	if cerr := L.CallByParam(lua.P{Fn: executeFn, NRet: 1, Protect: true}, robotUD); cerr != nil {
		return 0, 0, engine.ActionTiming{}, fmt.Errorf("执行脚本 %s 失败: %w", scriptName, cerr)
	}

	ret := L.Get(savedTop + 1)
	if code, detail, isErr := parseErrTable(ret); isErr {
		return send, recv, timing, buildActionError(code, detail, scriptName)
	}
	if ret != lua.LNil {
		return send, recv, timing, fmt.Errorf("脚本 %s 返回非法值 %s：须返回 nil（成功）或 err table（失败），旧式 return code 已废弃", scriptName, ret.String())
	}
	return send, recv, timing, nil
}
```

- [ ] **Step 4: 跑测试验证通过**

Run: `go test ./script/ -run TestRunActionScript -v`
Expected: 三个测试全 PASS

- [ ] **Step 5: Commit**

```bash
git add script/runtime.go script/runtime_cache_test.go
git commit -m "refactor(script): RunActionScript 改解析 err table，签名返回 error"
```

---

### Task 3.2:简化 `executeLuaAction`(删对账)+ 迁移测试夹具

**Files:**
- Modify: `robot/robot.go:620-659`、`script/runtime_cache_test.go:38-39,46,66-68,74,96`

- [ ] **Step 1: 重写 `executeLuaAction`(robot.go:620-659)**

```go
func (h *robotActionHandler) executeLuaAction(actionDef *engine.ActionDef) (int, int, engine.ActionTiming, time.Duration, error) {
	if h.robot.l == nil || h.robot.luaPool == nil {
		stresslog.Error("[ROBOT] Lua 运行时未初始化，无法执行脚本",
			zap.Int("id", h.robot.id), zap.String("account", h.robot.account), zap.String("script", actionDef.Script))
		return 0, 0, engine.ActionTiming{}, 0, engine.NewActionError(errcode.ErrLuaNotInit, "")
	}
	if actionDef.Script == "" {
		stresslog.Error("[ROBOT] 脚本名为空，无法执行",
			zap.Int("id", h.robot.id), zap.String("account", h.robot.account), zap.String("action", actionDef.Name))
		return 0, 0, engine.ActionTiming{}, 0, engine.NewActionError(errcode.ErrLuaNoScript, "")
	}

	start := time.Now()
	send, recv, timing, err := h.robot.luaPool.RunActionScript(h.robot.l, actionDef.Script)
	wallClock := time.Since(start)
	if err != nil {
		var actionErr *engine.ActionError
		if errors.As(err, &actionErr) {
			// 补 action 名（声明式侧 detail 都带 action=，lua 侧出口补齐对齐 A 组 #7）
			if actionErr.Detail != "" && !strings.Contains(actionErr.Detail, "action=") {
				actionErr.Detail = "action=" + actionDef.Name + " " + actionErr.Detail
			}
			return send, recv, timing, wallClock, actionErr
		}
		return send, recv, timing, wallClock, engine.NewActionError(errcode.ErrLuaExecFailed, "script="+actionDef.Script, err)
	}
	return send, recv, timing, wallClock, nil
}
```

> `errors`/`strings` robot.go 已 import。补 `action=` 解决 A 组 #7。`actionErr.Detail` 是可写字段,直接赋值合法。

- [ ] **Step 2: 迁移 `runtime_cache_test.go` 的 3 处 caller + 2 处内嵌 lua**

caller(:46/:74/:96):去 code → `_, _, _, err := rp.RunActionScript(...)`(参数从 5 元组改 4 元组)。
内嵌 lua(:38-39/:66-68):`function execute(r) return 0 end` → `function execute(r) return nil end`(否则新 RunActionScript fail loud)。

- [ ] **Step 3: 编译验证**

Run: `go build ./... && go test ./script/ -run "TestRunActionScript|TestCache" -v`
Expected: 编译通过,测试 PASS

- [ ] **Step 4: Commit**

```bash
git add robot/robot.go script/runtime_cache_test.go
git commit -m "refactor(robot): executeLuaAction 删对账；runtime_cache_test 适配新签名"
```

---

### Task 3.3:迁移所有 action 脚本(批量,机械)

**Files:** `conf/scripts/` 下全部 action 脚本

**迁移规则(逐行套用):**

| 旧写法 | 新写法 |
|---|---|
| `local code = network.<api>(...)` | `local err = network.<api>(...)` |
| `local code, resp = network.tcp_request(...)` | `local err, resp = network.tcp_request(...)` |
| `local code, body = network.http_request(...)` | `local err, status, body = network.http_request(...)` |
| `if code ~= 0 then return code end` | `if err then return err end` |
| `if code ~= 0 then log.error(...); return code end` | `if err then log.error(..., err.code, err.detail); return err end` |
| `if code == 4 then ... end` | `if err and err.code == 4 then ... end` |
| `return code`(透传变量) | `return err` |
| `return 0` | `return nil` |
| `return 54`(业务失败裸码) | 按下"三类细分指南"判断类别:`return robot.error(<对应码>, "<具体原因>: <上下文>")` |
| `return failCode`(变量) | `return robot.error(failCode, "<动作> 失败")` |
| `local failCode = code or 3; return failCode` | `local c = (err and err.code) or 3; return robot.error(c, "<动作> 失败")` |

**`return 54` 三类细分指南**(设计决定 8:54 保留重命名为 `ErrLuaScriptCheck`,迁移时把三类错误拆开,而非倒进同一个新桶):

每个 `return 54` **必须读上下文判断属于哪类**,按下表选码:

| 类别 | 失败性质 | 映射码 | 典型脚本 |
|---|---|---|---|
| ① 协议错误 | proto.Parse 失败 / 消息结构不对 / 协议层异常 | `errcode.ErrParseFailed`(12) | connect_logic.lua:29、connect_battle_tcp.lua:50、match_succeed.lua:48 |
| ② 服务端业务错误 | 脚本从响应读到服务端错误码,判定业务失败(登录/创角/选角结果不对) | 透传该服务端码(≥100):`robot.error(服务端码, ...)` | post_login.lua、new_role.lua 里能拿到服务端 result code 的分支 |
| ③ 脚本断言失败 | 字段缺失/为空、值校验不通过等纯脚本逻辑判定(既非协议错误也非服务端码) | `errcode.ErrLuaScriptCheck`(54,重命名后) | match_succeed.lua:57、listen_start_loading.lua:108/124、post_login.lua/new_role.lua 里无服务端码的校验分支 |

> **关键纪律**:不能把三类都倒进同一个码(那是换垃圾桶,等于白改)。① 回 12(协议语义)、② 回服务端码体系、③ 才用 54。这样 54 从"什么都塞"变小专桶(只放脚本断言),12 不被污染。每个 `return 54` 要 Read 上下文判断类别,这是迁移里最需人工判断的部分,grep 清单见 Step 1。

**before/after 例子(`conf/scripts/match_succeed.lua`):**

```lua
-- 旧
local code, resp = network.tcp_listen("logic", {cmd=2, act=6}, "Game.MatchSucceedS2C", 60, 500)
if code ~= 0 then log.error("...code="..tostring(code)); return code end
if not battleId then return 54 end
return 0

-- 新
local err, resp = network.tcp_listen("logic", {cmd=2, act=6}, "Game.MatchSucceedS2C", 60, 500)
if err then log.error("...code="..tostring(err.code).." detail="..tostring(err.detail)); return err end
if not battleId then return robot.error(54, "MatchSucceed battleId 缺失: roleId="..tostring(roleId)) end
return nil
```

**待迁移 action 脚本清单:**

```
battle_end.lua  connect_battle_tcp.lua  connect_battle_udp.lua  connect_logic.lua
guild_appoint_member.lua  guild_audit_join.lua  guild_exit.lua  guild_join.lua  guild_kick_member.lua
hero_activate_talent.lua  hero_equipment_talent.lua  listen_start_loading.lua  load_progress.lua
match_succeed.lua  new_role.lua  post_login.lua
ranked_member_wait_match.lua  ranked_start_match.lua  ranked_team_cleanup.lua  ranked_team_prepare.lua  ranked_team_reset.lua
register_battle.lua  request_notice.lua(http,3返回值)  request_outside_notice.lua(http,3返回值)
request_player_data.lua  request_zone_list.lua  select_role.lua  sync_frame_data.lua  system_shop_buy.lua  unlock_basic_functions.lua
```

> **不动**:boolean(`check_role/has_guild/is_guild_leader/is_guild_manager`)、listen(`listen_guild_update/join/kick_member/member_update`)。
> **`listen_start_loading.lua`** 函数名 execute 且返回 code,是 action 脚本(虽名 listen),要迁移。
> **`return 54` 补 detail 是唯一人工判断点**:grep `return 54` 找全(约 15-20 处:`post_login`/`new_role`/`match_succeed`/`select_role`/`register_battle`/`request_zone_list` 等),逐个据上下文 log 补"为什么失败"。

- [ ] **Step 1: 逐个脚本迁移**(套规则表)
- [ ] **Step 2: grep 验证无遗漏**

Run:
```bash
grep -rn "return 0$\|return 54$\|return code$\|return failCode$" conf/scripts/
grep -rn "local code = network\.\|local code, .* = network\.\|code ~= 0" conf/scripts/
```
Expected: 空输出(命中说明漏改)。注意排除 `ranked_team_prepare.lua` 等可能的 `return 1` 凭空捏造码,也要改。

- [ ] **Step 3: Commit**

```bash
git add conf/scripts/
git commit -m "refactor(scripts): action 脚本迁移到 err table 返回契约"
```

---

### Task 3.4:删除旧机制

**Files:** `script/runtime.go`、`script/api_network.go`、`robot/robot.go`

- [ ] **Step 1: 删除**

1. `script/runtime.go`:`Context.lastActionError` 字段(:68)、`SetLastActionError`(:140-147)、`LastActionError`(:150-157)、`ClearLastActionError`(:160-167)、`resetMetrics` 内 `c.lastActionError = nil` 行(:80)
2. `script/api_network.go`:`errToCode`(:93-98)、`rememberActionErr`(:100-107)、`rememberFrameworkErr`(:109-114)、`rememberHeaderErr`(:139-155)。保留 `resolveDescribeError`(:124-137)
3. `robot/robot.go`:`luaCodeToActionErr`(:677-695)

- [ ] **Step 2: 编译 + vet 验证**

Run: `go build ./... && go vet ./script/... ./robot/...`
Expected: 编译通过,无 vet 告警

- [ ] **Step 3: grep 验证无残留引用**

Run:
```bash
grep -rn "lastActionError\|SetLastActionError\|LastActionError\|ClearLastActionError\|errToCode\|rememberActionErr\|rememberFrameworkErr\|rememberHeaderErr\|luaCodeToActionErr\|pushRequestResult" script/ robot/ engine/ agent/
```
Expected: 空输出

- [ ] **Step 4: 跑全量 script 包测试**

Run: `go test ./script/... ./robot/... -v`
Expected: 全 PASS

- [ ] **Step 5: Commit**

```bash
git add script/runtime.go script/api_network.go robot/robot.go
git commit -m "refactor: 删除 lua 错误双轨旧机制（lastActionError/errToCode/luaCodeToActionErr）"
```

---

## 阶段 4:前端契约同步

> network API 返回契约变了(层 1),前端 API 文档(`luaApiSpec.ts`)必须同步;action 顶层返回契约变了(层 2),脚本编辑器模板/文案(`LuaForm.tsx`/`LuaScriptField.tsx`)必须同步。

### Task 4.1:同步 `luaApiSpec.ts`(API hover/completion 文档)

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/lua/luaApiSpec.ts`

- [ ] **Step 1: robotModule 补 `error` 条目**

在 `robotModule.functions`(:44-152 区间,`get_context` 后)追加:

```ts
{
  name: 'error',
  module: 'robot',
  params: [
    { name: 'code', type: 'number', doc: '错误码（框架码 <100 / 服务端码 ≥100）' },
    { name: 'detail', type: 'string', doc: '错误详情（业务上下文）' },
  ],
  returns: 'err table {code, detail}',
  summary: '构造 err table，用于 action 脚本 return err',
  example: 'return robot.error(54, "battleId 缺失")',
},
```

- [ ] **Step 2: network.* 返回描述全部改 err table**

对照原 `luaApiSpec.ts`,逐个改(每个函数的 `returns` + `detail`):

| 函数 | 旧 returns | 新 returns | detail 改 |
|---|---|---|---|
| `connect_tcp`/`connect_udp`(:165-178) | `code : number` | `err : nil\|table` | `err=nil 成功；err table 失败（code=6 取消 / 2 连接关闭 / ...）` |
| `tcp_request`/`tcp_request_route`(:204-235) | `code, data` | `err, data : (nil\|table, ...\|nil)` | `err=nil 成功；err table 失败（含 code/detail）` |
| `udp_request`/`udp_request_route`(:233-261) | `code, data` | `err, data` | 同上 |
| `tcp_send`/`udp_send`(:245-275) | `code` | `err : nil\|table` | 同上 |
| `tcp_listen`/`udp_listen`(:273-291) | `code, data` | `err, data` | 同上 |
| `try_tcp_listen`/`try_udp_listen` | `code, data` | `err, data` | `err=nil 成功（可能无消息 data=nil）；err table 失败` |
| `http_request`(:294-304) | `status_code, body` | `err, status, body : (nil\|table, number, string)` | `err=nil 表示拿到响应（status 是 HTTP 状态码）；err table 表示框架传输错误` |

> **不改**:`close_tcp/udp`、`ensure_tcp/udp_listener`(0 返回值)、`set/get_tcp/udp_secret_key`。

- [ ] **Step 3: 前端编译验证**

Run: `cd cmd/web && npx tsc -b`
Expected: 无类型错误

- [ ] **Step 4: Commit**

```bash
git add cmd/web/src/components/FlowEditor/lua/luaApiSpec.ts
git commit -m "docs(web): luaApiSpec 同步 err table 契约 + robot.error"
```

---

### Task 4.2:同步 `LuaForm.tsx` 模板/文案 + `LuaScriptField.tsx`

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/editors/ActionEditor/LuaForm.tsx`、`editors/shared/LuaScriptField.tsx`

- [ ] **Step 1: 改 `LuaForm.tsx:43-56` 的 `TEMPLATE.action`**

```ts
action: `-- script_name.lua
local network = require('network')
local robot = require('robot')

function execute(r)
  -- TODO: 业务逻辑
  -- 示例：
  -- local err, resp = network.tcp_request('logic', {cmd=1, act=2}, msg, 'Game.SomeS2C')
  -- if err then return err end
  return nil  -- 成功；失败时 return robot.error(code, 'detail') 或透传 err
end
`,
```

- [ ] **Step 2: 改 `LuaForm.tsx:353-358` Alert banner**

```tsx
{mode === 'action' && (
  <>
    ；<code>return nil</code>（成功）/ <code>return err table</code>（失败）
  </>
)}
```

(boolean :359-363 / listen :364-368 分支**不变**)

- [ ] **Step 3: 改 `LuaScriptField.tsx:27-33` 的 `DEFAULT_HELP.action`**

```tsx
action: (
  <>
    入口 <code>function execute(r)</code>，返回 <code>nil</code>（成功）/ <code>err table</code>（失败）。
    点「编辑」按钮可在编辑器里直接写脚本，按 Ctrl+S 保存到本地。
  </>
),
```

(listen :34-40 / boolean :41-46 不变)

- [ ] **Step 4: 顺手改注释(可选,保持一致)**

`LuaForm.tsx:5-10`、`LuaScriptField.tsx:5`、`luaSyntaxWorker.ts:5` 文件头注释的 `return code` → `return nil / err table`。

- [ ] **Step 5: 前端编译 + 测试**

Run: `cd cmd/web && npx tsc -b && npm run test`
Expected: 无类型错误;Vitest 全过

> 注:`luaEntryCheck.test.ts` 里的 `return 0` 样本只测入口识别,不关心返回值,可改可不改(建议顺手改 `return nil` 保持一致)。

- [ ] **Step 6: Commit**

```bash
git add cmd/web/src/components/FlowEditor/editors/ActionEditor/LuaForm.tsx \
        cmd/web/src/components/FlowEditor/editors/shared/LuaScriptField.tsx \
        cmd/web/src/components/FlowEditor/lua/luaSyntaxWorker.ts
git commit -m "docs(web): LuaForm 模板/文案同步 err table 契约"
```

---

## 阶段 5:errcode 描述 + 文档同步

### Task 5.1:重命名 `ErrLuaExitCode` → `ErrLuaScriptCheck`(保留数值 54)

**Files:**
- Modify: `errcode/codes.go:54`(常量声明)、`:97`(codeRegistry 项)

> **执行顺序依赖**:本 task 在 Task 3.4(删除 `robot.luaCodeToActionErr`)之后。`ErrLuaExitCode` 的唯一生产引用是 `luaCodeToActionErr`(robot.go:694),Task 3.4 删除该函数后,errcode 侧的常量重命名不会造成生产代码编译断裂。

- [ ] **Step 1: 重命名常量 + 改 registry 名称/描述**

`errcode/codes.go:54`:
```go
	ErrLuaScriptCheck ErrorCode = 54 // 脚本主动校验失败（字段缺失/值不符等业务断言；原 ErrLuaExitCode 重命名）
```

`errcode/codes.go:97` codeRegistry 项:
```go
	{54, "LUA_SCRIPT_CHECK", KindFramework, "脚本校验失败"},
```

> 数值 54 **保留**(历史归档/CSV 兼容,monitor 桶不空 codeName);只改常量名、registry 名称字符串、描述文案。

- [ ] **Step 2: 编译 + 测试验证**

Run: `go build ./... && go test ./errcode/... -v`
Expected: 编译通过(Task 3.4 已删 luaCodeToActionErr,无残留旧名引用);`errcode.AllCodes()` 含 54,名称为 LUA_SCRIPT_CHECK

- [ ] **Step 3: grep 验证无旧名残留**

Run: `grep -rn "ErrLuaExitCode\|LUA_EXIT_CODE" --include="*.go" .`
Expected: 空输出(`docs/*.md` 的引用在 Task 5.3 同步改名)

- [ ] **Step 4: Commit**

```bash
git add errcode/codes.go
git commit -m "refactor(errcode): ErrLuaExitCode 重命名为 ErrLuaScriptCheck（语义清晰化，数值 54 保留）"
```

---

### Task 5.2:同步 `CLAUDE.md` + `README.md`

**Files:**
- Modify: `CLAUDE.md:69`、`README.md:210,394`

- [ ] **Step 1: 改 `CLAUDE.md:69`**

原文:`→ 返回 0 表示成功`
改为:`→ 返回 nil 表示成功，err table 表示失败（robot.error(code, detail) 构造）`

- [ ] **Step 2: 改 `README.md:210`**

原文:`\| \`lua\` \| 执行 \`script\` 指定的 Lua 脚本（\`execute(r)\` 返回 0 表示成功） \|`
改为:`\| \`lua\` \| 执行 \`script\` 指定的 Lua 脚本（\`execute(r)\` 返回 nil 表示成功，err table 表示失败） \|`

- [ ] **Step 3: 改 `README.md:394`**

原文:`- **Lua**：\`lua:script_name.lua\`，执行 Lua 脚本，返回 0 = true，非 0 = false。`

> **注意**:这行描述的是**条件表达式 `lua:` 前缀**(boolean 节点 / loop condition),走 `RunBooleanScript`,应是 `return true/false`。原文"返回 0 = true"是把旧 0/1 约定与 boolean 混淆。改为:
> `- **Lua**：\`lua:script_name.lua\`，执行 Lua boolean 脚本，return true/false（true 满足）。`

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md README.md
git commit -m "docs: CLAUDE.md/README 同步 lua 返回契约"
```

---

### Task 5.3:同步 `docs/` 下文档

**Files:**
- Modify: `docs/visual-flow-editor.md:657-661`、`docs/flow-node-system.md:622-627`、`docs/monitoring-system.md:1165`、`docs/error-code-system.md:141,402,413,416`

- [ ] **Step 1: 逐处更新**

| 文件:行 | 改法 |
|---|---|
| `docs/visual-flow-editor.md:657-661` | LuaForm mode 表 action 行返回值列:`return code` → `return nil / err table`(boolean/listen 行不动) |
| `docs/flow-node-system.md:622-627` | lua 动作第 5 条:`返回码 != 0 时返回 ErrLuaExitCode` → `脚本 return nil 成功；return err table 由 runtime 重建 *ActionError 透传；返回 number 等非法值 fail loud` |
| `docs/monitoring-system.md:1165` | `脚本只返回 code` → `脚本返回 nil（成功）或 err table（失败）` |
| `docs/error-code-system.md:141` | 54 行:常量名 `ErrLuaExitCode`→`ErrLuaScriptCheck`、名称 `LUA_EXIT_CODE`→`LUA_SCRIPT_CHECK`、描述→"脚本校验失败(字段缺失/值不符等业务断言)" |
| `docs/error-code-system.md:402` | robotActionHandler 错误路径:`NewActionError(ErrLuaExitCode, ...)` 整段改为描述新路径(err table 重建 + fail loud;常量已重命名 ErrLuaScriptCheck,仅脚本断言失败用) |
| `docs/error-code-system.md:413,416` | network API code 返回模型:重写为 `network API 返回 err table（单返回值）或 (err, data)（双返回值）/ (err,status,body)（http），不再折算回 errcode` |

- [ ] **Step 2: Commit**

```bash
git add docs/visual-flow-editor.md docs/flow-node-system.md docs/monitoring-system.md docs/error-code-system.md
git commit -m "docs: 同步 lua err table 契约到设计文档"
```

---

## 阶段 6:端到端验证

### Task 6.1:运行验证 + 日志审查 + monitor 错误分布 + 前端编译

**Files:** 无修改,纯验证

- [ ] **Step 1: 后端全量编译 + 测试**

Run: `go build ./... && go test ./script/... ./robot/... ./errcode/... -v`
Expected: 全 PASS

- [ ] **Step 2: 前端编译 + 测试**

Run: `cd cmd/web && npx tsc -b && npm run test`
Expected: 无类型错误,Vitest 全过

- [ ] **Step 3: 清日志 + 启动单机压测**

Run:
```bash
rm -f log/stressbot.log
go run ./cmd/agent -config conf/config.json
```
运行 2-5 分钟(用现有 conf/flow/flow.json,引用迁移后的脚本)。

- [ ] **Step 4: 日志审查——无 fail loud 误触发**

Run: `grep -i "返回非法值\|旧式 return code\|panic\|Traceback" log/stressbot.log | grep -v "headError"`
Expected: 空。若有"返回非法值",某脚本漏迁移,回 Task 3.3 补。

- [ ] **Step 5: 日志审查——无异常 error/warn**

Run: `grep -i "error\|warn\|失败" log/stressbot.log | grep -v "headError"`
Expected: 无新增异常。lua action 失败 error 日志应带 `action=` 前缀(验证 A 组 #7 修复)。

- [ ] **Step 6: monitor 错误分布检查**

前端 MonitorDock 或 `/api/metrics/summary`:lua action 错误桶应从塌缩的 `[framework/54]` 细分为具体业务码桶,每条 detail 带 `action=X script=Y <原因>`。

- [ ] **Step 7: FlowEditor 校验**

编辑器打开 `conf/flow/flow.json`,校验报告无错误;新建一个 lua action 脚本,确认 `TEMPLATE.action` 默认骨架是 `return nil`(Task 4.2 生效)。

- [ ] **Step 8: 最终提交(若验证中发现补丁)**

```bash
git add <修复的文件>
git commit -m "fix: 端到端验证补丁"
```

---

## Self-Review

**1. Spec coverage(A 组 5 子问题 + 全影响面):**
- #5 双轨 → Task 2.x + 3.1 + 3.4 ✅
- #6 异常岔路 → 设计决定 7(RaiseError 保留为编程错误)✅
- #7 Detail 缺 action → Task 3.2 出口补 `action=` ✅
- #8 return 54 垃圾桶 → Task 3.3 三类细分(协议→12 / 服务端码→透传 / 脚本断言→54 ErrLuaScriptCheck)+ 5.1 重命名 ✅
- **后端影响面**:Task 3.4 删除清单覆盖 lastActionError/errToCode/luaCodeToActionErr/remember*/pushRequestResult;runtime_cache_test caller 适配(Task 3.2);agent/monitor/admin/engine 零引用已确认 ✅
- **前端影响面**:Task 4.1(luaApiSpec network.* + robot.error)、4.2(LuaForm 模板/Alert + LuaScriptField)✅
- **文档影响面**:Task 5.2(CLAUDE/README)、5.3(docs×4)✅
- **errcode**:Task 5.1(ErrLuaExitCode→ErrLuaScriptCheck 重命名,数值 54 保留)+ Task 3.3 三类细分映射 ✅
- **测试夹具内嵌 lua**:Task 3.2(runtime_cache_test return 0→nil)✅
- **不受影响确认**:codec.lua/error.lua oracle、errors.json、boolean/listen 脚本内部、前端错误展示组件(泛化 Kind/Code)—— 排查已证实排除 ✅

**2. Placeholder scan:** 无 TBD/TODO;每个 task 有完整代码或精确行号+改法;脚本迁移给完整 before/after + 全清单 ✅

**3. Type consistency:**
- `newErrTable(L, code int, detail string) *lua.LTable` — 1.1 定义,2.x/1.3 使用 ✅
- `pushResult(L, err, data lua.LValue) int` — 1.1 定义,2.x 使用 ✅
- `parseErrTable(v lua.LValue) (code int, detail string, ok bool)` — 1.1 定义,3.1 使用 ✅
- `RunActionScript(...) (send, recv int, timing engine.ActionTiming, err error)` — 3.1 定义,3.2 executeLuaAction 使用 ✅
- `robotError(L) int` — 1.3 定义 + 两处注册 ✅
- err table schema `{code, detail}` — Go(newErrTable/parseErrTable)、Lua 脚本(robot.error/透传)、前端文档(luaApiSpec returns `err table {code, detail}`)四处一致 ✅

**4. 已知风险(plan 已体现):**
- Task 1.2 fixture 的 `state.NewStore()`/`protox.Factory` 构造签名,执行者按实际包 API 核对
- Task 3.1 测试 `rp.Acquire()`/`rp.Release()` 按 RuntimePool 实际 API
- Task 3.3 `return 54` 补 detail 唯一人工点,清单已列全
- 阶段 2 中间状态脚本运行会错(预期),阶段 3 原子迁移后恢复;`go build` 全程通过
- 前端 `luaEntryCheck.test.ts` 的 `return 0` 样本可不改(只测入口识别)

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-22-lua-error-table.md`. Two execution options:

**1. Subagent-Driven(推荐)** — 每个 task 派一个新 subagent 执行,task 间 review,快速迭代

**2. Inline Execution** — 在当前会话用 executing-plans 批量执行,带检查点 review

Which approach?
