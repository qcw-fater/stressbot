# 错误码统一映射表 + 删除 Kind 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 删除 `Kind`（framework/server）错误来源类别，改为"工具契约码段（< 100 框架保留 / ≥ 100 业务）+ 加载期撞码硬报错 + 前端编辑期校验"的统一单维模型，并对游戏服务器零耦合。

**Architecture:** 错误对象（`ActionError`/err table）只携带 `{code, detail}`，不再有 origin/Kind 字段。框架/业务区分**不进错误对象**：展示层按 `code < 100` 现场推导（工具单方面保留 < 100 为框架码，是用工具的前提规则，不是对服务器的假设 → 零耦合）。命名靠两源各按上下文查询：框架名 `errcode.String()`、业务名 `errors.json`（adapter.DescribeError）。errors.json 加载期硬报错拒绝 < 100 码；前端编辑器结构化表单 + 实时校验（< 100/重复）+ 展示保留框架码，与后端同规则纵深防御。

**Tech Stack:** Go（标准库 testing）+ React 18 / TypeScript / Ant Design 5 / Vitest。

## Global Constraints

- **零耦合**：不假设任何具体服务器的码段分布或行为。`< 100 = 框架` 是工具契约（用工具的前提规则），不是服务器假设。
- **禁止兼容性兜底**（`feedback_no_compat_hacks`）：不写迁移函数、不留 Kind fallback。新契约全链路一致。
- **通用模块零耦合**（`feedback_module_zero_coupling`）：adapter/codec 等通用模块不预设服务器约定；撞码检查用纯数值 `< 100`，不 import errcode（adapter 不依赖工具错误词汇）。
- **日志/错误信息用中文**。
- **Go 字段名与 JSON tag 一致**；commit 用 conventional commits + 中文描述，末尾 `Co-Authored-By: Claude <noreply@anthropic.com>`。
- **基线**：master@0c0ed6b（含 Phase P 的 err table `{code, detail}` + 协程调度 + 通用 `robot.error`，以及后续调度器 wait pump 内联化重构）。本计划是增量精修。**审查确认**：0c0ed6b 相对 d7f0dff 改的是 robot/executor/scheduler + 前端 ProtocolConfigEditor/codecEditor；后端错误码文件（errcode/engine.errors/action/monitor/script.errtable/adapter）与前端 Task 3.2 文件（ActionMetricsTable/reportCharts/ReportHtml/MetricsBadge）**未受影响，锚点仍有效**；仅 Task 3.3 的 ProtocolConfigEditor 接入点需按新结构（见 Task 3.3）。
- **测试**：Go 标准库 testing；前端 Vitest。

## 设计决定（已定）

1. **删 Kind**：`errcode.Kind` 类型、`KindFramework`/`KindServer`、`CodeInfo.Kind`、`codeRegistry` 的 Kind 列、`ActionError.Kind`、`NewServerError`、`IsServerError`、`ErrorKind()`、monitor `CodedError.ErrorKind()`/`errKey.Kind`/`ErrorEntry.kind` 全删。
2. **码段契约**：`< 100` 框架保留（工具自产），`≥ 100` 业务（服务器返回 / 脚本 `robot.error`）。展示按 `code < 100` 推导标签。
3. **命名两源**：框架名 `errcode.ErrorCode(code).String()`（registry）；业务名 `adapter.DescribeError(code)`（errors.json）。不强行合并成一张 map——按上下文查询，`< 100` 为判别。monitor `codeName` 维持 `errcode.String()`（业务码 codeName 为空，前端兜底 `#code`，与现状一致）。
4. **撞码硬报错**：errors.json 任一码 `< 100` → adapter 加载期 `LoadCodecResolver` 返回 error，启动失败。纯数值检查，adapter 不 import errcode。
5. **复用现有端点**：`GET /sbot/api/error-codes`（admin/handlers.go:102）已返回 `AllCodes()`；删 Kind 后返回 `{code,name}`，前端新增 client 读取用于"保留码展示"。不新增端点。
6. **前端 errors.json 结构化 KV 表单**：每行 {码, 描述} + 行内实时校验（< 100 / 重复 / 非正整数 / 描述空）+ 禁保存 + 顶部展示保留框架码（只读）。
7. **`robot.error(code, detail)` 通用构造器不变**；不引入 `error_detail`；脚本维持 `error(54,…)`/`error(业务码,…)`，**零迁移**。
8. **HTTP**：status 是**数据不是错误码**（100–599 绝不进 code 空间，否则污染业务码段）。Lua `http_request` 返回 `(err,status,body)`、任意响应 err=nil，**已正确不改**；声明式无法按 status 分支，保留非 2xx → `ErrHTTPStatus`(48,框架) 作为二元失败信号，**detail 只补 status 文本**（不含 body，避免过长），**body 截断 512 进 warn 日志**做诊断（Task 2.2）；不解析 body（零耦合，要业务码解析走 Lua）。

## File Structure

**修改（Go 后端）:**
- `errcode/codes.go` — 删 Kind 类型/consts/CodeInfo.Kind/registry Kind 列；保留 AllCodes/String
- `engine/errors.go` — `ActionError` 删 Kind；`NewServerError` 并入 `NewActionError`；删 `IsServerError`/`ErrorKind()`；`Error()` 格式去 kind
- `engine/action.go:979` — 唯一 `NewServerError` 调用点改 `NewActionError`（注释 :971 同步）
- `monitor/collector.go` — `CodedError` 删 `ErrorKind()`；`errKey` 删 Kind；`recordError`/`ErrorEntry` 去 kind（:63/:72/:107/:437/:574）
- `monitor/reporter.go:116` — 错误打印格式去 kind，标签按 `< 100` 推导
- `monitor/snapshot.go:481-487` — `mergedErrorKey` 删 Kind
- `script/errtable.go:51-72` — 删 `classifyCode`；`buildActionError` 统一 `NewActionError`（不再 Kind 分流）
- `script/errtable_test.go` — 删 `TestClassifyCode`；`TestBuildActionError` 去 Kind 断言
- `script/api_network.go:103` — 注释 NewServerError → NewActionError
- `adapter/codec_resolver.go:136-144` — 加载 errors.json 后加 `< 100` 撞码硬报错

**修改（前端 TS）:**
- `cmd/web/src/types/api.ts:265-273` — 删 `ErrorKind`、`ErrorEntry.kind`；新增 `FrameworkCode` 类型
- `cmd/web/src/services/api.ts`（或 resourcesStore）— 新增 `getErrorCodes()` client 调 `/sbot/api/error-codes`
- `cmd/web/src/components/monitoring/shared/ActionMetricsTable.tsx:299-301` — 错误渲染去 kind，单维 key，标签按 `< 100`
- `cmd/web/src/components/modules/history/report/reportCharts.ts:421-428` — 错误聚合去 kind
- `cmd/web/src/components/modules/history/report/ReportHtml.tsx:265` — 错误名兜底去 kind
- `cmd/web/src/components/FlowEditor/nodes/shared/MetricsBadge.tsx:113` — 错误名兜底去 kind
- `cmd/web/src/components/modules/ProtocolConfigEditor.tsx` — errors.json 改结构化 KV 表单 + 校验 + 保留码展示

