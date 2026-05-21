# 流程节点系统重设计 — 详细设计方案

## 1. 设计目标

### 1.1 核心思路

将流程节点系统设计为**编程语言控制流原语的声明式对应物**：

- `action` 节点 = 一条可执行语句（函数调用），是唯一产生实际副作用的节点
- 其余所有节点 = 纯控制流结构，仅负责**串联** `action` 的执行顺序，不产生副作用

这意味着复杂的业务流程可以完全由 `sequence / loop / boolean / weighted / wait / break / continue` 这几类基础原语拼出来，就像编程一样。

### 1.2 设计原则

| 原则 | 说明 |
|---|---|
| **最小原语集** | 只保留无法由现有类型组合出来的节点类型 |
| **全部串行** | 流程内不存在并行语义，所有节点按声明顺序执行 |
| **单一职责** | 每种节点只做一件事：`loop` 只负责循环控制，不内嵌多节点列表；`sequence` 负责顺序编排 |
| **干净的 JSON 格式** | `nodes` 直接使用 map 格式（key = 节点 ID），消除冗余字段和转换逻辑 |
| **两类延迟职责分离** | `defaultDelayMs`（框架负责节奏控制）与 `wait` 节点（用户负责业务等待）严格区分 |
| **类型精确** | 每种节点只保留与其语义相关的字段，不共用语义不明的字段 |

### 1.3 当前设计的问题

| 问题 | 位置 | 具体表现 |
|---|---|---|
| `start` 节点冗余 | `executor.go` | `executeStart` 直接委托给 `executeSequence`，语义完全重叠 |
| 并行执行与串行原则矛盾 | `executor.go executeNextList` | `next` 有多个元素时启动 goroutine 并行执行，实际上任何节点都不应该并行 |
| `loop` 内嵌多节点列表 | `executor.go executeLoop` | `loop.next` 是一组节点的列表，循环体内有自己的 for 循环，职责与 `sequence` 重叠 |
| `loop` 表达力弱 | `executor.go executeLoop` | 只支持固定次数，无法表达 `for condition {}`，无法从 body 内部 break |
| 缺少 `continue` | — | 无法在 loop body 内跳过本次迭代剩余步骤 |
| 延迟值硬编码 | `executor.go DefaultNodeDelayMs` | 全局常量 1000ms 无法按流程配置 |
| `next` 字段类型混用 | `flow.go` | `sequence/loop` 的 `next` 和 `weighted` 的 `next` 都是 `[]NextNode`，但 `weight` 字段只对 weighted 有意义，引发混淆 |
| `wait` 使用 `float64` 秒 | `flow.go` | 与其他延迟字段（`delayMs`、`intervalMs`）单位不一致 |
| JSON 数组转 map 的复杂反序列化 | `flow.go UnmarshalJSON` | `nodes` 是数组，需要自定义 `UnmarshalJSON` 转换为 map，不够干净 |

---

## 2. 节点类型体系

### 2.1 完整节点类型表

| 类型 | 编程类比 | 核心作用 |
|---|---|---|
| `sequence` | `{ stmt1; stmt2; stmt3 }` | 顺序执行 `next` 中列出的所有子节点 |
| `action` | `funcCall()` | 执行一个声明式动作或 Lua 脚本，唯一产生副作用的节点 |
| `loop` | `for` | 循环执行单个 `body` 节点，支持次数/前置条件/后置条件；多步骤循环体用 `sequence` 节点包装后传入 |
| `boolean` | `if / else` | 对 `condition` 求值，跳转到 `trueNext` 或 `falseNext` |
| `weighted` | `switch(rand)` | 按 `options` 中的权重随机选择一路节点执行 |
| `wait` | `time.Sleep(n)` | 用户显式等待，与全局默认延迟无关 |
| `break` | `break` | 产生 `errBreak` 信号，中断最近的 `loop` |
| `continue` | `continue` | 产生 `errContinue` 信号，跳过本次迭代剩余步骤，进入下一次迭代 |

**说明**：原有的 `start` 类型被完全移除，用 `sequence` 替代。

### 2.2 两类延迟的职责分离

