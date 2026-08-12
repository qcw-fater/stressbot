# 流程节点系统技术文档

## 1. 概述

`engine` 包实现了基于有向图的流程执行引擎，通过 8 种节点类型表达完整的控制流语义。所有副作用集中在 `action` 节点，其余节点为纯控制流。流程定义来自 `flow.json` 配置文件。

核心设计思路：将流程节点系统设计为编程语言控制流原语的声明式对应物。`action` 节点相当于一条可执行语句（函数调用），是唯一产生实际副作用的节点。其余所有节点相当于纯控制流结构，仅负责串联 `action` 的执行顺序，不产生副作用。

### 设计原则

| 原则 | 说明 |
|------|------|
| 最小原语集 | 只保留无法由现有类型组合出来的节点类型 |
| 全部串行 | 流程内不存在并行语义，所有节点按声明顺序执行 |
| 单一职责 | 每种节点只做一件事：`loop` 只负责循环控制，不内嵌多节点列表；`sequence` 负责顺序编排 |
| 干净的 JSON 格式 | `nodes` 直接使用 map 格式（key = 节点 ID），无需自定义反序列化 |
| 两类延迟职责分离 | `defaultDelayMs`（框架负责节奏控制）与 `wait` 节点（用户负责业务等待）严格区分 |
| 类型精确 | 每种节点只保留与其语义相关的字段，不共用语义不明的字段 |

## 2. 文件结构

| 文件 | 职责 |
|------|------|
| `engine/flow.go` | TaskFlow 数据模型 -- 节点图 + 动作定义 + 监听定义 + 所有常量定义 |
| `engine/executor.go` | Executor -- 图遍历与节点调度、循环控制信号、延迟机制 |
| `engine/action.go` | ActionExecutor -- 声明式动作执行引擎、NetSender 接口、字段绑定解析 |
| `engine/errors.go` | ActionError 结构化错误 + 哨兵错误 + 循环控制信号定义 |
| `engine/cond_eval.go` | 条件表达式求值入口（`state:` 前缀） |
| `engine/cond_parser.go` | 递归下降布尔表达式解析器（支持 `&&`/`||`/`!`/括号嵌套） |

## 3. TaskFlow 数据模型

### 3.1 顶层结构

```go
type TaskFlow struct {
    DefaultDelayMs int                   `json:"defaultDelayMs"` // 全局节点间默认延迟（毫秒）
    Nodes          map[string]*Node      `json:"nodes"`          // 节点映射，key 为节点 ID
    Actions        map[string]*ActionDef `json:"actions"`        // 动作定义映射
    Listens        map[string]*ListenDef `json:"listens"`        // 监听定义映射
}
```

`Nodes` 使用 JSON 对象反序列化为 `map[string]*Node`，key 即节点 ID，节点内部无需 `id` 字段。入口节点固定为 `"main"`。

访问方法：
- `Node(id string) (*Node, bool)` -- 获取指定 ID 的节点
- `Action(name string) (*ActionDef, bool)` -- 获取指定名称的动作
- `Listen(name string) (*ListenDef, bool)` -- 获取指定名称的监听定义

### 3.2 flow.json 格式

```json
{
  "defaultDelayMs": 500,
  "nodes": {
    "main": { "type": "sequence", "next": ["init", "businessLoop"] },
    "businessLoop": { "type": "loop", "loopCount": -1, "body": "businessWeight" }
  },
  "actions": { ... },
  "listens": { ... }
}
```

## 4. 8 种节点类型

### 4.1 完整节点类型表

| 类型 | 编程类比 | 核心作用 | 对应常量 |
|------|----------|----------|----------|
| `sequence` | `{ stmt1; stmt2; stmt3 }` | 顺序执行 `next` 中列出的所有子节点 | `NodeSequence` |
| `action` | `funcCall()` | 执行声明式动作或 Lua 脚本，唯一产生副作用的节点 | `NodeAction` |
| `loop` | `for` | 循环执行单个 `body` 节点，支持次数/前置条件/后置条件 | `NodeLoop` |
| `boolean` | `if / else` | 对 `condition` 求值，跳转到 `trueNext` 或 `falseNext` | `NodeBoolean` |
| `weighted` | `switch(rand)` | 按 `options` 中的权重随机选择一路节点执行 | `NodeWeighted` |
| `wait` | `time.Sleep(n)` | 用户显式等待，与全局默认延迟无关 | `NodeWait` |
| `break` | `break` | 产生 `errBreak` 信号，中断最近的 `loop` | `NodeBreak` |
| `continue` | `continue` | 产生 `errContinue` 信号，跳过本次迭代 | `NodeContinue` |

### 4.2 sequence 节点

**用途**：顺序执行 `next` 列表中的所有子节点。

**字段**：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"sequence"` |
| `Next` | `[]string` | `next` | 按顺序依次执行的子节点 ID 列表 |

**行为**：
- 按 `Next` 列表顺序依次执行每个子节点
- 捕获 `errSkip` 信号时跳过剩余子节点（视为本 sequence 正常完成）
- 透传其他所有错误和信号（含 `errBreak` / `errContinue`）
- 子节点执行出错时立即返回错误，不继续后续子节点

**JSON 示例**：
```json
{
  "type": "sequence",
  "next": ["authLogin", "logicLogin", "businessLoop"]
}
```

### 4.3 action 节点

**用途**：执行一个声明式动作或 Lua 脚本。这是唯一产生实际副作用的节点类型。

**字段**：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"action"` |
| `Action` | string | `action` | 引用 `actions` 表中的动作名称 |
| `OnError` | `*OnErrorDef` | `onError` | 动作失败后的错误链路：ignoreCodes / handler / retry / strategy |
| `ListenRefs` | `[]ListenRef` | `listenRefs` | 动作执行后注册的持久化推送监听引用 |
| `DelayMs` | int | `delayMs` | 节点级延迟覆盖（>0 使用此值，=0 使用 DefaultDelayMs，<0 禁用） |

**行为**：
- 查找 `actions[action]` 获取 ActionDef
- 调用 `ActionHandler.ExecuteAction(ctx, actionDef)` 执行动作
- 执行失败时进入 `onError` 链路：
  - context 取消（Canceled/DeadlineExceeded）直接传播，不走 `onError`，不执行节点延迟
  - `ErrActionCanceled` 映射为 `context.Canceled`，不走 `onError`，不执行节点延迟
  - 命中 `ignoreCodes`：warn 日志，流程继续，monitor 保留原始失败样本
  - `handler`：执行普通节点子流程，完成后回到错误链路继续判断 retry / strategy
  - `retry`：只重试当前 action；`maxRetries` 是额外重试次数
  - `strategy`：空/`resume` 继续，`skip` 返回 `errSkip`，`abort` 返回 `ActionError(errcode.ErrExecFailed)`
