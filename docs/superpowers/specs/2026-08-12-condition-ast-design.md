# 条件表达式专用 AST 预编译设计

日期：2026-08-12

## 1. 目标与范围

本项目只改造 `state:` 条件表达式的执行方式：从“每次求值都分词、解析并执行”改为“配置加载时编译一次，运行时直接执行不可变 AST”。

本次范围包括所有使用同一套条件语法的入口：

- `Node.Condition`
- `Node.BreakCondition`
- `SwitchCase.Condition`
- `ActionDef.Bindings` 中所有 `FieldBind.Condition`
- `FieldBind.Entries` 中递归嵌套的条件
- codec 心跳配置转换得到的 `engine.FieldBind.Condition`

不在本次范围内：

- 不引入 CEL 或其他通用表达式引擎。
- 不扩展条件语法、运算符或隐式类型转换。
- 不修改已经完成的事件化 `listen`。
- 不修改 Lua 条件脚本的执行方式。
- 不做 JIT、字节码虚拟机或激进常量折叠。

## 2. 当前问题

当前 `EvalCondition` 每次调用都会完成以下工作：

1. 检查并剥离 `state:` 前缀。
2. 扫描表达式并分配 token 切片。
3. 使用递归下降解析器重新识别语法结构。
4. 在解析过程中读取当前 Robot 的 `state.Store` 并求值。

同一个流程会被大量 Robot 反复执行，表达式文本和语法结构始终不变，只有 `state.Store` 中的数据变化。重复解析因此属于可以消除的固定开销。

## 3. 方案比较

### 方案 A：在 `EvalCondition` 内增加全局字符串缓存

改动最小，但每次执行仍需做全局 map 查询；缓存生命周期与流程生命周期无关，热加载后存在无界增长风险；也无法保证所有生产条件都在加载期编译完成。

### 方案 B：在 `TaskFlow` 上维护 `map[string]*Program`

缓存生命周期得到控制，同一表达式可以去重。不过执行热路径仍要通过原字符串查询 map，条件字段与已编译程序之间没有结构化绑定，漏编译时容易退回旧解析路径。

### 方案 C：条件拥有者直接保存编译结果（采用）

在 `Node`、`SwitchCase` 和 `FieldBind` 上增加不参与 JSON 序列化的私有编译字段。加载阶段递归编译，执行阶段直接取对应程序指针，不做字符串查找。单次准备过程中按规范化后的原表达式去重，因此相同条件仍共享同一个不可变程序。

该方案热路径最短，缓存生命周期严格受 `TaskFlow` 或心跳配置约束，也最容易强制执行“没有编译就不运行”。

## 4. 编译模型

### 4.1 编译产物

编译产物使用项目专用的不可变 `ConditionProgram`，内部采用紧凑节点数组和节点索引，而不是由大量接口对象组成的树：

```go
type ConditionProgram struct {
    source string
    root   nodeIndex
    nodes  []conditionNode
}
```

节点类型只覆盖现有语法：

- state 路径
- 整数、浮点数、字符串字面量
- 逻辑与、逻辑或、逻辑非
- 比较运算
- 加减乘除、取模和一元负号

`ConditionProgram` 不保存 `state.Store`、Robot、Lua 状态或任何运行期结果。程序完成构造后只读，可由同一个 `TaskFlow` 下的所有 Robot 并发共享。

### 4.2 准备入口

把现有只做校验的流程准备阶段收口为统一入口，例如：

```go
func PrepareTaskFlow(flow *TaskFlow) error
```

它同时完成当前 `ValidateStateActions` 的保护状态检查和所有条件的编译。standalone 与 distributed Agent 的 flow 加载入口都必须调用它。成功返回后，flow 才能交给 `Manager` 和 `Executor`。

codec 心跳绑定不是 `TaskFlow` 的成员，转换为 `engine.FieldBind` 后通过独立的绑定准备函数递归编译；注册心跳前任何编译错误都直接终止注册并返回带位置上下文的中文错误。

### 4.3 去重

一次准备过程维护局部编译器：

```text
规范化表达式文本 → *ConditionProgram
```

相同 `state:` 条件只编译一次。该表只在准备阶段存在，准备完成后由各字段直接持有程序指针，不进入运行热路径，也不会成为跨流程的全局缓存。

## 5. 运行时求值

每次求值创建栈上求值上下文：

```go
type conditionEvalContext struct {
    store    *state.Store
    firstErr error
}
```

AST 中的路径节点只保存路径文本。执行到该节点时才调用当前 Robot 的 `store.GetPath(path)`，因此 state 可以在不同时间存在、缺失、更新或被清除；不同 Robot 共享 AST 也不会共享状态值。

运行时不再分词、不再解析、不查全局缓存，也不把 Store 整体物化成 map。

### 5.1 保持的既有语义

新执行器必须逐字保持以下行为：