```
┌──────────────────────────────────────────────────────────────────┐
│  defaultDelayMs（框架负责 — 全局节奏控制）                            │
│                                                                  │
│  每个 action / boolean 节点执行完后，框架自动等待的最小间隔。              │
│  目的：避免压测机器人执行过快，模拟真实用户的操作节奏。                    │
│  来源：TaskFlow.defaultDelayMs，节点级 delayMs 可覆盖。              │
│  引擎兜底默认值：1000ms                                             │
└──────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────┐
│  wait 节点（用户负责 — 业务逻辑控制）                                   │
│                                                                  │
│  用户在 flow.json 中显式插入的等待步骤。                                │
│  目的：在特定业务节点之间插入固定等待，如等待服务器处理、等待动画等。           │
│  与 defaultDelayMs 完全独立，两者叠加生效。                             │
└──────────────────────────────────────────────────────────────────┘
```

---

## 3. flow.json 新格式

### 3.1 顶层结构

```json
{
  "startNode": "init",
  "defaultDelayMs": 500,
  "nodes": {
    "init": { ... },
    "businessLoop": { ... }
  },
  "actions": { ... },
  "callbacks": { ... }
}
```

**变化**：
- `nodes` 由数组改为**对象（map）**，key 即节点 ID，节点内部不再需要 `id` 字段
- 新增顶层 `defaultDelayMs` 字段

### 3.2 各节点类型的 JSON 格式

```json
// sequence — 顺序执行
{
  "type": "sequence",
  "next": ["nodeA", "nodeB", "nodeC"]
}

// action — 执行动作
{
  "type": "action",
  "action": "Login",
  "breakOff": true,
  "listenCallbacks": [...],
  "delayMs": -1
}

// loop — 循环（loopCount ≤ 0 视为无限）
// body 只能是单个节点 ID；需要循环多个步骤时，用 sequence 节点包装后填入 body
{
  "type": "loop",
  "loopCount": -1,
  "condition": "lua:check.lua",
  "breakCondition": "lua:should_stop.lua",
  "body": "loopBodyNode"
}

// boolean — 条件分支
{
  "type": "boolean",
  "condition": "lua:has_role.lua",
  "trueNext": "startGame",
  "falseNext": "createRole"
}

// weighted — 加权随机
{
  "type": "weighted",
  "options": [
    {"node": "battle", "weight": 40},
    {"node": "lobby", "weight": 60}
  ]
}

// wait — 显式等待
{
  "type": "wait",
  "waitMs": 2000
}

// break — 中断循环（无额外字段）
{
  "type": "break"
}

// continue — 跳过本次迭代（无额外字段）
{
  "type": "continue"
}
```

### 3.3 完整示例：带 break 的循环

等价 Go 代码：
```go
// 最多尝试 10 次匹配，匹配成功则 break
for i := 0; i < 10; i++ {
    StartMatch()
    if matchSucceeded { break }
    wait(1000ms)
}
```

flow.json 写法：
```json
"nodes": {
  "matchRetryLoop": {
    "type": "loop",
    "loopCount": 10,
    "body": "matchRetryBody"
  },
  "matchRetryBody": {
    "type": "sequence",
    "next": ["StartMatch", "checkMatchResult", "retryWait"]
  },
  "StartMatch": {
    "type": "action",
    "action": "StartMatch"
  },
  "checkMatchResult": {
    "type": "boolean",
    "condition": "lua:match_succeeded.lua",
    "trueNext": "doBreak"
  },
  "doBreak": { "type": "break" },
  "retryWait": { "type": "wait", "waitMs": 1000 }
}
```

`loop` 只持有一个 `body` 引用，循环内多个步骤的编排完全交给 `sequence`，两者职责不再重叠。

### 3.4 完整示例：带 continue 的循环

等价 Go 代码：
```go
for {
    hero := pickRandHero()
    if hero == nil { continue }  // 没有英雄，跳过本次
    UpgradeHero(hero)
    wait(500ms)
}
```

flow.json 写法：
```json
"nodes": {
  "heroLoop": {
    "type": "loop",
    "loopCount": -1,
    "body": "heroLoopBody"
  },
  "heroLoopBody": {
    "type": "sequence",
    "next": ["SelectHero", "checkHero", "UpgradeHero", "upgradeWait"]
  },
  "SelectHero": { "type": "action", "action": "SelectHero" },
  "checkHero": {
    "type": "boolean",
    "condition": "lua:hero_selected.lua",
    "falseNext": "doContinue"
  },
  "doContinue": { "type": "continue" },
  "UpgradeHero": { "type": "action", "action": "UpgradeHero" },
  "upgradeWait": { "type": "wait", "waitMs": 500 }
}
```