**修改（文档）:**
- `docs/error-code-system.md`、`CLAUDE.md`、`README.md` — 同步"删 Kind + 码段契约 + 撞码硬报错"

---

## 阶段 1：后端删除 Kind

### Task 1.1:`errcode` 删除 Kind

**Files:**
- Modify: `errcode/codes.go`
- Test: `errcode/codes_test.go`（新建，包内原本无测试）

**Interfaces:**
- Produces: `AllCodes() []CodeInfo`（CodeInfo 现 `{Code uint64, Name string}`，无 Kind）；`ErrorCode.String()` 不变；`Kind` 类型/常量删除。

- [ ] **Step 1: 写失败测试 `errcode/codes_test.go`**

```go
package errcode

import "testing"

func TestAllCodes_HasNoKind(t *testing.T) {
	codes := AllCodes()
	if len(codes) == 0 {
		t.Fatal("AllCodes 不应为空")
	}
	for _, c := range codes {
		// CodeInfo 不再有 Kind 字段——编译期已保证；此处校验码段与名称非空
		if c.Code >= 100 {
			t.Fatalf("框架码应 < 100，实际 %d", c.Code)
		}
		if c.Name == "" {
			t.Fatalf("码 %d 名称空", c.Code)
		}
	}
}

func TestString_FrameworkCode(t *testing.T) {
	if got := ErrRecvTimeout.String(); got != "RECV_TIMEOUT" {
		t.Fatalf("ErrRecvTimeout.String()=%q want RECV_TIMEOUT", got)
	}
	if got := ErrorCode(99999).String(); got != "" {
		t.Fatalf("未注册码应返回空串，实际 %q", got)
	}
}
```

- [ ] **Step 2: 跑测试验证失败**

Run: `go test ./errcode/ -run TestAllCodes_HasNoKind -v`
Expected: FAIL（编译失败：`c.Kind` 字段引用——若测试里引用了；或当前 CodeInfo 有 Kind 但测试不引用则 PASS。先确认当前 `codes.go` 的 CodeInfo 有 Kind 字段需删除）

- [ ] **Step 3: 修改 `errcode/codes.go`**

删除 Kind 类型与常量（第 7-14 行）：

```go
// （删除整段）
// type Kind string
// const ( KindFramework Kind = "framework"; KindServer Kind = "server" )
```

`ErrorCode` 注释（第 3-4 行）改为：

```go
// ErrorCode 统一错误码类型。
// 码段契约：< 100 为框架码（工具自产，本 registry 分配），≥ 100 为业务码（服务器返回）。
// 单一 code 即唯一标识一类错误，不再需要 Kind 维度。
type ErrorCode uint64
```

`CodeInfo`（第 61-66 行）删 Kind 字段：

```go
// CodeInfo 单条错误码元数据，HTTP 端点 /sbot/api/error-codes 返回此结构。
type CodeInfo struct {
	Code uint64 `json:"code"`
	Name string `json:"name"`
}
```

`codeRegistry`（第 70-100 行）：每行去掉第三列 `KindFramework`，例如：

```go
var codeRegistry = []CodeInfo{
	{uint64(ErrConnNotFound), "CONN_NOT_FOUND"},
	{uint64(ErrConnClosed), "CONN_CLOSED"},
	// ... 其余每行同样去掉 KindFramework ...
	{uint64(ErrCallbackParse), "CALLBACK_PARSE"},
}
```

`codeNameIndex`/`String()`/`AllCodes()` 不变（它们不依赖 Kind）。

- [ ] **Step 4: 跑测试 + 全量编译**

Run: `go test ./errcode/ -v && go build ./...`
Expected: errcode 测试 PASS；但 `go build ./...` 会因下游（engine/monitor）仍引用 `errcode.Kind`/`KindFramework`/`KindServer` 编译失败——这是预期，Task 1.2/1.3 修复。

- [ ] **Step 5: Commit（暂不提交，等 1.2/1.3 一起编译通过再提交——见 1.3 末尾）**

> 本任务单独无法编译通过（下游引用 Kind），故阶段 1 三任务完成后统一提交。

---

### Task 1.2:`engine` ActionError 删 Kind + 合并 NewServerError

**Files:**
- Modify: `engine/errors.go`、`engine/action.go:913`

**Interfaces:**
- Produces: `NewActionError(code errcode.ErrorCode, detail string, cause ...error) *ActionError`（签名不变，内部不再设 Kind）；`NewServerError` 删除；`ActionError` 无 Kind 字段；`Error()` 格式 `[code] detail`；`IsServerError()`/`ErrorKind()` 删除；保留 `ErrorCode()`/`ErrorDetail()`/`Unwrap()`。

- [ ] **Step 1: 修改 `engine/errors.go`**

整文件替换为：

```go
package engine

import (
	"errors"
	"fmt"

	"stressbot/errcode"
)

// 流程配置错误哨兵。这些是 flow.json 配置错误，不是运行时动作错误。
var (
	ErrNodeNotFound    = errors.New("节点不存在")
	ErrUnknownNodeType = errors.New("未知节点类型")
	ErrActionNotFound  = errors.New("动作不存在")
)

// ActionError 携带错误码的结构化错误。单一 code 唯一标识（< 100 框架 / ≥ 100 业务）。
type ActionError struct {
	Code   errcode.ErrorCode // 错误码
	Detail string            // 上下文描述（service / route / action 等），不含 [code] 前缀
	cause  error             // 可选下层错误，用于 errors.Is 链式判断
}

// NewActionError 创建结构化错误（框架码与业务码统一入口）。
// 可选 cause 参数用于包装下层 error。
func NewActionError(code errcode.ErrorCode, detail string, cause ...error) *ActionError {
	e := &ActionError{Code: code, Detail: detail}
	if len(cause) > 0 {
		e.cause = cause[0]
	}
	return e
}

// Error 格式：[1] service=logic: cause message 或 [1004] desc: route=CreateTeam。
func (e *ActionError) Error() string {
	s := fmt.Sprintf("[%d]", e.Code)
	if e.Detail != "" {
		s += " " + e.Detail
	}
	if e.cause != nil {
		s += ": " + e.cause.Error()
	}
	return s
}

// Unwrap 返回被包装的下层错误，支持 errors.Is 链式判断。
func (e *ActionError) Unwrap() error { return e.cause }

// ErrorCode 返回数值错误码，供 monitor 通过接口提取。
func (e *ActionError) ErrorCode() uint64 { return uint64(e.Code) }

// ErrorDetail 返回错误上下文描述，供 monitor 通过接口提取。
func (e *ActionError) ErrorDetail() string { return e.Detail }
```