- 空条件为 `true`。
- `state:` 路径每次从当前 Store 实时读取。
- 缺失路径产生一次警告，并在当前子表达式中按 effective-false 处理。
- `missing || fallback` 可以返回 `true`，同时保留 missing 警告。
- `&&` 和 `||` 严格短路；被短路一侧不得读取 Store，也不得产生错误或警告。
- 布尔上下文只接受 `bool`，不增加 truthy 规则。
- 保持现有运算符优先级，包括现有的逻辑非解析语义。
- 保持 int、uint、float 混合比较和大 `uint64` 的精确比较。
- 保持整数除法向零截断、取模符号和除零错误行为。
- 顶层结果不是 bool 时返回 `false` 并警告。
- 只记录首次运行期错误，日志内容继续携带原始表达式。

数值字面量在编译阶段完成解析。对于当前属于“语法合法、运行时数值错误”的情况（例如超出 `int64` 的整数字面量），编译器生成确定性错误节点，仍在实际执行到该节点时按原时机报告；不借预编译偷偷改变短路和错误可见性。

### 5.2 Lua 与未知前缀

`lua:` 条件只在准备阶段形成轻量的条件描述，仍由 `robotActionHandler` 调用现有 Lua 布尔脚本执行，不编译进 state AST。

字段绑定当前只具备 state 条件执行能力。本次不顺带新增 Lua 绑定能力；非 `state:` 绑定条件保持当前失败语义，不扩大功能范围。

## 6. 严格性与生命周期

生产运行路径不保留“AST 不存在时重新解析字符串”的兼容兜底：

- flow 加载时表达式结构错误，`PrepareTaskFlow` 直接失败。
- 心跳绑定编译失败，心跳注册直接失败。
- 已准备对象的条件字符串若随后被修改，编译产物中的规范化 source 与当前字符串不一致，视为未准备的配置错误；不得执行旧 AST，也不得动态重编译。
- 动态构造带条件的 `TaskFlow`（主要是测试）必须显式调用准备入口。

这项约束防止某个遗漏入口继续在运行期解析，也避免配置热修改后执行与文本不一致的旧程序。流程对象在准备成功后应按不可变配置使用。

## 7. 数据流

```text
读取 flow.json
    ↓
JSON 反序列化为 TaskFlow
    ↓
PrepareTaskFlow
    ├─ 原有状态动作约束检查
    ├─ 遍历 Node.Condition / BreakCondition / SwitchCase
    ├─ 递归遍历 ActionDef.Bindings / map entries
    └─ 编译并按表达式去重
    ↓
共享只读 TaskFlow + ConditionProgram
    ↓
每个 Robot 执行时传入自己的 state.Store
```

## 8. 错误处理

编译错误必须包含条件所在位置和原始表达式，例如：

```text
节点 "match" condition 条件表达式语法错误 "state:hp >": 缺少右操作数
```

运行期数据错误继续是条件结果而不是流程加载错误，例如路径暂时不存在、当前值类型不匹配或除数来自 state 且本次为零。这些错误保持 local-false、短路和一次警告语义。

“未准备”“准备后文本被修改”属于程序配置生命周期错误，应 fail-closed 并输出明确错误，不回退解析。

## 9. 测试与性能验收

### 9.1 TDD 与语义测试

先增加会失败的编译/执行测试，再实现生产代码。测试至少覆盖：

- AST 结构和运算符优先级。
- 所有现有条件测试迁移到 compile-once/evaluate-many 模式。
- 同一程序在 state 缺失、写入、更新、清除后的实时结果。
- 短路侧不读取缺失路径。
- local-false、首次错误、非 bool 顶层结果。
- 全部数值边界，尤其大 `uint64`、整数除法和数值字面量溢出。
- flow 中所有条件位置都已准备，包括递归 map entry。
- 重复表达式共享程序。
- 未准备及准备后文本修改时 fail-closed。
- 同一程序被多个 goroutine 配合不同 Store 并发读取，程序本身无可变状态。
- codec 心跳绑定条件完成准备。

### 9.2 基准

在改造前先记录当前解释执行基线，再用同一组表达式测量预编译版本：

- 简单路径比较。
- 算术与取模。
- 多层 `&&` / `||` 和括号。
- 短路命中。
- 嵌套路径读取。

报告 `ns/op`、`B/op` 和 `allocs/op`。验收目标为：

- 成功求值热路径不再包含 tokenize/parse 分配。
- 常见成功表达式争取达到 `0 allocs/op`；若 Store 路径实现导致不可消除分配，必须在结果中单独归因。
- 预编译版本在上述基准中不得慢于当前解释执行。
- 语义测试全部通过后才能删除旧运行时解析入口。

## 10. 交付边界

本阶段只提交条件 AST 所需的引擎、加载入口、测试和必要注释。不会改 flow.json 业务内容、前端、事件化 listen、sqlc 或 CEL，也不会提交工作区中已有的其他修改。