- action 最终成功或命中 `ignoreCodes` 后，如果 `ListenRefs` 非空，调用 `ActionHandler.RegisterListen(listenRefs)` 注册监听
- 监听注册失败不进入 handler/retry/ignoreCodes；执行节点延迟后按 `onError.strategy` 收束
- 最终成功、ignore 或最终失败收束前执行节点级延迟 `nodeDelay()`；重试间隔只执行 `retryDelayMs`

**onError.strategy 常量**：

| 常量 | 值 | 行为 |
|------|----|------|
| `StrategyResume` | `"resume"` | 继续原流程 |
| `StrategyAbort` | `"abort"` | 返回 `ActionError`，中断流程 |
| `StrategySkip` | `"skip"` | 发射 `errSkip`，跳过当前层级 |

**JSON 示例**：
```json
{
  "type": "action",
  "action": "AuthLogin",
  "onError": { "strategy": "abort" },
  "listenRefs": [
    { "route": {"cmd": 3, "act": 1}, "server": "tcp:logic", "listen": "matchPoll" }
  ],
  "delayMs": 500
}
```

### 4.4 loop 节点

**用途**：循环执行单个 `body` 节点。多步骤循环体通过 `sequence` 节点包装。

**字段**：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"loop"` |
| `Body` | string | `body` | 循环体节点 ID（单个）；多步骤时指向一个 sequence 节点 |
| `LoopCount` | int | `loopCount` | 循环次数；<=0 = 无限循环；0 = 不执行 |
| `Condition` | string | `condition` | 前置条件：每次迭代开始前求值，false 时退出循环 |
| `BreakCondition` | string | `breakCondition` | 后置条件：每次迭代结束后求值，true 时退出循环 |

**行为**：
- `LoopCount == 0` 时直接跳过循环体，返回 nil
- `LoopCount < 0` 时为无限循环
- 每次迭代：
  1. 检查 `ctx` 取消信号
  2. 前置条件检查：如果 `Condition` 非空，调用 `ExecuteBoolean(condition)`，返回 false 时退出循环
  3. 执行循环体 `body` 节点
  4. 捕获 `errBreak` 或 `errSkip` 时退出循环
  5. 捕获 `errContinue` 时跳到下一次迭代
  6. 真实错误向上传播
  7. 后置条件检查：如果 `BreakCondition` 非空，调用 `ExecuteBoolean(breakCondition)`，返回 true 时退出循环

**四种循环模式对应关系**：

| Go 写法 | loop 节点配置 |
|---------|---------------|
| `for {}` | `loopCount: -1`（或省略） |
| `for i < N {}` | `loopCount: N` |
| `for condition {}` | `loopCount: -1, condition: "lua:cond.lua"` |
| `do {} while !stop` | `loopCount: -1, breakCondition: "lua:stop.lua"` |
| `for { if x { break } }` | body 内 sequence 含 boolean -> break 节点 |
| `for { if x { continue } }` | body 内 sequence 含 boolean -> continue 节点 |

**JSON 示例 -- 带条件退出**：
```json
"matchRetryLoop": {
  "type": "loop",
  "loopCount": 10,
  "body": "matchRetryBody"
},
"matchRetryBody": {
  "type": "sequence",
  "next": ["StartMatch", "checkMatchResult", "retryWait"]
}
```

### 4.5 boolean 节点

**用途**：对条件表达式求值，根据结果跳转到不同分支。

**字段**：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"boolean"` |
| `Condition` | string | `condition` | 条件表达式（`state:` 或 `lua:` 前缀） |
| `TrueNext` | string | `trueNext` | 条件为 true 时跳转的节点 ID（空 = 不跳转） |
| `FalseNext` | string | `falseNext` | 条件为 false 时跳转的节点 ID（空 = 不跳转） |

**行为**：
- 调用 `ExecuteBoolean(condition)` 对条件表达式求值
- 根据结果选择 `TrueNext` 或 `FalseNext` 作为目标节点
- 目标节点 ID 为空时，不执行任何操作（相当于"顺序流出"）
- 执行目标节点后，如果返回 `errSkip`，静默处理（返回 nil）

**注意**：`Condition` 字段在 `loop` 和 `boolean` 之间复用。两者语义一致（求值为 bool），且一个节点只会是一种类型，不会产生歧义。

**与计划的差异**：计划中 boolean 节点执行后调用 `nodeDelay()`，实际代码中 boolean 节点不调用 `nodeDelay()`（仅 action 节点调用延迟）。

**JSON 示例**：
```json
{
  "type": "boolean",
  "condition": "lua:has_role.lua",
  "trueNext": "startGame",
  "falseNext": "createRole"
}
```

### 4.6 weighted 节点

**用途**：按权重随机选择一个 option 执行。

**字段**：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"weighted"` |
| `Options` | `[]WeightedOption` | `options` | 加权选项列表 |

**WeightedOption 结构**：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Node` | string | `node` | 目标节点 ID |
| `Weight` | int | `weight` | 权重值 |

**行为**：
- 计算所有选项权重之和（负权重视为 0）
- 权重之和 <= 0 时跳过执行
- 使用累积和算法随机选择一个 option
- 执行选中的节点，捕获 `errSkip` 时静默处理
- 如果所有选项都未命中（浮点精度问题），执行最后一个选项作为兜底

**JSON 示例**：
```json
{
  "type": "weighted",
  "options": [
    {"node": "battle", "weight": 40},
    {"node": "lobby", "weight": 60}
  ]
}
```

### 4.7 wait 节点

**用途**：用户在流程中显式插入的等待步骤，与全局默认延迟完全独立。