### 3.5 简单循环：body 直接是 action

当循环体只有一个动作时，不需要 `sequence` 包装：

```json
"syncLoop": {
  "type": "loop",
  "loopCount": 100,
  "body": "SyncFrameData"
},
"SyncFrameData": { "type": "action", "action": "SyncFrameData" }
```

---

## 4. Go 结构体设计（engine/flow.go）

### 4.1 TaskFlow

```go
// TaskFlow 流程图定义。
type TaskFlow struct {
    StartNode      string                  `json:"startNode"`      // 起始节点 ID
    DefaultDelayMs int                     `json:"defaultDelayMs"` // 全局节点间默认延迟（毫秒）。0=引擎默认(1000ms)，<0=禁用
    Nodes          map[string]*Node        `json:"nodes"`          // 节点映射，key 为节点 ID
    Actions        map[string]*ActionDef   `json:"actions"`        // 动作定义映射
    Listens      map[string]*ListenDef `json:"callbacks"`      // 回调定义映射
}
```

`nodes` 直接使用 JSON object 反序列化为 `map[string]*Node`，无需自定义 `UnmarshalJSON`。

### 4.2 Node

```go
// Node 流程节点。
// 每种 type 只使用其对应的字段，其余字段留空。
type Node struct {
    Type string `json:"type"` // 节点类型：sequence / action / loop / boolean / weighted / wait / break / continue

    // ── sequence 专用 ────────────────────────────────────────────
    // 按顺序依次执行的子节点 ID 列表
    Next []string `json:"next"`

    // ── loop 专用 ────────────────────────────────────────────────
    Body           string `json:"body"`           // 循环体节点 ID（单个）；多步骤时指向一个 sequence 节点
    LoopCount      int    `json:"loopCount"`      // 循环次数；≤0 = 无限循环
    Condition      string `json:"condition"`      // 前置条件：每次迭代开始前求值，false 时退出循环
    BreakCondition string `json:"breakCondition"` // 后置条件：每次迭代结束后求值，true 时退出循环

    // ── boolean 专用 ─────────────────────────────────────────────
    // Condition 字段同 loop，此处复用：boolean 的分支判断条件
    TrueNext  string `json:"trueNext"`  // 条件为 true 时跳转的节点 ID（空 = 不跳转）
    FalseNext string `json:"falseNext"` // 条件为 false 时跳转的节点 ID（空 = 不跳转）

    // ── action 专用 ─────────────────────────────────────────────
    Action          string      `json:"action"`          // 引用 actions 表中的动作名称
    BreakOff        bool        `json:"breakOff"`        // true = 动作失败时中断整个流程
    ListenCallbacks []ListenRef `json:"listenCallbacks"` // 动作执行后注册的持久化推送监听

    // ── weighted 专用 ─────────────────────────────────────────────
    Options []WeightedOption `json:"options"` // 加权选项列表

    // ── wait 专用 ─────────────────────────────────────────────────
    WaitMs int `json:"waitMs"` // 等待时长（毫秒）

    // ── 通用（action / boolean 节点有效）────────────────────────────
    // > 0: 使用此值；= 0: 使用 TaskFlow.DefaultDelayMs；< 0: 禁用延迟
    DelayMs int `json:"delayMs"`
}
```

**关于 `Condition` 字段复用**：`loop` 的前置条件和 `boolean` 的分支条件都用 `condition` 字段表达，两者语义一致（求值为 bool），且一个节点只会是一种类型，不会产生歧义。

**`loop.body` 与 `sequence.next` 的分工**：

```
loop.body  = "谁是循环体"  → 单个节点 ID（职责：控制循环次数和条件）
sequence.next = "按顺序做什么" → 节点 ID 列表（职责：编排多个步骤）

需要循环多个步骤时：
  loop { body → sequence { next: [A, B, C] } }
  类比：for { { A(); B(); C() } }
```

### 4.3 WeightedOption

```go
// WeightedOption 加权选项，用于 weighted 节点。
type WeightedOption struct {
    Node   string `json:"node"`   // 目标节点 ID
    Weight int    `json:"weight"` // 权重值
}
```

这取代了原来 `NextNode` 中混用 `weight` 字段的设计（`sequence` 的 `next` 现在是 `[]string`，不再需要 `NextNode`）。