（删除 `NewServerError`、`IsServerError`、`ErrorKind` 三者）

- [ ] **Step 2: 改 `engine/action.go:979` 的 NewServerError 调用**

原（约 971-979 行，headerErr 分支）：

```go
// headerErr 描述缺失不致命，仅 detail 不含人类可读前缀；上层仍按 NewServerError 上抛原错误码。
...
return NewServerError(headerErr, detail)
```

改为：

```go
// headerErr 是服务端返回的错误码（≥ 100 业务码）；统一用 NewActionError 上抛原码。
return NewActionError(errcode.ErrorCode(headerErr), detail)
```

> 确认 `engine/action.go` 顶部已 import `stressbot/errcode`（文件内大量 `errcode.ErrXxx` 已用，已 import）。

- [ ] **Step 3: 编译验证（engine 包）**

Run: `go build ./engine/...`
Expected: 通过（engine 包内不再引用 Kind/NewServerError/IsServerError/ErrorKind）。全量 `go build ./...` 仍因 monitor 失败，Task 1.3 修。

---

### Task 1.3:`monitor` 删 Kind（聚合改单维 code）

**Files:**
- Modify: `monitor/collector.go`、`monitor/reporter.go`、`monitor/snapshot.go`

- [ ] **Step 1: 改 `monitor/collector.go`**

(a) `ErrorEntry`（约 61-66 行）删 Kind 字段：

```go
// ErrorEntry 错误分布条目，按 code 单维聚合。
type ErrorEntry struct {
	Code     uint64   `json:"code"`
	CodeName string   `json:"codeName"` // 框架码名称（ErrorCode.String()）；业务码为 ""
	Msgs     []string `json:"msgs"`
	Count    int      `json:"count"`
}
```

(b) `errKey`（约 70-73 行）删 Kind：

```go
// errKey 错误桶的键，按 code 唯一标识一类错误。
type errKey struct {
	Code uint64
}
```

(c) `CodedError` 接口（约 104-108 行）删 `ErrorKind()`：

```go
// CodedError 带错误码的错误接口。monitor 包定义此接口以避免循环依赖 engine 包。
type CodedError interface {
	Error() string
	ErrorCode() uint64
	ErrorDetail() string
}
```

(d) `recordError`（约 428-437 行）改键：

```go
	var ce CodedError
	if errors.As(err, &ce) {
		key := errKey{Code: ce.ErrorCode()}
		// ... 后续逻辑不变 ...
	}
```

(e) `ErrorEntry` 构造（约 574 行附近，把 k.Kind 去掉）：

```go
			CodeName: errcode.ErrorCode(k.Code).String(),
```
（这行本来就没用 Kind，确认周围构造 ErrorEntry 的结构体字面量去掉 `Kind: k.Kind`。Read 570-580 确认后删除 Kind 字段赋值。）

- [ ] **Step 2: 改 `monitor/reporter.go:116` 错误打印格式**

原（约 116 行）：

```go
fmt.Printf("%s→[%s/%d %s]×%d %s", a.Name, e.Kind, e.Code, e.CodeName, e.Count, ...)
```

改为（标签按 `< 100` 推导）：

```go
label := "业务"
if e.Code < 100 {
	label = "框架"
}
name := e.CodeName
if name == "" {
	name = fmt.Sprintf("#%d", e.Code)
}
fmt.Printf("%s→[%s %s]×%d %s", a.Name, label, name, e.Count, truncateError(firstMsg(e.Msgs), 40))
```

- [ ] **Step 3: 改 `monitor/snapshot.go:481-487` 合并键**

原 `mergedErrorKey`（约 481 行）：

```go
type mergedErrorKey struct {
	Kind errcode.Kind
	Code uint64
}
```

改为：

```go
type mergedErrorKey struct {
	Code uint64
}
```

构造处（约 487 行）`mergedErrorKey{Kind: e.Kind, Code: e.Code}` → `mergedErrorKey{Code: e.Code}`。删除 `errcode` import 若 snapshot.go 不再使用它（grep 确认）。

- [ ] **Step 4: 编译 + 全量测试**

Run: `go build ./... && go vet ./errcode/... ./engine/... ./monitor/... && go test ./... -count=1`
Expected: 全量编译通过，所有测试 PASS。

- [ ] **Step 5: grep 确认 Kind 残留**

Run: `grep -rn "errcode.Kind\|KindFramework\|KindServer\|\.ErrorKind()\|IsServerError\|NewServerError" --include=*.go .`
Expected: 空。

- [ ] **Step 6: 暂不提交**

阶段 1 四任务（1.1–1.4）全部完成后在 Task 1.4 统一提交（errtable.go 仍引用 `classifyCode`/`NewServerError`，单独提交无法编译）。

---

### Task 1.4:`script/errtable.go` buildActionError 去 Kind

**背景**：errtable.go 的 `classifyCode` 返回 `errcode.Kind`、`buildActionError` 据此调 `NewServerError`/`NewActionError` 二分流。删 Kind 后 classifyCode 无意义，buildActionError 统一用 `NewActionError`。

**Files:**
- Modify: `script/errtable.go:51-72`
- Modify: `script/errtable_test.go`（删 `TestClassifyCode`、`TestBuildActionError` 去 Kind 断言）

- [ ] **Step 1: 改 `script/errtable.go`**

删除整个 `classifyCode` 函数（51-57 行）。`buildActionError`（59-72 行）改为：

```go
// buildActionError 由 code+detail 构造 *engine.ActionError，补 script= 上下文。
// 单一 code 即唯一标识（< 100 框架 / ≥ 100 业务），无需 Kind 分流。
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
```

- [ ] **Step 2: 改 `script/errtable_test.go`**

(a) 删除整个 `TestClassifyCode` 函数（classifyCode 已删）。

(b) `TestBuildActionError` 中删除对 `ae.Kind` 的断言（约 116-117、130-131 行的 `if ae.Kind != ... { ... }` 两段），保留 `ae.Code` 与 `ae.Detail`（含 `script=`）断言：

```go
func TestBuildActionError(t *testing.T) {
	// 框架码
	err := buildActionError(int(errcode.ErrRecvTimeout), "service=logic route=1:2", "match_succeed.lua")
	var ae *engine.ActionError
	if !errors.As(err, &ae) {
		t.Fatalf("err 不是 *ActionError: %T", err)
	}
	if ae.Code != errcode.ErrRecvTimeout {
		t.Fatalf("code=%v want %v", ae.Code, errcode.ErrRecvTimeout)
	}
	if !strings.Contains(ae.Detail, "script=match_succeed.lua") {
		t.Fatalf("detail 缺 script=: %q", ae.Detail)
	}
	// 业务码（≥100）同样走 NewActionError，code 原样保留
	err = buildActionError(1004, "队伍已满: route=CreateTeam", "guild_join.lua")
	if !errors.As(err, &ae) {
		t.Fatal("业务码 err 不是 *ActionError")
	}
	if ae.Code != errcode.ErrorCode(1004) {
		t.Fatalf("code=%v want 1004", ae.Code)
	}
	if !strings.Contains(ae.Detail, "队伍已满") {
		t.Fatalf("detail 缺原因: %q", ae.Detail)
	}
}
```