**字段**：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"wait"` |
| `WaitMs` | int | `waitMs` | 固定等待时长（毫秒） |
| `WaitMin` | int | `waitMin` | 随机等待最小值（毫秒，含） |
| `WaitMax` | int | `waitMax` | 随机等待最大值（毫秒，含） |
| `Then` | string | `then` | 等待完成后执行的唯一后继节点 ID（可选） |

**行为**：
- 优先级：`WaitMin/WaitMax`（随机模式）> `WaitMs`（固定模式）
- `WaitMin > 0 && WaitMax > 0` 时使用随机模式：等待 `[WaitMin, WaitMax]` 范围内的随机毫秒数
  - `WaitMin >= WaitMax` 时使用 `WaitMin` 作为固定值
- 仅 `WaitMin` 或 `WaitMax` 其中一个 > 0 时，打印警告并跳过
- `WaitMs > 0` 时等待固定毫秒数
- `WaitMs < 0` 时打印警告并跳过
- 所有等待均可被 context 取消中断
- 等待成功或因参数无效被跳过后，`Then` 非空时执行对应后继节点
- context 取消或协作式休眠失败时直接返回，不执行 `Then`

**与计划的差异**：计划中 `wait` 节点仅有 `WaitMs int` 单一字段。实际代码额外支持 `WaitMin/WaitMax` 随机等待范围，提供更灵活的业务模拟。

**JSON 示例**：
```json
{ "type": "wait", "waitMs": 2000 }
{ "type": "wait", "waitMin": 1000, "waitMax": 3000 }
{ "type": "wait", "waitMs": 2000, "then": "nextAction" }
```

### 4.8 break 节点

**用途**：产生 `errBreak` 信号，中断最近的 `loop`。

**字段**：无额外字段。仅有 `type: "break"`。

**行为**：
- 在 `executeNode` 的 switch 中直接返回 `errBreak`
- 信号通过 Go error 机制在节点树中向上冒泡
- 冒泡路径：`executeBreak -> executeSequence（透传） -> executeLoop（捕获）`
- 信号不会穿透 `executeLoop`，外层 sequence 不会感知到它

**JSON 示例**：
```json
{ "type": "break" }
```

### 4.9 continue 节点

**用途**：产生 `errContinue` 信号，跳过本次迭代剩余步骤，进入下一次迭代。

**字段**：无额外字段。仅有 `type: "continue"`。

**行为**：
- 在 `executeNode` 的 switch 中直接返回 `errContinue`
- 信号冒泡到最近的 `executeLoop` 时被捕获，进入下一次迭代
- 冒泡路径与 `break` 相同，只是捕获方处理方式不同

**JSON 示例**：
```json
{ "type": "continue" }
```

## 5. 循环控制信号传播

### 5.1 信号定义

```go
var (
    errBreak    = errors.New("break")
    errContinue = errors.New("continue")
    errSkip     = errors.New("skip")
)
```

这三个信号通过 Go 的 error 传播机制在节点树中冒泡。

### 5.2 传播规则

| 节点 | 对 errBreak 的处理 | 对 errContinue 的处理 | 对 errSkip 的处理 |
|------|--------------------|-----------------------|-------------------|
| `executeBreak` | 产生 errBreak | - | - |
| `executeContinue` | - | 产生 errContinue | - |
| `executeSequence` | 透传（不捕获） | 透传（不捕获） | 捕获，跳过剩余子节点 |
| `executeBoolean` | 透传 | 透传 | 捕获，返回 nil |
| `executeWeighted` | 透传 | 透传 | 捕获，返回 nil |
| `executeLoop` | 捕获，退出循环 | 捕获，继续下次迭代 | 捕获，退出循环 |
| `executeAction` | - | - | 由 `onError.strategy=skip` 产生 |

### 5.3 信号传播完整链路

以"在循环内用 boolean 触发 break"为例：

```
executeLoop()
  iter 1: executeNode(node.Body)  ->  executeSequence()
    executeNode("doSomething") -> nil
    executeNode("checkBreak")
      executeBoolean()
        condition = true -> executeNode("doBreak")
          case "break": return errBreak
        <- errBreak
      <- errBreak
    <- errBreak (sequence 透传)
  <- err = errBreak
  if errors.Is(err, errBreak) { break }  <- 被 loop 捕获
  return nil  <- loop 正常退出
