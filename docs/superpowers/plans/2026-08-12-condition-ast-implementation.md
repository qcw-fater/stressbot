# Compiled Condition AST Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将所有 `state:` 条件从运行时重复分词/解析改为加载期编译一次、运行时直接执行共享不可变 AST，同时逐字保持现有条件语义。

**Architecture:** `engine` 新增紧凑节点数组形式的 `CompiledCondition`，编译产物只保存表达式结构和字面量，求值时传入当前 Robot 的 `state.Store`。`PrepareTaskFlow` 和心跳绑定准备入口递归编译并按表达式去重；`Node`、`SwitchCase`、`FieldBind` 私有字段直接保存程序指针，热路径不查 map，也不保留字符串解析兜底。

**Tech Stack:** Go 1.23+、现有 tokenizer、`state.Store`、Go `testing`/Benchmark、现有 engine/robot 测试。

---

## 实施结果（2026-08-12）

- 五组条件热路径均为 `0 B/op`、`0 allocs/op`；最终五轮单次求值范围由改造前的约 `380–1397 ns/op` 降至 `67–373 ns/op`（多数样本为 `67–245 ns/op`）。
- `go build ./...`、`go test ./...`、相关包 `go test -race`、`go vet`、前端 TypeScript 编译及 647 个 Vitest 用例通过。
- 当前 `conf/flow/flow.json` 在编辑器中无校验错误（4 处既有警告）；standalone 使用该配置运行 180 秒，条件未准备、AST 异常、panic、fatal 均为零。
- 运行日志包含现有业务返回码与监听队列覆盖警告，未将这些外部/业务告警误记为本改造通过项。

---

## 文件结构

- Create: `engine/cond_compile.go` — AST 节点、编译器和表达式去重器。
- Create: `engine/cond_program.go` — 不可变程序的运行时求值、短路和错误语义。
- Create: `engine/cond_compile_test.go` — 编译、实时 state、短路、并发和生命周期测试。
- Create: `engine/cond_benchmark_test.go` — 改造前后共用的 Benchmark。
- Modify: `engine/flow.go` — 条件拥有者增加不参与 JSON 的私有编译字段。
- Modify: `engine/state_action.go` — `PrepareTaskFlow`/`PrepareFieldBindings` 递归准备全部条件。
- Modify: `engine/action.go`、`engine/executor.go`、`robot/robot.go` — 热路径切换为预编译条件。
- Modify: `cmd/agent/main.go`、`agent/task_runner.go` — 两个生产 flow 加载入口执行准备。
- Modify: engine/robot 条件相关测试 — 迁移到 compile-once/evaluate-many。
- Delete: `engine/cond_eval.go` — 删除运行时字符串入口。
- Delete/replace: `engine/cond_parser.go` — 删除 inline 解释器，编译解析逻辑进入 `cond_compile.go`。

## Task 1: 建立解释执行性能基线

**Files:**
- Create: `engine/cond_benchmark_test.go`

- [x] **Step 1: 写入改造前 Benchmark**

创建固定 Store 和五类表达式，计时循环中只调用当前 `EvalCondition`：

```go
func BenchmarkConditionEvaluation(b *testing.B) {
    store := state.NewStore()
    store.Set("hp", int64(80))
    store.Set("index", 8)
    store.Set("alive", true)
    store.Set("admin", false)
    store.Set("profile", map[string]any{"level": int64(12)})
    cases := []struct{ name, expr string }{
        {"simple_compare", "state:hp > 0"},
        {"arithmetic", "state:(index + 2) % 5 == 0"},
        {"logical", "state:hp > 0 && (alive || admin)"},
        {"short_circuit", "state:alive || missing.path > 0"},
        {"nested_path", "state:profile.level >= 10"},
    }
    for _, tc := range cases {
        b.Run(tc.name, func(b *testing.B) {
            b.ReportAllocs()
            for b.Loop() {
                if !EvalCondition(tc.expr, store) {
                    b.Fatal("condition unexpectedly false")
                }
            }
        })
    }
}
```