> 同时顺手改 `script/api_network.go:103` 注释："headerErr 错误码本身仍按 NewServerError 上抛" → "...仍按 NewActionError 上抛"（NewServerError 已删）。

- [ ] **Step 3: 编译 + 全量测试 + grep 终检**

Run: `go build ./... && go test ./... -count=1`
Expected: 全 PASS。

Run: `grep -rn "errcode.Kind\|KindFramework\|KindServer\|classifyCode\|NewServerError\|IsServerError\|\.ErrorKind()\|\.Kind\b" --include=*.go . | grep -v "/.claude/worktrees/"`
Expected: 空（命中说明漏改；注意排除 spec.Kind/field.Kind 等无关项）。

- [ ] **Step 4: Commit（阶段 1 统一提交）**

```bash
git add errcode/ engine/ monitor/ script/errtable.go script/errtable_test.go script/api_network.go
git commit -m "refactor: 删除错误码 Kind 维度，改为单维 code + 码段契约"
```

---

## 阶段 2：后端撞码硬报错

### Task 2.1:errors.json 加载期 `< 100` 撞码硬报错

**Files:**
- Modify: `adapter/codec_resolver.go:136-145`
- Test: `adapter/codec_resolver_test.go`

**Interfaces:**
- Produces: `LoadCodecResolver` 在加载 errors.json 后，若任一 code `< 100` 返回 error。

- [ ] **Step 1: 在 `adapter/codec_resolver_test.go` 加失败测试**

参考现有 `TestLoadCodecResolver_ThreeCodecs_Success`（约 32-34 行）的结构，新增：