```

`errBreak` 不会穿透 `executeLoop`，外层的 sequence 或其他节点不会感知到它。

## 6. 延迟机制

### 6.1 两类延迟的职责分离

**defaultDelayMs（框架负责 -- 全局节奏控制）**：

每个 action 节点执行完后，框架自动等待的最小间隔。目的：避免压测机器人执行过快，模拟真实用户的操作节奏。来源：`TaskFlow.DefaultDelayMs`，节点级 `DelayMs` 可覆盖。

**wait 节点（用户负责 -- 业务逻辑控制）**：

用户在 flow.json 中显式插入的等待步骤。目的：在特定业务节点之间插入固定等待，如等待服务器处理、等待动画等。与 defaultDelayMs 完全独立，两者叠加生效。

### 6.2 nodeDelay 方法

```go
func (e *Executor) nodeDelay(ctx context.Context, node *Node) {
    ms := node.DelayMs
    if ms == 0 {
        ms = e.defaultDelayMs
    }
    if ms < 0 {
        return // 禁用延迟
    }
    select {
    case <-ctx.Done():
    case <-time.After(time.Duration(ms) * time.Millisecond):
    }
}
```

**延迟优先级**：`node.DelayMs` > `TaskFlow.DefaultDelayMs`

| DelayMs 值 | 行为 |
|------------|------|
| > 0 | 使用此值作为延迟毫秒数 |
| = 0 | 使用 `TaskFlow.DefaultDelayMs` |
| < 0 | 禁用延迟 |

**注意**：`nodeDelay` 仅在 `action` 节点执行完后调用。`boolean` 节点不触发延迟。

## 7. Executor -- 图遍历

### 7.1 Executor 结构体

```go
type Executor struct {
    flow           *TaskFlow
    handler        ActionHandler
    defaultDelayMs int    // 解析自 flow.DefaultDelayMs，初始化后只读
    caller         string // 调用方标识（机器人账号），用于日志追踪
}
```

### 7.2 创建与运行

```go
func NewExecutor(flow *TaskFlow, handler ActionHandler, caller string) *Executor
func (e *Executor) Run(ctx context.Context) error   // 从 "main" 节点开始执行
func (e *Executor) Flow() *TaskFlow                  // 返回流程图定义
```

`Run()` 从 `"main"` 节点开始执行流程，阻塞直到流程结束或 context 取消。

### 7.3 executeNode 路由

```go
func (e *Executor) executeNode(ctx context.Context, nodeID string) error
```

按 nodeID 查找节点，按 `Type` 字段分发到对应的执行方法。每步执行前检查 `ctx.Err()`。

路由规则：
- `NodeSequence` -> `executeSequence()`
- `NodeAction` -> `executeAction()`
- `NodeLoop` -> `executeLoop()`
- `NodeBoolean` -> `executeBoolean()`
- `NodeWeighted` -> `executeWeighted()`
- `NodeWait` -> `executeWait()`
- `NodeBreak` -> 直接返回 `errBreak`
- `NodeContinue` -> 直接返回 `errContinue`

### 7.4 ActionHandler 接口

```go
type ActionHandler interface {
    ExecuteAction(actionDef *ActionDef) error
    ExecuteBoolean(expression string) bool
    RegisterListen(refs []ListenRef) error
}
```

由 `robot.robotActionHandler` 实现，负责具体的网络请求、Lua 脚本执行、条件判断和推送监听注册。

**与计划的差异**：计划中的 `ActionHandler` 有 3 个方法，实际代码一致。计划中提到了删除 `OnNodeError`，实际代码中确实没有这个方法。

## 8. 节点字段速查表

| 字段 | Go 类型 | JSON 键 | 适用节点 | 说明 |
|------|---------|---------|----------|------|
| `Type` | string | `type` | 所有 | 节点类型 |
| `Next` | `[]string` | `next` | sequence | 子节点 ID 列表（顺序执行） |
| `Body` | string | `body` | loop | 循环体节点 ID（单个） |
| `LoopCount` | int | `loopCount` | loop | 循环次数，<=0 无限，0 不执行 |
| `Condition` | string | `condition` | loop / boolean | loop：前置条件；boolean：分支条件 |
| `BreakCondition` | string | `breakCondition` | loop | 后置退出条件 |
| `TrueNext` | string | `trueNext` | boolean | 条件为 true 时的目标节点 |
| `FalseNext` | string | `falseNext` | boolean | 条件为 false 时的目标节点 |
| `Options` | `[]WeightedOption` | `options` | weighted | 加权选项列表 |
| `Action` | string | `action` | action | 引用 actions 表中的动作名 |
| `OnError` | `*OnErrorDef` | `onError` | action | 错误处理链路 |
| `ListenRefs` | `[]ListenRef` | `listenRefs` | action | 动作后注册的推送监听引用 |
| `WaitMs` | int | `waitMs` | wait | 固定等待时长（毫秒） |
| `WaitMin` | int | `waitMin` | wait | 随机等待最小值 |
| `WaitMax` | int | `waitMax` | wait | 随机等待最大值 |
| `Then` | string | `then` | wait | 等待完成后的唯一后继节点 ID |
| `DelayMs` | int | `delayMs` | action | 覆盖 defaultDelayMs |

## 9. ActionDef -- 动作定义

### 9.1 结构体

```go
type ActionDef struct {
    Name        string         `json:"-"`           // 运行时回填（actions map 的 key）
    Pattern     string         `json:"pattern"`     // 动作模式
    Service     string         `json:"service"`     // 目标服务名
    Route       any            `json:"route"`       // 不透明路由
    Script      string         `json:"script"`      // Lua 脚本路径（lua 模式）
    Address     string         `json:"address"`     // 连接地址（connect 模式）
    C2SProto    string         `json:"c2sProto"`    // 客户端消息 proto 全名
    S2CProto    string         `json:"s2cProto"`    // 服务器响应 proto 全名
    Bindings    []FieldBind    `json:"bindings"`    // C2S 字段绑定
    Store       []StoreMapping `json:"store"`       // S2C 响应字段 -> 状态存储映射
    Timeout     int            `json:"timeout"`     // 超时秒数（listen 模式）
    Keys        []string       `json:"keys"`        // clearState 要清除的 key 列表
    URL         string         `json:"url"`         // HTTP 请求 URL
    Method      string         `json:"method"`      // HTTP 方法（POST 默认 / GET）
    ContentType string         `json:"contentType"` // HTTP 内容类型（json 默认 / form）
}
```

### 9.2 14 种 Pattern

**网络通信（TCP）**：

| Pattern 常量 | 值 | 说明 |
|---------------|----|------|
| `PatternTCPSend` | `"tcpSend"` | TCP 单向发送 |
| `PatternTCPRequest` | `"tcpRequest"` | TCP 请求-响应 |
| `PatternTCPConnect` | `"tcpConnect"` | TCP 连接建立 |
| `PatternTCPClose` | `"tcpClose"` | TCP 连接关闭 |
| `PatternTCPListen` | `"tcpListen"` | TCP 持久推送监听 |

**网络通信（UDP）**：

| Pattern 常量 | 值 | 说明 |
|---------------|----|------|
| `PatternUDPSend` | `"udpSend"` | UDP 单向发送 |
| `PatternUDPRequest` | `"udpRequest"` | UDP 请求-响应 |
| `PatternUDPConnect` | `"udpConnect"` | UDP 连接建立 |
| `PatternUDPClose` | `"udpClose"` | UDP 连接关闭 |
| `PatternUDPListen` | `"udpListen"` | UDP 持久推送监听 |

**HTTP**：

| Pattern 常量 | 值 | 说明 |
|---------------|----|------|
| `PatternHTTPRequest` | `"httpRequest"` | HTTP 请求（支持 JSON/form body） |

**状态操作**：

| Pattern 常量 | 值 | 说明 |
|---------------|----|------|
| `PatternSetState` | `"setState"` | 设置状态变量 |
| `PatternClearState` | `"clearState"` | 清除状态变量 |

**脚本**：

| Pattern 常量 | 值 | 说明 |
|---------------|----|------|
| `PatternLua` | `"lua"` | Lua 脚本执行（由 robot 层 ActionHandler 处理） |

**与计划的差异**：计划中有 16 种 pattern（含 `exchangeKey` 和 `sleep`）。实际代码移除了 `exchangeKey`（由 Lua 脚本或通用 tcpRequest 替代）和 `sleep`（由 `wait` 节点替代），共 14 种。

### 9.3 各 Pattern 执行行为

**tcpSend / udpSend**：
1. 调用 `buildBody(def)` 构建 protobuf 消息体
2. 调用 `adapter.ExpectedRouteKey(def.Route)` 计算路由键
3. 获取加密密钥
4. 调用 `adapter.EncodeTCP/UDP(route, body, secretKey)` 编码完整包
5. 调用 `netSender.TCPSend/UDPSend(service, packet)` 发送
6. 编码返回 nil 时返回 `ErrEncodeFailed`

**tcpRequest / udpRequest**：
1. 同 tcpSend 的 1-4 步构建并发送请求包
2. 调用 `netSender.TCPRequest/UDPRequest(service, packet, routeKey, timeout...)` 等待响应
3. `headerErr != 0` 时：解析响应体，构造 `NewActionError(errcode.ErrorCode(headerErr), ...)` 返回
4. 调用 `parseAndStoreResponse(def, respBody)` 解析 S2C proto 并存储字段
5. 返回发送字节数、接收字节数和错误

**tcpConnect / udpConnect**：
1. 调用 `resolveAddress(def.Address)` 解析地址（支持 `state:` 前缀）
2. 地址为空时返回 `ErrAddrEmpty`
3. 调用 `netSender.ConnectTCP/UDP(service, addr)` 建立连接

**tcpClose / udpClose**：
1. 调用 `netSender.CloseTCP/UDP(service)` 关闭连接

**tcpListen / udpListen**：
1. 计算超时（默认 `DefaultListenTimeoutSec = 60` 秒）
2. 计算路由键
3. 通过队列事件等待已预注册监听（等待期间继续处理 Robot mailbox）：
   - 队列已有消息时立即返回
   - 新消息入队时由容量 1 的边沿通知唤醒
   - 同时监听 context 取消和 deadline
   - 收到响应后：检查 headerErr、解析存储、返回
4. 超时时返回 `NewActionError(ErrListenTimeout, ...)`

**httpRequest**：
1. 解析 URL（支持 `state:` 前缀）
2. 确定 HTTP 方法（默认 POST）和内容类型（默认 json）
3. 根据 contentType 构建请求体：
   - `json`：将 bindings 转为 map，JSON 序列化
   - `form`：将 bindings 转为 url.Values
4. 调用 `netSender.HTTPRequest` 发送请求
5. 非 2xx 状态码返回 `ErrHTTPStatus`
6. 成功时解析 JSON 响应并存储

**setState**：
1. 遍历 bindings，对每个绑定：
   - 检查 Condition 表达式
   - 调用 `resolveFieldValue` 解析值
   - 跳过 nil 值
   - 使用 `Field` 作为 key 写入 state

**clearState**：
1. 遍历 `Keys` 列表，逐一调用 `store.Delete(key)`

**lua**：
1. 由 `robotActionHandler.ExecuteAction` 特殊处理
2. 当前 Robot 主流程获取独占 LState
3. 调用 `luaPool.RunActionScript(L, scriptName)` 同步执行脚本
4. 阻塞型 Lua API 只暂停当前主流程；connectionPump 与声明式心跳继续独立运行
5. 脚本 `return nil` 表示成功；`return err table` 时由 runtime 重建 `*ActionError` 透传（含 Code/Detail）；返回 number 等非法值 fail loud（报错）

## 10. FieldBind -- 字段绑定

### 10.1 结构体

```go
type FieldBind struct {
    Field         string      `json:"field"`         // 目标 proto 字段名
    Type          string      `json:"type"`          // 绑定类型
    Value         any         `json:"value"`         // fixed: 固定值
    Source        string      `json:"source"`        // 数据来源 state key
    Path          string      `json:"path"`          // 嵌套字段导航
    Values        []any       `json:"values"`        // 候选值列表
    Required      bool        `json:"required"`      // 缺失时报错
    Filters       []FilterDef `json:"filters"`       // 过滤条件列表
    Min           int         `json:"min"`           // 随机数最小值
    Max           int         `json:"max"`           // 随机数最大值
    Precision     int         `json:"precision"`     // 浮点精度
    Length        int         `json:"length"`        // 字符串长度
    Count         int         `json:"count"`         // 选取数量
    Charset       string      `json:"charset"`       // 字符集别名或自定义字符集
    ExcludeSource string      `json:"excludeSource"` // 排除列表来源
    Optional      bool        `json:"optional"`      // 允许字段为空
    Wrap          bool        `json:"wrap"`          // 单值包装为切片
    StoreAs       string      `json:"storeAs"`       // 中间变量存储
    KeySource     string      `json:"keySource"`     // randomPickMap 的 key 来源
    Condition     string      `json:"condition"`     // 条件绑定
}
```

### 10.2 17 种 Binding Type

**取值类**：

| 常量 | 值 | 说明 |
|------|----|------|
| `BindFixed` | `"fixed"` | 固定值（`Value` 字段） |
| `BindState` | `"state"` | 从 StateStore 读取（`Source` 为 key），隐式 required |
| `BindStateFirst` | `"stateFirst"` | 从 StateStore 列表取第一个元素，空列表返回 nil |

**随机选取类**：

| 常量 | 值 | 说明 |
|------|----|------|
| `BindStateRandom` | `"stateRandom"` | 从 StateStore 列表随机选一个，支持 Filters 过滤，隐式 required |
| `BindStateRandomN` | `"stateRandomN"` | 从 StateStore 列表随机选 N 个不重复，隐式 required |
| `BindStateMapKey` | `"stateMapKey"` | 从 state map 随机选一个 key，隐式 required |
| `BindStateMapValue` | `"stateMapValue"` | 从 state map 随机选一个 value（支持 Path/Filter），隐式 required |
| `BindRandomPick` | `"randomPick"` | 从 `Values` 列表随机选一个 |
| `BindRandomPickN` | `"randomPickN"` | 从 `Values` 列表随机选 N 个 |
| `BindRandomPickMap` | `"randomPickMap"` | 按 `KeySource` 从 Values 映射表查找并随机选一个 |
| `BindRandomExclude` | `"randomExclude"` | 从 Values 或 state list 中随机选一个，排除 ExcludeSource |

**随机生成类**：

| 常量 | 值 | 说明 |
|------|----|------|
| `BindRandomInt` | `"randomInt"` | 随机整数 [Min, Max] |
| `BindRandomFloat` | `"randomFloat"` | 随机浮点数，Precision 控制精度（默认 2） |
| `BindRandomBool` | `"randomBool"` | 随机布尔值 |
| `BindRandomString` | `"randomString"` | 随机字符串，Length + Charset；Charset 支持 `lower`/`upper`/`alpha`/`numeric`/`alphanum` 或自定义字符集 |

`Charset` 为空时默认 `alphanum`；其他非空字符串按自定义字符集字面量处理，例如 `"ABC-123_"`。

**辅助类**：

| 常量 | 值 | 说明 |
|------|----|------|
| `BindListSize` | `"listSize"` | 返回 state 列表长度（int） |

**与计划的差异**：计划中列出 17 种 binding type，实际代码一致。`randomFloat` 虽然在常量中有定义，但在 `resolveFieldValue` 中实现为 `BindRandomFloat` case。

### 10.3 绑定处理流程

每个绑定的处理流程（跳过优先级从高到低）：

1. **Condition 检查**：condition 表达式求值为 false -> 跳过该绑定
2. **nil 值处理**：`resolveFieldValue` 返回 nil 时：
   - `Optional=true` -> 静默跳过
   - `Required=true` 或隐式必需类型 -> 返回错误
   - 其余情况 -> 静默跳过
3. **StoreAs 写入**：值非 nil 且配置了 StoreAs -> 写入 state
4. **空 Field 跳过**：Field 为空字符串 -> 跳过 proto 赋值
5. **proto 赋值**：调用 `Factory.SetField` 写入消息字段

### 10.4 Path 导航

`Path` 支持点分路径从嵌套 map/list 中提取值。例如 `"heroList[0].heroId"` 会先取 `heroList` 字段，再取索引 0，再取 `heroId` 字段。

`Path` 还支持用 `|` 分隔多条候选路径，按顺序尝试，返回第一个非 nil 的值。例如 `"mailUid|gid"` 会先尝试 `mailUid`，不存在则取 `gid`。

### 10.5 Wrap 包装

`Wrap=true` 时，将单个值包装为 `[]any{val}`，用于 repeated 字段赋单个元素的场景。

### 10.6 隐式必需类型

以下 binding type 即使不设置 `Required=true`，值缺失时也会触发动作跳过：
- `state`, `stateFirst`, `stateRandom`, `stateRandomN`, `stateMapKey`, `stateMapValue`

### 10.7 Filters 过滤

```go
type FilterDef struct {
    Path   string `json:"path"`   // 字段导航路径
    Op     string `json:"op"`     // 比较运算符
    Value  any    `json:"value"`  // 比较目标值（固定值）
    Source string `json:"source"` // 比较目标值（从 state 读取）
}
```

支持 10 种比较运算符：`eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `contains`, `in`, `notNil`, `isNil`。