- [x] **Step 2: 运行并记录基线**

Run:

```powershell
$env:GOCACHE = Join-Path (Get-Location) '.gocache'
go test ./engine -run '^$' -bench '^BenchmarkConditionEvaluation$' -benchmem -count 5
```

Expected: 五个子基准全部完成并输出 `ns/op`、`B/op`、`allocs/op`。完整结果保留在任务记录中，Task 5 使用同一命令比较。

- [x] **Step 3: 提交基准**

```powershell
git add engine/cond_benchmark_test.go
git diff --cached
git commit -m "test: benchmark condition interpretation"
```

## Task 2: 编译并执行不可变 AST

**Files:**
- Create: `engine/cond_compile.go`
- Create: `engine/cond_program.go`
- Create: `engine/cond_compile_test.go`
- Modify: `engine/cond_compare.go`

- [x] **Step 1: 写编译一次、重复读取实时 state 的失败测试**

```go
func TestCompiledConditionReadsCurrentStore(t *testing.T) {
    condition, err := compileCondition("state:hp > 0")
    if err != nil { t.Fatal(err) }
    store := state.NewStore()
    if condition.EvalState(store) { t.Fatal("missing hp must be false") }
    store.Set("hp", int64(1))
    if !condition.EvalState(store) { t.Fatal("updated hp must be visible") }
    store.Set("hp", int64(0))
    if condition.EvalState(store) { t.Fatal("latest hp must be used") }
    store.Delete("hp")
    if condition.EvalState(store) { t.Fatal("deleted hp must be missing") }
}
```

- [x] **Step 2: 运行测试确认 API 尚不存在**

Run: `go test ./engine -run '^TestCompiledConditionReadsCurrentStore$'`

Expected: FAIL，错误指向 `compileCondition` 或 `CompiledCondition` 未定义。

- [x] **Step 3: 定义编译产物和紧凑节点**

在 `engine/cond_compile.go` 定义：

```go
type conditionKind uint8
const (
    conditionState conditionKind = iota + 1
    conditionLua
    conditionUnsupported
)

type conditionNodeKind uint8
const (
    conditionNodeLiteral conditionNodeKind = iota + 1
    conditionNodePath
    conditionNodeUnary
    conditionNodeBinary
    conditionNodeRuntimeError
)

type conditionNode struct {
    kind conditionNodeKind
    op string
    left, right int32
    value any
    text string
    evalErr error
}

type conditionProgram struct {
    root int32
    nodes []conditionNode
}

type CompiledCondition struct {
    source string
    kind conditionKind
    script string
    program *conditionProgram
}

type conditionCompiler struct {
    conditions map[string]*CompiledCondition
}

func newConditionCompiler() *conditionCompiler {
    return &conditionCompiler{conditions: make(map[string]*CompiledCondition)}
}

func (c *conditionCompiler) compile(source string) (*CompiledCondition, error) {
    source = strings.TrimSpace(source)
    if condition := c.conditions[source]; condition != nil {
        return condition, nil
    }
    condition, err := compileCondition(source)
    if err != nil { return nil, err }
    c.conditions[source] = condition
    return condition, nil
}

func (c *CompiledCondition) Source() string { return c.source }
func (c *CompiledCondition) LuaScript() (string, bool) {
    if c == nil || c.kind != conditionLua { return "", false }
    return c.script, true
}
```

`compileCondition` 整体 `TrimSpace`；`state:` 使用现有 tokenizer 和递归下降优先级编译为节点索引；`lua:` 保存脚本名；未知前缀生成 `conditionUnsupported`。数字字面量在编译期解析，溢出生成 `conditionNodeRuntimeError`，仍在实际执行到该节点时失败。`conditionCompiler.compile(source)` 先按规范化 source 查询 `conditions`，未命中才调用 `compileCondition` 并保存结果。

- [x] **Step 4: 实现局部求值上下文**

在 `engine/cond_program.go` 定义：