### 4.4 移除的内容

- `NextNode` 类型（`sequence` 用 `[]string`，`weighted` 用 `[]WeightedOption`，`loop` 用 `string`）
- `Node.WaitSeconds` 字段（改为 `WaitMs int`，单位统一为毫秒）
- `taskFlowRaw` 辅助结构和 `TaskFlow.UnmarshalJSON`（`nodes` 直接用 map 反序列化）
- `nodeRaw` 辅助结构和 `Node.UnmarshalJSON`（无需兼容旧字段别名）
- `ActionHandler.OnNodeError` 回调（仅做日志打印，无扩展意义；错误日志改为 executor 内直接输出）
- boolean 节点的 `action` fallback（旧版 `condition` 为空时回退到 `action` 字段；新设计强制使用 `condition`）
- `ActionDef.Delay` 字段和 `sleep` pattern（功能由 `wait` 节点和 `Node.DelayMs` 覆盖）

---

## 5. 执行器设计（engine/executor.go）

### 5.1 Executor 结构体

```go
// DefaultEngineDelayMs 引擎兜底的节点间延迟，TaskFlow.DefaultDelayMs 为 0 时使用。
const DefaultEngineDelayMs = 1000

// Executor 流程执行器，每个 Robot 持有一个独立实例。
type Executor struct {
    flow           *TaskFlow
    handler        ActionHandler
    defaultDelayMs int // 解析自 flow.DefaultDelayMs，初始化后只读
}

func NewExecutor(flow *TaskFlow, handler ActionHandler) *Executor {
    delayMs := flow.DefaultDelayMs
    if delayMs == 0 {
        delayMs = DefaultEngineDelayMs
    }
    // delayMs < 0 表示禁用，保留原值不做处理
    return &Executor{
        flow:           flow,
        handler:        handler,
        defaultDelayMs: delayMs,
    }
}
```

### 5.2 errBreak / errContinue 信号

```go
// errBreak 和 errContinue 是循环控制的内部信号，不是真正的错误。
// 它们通过 Go 的 error 传播机制在节点树中向上冒泡，
// 直到被最近的 executeLoop 捕获并消费，不会传播到 loop 之外。
var (
    errBreak    = errors.New("break")
    errContinue = errors.New("continue")
)
```

**传播规则**：

```
executeBreak()       → returns errBreak
executeSequence()    → 透传（不捕获 errBreak/errContinue）
executeBoolean()     → 透传（分支跳转后执行的节点返回信号时透传）
executeLoop()        → 捕获 errBreak（退出循环）和 errContinue（进入下一次迭代）
```

### 5.3 nodeDelay 方法