## 11. StoreMapping -- 响应存储映射

```go
type StoreMapping struct {
    Field  string `json:"field"`  // S2C 响应中的字段名
    Setter string `json:"setter"` // 写入 StateStore 的 key
}
```

- `Field` 支持嵌套路径如 `"heroList[0].heroId"`
- `Field` 为空字符串时，存储整个 fieldMap

## 12. ListenRef -- 监听引用

```go
type ListenRef struct {
    Route  any    `json:"route"`  // 不透明路由
    Server string `json:"server"` // 连接名，格式：协议:服务名（如 "tcp:logic"）
    Listen string `json:"listen"` // 监听定义名称，空 = 仅缓存不回调
}
```

运行时通过 `adapter.ExpectedRouteKey(route)` 计算实际的 routeKey 字符串。

**与计划的差异**：计划中 ListenRef 使用 `ResponseKey string` 字段（直接存储路由键字符串），实际代码使用 `Route any`（不透明路由），运行时通过 adapter 计算 routeKey。另外 `Listen` 字段名在计划中为 `Callback`，实际为 `Listen`。`Server` 字段格式为 `"协议:服务名"`（如 `"tcp:logic"`），而非简单的服务名字符串。

## 13. ListenDef -- 监听回调定义

```go
type ListenDef struct {
    S2CProto string         `json:"s2cProto"` // 解析推送消息的 proto 全名
    Store    []StoreMapping `json:"store"`    // 响应字段到 StateStore 的映射
    Script   string         `json:"script"`   // Lua listen 回调脚本；与 Store 互斥
}
```