```go
type conditionEvalContext struct {
    store *state.Store
    firstErr error
}

func (c *CompiledCondition) EvalState(store *state.Store) bool {
    if c == nil {
        stresslog.Error("[ENGINE] 条件表达式尚未编译")
        return false
    }
    if c.kind != conditionState || c.program == nil {
        stresslog.Warn("[ENGINE] 条件表达式格式错误，仅支持 state: 前缀",
            zap.String("expr", c.source))
        return false
    }
    return c.program.eval(c.source, store)
}
```

逻辑节点显式短路；其他二元节点先求左侧，左侧失败时不读右侧。比较、算术和负号继续调用 `strictCompare`、`evalArith`、`negate`，保持大 `uint64` 和整数除法语义。

- [x] **Step 5: 增加完整语义和并发测试**

表驱动用例必须包含：`index % 2`、`7 / 2`、负数取模、`!a == b`、`missing || fallback`、`uint64(9007199254740993)`、顶层非 bool、短路跳过错误节点。另用多个 goroutine 共享一个 CompiledCondition、各用独立 Store 求值，证明程序无共享可变状态。

- [x] **Step 6: 运行 AST 测试**

Run: `go test ./engine -run '^(TestCompiledCondition|TestCompileCondition)'`

Expected: PASS，无 panic。

- [x] **Step 7: 提交 AST 核心**

```powershell
git add engine/cond_compile.go engine/cond_program.go engine/cond_compile_test.go engine/cond_compare.go
git diff --cached
git commit -m "feat: compile state conditions to immutable AST"
```

## Task 3: 加载阶段准备所有条件位置

**Files:**
- Modify: `engine/flow.go`
- Modify: `engine/state_action.go`
- Modify: `engine/cond_compile_test.go`
- Modify: `engine/action_state_test.go`
- Modify: `engine/cond_parser_test.go`

- [x] **Step 1: 写递归准备和去重失败测试**

```go
func TestPrepareTaskFlowCompilesEveryConditionAndDeduplicates(t *testing.T) {
    flow := &TaskFlow{
        Nodes: map[string]*Node{"main": {
            Condition: "state:ready", BreakCondition: "state:done",
            Cases: []SwitchCase{{Condition: "state:ready"}},
        }},
        Actions: map[string]*ActionDef{"send": {Bindings: []FieldBind{{
            Condition: "state:ready",
            Entries: []MapEntryBind{{Value: FieldBind{Condition: "state:nested"}}},
        }}}},
    }
    if err := PrepareTaskFlow(flow); err != nil { t.Fatal(err) }
    node := flow.Nodes["main"]
    binding := &flow.Actions["send"].Bindings[0]
    if node.compiledCondition == nil || node.compiledBreakCondition == nil ||
        node.Cases[0].compiledCondition == nil || binding.compiledCondition == nil ||
        binding.Entries[0].Value.compiledCondition == nil {
        t.Fatal("not every condition location was prepared")
    }
    if node.compiledCondition != node.Cases[0].compiledCondition {
        t.Fatal("identical expressions must share one program")
    }
}
```

- [x] **Step 2: 运行测试确认准备 API 尚不存在**

Run: `go test ./engine -run '^TestPrepareTaskFlowCompilesEveryConditionAndDeduplicates$'`

Expected: FAIL，缺少 `PrepareTaskFlow` 或私有编译字段。

- [x] **Step 3: 给条件拥有者增加私有字段**

在 `Node`、`SwitchCase` 和 `FieldBind` 三个既有结构体末尾分别追加对应私有字段；不要改动其 JSON 字段：

```go
// Node
compiledCondition *CompiledCondition
compiledBreakCondition *CompiledCondition

// SwitchCase
compiledCondition *CompiledCondition

// FieldBind
compiledCondition *CompiledCondition
```

- [x] **Step 4: 实现统一准备入口**