```go
// nodeDelay 执行节点级延迟，仅在 action 和 boolean 节点执行完后调用。
// 延迟值优先级：node.DelayMs > e.defaultDelayMs
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

### 5.4 executeNode 路由

```go
func (e *Executor) executeNode(ctx context.Context, nodeID string) error {
    if ctx.Err() != nil {
        return ctx.Err()
    }
    node, ok := e.flow.Nodes[nodeID]
    if !ok {
        return fmt.Errorf("节点不存在: %s", nodeID)
    }
    switch node.Type {
    case "sequence":
        return e.executeSequence(ctx, node)
    case "action":
        return e.executeAction(ctx, node, nodeID)
    case "loop":
        return e.executeLoop(ctx, node)
    case "boolean":
        return e.executeBoolean(ctx, node)
    case "weighted":
        return e.executeWeighted(ctx, node)
    case "wait":
        return e.executeWait(ctx, node)
    case "break":
        return errBreak
    case "continue":
        return errContinue
    default:
        return fmt.Errorf("未知节点类型: %s (node=%s)", node.Type, nodeID)
    }
}
```

注意：`break` 和 `continue` 足够简单，直接在 switch 中内联返回信号，无需独立函数。

### 5.5 executeSequence

```go
func (e *Executor) executeSequence(ctx context.Context, node *Node) error {
    for _, childID := range node.Next {
        if ctx.Err() != nil {
            return ctx.Err()
        }
        if err := e.executeNode(ctx, childID); err != nil {
            return err // 透传所有错误和信号（含 errBreak / errContinue）
        }
    }
    return nil
}
```

**说明**：`sequence` 透传 `errBreak` 和 `errContinue`，这两个信号会向上冒泡直到被 `executeLoop` 捕获。

### 5.6 executeLoop（核心升级）

```go
func (e *Executor) executeLoop(ctx context.Context, node *Node) error {
    for i := 0; node.LoopCount <= 0 || i < node.LoopCount; i++ {
        if ctx.Err() != nil {
            return ctx.Err()
        }

        // 前置条件检查（对应 Go: for condition { }）
        if node.Condition != "" {
            if !e.handler.ExecuteBoolean(node.Condition) {
                break
            }
        }

        // 执行循环体（单个节点）
        err := e.executeNode(ctx, node.Body)

        if err == errBreak {
            break // body 内 break 节点触发，正常退出循环
        }
        if err == errContinue {
            continue // body 内 continue 节点触发，跳到下一次迭代
        }
        if err != nil {
            return err // 真实错误，向上传播
        }

        // 后置条件检查（对应 Go: do { } while !breakCondition）
        if node.BreakCondition != "" {
            if e.handler.ExecuteBoolean(node.BreakCondition) {
                break
            }
        }
    }
    return nil
}
```

**`executeLoop` 内部不再有自己的 for 循环来遍历子节点**，body 的编排完全交给 `sequence`。`executeLoop` 只做一件事：控制迭代。

**四种循环模式对应关系**：

| Go 写法 | loop 节点配置 |
|---|---|
| `for {}` | `loopCount: -1`（或省略） |
| `for i < N {}` | `loopCount: N` |
| `for condition {}` | `loopCount: -1, condition: "lua:cond.lua"` |
| `do {} while !stop` | `loopCount: -1, breakCondition: "lua:stop.lua"` |
| `for { if x { break } }` | body 内 sequence 含 boolean → break 节点 |
| `for { if x { continue } }` | body 内 sequence 含 boolean → continue 节点 |

### 5.7 executeBoolean

> **注意**：新设计移除了旧版 boolean 节点的 `action` fallback（旧版在 `condition` 为空时回退到 `action` 字段）。
> boolean 节点必须使用 `condition` 字段指定条件表达式，不再有 fallback。

```go
func (e *Executor) executeBoolean(ctx context.Context, node *Node) error {
    result := e.handler.ExecuteBoolean(node.Condition)

    // boolean 节点也参与节奏控制（条件判断可能涉及 Lua 执行）
    e.nodeDelay(ctx, node)

    var targetID string
    if result {
        targetID = node.TrueNext
    } else {
        targetID = node.FalseNext
    }
    if targetID == "" {
        return nil // 对应分支未配置，顺序流出
    }
    return e.executeNode(ctx, targetID)
}
```

### 5.8 executeWeighted

```go
func (e *Executor) executeWeighted(ctx context.Context, node *Node) error {
    if len(node.Options) == 0 {
        return nil
    }
    total := 0
    for _, opt := range node.Options {
        total += opt.Weight
    }
    if total == 0 {
        return nil
    }
    r := rand.Intn(total)
    cumulative := 0
    for _, opt := range node.Options {
        cumulative += opt.Weight
        if r < cumulative {
            return e.executeNode(ctx, opt.Node)
        }
    }
    return e.executeNode(ctx, node.Options[len(node.Options)-1].Node)
}
```

### 5.9 executeWait

```go
func (e *Executor) executeWait(ctx context.Context, node *Node) error {
    if node.WaitMs <= 0 {
        return nil
    }
    select {
    case <-ctx.Done():
        return ctx.Err()
    case <-time.After(time.Duration(node.WaitMs) * time.Millisecond):
        return nil
    }
}
```

### 5.10 executeAction

```go
func (e *Executor) executeAction(ctx context.Context, node *Node, nodeID string) error {
    if node.Action == "" {
        return nil
    }
    actionDef, ok := e.flow.GetAction(node.Action)
    if !ok {
        return fmt.Errorf("动作不存在: %s", node.Action)
    }

    err := e.handler.ExecuteAction(actionDef)
    if err != nil {
        stresslog.Error("[ENGINE] 动作执行失败",
            zap.String("node", nodeID), zap.String("action", node.Action), zap.Error(err))
        if node.BreakOff {
            return fmt.Errorf("动作执行失败 [%s]: %w", node.Action, err)
        }
        // 非中断模式，记录错误后继续执行
        return nil
    }

    if len(node.ListenCallbacks) > 0 {
        if err := e.handler.RegisterListen(node.ListenCallbacks); err != nil {
            stresslog.Error("[ENGINE] 注册监听失败",
                zap.String("node", nodeID), zap.Error(err))
            if node.BreakOff {
                return fmt.Errorf("注册监听失败: %w", err)
            }
        }
    }

    e.nodeDelay(ctx, node)
    return nil
}
```

### 5.11 ActionHandler 接口精简

删除 `OnNodeError` 回调（原本只做日志打印，无扩展意义），错误日志直接在 executor 中输出：

```go
type ActionHandler interface {
    ExecuteAction(actionDef *ActionDef) error
    ExecuteBoolean(expression string) bool
    RegisterListen(refs []ListenRef) error
}
```

---

## 6. 信号传播完整链路

以"在循环内用 boolean 触发 break"为例，完整追踪信号流：

body 为一个 sequence 节点，sequence 包含 `doSomething` 和 `checkBreak`：

```
executeLoop()
  └─ 迭代开始：executeNode(node.Body)  →  executeSequence()
       ├─ executeNode("doSomething") → nil（正常执行）
       └─ executeNode("checkBreak")
            └─ executeBoolean()
                 └─ condition = true → executeNode("doBreak")
                      └─ case "break": return errBreak
                 ← errBreak
            ← errBreak
       ← errBreak（sequence 透传）
  ← err = errBreak
  └─ if err == errBreak { break }  ← 被 loop 捕获，不再向上传播
  └─ return nil  ← loop 正常退出