当前判别：
- `Script` 非空 -> 协作式 Lua 回调（由 Robot mailbox 串行执行，不在网络 pump 中执行 Lua）
- `S2CProto` 非空且 `Store` 非空 -> Go-store 回调（解析 proto + 存储字段）
- 否则 -> 仅缓存到监听队列，由主流程事件等待消费

`Script` 与 `Store` 同时非空会在注册阶段直接报配置错误，避免同一推送双写 state。

## 14. 条件解析器

### 14.1 条件表达式前缀

| 前缀常量 | 值 | 说明 |
|----------|----|------|
| `PrefixState` | `"state:"` | 状态存储条件表达式 |
| `PrefixLua` | `"lua:"` | Lua 脚本条件 |

### 14.2 文法（严格类型，零关键字）

```
expr       → or
or         → and ("||" and)*
and        → unary ("&&" unary)*
unary      → "!" unary | comparison
comparison → arith (comp_op arith)?
arith      → term (("+"|"-") term)*
term       → factor (("*"|"/"|"%") factor)*
factor     → NUMBER | STRING | PATH | "(" expr ")" | "-" factor
comp_op    → "==" | "!=" | ">" | ">=" | "<" | "<="
```

字面量只有数字（`123`、`1.5`）和带引号字符串（`"member"`）；裸标识符恒为 state 路径。
**无 `true`/`false`/`nil` 关键字**——store 值域闭合且无指针，nil 只可能是「key 缺失」，
由「缺失 → 错误」处理；存在性用按类型的非零比较表达（`!= 0` / `!= ""`）。

### 14.3 词法分析（`cond_tokenizer.go`）

`tokenize(input)` 产出 token 序列（末尾 EOF 哨兵）：NUMBER、STRING、PATH、运算符、括号。

- 数字 `.` 后必须跟数字；拒绝科学计数/十六进制；整数字面量溢出报错（不退化为 float）。
- 字符串 `"..."` 无转义；未闭合报错；`""` 合法。
- PATH 支持点分与 `[N]` 续段；空/非数字下标报错。
- 单字符 `=`/`|`/`&` 直接报错（常见笔误）。

### 14.4 解析函数（`cond_parser.go`，递归下降解释器）