```go
func PrepareTaskFlow(flow *TaskFlow) error {
    if flow == nil { return nil }
    compiler := newConditionCompiler()
    for name, def := range flow.Actions {
        if def == nil { continue }
        if def.Pattern == PatternClearState {
            if err := validateClearStateKeys(name, def.Keys); err != nil { return err }
        }
        for i := range def.Bindings {
            where := fmt.Sprintf("action %q bindings[%d]", name, i)
            if err := compiler.prepareBinding(where, &def.Bindings[i]); err != nil { return err }
        }
    }
    for id, node := range flow.Nodes {
        if node == nil { continue }
        if err := compiler.prepare(fmt.Sprintf("节点 %q condition", id), node.Condition,
            &node.compiledCondition); err != nil { return err }
        if err := compiler.prepare(fmt.Sprintf("节点 %q breakCondition", id), node.BreakCondition,
            &node.compiledBreakCondition); err != nil { return err }
        for i := range node.Cases {
            where := fmt.Sprintf("节点 %q cases[%d]", id, i)
            if err := compiler.prepare(where, node.Cases[i].Condition,
                &node.Cases[i].compiledCondition); err != nil { return err }
        }
    }
    return nil
}

func PrepareFieldBindings(bindings []FieldBind) error {
    compiler := newConditionCompiler()
    for i := range bindings {
        if err := compiler.prepareBinding(fmt.Sprintf("bindings[%d]", i), &bindings[i]); err != nil {
            return err
        }
    }
    return nil
}

func (c *conditionCompiler) prepare(where, source string, target **CompiledCondition) error {
    source = strings.TrimSpace(source)
    if source == "" { *target = nil; return nil }
    condition, err := c.compile(source)
    if err != nil {
        return fmt.Errorf("%s 条件表达式语法错误 %q: %w", where, source, err)
    }
    *target = condition
    return nil
}

func (c *conditionCompiler) prepareBinding(where string, binding *FieldBind) error {
    if binding == nil { return nil }
    if err := c.prepare(where, binding.Condition, &binding.compiledCondition); err != nil {
        return err
    }
    for i := range binding.Entries {
        childWhere := fmt.Sprintf("%s entries[%d]", where, i)
        if err := c.prepareBinding(childWhere, &binding.Entries[i].Value); err != nil {
            return err
        }
    }
    return nil
}
```

`conditionCompiler.prepare` 对空字符串把 target 置 nil；非空时调用去重编译器并用 `fmt.Errorf("%s 条件表达式语法错误 %q: %w", where, source, err)` 包装错误。`prepareBinding` 先准备当前 binding，再递归处理 `Entries[i].Value`。删除 `ValidateStateActions`，不保留兼容别名。

- [x] **Step 5: 测试准备后文本被修改时 fail-closed**

增加通用匹配函数及四个拥有者访问器：

```go
func matchingCondition(source string, compiled *CompiledCondition) *CompiledCondition {
    if compiled == nil || compiled.Source() != strings.TrimSpace(source) { return nil }
    return compiled
}
func (n *Node) preparedCondition() *CompiledCondition {
    return matchingCondition(n.Condition, n.compiledCondition)
}
func (n *Node) preparedBreakCondition() *CompiledCondition {
    return matchingCondition(n.BreakCondition, n.compiledBreakCondition)
}
func (c *SwitchCase) preparedCondition() *CompiledCondition {
    return matchingCondition(c.Condition, c.compiledCondition)
}
func (b *FieldBind) preparedCondition() *CompiledCondition {
    return matchingCondition(b.Condition, b.compiledCondition)
}
```

测试把 `Condition` 从 `state:ready` 改成 `state:other`，断言访问器返回 nil；不重编译。

- [x] **Step 6: 运行准备阶段测试并提交**

Run: `go test ./engine -run '^(TestPrepareTaskFlow|TestPreparedCondition)'`

Expected: PASS。随后只暂存上述五个文件并提交：

```powershell
gofmt -w engine/flow.go engine/state_action.go engine/cond_compile_test.go engine/action_state_test.go engine/cond_parser_test.go
git add engine/flow.go engine/state_action.go engine/cond_compile_test.go engine/action_state_test.go engine/cond_parser_test.go
git diff --cached
git commit -m "feat: prepare flow conditions at load time"
```

## Task 4: 切换所有运行路径并删除解释器