```

`errBreak` 不会穿透 `executeLoop`，因此外层的 `sequence` 或其他节点不会感知到它。

---

## 7. 新旧对比

### 7.1 JSON 格式对比

**旧格式**：
```json
{
  "startNode": "start",
  "nodes": [
    {"id": "start", "type": "start", "next": [{"node": "authLogin"}, {"node": "logicLogin"}]},
    {"id": "businessLoop", "type": "loop", "loopCount": -1, "next": [{"node": "businessWeight"}]},
    {"id": "businessWeight", "type": "weighted", "next": [
      {"node": "battle", "weight": 40},
      {"node": "lobby", "weight": 60}
    ]}
  ]
}
```

**新格式**：
```json
{
  "startNode": "init",
  "defaultDelayMs": 1000,
  "nodes": {
    "init": {
      "type": "sequence",
      "next": ["authLogin", "logicLogin", "businessLoop"]
    },
    "businessLoop": {
      "type": "loop",
      "loopCount": -1,
      "body": "businessWeight"
    },
    "businessWeight": {
      "type": "weighted",
      "options": [
        {"node": "battle", "weight": 40},
        {"node": "lobby", "weight": 60}
      ]
    }
  }
}
```

### 7.2 关键变化

| 变化点 | 旧 | 新 |
|---|---|---|
| `nodes` 格式 | JSON 数组，节点内含 `id` 字段，需自定义反序列化 | JSON map，key 即 ID，标准反序列化 |
| 顶层延迟配置 | 无（硬编码常量 1000ms） | `defaultDelayMs` 字段 |
| `start` 节点 | 独立类型 | 移除，用 `sequence` 替代 |
| `sequence` 子节点 | `next: [{"node": "id"}]` | `next: ["id"]` |
| `loop` 循环体 | `next: [{"node": "id1"}, {"node": "id2"}]`（列表） | `body: "nodeId"`（单个节点，多步骤用 sequence 包装） |
| 加权子节点 | `next: [{"node": "id", "weight": N}]` | `options: [{"node": "id", "weight": N}]` |
| `wait` 字段 | `waitSeconds: float64` | `waitMs: int` |
| 循环条件 | 仅 `loopCount` | 新增 `condition`（前置）和 `breakCondition`（后置） |
| 循环中断 | 不支持 | 新增 `break` / `continue` 节点类型 |
| 并行执行 | `executeNextList` 有 goroutine 并行 | 全部串行，彻底移除 |
| 错误回调 | `OnNodeError(node, err)` 接口方法 | 删除，executor 内直接打日志 |
| boolean 条件 | `condition` 为空时 fallback 到 `action` | 移除 fallback，强制使用 `condition` |
| sleep pattern / ActionDef.Delay | 存在 | 删除，由 `wait` 节点 + `Node.DelayMs` 覆盖 |

---

## 8. 实施阶段

### Phase 1：engine/flow.go

1. 删除 `NextNode` 类型，新增 `WeightedOption` 类型
2. `TaskFlow` 新增 `DefaultDelayMs`，移除自定义 `UnmarshalJSON`
3. 重写 `Node` 结构体：
   - `sequence` 专用：`Next []string`
   - `loop` 专用：`Body string`、`LoopCount int`、`Condition string`、`BreakCondition string`
   - `boolean` 复用 `Condition`，新增 `TrueNext`、`FalseNext`
   - `weighted` 专用：`Options []WeightedOption`
   - `wait` 专用：`WaitMs int`
   - 通用：`DelayMs int`
4. 移除 `nodeRaw`、`taskFlowRaw` 辅助类型和相关 `UnmarshalJSON`
5. 删除 `ActionDef.Delay` 字段
6. 更新节点类型文档注释

### Phase 2：engine/executor.go

1. `Executor` 新增 `defaultDelayMs int` 字段
2. `NewExecutor` 增加 `defaultDelayMs` 初始化逻辑
3. 移除全局函数 `nodeDelay`，改为 `Executor` 方法
4. `executeNode` 参数改为 `nodeID string`（直接在函数内查找），新增 `break`/`continue` 分支
5. 移除 `executeStart`，`executeSequence` 改为遍历 `[]string`
6. 移除 `executeNextList`（并行逻辑彻底删除）
7. 重写 `executeLoop`：`body` 单节点执行，前置条件、后置条件、`errBreak`/`errContinue` 捕获
8. `executeBoolean` 移除 `action` fallback，强制使用 `node.Condition`
9. `executeWeighted` 改为遍历 `node.Options`
10. `executeWait` 改为使用 `node.WaitMs`
11. `executeAction` 移除 `OnNodeError` 调用，改为 `stresslog` 直接输出；`RegisterListen` 错误尊重 `breakOff`
12. 新增 `errBreak`、`errContinue` 变量
13. `ActionHandler` 接口移除 `OnNodeError` 方法

### Phase 3：engine/action.go

1. 删除 `sleep` case 和 `execSleep` 方法
2. 删除 `Execute` 方法中的 `if err == nil && def.Delay > 0` 后处理

### Phase 4：robot/robot.go

1. 删除 `OnNodeError` 方法实现

### Phase 5：cmd/validate

更新 flow.json 校验工具，同步识别新节点类型集（`sequence/action/loop/boolean/weighted/wait/break/continue`）和新字段格式（`next []string`、`body string`、`options`、`waitMs`）。

### Phase 6：重写 conf/flow/flow.json

按新格式重新编写 `conf/flow/flow.json`，覆盖完整的压测流程（登录 → 业务循环 → 战斗）。多步骤循环体统一改为 `loop { body → sequence }` 结构。

### Phase 7：验证

按 CLAUDE.md 验证流程：
- `go build ./...` 无报错
- `go run ./cmd/validate conf/flow/flow.json` 通过
- 运行 2~5 分钟，`BattleEnd` 计数 ≥ 2，无 error/warn 日志

---

## 9. 节点字段速查表

| 字段 | 类型 | 适用节点 | 说明 |
|---|---|---|---|
| `type` | string | 所有 | 节点类型 |
| `next` | `[]string` | sequence | 子节点 ID 列表（顺序执行） |
| `body` | string | loop | 循环体节点 ID（单个）；多步骤时指向 sequence |
| `loopCount` | int | loop | 循环次数，≤0 = 无限 |
| `condition` | string | loop / boolean | loop：前置退出条件；boolean：分支条件 |
| `breakCondition` | string | loop | 后置退出条件，true 时退出循环 |
| `trueNext` | string | boolean | 条件为 true 时跳转的节点 ID |
| `falseNext` | string | boolean | 条件为 false 时跳转的节点 ID |
| `options` | `[]WeightedOption` | weighted | 加权选项（含 node + weight） |
| `action` | string | action | 引用 `actions` 表中的动作名 |
| `breakOff` | bool | action | 动作失败是否中断整个流程 |
| `listenCallbacks` | `[]ListenRef` | action | 动作后注册的持久化推送监听 |
| `waitMs` | int | wait | 等待时长（毫秒） |
| `delayMs` | int | action / boolean | 覆盖 defaultDelayMs；<0 = 禁用 |