| 函数 | 职责 |
|------|------|
| `parseExpr` | 入口，剥离 `state:` 后求值；错误 → 一条 warn |
| `parseOr` / `parseAnd` | `\|\|` / `&&`；仅当运算符出现才对操作数 asBool，单操作数透传 |
| `parseUnary` | `!` 取反（要求操作数 bool） |
| `parseComparison` | `arith (comp_op arith)?`，有 comp_op 返回 bool，否则透传值 |
| `parseArith` / `parseTerm` | 加减 / 乘除模 |
| `parseFactor` | NUMBER/STRING/PATH/`( expr )`/一元 `-` |

短路：`&&` 在左操作数 effective-false 时跳过右侧求值、`||` 在 effective-true 时跳过
（通过 `skip` 标志在解析结构的同时跳过求值，token 仍被正确消费）。

### 14.5 严格类型纪律与错误语义（`cond_compare.go`）

- 裸操作数（布尔上下文）必须 bool；数/串/missing → warn + false。
- `> >= < <=` 仅数值；`== !=` 仅同类型（number/string/bool），跨类型 → warn + false。
- 算术 `+ - * / %` 仅数值；`%` 仅整数；除法两边整型→整除、任一浮点→浮点除；不做字符串拼接。
- missing key → warn + false；除零、浮点取模、uint64 越界参与整数运算 → warn + false。
- `[]byte`/list/map 非标量，任何操作 → warn + false。
- 数值比较用 `cmpNumbersExact`（int64/uint64 精确比较，防玩家 ID 超 2^53 失真）。
- `state.CompareValues` 不再用于条件求值，仅 FilterDef 过滤器沿用（语义未变）。

错误语义（local-false）：出错的子表达式视为 effective-false 并记录首个错误；短路照常；
顶层若有错误打一条 warn，但返回实际计算出的布尔结果（如 `missing || fallback`，fallback
为真时结果为 true）。仅当结果不是 bool（裸数值/字符串顶层、结构错误）才返回 false。

### 14.6 条件求值入口

```go
func EvalCondition(expr string, s *state.Store) bool
```

仅处理 `state:` 前缀的表达式。`lua:` 前缀由 `robotActionHandler.ExecuteBoolean` 在 Robot 层处理。

## 15. ActionExecutor -- 声明式动作执行

### 15.1 结构体

```go
type ActionExecutor struct {
    netSender NetSender       // 网络发送委托
    store     *state.Store    // Robot 状态存储
    factory   *protox.Factory // protobuf 消息工厂
    adp       adapter.Adapter // 协议适配器
}
```

### 15.2 NetSender 接口

由 Robot 层的 `netSenderAdapter` 实现，19 个方法：

**发送/请求**：
- `TCPSend(service, packet) (int, error)`
- `UDPSend(service, data) (int, error)`
- `TCPRequest(service, packet, routeKey, timeout...) (body, headerErr, err)`
- `UDPRequest(service, packet, routeKey, timeout...) (body, headerErr, err)`
- `ConnectTCP(service, address) error`
- `ConnectUDP(service, address) error`
- `HTTPRequest(url, method, contentType, body) (statusCode, respBody, err)`

**连接管理**：
- `CloseTCP(service)`
- `CloseUDP(service)`

**监听**：
- `GetTCPListenResp(service, routeKey) ([]byte, uint64)`
- `GetUDPListenResp(service, routeKey) ([]byte, uint64)`
- `EnsureTCPListener(service, routeKey)`
- `EnsureUDPListener(service, routeKey)`

心跳不再是 flow action，由 Robot 建连后读取对应 codec 的 `heartbeat` 配置并注册到连接。

**密钥**：
- `GetTCPSecretKey(service) []byte`
- `SetTCPSecretKey(service, key)`
- `GetUDPSecretKey(service) []byte`
- `SetUDPSecretKey(service, key)`

### 15.3 消息构建流水线

```
buildBody() -> factory.Create(c2sProto)
           -> bindFields(bindings, actionName)
           -> factory.Serialize()
```

### 15.4 协议策略辅助方法

消除 TCP/UDP 代码重复的辅助方法：

| 方法 | 说明 |
|------|------|
| `protocolSend(protocol, service, packet)` | 按 protocol 分发到 TCP/UDP Send |
| `protocolEncode(protocol, route, body, key)` | 按 protocol 调用 EncodeTCP/UDP |
| `protocolSecretKey(protocol, service)` | 按 protocol 获取 TCP/UDP 密钥 |
| `protocolRequest(protocol, service, packet, routeKey, timeout...)` | 按 protocol 调用 TCP/UDP Request |
| `protocolListenResp(protocol, service, routeKey)` | 按 protocol 获取监听响应 |

### 15.5 响应处理

```
接收响应 -> parseAndStoreResponse(def, respBody)
         -> factory.Parse(s2cProto, body)
         -> storeResponse(storeMappings, fieldMap)
         -> navigatePath(fieldMap, field) 逐个字段存储到 StateStore
```

`handleHeaderError` 统一处理服务端返回的非零 headerErr：解析响应体 + 调用 `adapter.DescribeError` 获取描述 + 构造 `NewActionError(errcode.ErrorCode(headerErr), ...)`。

## 16. ActionError -- 结构化错误

### 16.1 结构

```go
type ActionError struct {
    Code   errcode.ErrorCode // 错误码（<100 框架错误，>=100 业务错误）
    Detail string            // 上下文描述
    cause  error             // 可选下层错误
}
```

### 16.2 错误格式

单一 code 维度，码段区分来源（<100 为框架错误，>=100 为业务/服务端错误），展示标签由 code 推导：

```
[#1] service=logic          // 框架错误（code<100）
[#1004] desc: route=CreateTeam // 业务错误（code>=100）
```

### 16.3 构造方法

| 方法 | 说明 |
|------|------|
| `NewActionError(code, detail, ...cause)` | 创建结构化错误（框架码 <100 / 业务码 ≥100 统一入口） |
### 16.4 哨兵错误

| 错误 | 用途 |
|------|------|
| `ErrNodeNotFound` | 节点 ID 不存在 |
| `ErrUnknownNodeType` | 未知的节点 Type |
| `ErrActionNotFound` | 动作名不存在 |

### 16.5 方法

| 方法 | 说明 |
|------|------|
| `Error() string` | 格式化输出 |
| `Unwrap() error` | 返回 cause，支持 `errors.Is` 链式判断 |
| `ErrorCode() uint64` | 返回数值错误码（<100 框架，>=100 业务） |
| `ErrorDetail() string` | 返回上下文描述 |

## 17. 默认常量