**Files:**
- Modify: `engine/executor.go`, `engine/action.go`, `robot/robot.go`
- Modify: `cmd/agent/main.go`, `agent/task_runner.go`
- Modify: `engine/executor_switch_test.go`, `engine/executor_wait_test.go`, `engine/executor_action_test.go`
- Modify: `robot/action_handler_test.go`, `engine/cond_parser_test.go`
- Delete: `engine/cond_eval.go`
- Delete/replace: `engine/cond_parser.go`

- [x] **Step 1: 写未准备条件不回退解析的失败测试**

```go
func TestExecutorDoesNotParseUnpreparedCondition(t *testing.T) {
    handler := &switchTestHandler{results: map[string]bool{"state:ready": true}}
    flow := &TaskFlow{Nodes: map[string]*Node{
        "main": {Type: NodeBoolean, Condition: "state:ready", TrueNext: "yes"},
        "yes": {Type: NodeSequence},
    }}
    executor := NewExecutor(flow, handler, "test")
    if err := executor.Run(context.Background()); err != nil { t.Fatal(err) }
    if len(handler.seen) != 0 {
        t.Fatal("unprepared condition must fail before reaching handler")
    }
}
```

当前字符串接口会调用 handler，因此测试应先失败。

- [x] **Step 2: 修改 ActionHandler 与控制流**

```go
type ActionHandler interface {
    ExecuteAction(ctx context.Context, actionDef *ActionDef) error
    ExecuteCondition(condition *CompiledCondition) bool
    RegisterListen(refs []ListenRef) error
    CooperativeSleep(ctx context.Context, d time.Duration) error
}
```

loop、boolean、switch 先取条件拥有者的 `preparedCondition()`；非空字符串没有匹配程序时记录错误并返回 false，不调用 handler。测试 handler 改为记录 `condition.Source()`。

- [x] **Step 3: 修改 Robot handler**

```go
func (h *robotActionHandler) ExecuteCondition(condition *engine.CompiledCondition) bool {
    if script, ok := condition.LuaScript(); ok {
        return h.executeLuaBoolean(script)
    }
    return condition.EvalState(h.robot.state)
}
```

- [x] **Step 4: 修改 FieldBind 热路径**

`ActionExecutor` 所有普通 binding 和 map entry 调用点改为传入 `*FieldBind`，并使用：

```go
func (ae *ActionExecutor) bindingConditionSatisfied(binding *FieldBind) bool {
    if strings.TrimSpace(binding.Condition) == "" { return true }
    condition := binding.preparedCondition()
    if condition == nil {
        stresslog.Error("[ENGINE] 字段绑定条件尚未准备",
            zap.String("condition", binding.Condition))
        return false
    }
    return condition.EvalState(ae.store)
}
```

- [x] **Step 5: 接入生产加载入口和 codec 心跳绑定**

`cmd/agent/main.go`、`agent/task_runner.go` 改调 `PrepareTaskFlow`。`codecFieldBindsToEngine` 改为 `([]engine.FieldBind, error)`，递归转换后调用 `PrepareFieldBindings`；注册心跳前处理错误，不增加懒编译。

- [x] **Step 6: 迁移既有条件测试并删除旧入口**

现有 parser 测试 helper 改为先 `compileCondition("state:"+expr)` 再 `EvalState(store)`，原断言不删。物理删除 `EvalCondition` 和 inline 求值 parser。

Run separately:

```powershell
rg -n -F "EvalCondition(" engine robot agent cmd
```

```powershell
rg -n -F "parseExpr(" engine robot agent cmd
```

Expected: 两次均零匹配；`rg` exit 1 在这里表示通过。

- [x] **Step 7: 运行相关测试并提交**

Run: `go test ./engine ./robot ./agent ./cmd/agent`

Expected: PASS。随后精确暂存 Task 4 文件并提交：