```go
func TestLoadCodecResolver_ErrorMapRejectsFrameworkRange(t *testing.T) {
	// 临时写一份含 < 100 码的 errors.json
	dir := t.TempDir()
	bad := []byte(`{"54": "撞框架码", "1004": "队伍已满"}`)
	if err := os.WriteFile(filepath.Join(dir, "errors.json"), bad, 0644); err != nil {
		t.Fatal(err)
	}
	// 至少需要一个 *_codec.json 让 resolver 不报"无 codec"
	if err := os.WriteFile(filepath.Join(dir, "tcp_logic_codec.json"),
		[]byte(`{"header": {"size": 4, "layout": "little"}, "route": {"field": "cmd"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCodecResolver(dir, map[string]string{"tcp:logic": "tcp_logic_codec.json"}, "errors.json")
	if err == nil {
		t.Fatal("errors.json 含 < 100 码应返回 error，实际 nil")
	}
	if !strings.Contains(err.Error(), "54") {
		t.Fatalf("错误信息应指出违规码 54，实际: %v", err)
	}
}
```

（顶部 import 补 `os/filepath/strings` 若缺；按现有测试 import 风格。）

- [ ] **Step 2: 跑测试验证失败**

Run: `go test ./adapter/ -run TestLoadCodecResolver_ErrorMapRejectsFrameworkRange -v`
Expected: FAIL（当前 LoadCodecResolver 不检查 < 100）。

- [ ] **Step 3: 改 `adapter/codec_resolver.go` 加载 errors.json 后加检查**

定位（约 136-145 行）：

```go
	// 可选加载共享 errors.json（一次）。
	var errorMap map[uint64]string
	if errorsFile != "" {
		errPath := resolvePath(codecDir, errorsFile)
		em, err := codec.LoadErrorMap(errPath)
		if err != nil {
			return nil, fmt.Errorf("加载 errors.json %s 失败: %w", errPath, err)
		}
		errorMap = em
	}
```

在 `errorMap = em` 后追加：

```go
		// 码段契约：< 100 为框架码保留段，errors.json（业务码）不得占用。
		for code := range errorMap {
			if code < 100 {
				return nil, fmt.Errorf("errors.json 码 %d < 100 属框架保留段，业务码请使用 ≥ 100", code)
			}
		}
```

> 不 import errcode——纯数值 `< 100` 检查，保持 adapter 通用模块零耦合。

- [ ] **Step 4: 跑测试验证通过 + 全量**

Run: `go test ./adapter/ -run TestLoadCodecResolver -v && go build ./...`
Expected: PASS + 编译通过。

- [ ] **Step 5: Commit**

```bash
git add adapter/codec_resolver.go adapter/codec_resolver_test.go
git commit -m "feat(adapter): errors.json 加载期拒绝 < 100 框架保留码（硬报错）"
```

---

### Task 2.2:声明式 HTTP 非 2xx 错误 detail 补 status 文本 + body 片段

**背景**：声明式 httpRequest 是配置驱动、不能像 Lua 那样按 status 分支，所以非 2xx → 抛 `ErrHTTPStatus`(48, 框架) 作为二元失败信号，**保留**。问题在 detail 当前只有 `action=X statusCode=404`，丢了状态文本。HTTP status 不能进 code 空间（100–599 会污染业务码段），所以 status 文本放 detail；**响应体不进 detail（会很长），改为截断后打 warn 日志做诊断**。

**Files:**
- Modify: `engine/action.go:912-913`
- Test: `engine/action_test.go`（若已有 httpRequest 测试则扩展；否则新增针对 non-2xx detail 的用例）

- [ ] **Step 1: 写/扩展失败测试**

在 `engine/action_test.go` 找到或新增 httpRequest non-2xx 的测试，断言返回的 ActionError detail 同时含 status 数字、状态文本、body 片段：

```go
func TestHTTPRequest_Non2xx_DetailIncludesStatusAndBody(t *testing.T) {
	// 构造一个返回 404 + body `{"error":1004,"errstr":"队伍已满"}` 的 fake（按现有 httpRequest 测试的 fake 方式）
	... // 复用现有 httpRequest 测试的 fake NetSender/HTTPExchange 构造
	_, _, _, err := ae.Execute(ctx, def) // def 是 httpRequest 动作
	var ae2 *ActionError
	if !errors.As(err, &ae2) { t.Fatalf("want *ActionError: %T", err) }
	if ae2.Code != errcode.ErrHTTPStatus { t.Fatalf("code=%v want ErrHTTPStatus", ae2.Code) }
	d := ae2.Detail
	if !strings.Contains(d, "status=404") { t.Fatalf("detail 缺 status=404: %q", d) }
	if !strings.Contains(d, "Not Found") { t.Fatalf("detail 缺状态文本: %q", d) }
	if strings.Contains(d, "队伍已满") { t.Fatalf("detail 不应含 body（太长，body 进日志即可）: %q", d) }
}
```

> 按现有 httpRequest 测试（grep `ErrHTTPStatus\|httpRequest\|HTTPExchange` in engine/*_test.go）的 fake 构造方式对齐；若没有现成 fake，参考 script/fake_netsender_test.go 的 HTTPExchange 字段。

- [ ] **Step 2: 跑测试验证失败**

Run: `go test ./engine/ -run TestHTTPRequest_Non2xx -v`
Expected: FAIL（当前 detail 不含 "Not Found" / body 片段）。

- [ ] **Step 3: 改 `engine/action.go:906-913`（整个非 2xx 块）**

原：

```go
	if exchange.StatusCode < 200 || exchange.StatusCode >= 300 {
		stresslog.Warn("[ACTION] HTTP 响应非 2xx",
			zap.String("action", def.Name),
			zap.String("url", resolvedURL), zap.String("method", method),
			zap.Int("statusCode", exchange.StatusCode),
			zap.Int("respBodyLen", len(respBody)))
		return exchange.SendWireBytes, exchange.RecvWireBytes, timing, NewActionError(errcode.ErrHTTPStatus,
			fmt.Sprintf("action=%s statusCode=%d", def.Name, exchange.StatusCode))
	}
```

改为（**body 截断 512 进 warn 日志**；**detail 只留 status 文本，不含 body**——避免 detail 过长污染 monitor 样本/日志）：

```go
	if exchange.StatusCode < 200 || exchange.StatusCode >= 300 {
		bodyForLog := respBody
		if len(bodyForLog) > 512 {
			bodyForLog = bodyForLog[:512]
		}
		stresslog.Warn("[ACTION] HTTP 响应非 2xx",
			zap.String("action", def.Name),
			zap.String("url", resolvedURL), zap.String("method", method),
			zap.Int("statusCode", exchange.StatusCode),
			zap.Int("respBodyLen", len(respBody)),
			zap.ByteString("body", bodyForLog))
		statusText := http.StatusText(exchange.StatusCode)
		hdetail := fmt.Sprintf("action=%s status=%d", def.Name, exchange.StatusCode)
		if statusText != "" {
			hdetail += " " + statusText
		}
		return exchange.SendWireBytes, exchange.RecvWireBytes, timing, NewActionError(errcode.ErrHTTPStatus, hdetail)
	}
```

> 确认 `engine/action.go` 顶部已 import `net/http`（httpRequest 已用，应有）。不再需要 `strings`。

- [ ] **Step 4: 跑测试验证通过 + 编译**

Run: `go test ./engine/ -run TestHTTPRequest -v && go build ./...`
Expected: PASS + 编译通过。

- [ ] **Step 5: Commit**

```bash
git add engine/action.go engine/action_test.go
git commit -m "fix(engine): 声明式 HTTP 非 2xx 错误 detail 补 status 文本与 body 片段"
```

---

## 阶段 3：前端

### Task 3.1:TS 类型删 kind + 框架码 client

**Files:**
- Modify: `cmd/web/src/types/api.ts:265-273`
- Modify: `cmd/web/src/services/api.ts`（新增 getErrorCodes）

- [ ] **Step 1: 改 `cmd/web/src/types/api.ts`**

删除（约 265 行）：

```ts
export type ErrorKind = 'framework' | 'server';
```

`ErrorEntry`（约 267-273 行）删 `kind`：

```ts
export interface ErrorEntry {
  code: number;
  codeName: string;
  msgs: string[];
  count: number;
}
```

新增框架码类型：

```ts
/** 框架错误码（工具自产，< 100），来自 GET /sbot/api/error-codes */
export interface FrameworkCode {
  code: number;
  name: string;
}
```

- [ ] **Step 2: 在 `cmd/web/src/services/api.ts` 新增 client**

参考该文件现有 `getJson` 用法（顶部应有 `API_PREFIX` 与 `getJson`），追加：

```ts
import type { FrameworkCode } from '@/types/api';

/** 获取工具内置框架错误码（< 100 保留段），供 errors.json 编辑器展示保留码 */
export function getErrorCodes(): Promise<FrameworkCode[]> {
  return getJson<FrameworkCode[]>('/sbot/api/error-codes');
}
```

> 确认 `/sbot/api/error-codes` 的实际前缀（admin 路由是 `/sbot/api/error-codes`，前端 api.ts 的 API_PREFIX 与现有调用对齐；若现有调用都带 `/sbot/api` 前缀则照此）。

- [ ] **Step 3: 前端编译**

Run: `cd cmd/web && npx tsc -b`
Expected: 报告所有 `.kind` / `ErrorKind` 引用点（ActionMetricsTable、reportCharts 等）——Task 3.2 修。

---

### Task 3.2:monitor 错误展示去 kind（单维）

**Files:**
- Modify: `cmd/web/src/components/monitoring/shared/ActionMetricsTable.tsx:295-302`
- Modify: `cmd/web/src/components/modules/history/report/reportCharts.ts:424-425`
- Modify: `cmd/web/src/components/modules/history/report/ReportHtml.tsx:265`
- Modify: `cmd/web/src/components/FlowEditor/nodes/shared/MetricsBadge.tsx:113`

> 审查确认：前端 error `.kind` 渲染共 4 处（ActionMetricsTable、reportCharts、ReportHtml、MetricsBadge），本任务全覆盖。其余 `.kind`（source.kind / spec.Kind / proto field kind / node kind / binding kind 等）均非错误 Kind，不动。

- [ ] **Step 1: 改 `ActionMetricsTable.tsx`（约 299-301 行）**

原：

```tsx
{r.errors.map((e) => (
  <div key={`${e.kind}:${e.code}`} style={{...}}>
    <span style={{...}}>×{e.count}</span>
    <span style={{ fontWeight: 500 }}>{e.codeName || `${e.kind}#${e.code}`}</span>
    {e.msgs.length > 0 && <span style={{...}}>{e.msgs.join('; ')}</span>}
  </div>
))}
```

改为（key 单维 code；标签按 `< 100` 推导 badge）：

```tsx
{r.errors.map((e) => {
  const isFramework = e.code < 100;
  return (
    <div key={e.code} style={{ marginTop: 3, fontSize: 11, lineHeight: '16px' }}>
      <span style={{ color: 'var(--color-error)', fontWeight: 700, fontSize: 10, fontVariantNumeric: 'tabular-nums', marginRight: 6 }}>×{e.count}</span>
      <Tag color={isFramework ? 'default' : 'blue'} style={{ fontSize: 10, marginInlineEnd: 4 }}>{isFramework ? '框架' : '业务'}</Tag>
      <span style={{ fontWeight: 500 }}>{e.codeName || `#${e.code}`}</span>
      {e.msgs.length > 0 && <span style={{ color: 'var(--text-tertiary)', marginLeft: 6 }}>{e.msgs.join('; ')}</span>}
    </div>
  );
})}
```

> 确认文件顶部已 import `Tag` from `antd`（Ant Design）；若无则补。

- [ ] **Step 2: 改 `reportCharts.ts`（约 424-425 行）**

原：

```ts
const key = `${e.kind}:${e.code}`;
const label = e.codeName || `${e.kind}#${e.code}`;
```

改为：

```ts
const key = `${e.code}`;
const label = e.codeName || `#${e.code}`;
```

- [ ] **Step 2b: 改 `ReportHtml.tsx:265` 与 `MetricsBadge.tsx:113`（同一 pattern）**

两处都是 `${e.kind}#${e.code}` 兜底，去掉 `.kind`：

`ReportHtml.tsx:265` 原 `const name = e.codeName || `${e.kind}#${e.code}`;` → `const name = e.codeName || `#${e.code}`;`

`MetricsBadge.tsx:113` 原 `{e.codeName || `${e.kind}#${e.code}`}` → `{e.codeName || `#${e.code}`}`

- [ ] **Step 3: 编译 + grep 终检**

`ErrorEntry` 删 `kind` 后，任何 `e.kind` / `ErrorKind` 引用会成 TS 编译错误，tsc 即可兜底。

Run: `cd cmd/web && npx tsc -b`
Expected: 无类型错误（有 `.kind` 残留会报错，逐一改）。

Run: `grep -rn "ErrorKind" cmd/web/src --include=*.ts --include=*.tsx`
Expected: 空（`ErrorKind` 类型已删，全仓零引用）。

> 注：`grep ".kind"` 会有大量**无关**命中（source.kind / spec.Kind / proto field kind / node kind / binding kind 等），属正常，不是错误 Kind。

Run: `cd cmd/web && npm run test`
Expected: Vitest 全过。

- [ ] **Step 4: Commit**

```bash
git add cmd/web/src/types/api.ts cmd/web/src/services/api.ts \
        cmd/web/src/components/monitoring/ \
        cmd/web/src/components/modules/history/ \
        cmd/web/src/components/FlowEditor/nodes/shared/MetricsBadge.tsx
git commit -m "refactor(web): 错误展示删除 Kind，改为单维 code + <100 标签"
```

---

### Task 3.3:errors.json 结构化 KV 表单 + 校验 + 保留码展示

**Files:**
- Modify: `cmd/web/src/components/modules/ProtocolConfigEditor.tsx`
- Create: `cmd/web/src/components/modules/ErrorMapEditor.tsx`（结构化表单组件）
- Create: `cmd/web/src/components/modules/__tests__/errorMapEditor.test.ts`

**Interfaces:**
- 新组件 `<ErrorMapEditor value={content} onChange={setContent} />`：把 errors.json 字符串解析成 `{code, desc}[]` 渲染可编辑表单 + 行内校验；导出 `validateErrorMap(entries)` 供保存前校验。
- ProtocolConfigEditor 在 `isErrorsView` 时渲染 `<ErrorMapEditor>` 替代裸 JSON；保存前调 `validateErrorMap`，有错则禁用保存 + 提示。

- [ ] **Step 1: 写失败测试 `__tests__/errorMapEditor.test.ts`**

```ts
import { describe, it, expect } from 'vitest';
import { parseErrorMap, validateErrorMap, serializeErrorMap } from '../ErrorMapEditor';

describe('validateErrorMap', () => {
  it('拒绝 < 100 框架保留码', () => {
    const errs = validateErrorMap([{ code: 54, desc: '撞框架' }]);
    expect(errs.some((e) => /54.*< 100|保留/.test(e.message))).toBe(true);
  });
  it('拒绝重复码', () => {
    const errs = validateErrorMap([
      { code: 1004, desc: 'a' },
      { code: 1004, desc: 'b' },
    ]);
    expect(errs.some((e) => /重复/.test(e.message))).toBe(true);
  });
  it('合法条目无错', () => {
    expect(validateErrorMap([{ code: 1004, desc: '队伍已满' }])).toHaveLength(0);
  });
});

describe('parseErrorMap / serializeErrorMap', () => {
  it('往返一致', () => {
    const json = '{"1004":"队伍已满","2002":"金币不足"}';
    const entries = parseErrorMap(json);
    expect(entries).toEqual([
      { code: 1004, desc: '队伍已满' },
      { code: 2002, desc: '金币不足' },
    ]);
    expect(JSON.parse(serializeErrorMap(entries))).toEqual(JSON.parse(json));
  });
});
```

- [ ] **Step 2: 跑测试验证失败**

Run: `cd cmd/web && npm run test -- errorMapEditor`
Expected: FAIL（模块未创建）。

- [ ] **Step 3: 实现 `ErrorMapEditor.tsx`**

```tsx
import { useMemo } from 'react';
import { Button, Input, InputNumber, Space, Tag, Alert } from 'antd';
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';

export interface ErrorMapEntry {
  code: number;
  desc: string;
}
export interface ErrorMapError {
  index: number;
  message: string;
}

/** 把 errors.json 字符串解析成条目数组（按 code 升序）。空串/空对象 → []。 */
export function parseErrorMap(json: string): ErrorMapEntry[] {
  const trimmed = json.trim();
  if (!trimmed || trimmed === '{}') return [];
  const obj = JSON.parse(trimmed) as Record<string, string>;
  const entries = Object.entries(obj).map(([k, v]) => ({ code: Number(k), desc: v }));
  entries.sort((a, b) => a.code - b.code);
  return entries;
}

/** 条目数组序列化回 errors.json 字符串。 */
export function serializeErrorMap(entries: ErrorMapEntry[]): string {
  const obj: Record<string, string> = {};
  for (const e of entries) {
    if (!Number.isNaN(e.code) && e.desc !== '') obj[String(e.code)] = e.desc;
  }
  return JSON.stringify(obj, null, 2);
}

/** 校验：返回错误列表（空 = 可保存）。规则与后端一致：< 100 / 重复 / 非正整数 / 描述空。 */
export function validateErrorMap(entries: ErrorMapEntry[]): ErrorMapError[] {
  const errs: ErrorMapError[] = [];
  const seen = new Map<number, number>();
  entries.forEach((e, i) => {
    if (!Number.isInteger(e.code) || e.code <= 0) {
      errs.push({ index: i, message: `第 ${i + 1} 行：码必须为正整数` });
    } else if (e.code < 100) {
      errs.push({ index: i, message: `第 ${i + 1} 行：码 ${e.code} < 100 属框架保留段，不可用` });
    } else if (seen.has(e.code)) {
      errs.push({ index: i, message: `第 ${i + 1} 行：码 ${e.code} 与第 ${seen.get(e.code)! + 1} 行重复` });
    } else {
      seen.set(e.code, i);
    }
    if (e.desc.trim() === '') {
      errs.push({ index: i, message: `第 ${i + 1} 行：描述不能为空` });
    }
  });
  return errs;
}

interface Props {
  value: string;              // errors.json 字符串
  onChange: (next: string) => void;
  frameworkCodes: { code: number; name: string }[]; // 保留框架码（只读展示）
}

export function ErrorMapEditor({ value, onChange, frameworkCodes }: Props) {
  const entries = useMemo(() => {
    try { return parseErrorMap(value); } catch { return []; }
  }, [value]);
  const errs = useMemo(() => validateErrorMap(entries), [entries]);

  const update = (i: number, patch: Partial<ErrorMapEntry>) => {
    const next = entries.map((e, idx) => (idx === i ? { ...e, ...patch } : e));
    onChange(serializeErrorMap(next));
  };
  const remove = (i: number) => onChange(serializeErrorMap(entries.filter((_, idx) => idx !== i)));
  const add = () => onChange(serializeErrorMap([...entries, { code: 1000, desc: '' }]));

  return (
    <div>
      {errs.length > 0 && (
        <Alert type="error" showIcon style={{ marginBottom: 8 }}
          message={`${errs.length} 处错误，保存前需全部修正`}
          description={errs.slice(0, 5).map((e) => <div key={e.index}>{e.message}</div>)} />
      )}
      <div style={{ marginBottom: 8, padding: 8, background: 'var(--bg-secondary)', borderRadius: 4 }}>
        <div style={{ fontSize: 12, color: 'var(--text-tertiary)', marginBottom: 4 }}>框架保留码（< 100，不可用）：</div>
        <Space size={[4, 4]} wrap>
          {frameworkCodes.map((c) => (
            <Tag key={c.code} style={{ fontSize: 11 }}>{c.code}={c.name}</Tag>
          ))}
        </Space>
      </div>
      {entries.map((e, i) => (
        <Space key={i} style={{ display: 'flex', marginBottom: 4 }} align="center">
          <InputNumber value={Number.isNaN(e.code) ? undefined : e.code} min={1} style={{ width: 110 }}
            status={errs.some((er) => er.index === i && /码|重复/.test(er.message)) ? 'error' : undefined}
            onChange={(v) => update(i, { code: Number(v) })} />
          <Input value={e.desc} style={{ width: 260 }}
            status={errs.some((er) => er.index === i && /描述/.test(er.message)) ? 'error' : undefined}
            onChange={(ev) => update(i, { desc: ev.target.value })} />
          <Button icon={<DeleteOutlined />} onClick={() => remove(i)} danger size="small" />
        </Space>
      ))}
      <Button icon={<PlusOutlined />} onClick={add} size="small" style={{ marginTop: 4 }}>新增业务码</Button>
    </div>
  );
}
```

- [ ] **Step 4: 跑测试验证通过**

Run: `cd cmd/web && npm run test -- errorMapEditor`
Expected: PASS。

- [ ] **Step 5: 接入 `ProtocolConfigEditor.tsx`（基线 0c0ed6b 结构）**

先 Read 该文件确认：errors 视图当前走 Monaco 源码编辑器（`<div className="pce-source-editor"><Editor .../></div>`，约 558 行）；Save 按钮调 `onSave`（约 491 行）；`validationSummary`（444 行）对 errors 视图仅一句提示"保存前检查 JSON 格式"，无真校验。

(1) 顶部 import：

```ts
import { ErrorMapEditor, validateErrorMap, parseErrorMap } from './ErrorMapEditor';
import { getErrorCodes } from '@/services/api';
import type { FrameworkCode } from '@/types/api';
```

(2) 组件内加状态（useState 区，约 145 行附近）：

```ts
const [frameworkCodes, setFrameworkCodes] = useState<FrameworkCode[]>([]);
useEffect(() => { getErrorCodes().then(setFrameworkCodes).catch(() => setFrameworkCodes([])); }, []);
const [errorMapErrors, setErrorMapErrors] = useState<string[]>([]); // errors 视图校验结果，喂给 validationSummary
```

(3) errors 视图渲染（约 547-560 行的 `<>` 分支）：errors 视图用 `<ErrorMapEditor>` **替代** Monaco 源码编辑器；codec 视图保持原样：

```tsx
{isErrorsView ? (
  <ErrorMapEditor
    value={content}
    onChange={(next) => { setContent(next); setErrorMapErrors(validateErrorMap(parseErrorMapSafe(next)).map((e) => e.message)); }}
    frameworkCodes={frameworkCodes}
  />
) : (
  <div className="pce-source-editor"><Editor ... /></div>  /* 原有 codec 源码编辑器 */
)}
```

> `parseErrorMapSafe` = try/catch 包 parseErrorMap（非法 JSON 返回 []）；可在 ErrorMapEditor.tsx 导出，或本文件内联。`validateErrorMap`/`parseErrorMap` 已在 (1) import。

(4) `validationSummary`（444 行）errors 分支接入真校验：

```ts
const validationSummary = isErrorsView
  ? errorMapErrors.length === 0
    ? '校验通过'
    : `${errorMapErrors.length} 处问题：${errorMapErrors[0]}`
  : liveErrors.length === 0 ? '校验通过' : `${liveErrors.length} 处问题：${liveErrors[0]}`;
```

(5) `onSave`（Save 按钮 handler）保存前 gate：

```ts
const onSave = async () => {
  if (isErrorsView && errorMapErrors.length > 0) {
    message.error('errors.json 有 ' + errorMapErrors.length + ' 处错误（含 < 100 或重复码），无法保存');
    return;
  }
  // ...原有 setErrorMap(content) 逻辑（305/337 行）...
};
```

> `message` 来自 antd（`App.useApp()` 或静态 `message`），按该文件现有提示方式对齐（Read 确认）。

- [ ] **Step 6: 前端编译 + 测试**

Run: `cd cmd/web && npx tsc -b && npm run test`
Expected: 无类型错误；Vitest 全过。

- [ ] **Step 7: Commit**

```bash
git add cmd/web/src/components/modules/ErrorMapEditor.tsx \
        cmd/web/src/components/modules/__tests__/errorMapEditor.test.ts \
        cmd/web/src/components/modules/ProtocolConfigEditor.tsx
git commit -m "feat(web): errors.json 结构化表单 + <100/重复校验 + 保留码展示"
```

---

## 阶段 4：文档 + 验证

### Task 4.1:文档同步

**Files:** `docs/error-code-system.md`、`CLAUDE.md`、`README.md`

- [ ] **Step 1: 改 `docs/error-code-system.md`**

- 删除 Kind（framework/server）相关整段描述；改为"单一 code 维度，< 100 框架 / ≥ 100 业务码段契约"。
- `CodeInfo` 结构改 `{code, name}`。
- 删除"NewServerError / IsServerError"描述；统一 `NewActionError(code, detail)`。
- 新增"errors.json 码段契约：< 100 框架保留，加载期硬报错；前端编辑期同规则校验"一节。

- [ ] **Step 2: 改 `CLAUDE.md`**

`errcode/` 分层描述（约 62 行）："27 个框架错误码常量 + `Kind`" → "框架错误码常量（< 100 保留段）+ 统一 code 单维聚合（无 Kind）"。`monitor` 描述里"按 (Kind, Code) 聚合" → "按 code 单维聚合"。

- [ ] **Step 3: 改 `README.md`**

错误码相关段落同步（若有 Kind 描述则删）。

- [ ] **Step 4: Commit**

```bash
git add docs/error-code-system.md CLAUDE.md README.md
git commit -m "docs: 同步错误码单维 code + 码段契约（删 Kind）"
```

---

### Task 4.2:全量验证

**Files:** 无修改，纯验证。

- [ ] **Step 1: 后端全量**

Run: `go build ./... && go test ./... -count=1`
Expected: 全 PASS。

- [ ] **Step 2: 前端全量**

Run: `cd cmd/web && npx tsc -b && npm run test`
Expected: 无类型错误；Vitest 全过。

- [ ] **Step 3: grep 终检**

Run:
```bash
grep -rn "errcode.Kind\|KindFramework\|KindServer\|NewServerError\|IsServerError\|ErrorKind()\|\.kind\b" --include=*.go . | grep -v "_test.go"
grep -rn "ErrorKind\|\.kind\b" cmd/web/src --include=*.ts --include=*.tsx | grep -v node_modules
```
Expected: 空（命中说明漏改）。

- [ ] **Step 4: 运行验证（环境依赖，可执行部分）**

启动单机压测 `go run ./cmd/agent -config conf/config.json` 跑 2-5 分钟，日志审查无异常；前端 FlowEditor 打开 errors.json 编辑器，确认结构化表单 + 校验（输入 < 100 码变红、禁保存）+ 保留码展示生效；monitor 错误分布按 code 单维展示、框架/业务标签正确。

> 此步依赖真实压测环境，执行者按可执行部分验证并报告。

---

## Self-Review

**1. Spec coverage:**
- 删 Kind（errcode/engine/monitor/script.errtable）→ Task 1.1/1.2/1.3/1.4 ✅（**审查补 1.4**：errtable.go 的 classifyCode/buildActionError 用 Kind/NewServerError + errtable_test.go 的 TestClassifyCode/TestBuildActionError，原计划漏）
- 统一映射表（命名两源 + `< 100` 判别）→ 设计决定 3，monitor codeName 已用 errcode.String（无需新 wiring）✅
- 撞码硬报错（adapter 加载期）→ Task 2.1 ✅
- 复用 /sbot/api/error-codes + 前端 client → Task 3.1 ✅
- 前端 errors.json 结构化表单 + 校验 + 保留码展示 → Task 3.3 ✅
- 前端错误展示去 Kind（**4 处**：ActionMetricsTable/reportCharts/ReportHtml/MetricsBadge）→ Task 3.2 ✅（**审查补 ReportHtml.tsx:265 + MetricsBadge.tsx:113**，原计划只列 2 处）
- TS ErrorEntry 删 kind → Task 3.1 ✅
- TS ErrorEntry 删 kind → Task 3.1 ✅
- robot.error 通用不变、脚本零迁移 → 设计决定 7（无任务，确认不变）✅
- HTTP：status 数据化（Lua 已正确不改）；声明式非 2xx 保留 ErrHTTPStatus + detail 补 status 文本（body 截断进日志、不进 detail） → Task 2.2 ✅
- 文档同步 → Task 4.1 ✅
- 全量验证 → Task 4.2 ✅

**2. Placeholder scan:** 无 TBD/TODO；每步有完整代码或精确行号+改法。Task 1.3(e) 与 3.3(5)(4) 标注"Read 确认变量名/行"——这是因确切行号需核对，已给改法，非占位。

**3. Type consistency:**
- `AllCodes() []CodeInfo`，`CodeInfo{Code, Name}`（无 Kind）— 1.1 定义，前端 FrameworkCode `{code, name}` 对齐 ✅
- `NewActionError(code, detail, cause...)` — 1.2 定义签名不变 ✅
- `errKey{Code}` / `mergedErrorKey{Code}` — 1.3 定义一致 ✅
- `ErrorEntry{code, codeName, msgs, count}`（Go 与 TS 一致，无 kind）✅
- `getErrorCodes(): Promise<FrameworkCode[]>` — 3.1 定义，3.3 使用 ✅
- `ErrorMapEditor` props `{value, onChange, frameworkCodes}` — 3.3 定义使用一致 ✅

**4. 已知风险:**
- 阶段 1 四任务（1.1–1.4）单独不可编译（下游引用 Kind），需顺序完成后在 Task 1.4 统一提交。
- adapter 撞码检查用纯 `< 100`（不 import errcode），保持通用模块零耦合；若要更友好错误信息（带框架码名）需 adapter import errcode——按零耦合原则不做。
- monitor codeName 维持 errcode.String()（业务码 codeName 空，前端兜底 `#code`）——这是现状，非回归；若要 monitor 展示业务码描述需额外 wiring（本计划不做）。

**5. 全仓审查（已执行）：** 对全仓 `*.go`（含 `*_test.go`）与 `cmd/web/src/**/*.{ts,tsx}` grep 了 `errcode.Kind`/`KindFramework`/`KindServer`/`classifyCode`/`NewServerError`/`IsServerError`/`ErrorKind()`/错误项 `.kind`。**真实 error-Kind 引用点全部落入任务**：
- 后端：errcode/codes.go(T1.1)、engine/errors.go+action.go:979(T1.2)、monitor collector/reporter/snapshot(T1.3)、script/errtable.go+errtable_test.go(T1.4)、api_network.go:103 注释(T1.4)。
- 前端：types/api.ts(T3.1)、ActionMetricsTable/reportCharts/ReportHtml/MetricsBadge(T3.2)。
- 排除的无关 `.Kind`：reflect.Kind(flatted)、proto field Kind(protox/api_proto/factory)、WaitSpec.Kind(robot/script runtime/coroutine_test/api_share_io_test)、codec source.kind/over.kind(codec/schema/resourcesStore/codecEditor)、node/listen/binding/view kind(FlowEditor 各处)——均非错误 Kind，不动。
- 复用既存端点 `/sbot/api/error-codes`（admin/handlers.go:102/1641），不新增。

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-26-error-code-unified.md`. Two execution options:

**1. Subagent-Driven（推荐）** — 每个 task 派新 subagent，task 间 review，快速迭代
**2. Inline Execution** — 当前会话批量执行，带检查点

Which approach?