| 常量 | 值 | 说明 |
|------|----|------|
| `DefaultRequestTimeoutSec` | 10 | tcpRequest/udpRequest 默认超时（秒） |
| `DefaultListenTimeoutSec` | 60 | tcpListen/udpListen 默认超时（秒） |
| `DefaultHeartbeatMs` | 3000 | 心跳默认间隔（毫秒） |

## 18. 与计划的差异汇总

| 差异点 | 计划设计 | 实际代码 |
|--------|----------|----------|
| action 节点错误处理 | 旧版布尔中断字段 | `onError` 错误链路（ignoreCodes/handler/retry/strategy） |
| wait 节点字段 | 仅 `WaitMs int` | 额外支持 `WaitMin/WaitMax` 随机等待 |
| boolean 节点延迟 | 执行后调用 `nodeDelay()` | 不调用 `nodeDelay()` |
| exchangeKey pattern | 存在 | 已移除（由通用 tcpRequest 替代） |
| sleep pattern | 存在 | 已移除（由 wait 节点替代） |
| ListenRef | `ResponseKey string`（直接路由键） | `Route any`（运行时通过 adapter 计算） |
| ListenRef.Listen | `Callback string` | `Listen string` |
| ListenRef.Server | 简单服务名 | `"协议:服务名"` 格式（如 `"tcp:logic"`） |
| TaskFlow 顶层 | 有 `startNode` 字段 | 无此字段，入口固定为 `"main"` |
| Executor.NewExecutor | 2 参数 | 3 参数（多 `caller string`） |
| executeAction 签名 | 2 参数（含 nodeID） | 2 参数（ctx + node，无 nodeID） |
| ActionHandler.ExecuteAction | 返回 `error` | 返回 `error`（一致） |
| 执行方法返回值 | ExecuteAction 仅返回 error | 实际由 robotActionHandler 封装，内部 actionExec.Execute 返回 (sendBytes, recvBytes, err) |
| NetSender 方法数 | 计划中约 12 个方法 | 实际 21 个方法（TCP/UDP 分离，增加 headerErr 返回值） |

## 19. 完整 JSON 示例

### 19.1 简单登录流程

```json
{
  "defaultDelayMs": 500,
  "nodes": {
    "main": {
      "type": "sequence",
      "next": ["connectLogic", "authLogin", "logicLogin", "businessLoop"]
    },
    "connectLogic": {
      "type": "action",
      "action": "ConnectLogic",
      "onError": { "strategy": "abort" }
    },
    "authLogin": {
      "type": "action",
      "action": "AuthLogin",
      "onError": { "strategy": "abort" }
    },
    "logicLogin": {
      "type": "action",
      "action": "LogicLogin",
      "onError": { "strategy": "abort" },
      "listenRefs": [
        { "route": {"cmd": 3, "act": 1}, "server": "tcp:logic", "listen": "matchPoll" }
      ]
    },
    "businessLoop": {
      "type": "loop",
      "loopCount": -1,
      "body": "businessWeight"
    },
    "businessWeight": {
      "type": "weighted",
      "options": [
        { "node": "battle", "weight": 40 },
        { "node": "lobby", "weight": 60 }
      ]
    },
    "battle": {
      "type": "action",
      "action": "StartBattle"
    },
    "lobby": {
      "type": "action",
      "action": "LobbyAction"
    }
  },
  "actions": {
    "ConnectLogic": {
      "pattern": "tcpConnect",
      "service": "logic",
      "address": "state:logicAddr"
    },
    "AuthLogin": {
      "pattern": "httpRequest",
      "url": "http://192.168.61.161:20000/auth/login",
      "method": "POST",
      "bindings": [
        { "field": "account", "type": "fixed", "value": "state:account" },
        { "field": "version", "type": "fixed", "value": "0.31.49" }
      ],
      "store": [
        { "field": "token", "setter": "token" },
        { "field": "logicAddr", "setter": "logicAddr" }
      ]
    }
  },
  "listens": {
    "matchPoll": {
      "s2cProto": "MatchSucceedS2C",
      "store": [
        { "field": "battleId", "setter": "battleId" }
      ]
    }
  }
}
```

### 19.2 带 break 的循环

等价 Go 代码：

```go
for i := 0; i < 10; i++ {
    StartMatch()
    if matchSucceeded { break }
    wait(1000ms)
}
```

flow.json 写法：

```json
"matchRetryLoop": {
  "type": "loop",
  "loopCount": 10,
  "body": "matchRetryBody"
},
"matchRetryBody": {
  "type": "sequence",
  "next": ["StartMatch", "checkMatchResult", "retryWait"]
},
"checkMatchResult": {
  "type": "boolean",
  "condition": "state:matchSucceeded",
  "trueNext": "doBreak"
},
"doBreak": { "type": "break" },
"retryWait": { "type": "wait", "waitMs": 1000 }
```

### 19.3 带 continue 的循环

等价 Go 代码：

```go
for {
    hero := pickRandHero()
    if hero == nil { continue }
    UpgradeHero(hero)
    wait(500ms)
}
```

flow.json 写法：

```json
"heroLoop": {
  "type": "loop",
  "loopCount": -1,
  "body": "heroLoopBody"
},
"heroLoopBody": {
  "type": "sequence",
  "next": ["SelectHero", "checkHero", "UpgradeHero", "upgradeWait"]
},
"checkHero": {
  "type": "boolean",
  "condition": "state:heroSelected",
  "falseNext": "doContinue"
},
"doContinue": { "type": "continue" },
"upgradeWait": { "type": "wait", "waitMin": 300, "waitMax": 800 }
```

### 19.4 条件前置循环

等价 Go 代码：

```go
for hasStamina() {
    DoQuest()
    wait(2000ms)
}
```

flow.json 写法：

```json
"questLoop": {
  "type": "loop",
  "loopCount": -1,
  "condition": "lua:has_stamina.lua",
  "body": "questBody"
},
"questBody": {
  "type": "sequence",
  "next": ["DoQuest", "questWait"]
},
"questWait": { "type": "wait", "waitMs": 2000 }
```

### 19.5 后置条件循环

等价 Go 代码：

```go
for {
    SyncFrameData()
    if battleEnded { break }
}
```

flow.json 写法：

```json
"battleSyncLoop": {
  "type": "loop",
  "loopCount": -1,
  "breakCondition": "state:battleEnded",
  "body": "SyncFrameData"
},
"SyncFrameData": { "type": "action", "action": "SyncFrame" }
```