```powershell
gofmt -w engine/executor.go engine/action.go robot/robot.go cmd/agent/main.go agent/task_runner.go engine/executor_switch_test.go engine/executor_wait_test.go engine/executor_action_test.go robot/action_handler_test.go engine/cond_parser_test.go
git add engine/executor.go engine/action.go robot/robot.go cmd/agent/main.go agent/task_runner.go engine/executor_switch_test.go engine/executor_wait_test.go engine/executor_action_test.go robot/action_handler_test.go engine/cond_parser_test.go engine/cond_eval.go engine/cond_parser.go
git diff --cached
git commit -m "perf: execute precompiled flow conditions"
```

## Task 5: Benchmark、后端审查和完整验证

**Files:**
- Modify: `engine/cond_benchmark_test.go`
- Modify: `docs/superpowers/plans/2026-08-12-condition-ast-implementation.md`

- [x] **Step 1: Benchmark 改为循环外编译**

```go
condition, err := compileCondition(tc.expr)
if err != nil { b.Fatal(err) }
b.ResetTimer()
b.ReportAllocs()
for b.Loop() {
    if !condition.EvalState(store) { b.Fatal("condition unexpectedly false") }
}
```

- [x] **Step 2: 运行相同 Benchmark 五次并比较 Task 1**

```powershell
$env:GOCACHE = Join-Path (Get-Location) '.gocache'
go test ./engine -run '^$' -bench '^BenchmarkConditionEvaluation$' -benchmem -count 5
```

Expected: 全部子基准不慢于解释执行；成功热路径不再包含 tokenize/parser 分配。若有分配，使用 memprofile 只定位并修复 AST 自身造成的分配。

- [x] **Step 3: 按 backend-review 检查结构与性能**

确认 AST 构造后只读、无全局缓存、不保存 Store/Robot/LState、所有 state 运行时读取、map entries 和心跳无遗漏、热路径无新增 goroutine/锁/对象池。

- [x] **Step 4: 后端编译和完整 Go 测试**

```powershell
$env:GOCACHE = Join-Path (Get-Location) '.gocache'
go build ./...
go test ./...
```

Expected: 两条命令 exit 0。

- [x] **Step 5: 前端类型检查和 Vitest**

```powershell
Push-Location cmd/web
npx.cmd tsc -b
npm.cmd run test
Pop-Location
```

Expected: TypeScript 编译和 Vitest 全部通过。

- [x] **Step 6: 配置校验与本地运行**

本阶段不改 `conf/flow/flow.json`，仍在前端编辑器打开并确认校验报告无错误。随后启动 standalone Agent：

```powershell
Remove-Item -LiteralPath log\stressbot.log -Force -ErrorAction SilentlyContinue
$agentProcess = Start-Process -FilePath "go" -ArgumentList @(
    "run", "./cmd/agent", "-config", "conf/config.json"
) -PassThru -WindowStyle Hidden
```

每 30 秒分别运行 `Get-Process -Id $agentProcess.Id` 和日志尾部检查，累计至少 2 分钟；每次等待单独执行，禁止一次阻塞超过 60 秒。结束时只运行 `Stop-Process -Id $agentProcess.Id`，不得按进程名批量终止。

- [x] **Step 7: 日志审查**

```powershell
Select-String -Path log\stressbot.log -Pattern 'error|warn|失败' -CaseSensitive:$false |
    Where-Object { $_.Line -notmatch 'headError' }
```

Expected: 无 AST 准备、未编译条件、panic 或新增异常。业务环境外部错误必须单独列出，不能声称为通过。

- [x] **Step 8: 检查最终差异并提交验证结果**

`git status --short` 和精确路径 diff 不得包含用户已有的 `conf/flow/flow.json`、事件化 listen 或前端业务修改。只暂存 benchmark 与已勾选计划记录并提交：

```powershell
git add engine/cond_benchmark_test.go docs/superpowers/plans/2026-08-12-condition-ast-implementation.md
git diff --cached
git commit -m "test: verify compiled condition performance"
```

- [x] **Step 9: 最终提交检查**

`git log -6 --oneline` 应显示基准、AST 核心、加载准备、运行切换、最终性能验证分阶段提交；`git status --short` 只保留本阶段开始前已有的用户修改和不相关未跟踪文件。
