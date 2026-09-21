# 流程节点系统技术文档

## 1. 概述

流程模型与执行引擎分属两个包：`flow` 保存节点 DAG、动作、监听、onError 与条件表达式的纯数据模型及加载期准备（`flow/flow.go:1-4` 包注释），`engine` 只负责图遍历与动作执行。流程通过 **9 种节点类型**表达完整的控制流语义，所有副作用集中在 `action` 节点，其余节点为纯控制流。流程定义来自 `flow.json` 配置文件。

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
| 条件加载期编译 | 所有条件表达式在 `PrepareTaskFlow` 阶段编译为不可变 AST，运行期只求值不解析（`flow/prepare.go:32-87`） |

## 2. 文件结构

| 文件 | 职责 |
|------|------|
| `flow/flow.go` | TaskFlow / Node / ActionDef / ListenDef / ListenRef 数据模型 + 节点、pattern、策略常量 |
| `flow/prepare.go` | PrepareTaskFlow 加载期准备：clearState 保护键校验、listenRefs 引用校验、条件编译 |
| `flow/condition_compile.go` | 条件编译器：前缀分流 + 递归下降解析为 conditionProgram AST |
| `flow/condition_tokenizer.go` | 条件词法分析（NUMBER/STRING/PATH/运算符/括号） |
| `flow/condition.go` | 编译产物的运行期求值（CompiledCondition.EvalState） |
| `flow/condition_compare.go` | 严格类型比较与算术（classifyNumber / cmpNumbersExact / strictCompare / evalArith） |
| `engine/executor.go` | Executor -- 图遍历与节点调度、循环控制信号、延迟机制、onError 链路 |
| `engine/action.go` | ActionExecutor -- 声明式动作执行引擎、NetSender/ActionNetSender 接口、字段绑定解析、计时拆解 |
| `engine/errors.go` | 流程配置错误哨兵（ErrNodeNotFound 等）+ 取消判据 |
| `engine/heartbeat.go` | 连接级声明式心跳模型（HeartbeatConfig / HeartbeatField）与 raw 小端打包 |
| `engine/heartbeat_plan.go` | 心跳布局编译（每 tick 零查表零增长） |
| `errcode/error.go` | ActionError 结构化错误（统一错误码体系，全仓共用） |

## 3. TaskFlow 数据模型

### 3.1 顶层结构

`flow/flow.go:15-20`：

```go
type TaskFlow struct {
    DefaultDelayMs int                   `json:"defaultDelayMs"` // 全局节点间默认延迟（毫秒）。0=引擎默认(1000ms)，<0=禁用
    Nodes          map[string]*Node      `json:"nodes"`          // 节点映射，key 为节点 ID
    Actions        map[string]*ActionDef `json:"actions"`        // 动作定义映射
    Listens        map[string]*ListenDef `json:"listens"`        // 监听定义映射
}
```

`Nodes` 使用 JSON 对象反序列化为 `map[string]*Node`，key 即节点 ID，节点内部无需 `id` 字段。入口节点固定为 `"main"`（`engine/executor.go:94`）。

访问方法（`flow/flow.go:198-213`）：
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

## 4. 9 种节点类型

### 4.1 完整节点类型表

常量定义见 `flow/flow.go:145-155`。

| 类型 | 编程类比 | 核心作用 | 对应常量 |
|------|----------|----------|----------|
| `sequence` | `{ stmt1; stmt2; stmt3 }` | 顺序执行 `next` 中列出的所有子节点 | `NodeSequence` |
| `action` | `funcCall()` | 执行声明式动作或 Lua 脚本，唯一产生副作用的节点 | `NodeAction` |
| `loop` | `for` | 循环执行单个 `body` 节点，支持次数/前置条件/后置条件 | `NodeLoop` |
| `boolean` | `if / else` | 对 `condition` 求值，跳转到 `trueNext` 或 `falseNext` | `NodeBoolean` |
| `switch` | `switch` 多条件 | 按 `cases` 顺序求值，执行第一条命中分支；全未命中走 `defaultNext` | `NodeSwitch` |
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

**行为**（`engine/executor.go:152-169`）：
- 按 `Next` 列表顺序依次执行每个子节点
- 捕获 `errSkip` 信号时跳过剩余子节点（视为本 sequence 正常完成）
- 透传其他所有错误和信号（含 `errBreak` / `errContinue`）
- 子节点执行出错时立即返回错误，不继续后续子节点
- 因 ctx 取消而中断时，给剩余的 action 子节点补记 canceled 样本（`reportRemainingCanceled`，`engine/executor.go:171-193`），保证监控面上各节点样本数可对齐

**JSON 示例**：
```json
{
  "type": "sequence",
  "next": ["authLogin", "logicLogin", "businessLoop"]
}
```

### 4.3 action 节点

**用途**：执行一个声明式动作或 Lua 脚本。这是唯一产生实际副作用的节点类型。

**字段**（`flow/flow.go:56-59,71`）：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"action"` |
| `Action` | string | `action` | 引用 `actions` 表中的动作名称 |
| `OnError` | `*OnErrorDef` | `onError` | 动作失败后的错误链路：ignoreCodes / handler / retry / strategy |
| `ListenRefs` | `[]ListenRef` | `listenRefs` | 动作执行后注册的持久化推送监听引用 |
| `DelayMs` | int | `delayMs` | 节点级延迟覆盖（>0 使用此值，=0 使用 DefaultDelayMs，<0 禁用） |

**行为**（`engine/executor.go:249-322`）：
- `Action` 为空但 `ListenRefs` 非空是合法的「纯注册监听」节点，走 `executeListenOnly`：只注册监听、不执行动作，注册成功后照常执行节点延迟（`engine/executor.go:250-253,305-322`）
- 既无 `action` 也无 `listenRefs` 的 action 节点 Warn 提示空转并返回 nil（`engine/executor.go:308-312`）
- 查找 `actions[action]` 获取 ActionDef，找不到返回 `ErrActionNotFound`
- 调用 `ActionHandler.ExecuteAction(ctx, actionDef)` 执行动作
- 执行失败时进入 `onError` 链路（`engine/executor.go:263-302`）：
  - context 取消（Canceled/DeadlineExceeded）与 `ErrActionCanceled` 直接传播，不走 `onError`，不执行节点延迟
  - 命中 `ignoreCodes`：warn 日志，流程继续，monitor 保留原始失败样本
  - `handler`：执行普通节点子流程，完成后回到错误链路继续判断 retry / strategy
  - `retry`：只重试当前 action；`maxRetries` 是额外重试次数，`retryDelayMs` 是每次重试前的协作式等待毫秒数（`engine/executor.go:394-399`）
  - `strategy`：空/`resume` 继续，`skip` 返回 `errSkip`，`abort` 返回 `errcode.NewActionError(errcode.ErrExecFailed, ...)`
- action 最终成功或命中 `ignoreCodes` 后，如果 `ListenRefs` 非空，调用 `ActionHandler.RegisterListen(listenRefs)` 注册监听
- 监听注册失败不进入 handler/retry/ignoreCodes；执行节点延迟后按 `onError.strategy` 收束（abort 时构造 `errcode.ErrListenRegister`，`engine/executor.go:363-385`）
- 最终成功、ignore 或最终失败收束前执行节点级延迟 `nodeDelay()`；重试间隔只执行 `retryDelayMs`

**onError.strategy 常量**（`flow/flow.go:164-168`）：

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

**字段**（`flow/flow.go:41-45`）：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"loop"` |
| `Body` | string | `body` | 循环体节点 ID（单个）；多步骤时指向一个 sequence 节点 |
| `LoopCount` | int | `loopCount` | 循环次数；<0 = 无限循环，=0 = 跳过循环体，>0 = 执行 N 次 |
| `Condition` | string | `condition` | 前置条件：每次迭代开始前求值，false 时退出循环 |
| `BreakCondition` | string | `breakCondition` | 后置条件：每次迭代结束后求值，true 时退出循环 |

**行为**（`engine/executor.go:197-247`）：
- `LoopCount == 0` 时直接跳过循环体，返回 nil（`engine/executor.go:198-201`）
- `LoopCount < 0` 时为无限循环（`engine/executor.go:203`）
- 每次迭代：
  1. 检查 `ctx` 取消信号
  2. 前置条件检查：如果 `Condition` 非空，求值返回 false 时退出循环（`engine/executor.go:209-215`）
  3. 执行循环体 `body` 节点
  4. 捕获 `errBreak` 或 `errSkip` 时退出循环
  5. 捕获 `errContinue` 时跳到下一次迭代，但**仍评估后置退出条件**——否则依赖 breakCondition 退出的无限 loop 一旦命中 continue 就永不退出（`engine/executor.go:220-237`）
  6. 真实错误向上传播
  7. 后置条件检查：如果 `BreakCondition` 非空，求值返回 true 时退出循环

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

**字段**（`flow/flow.go:47-50`）：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"boolean"` |
| `Condition` | string | `condition` | 条件表达式（`state:` 或 `lua:` 前缀） |
| `TrueNext` | string | `trueNext` | 条件为 true 时跳转的节点 ID（空 = 不跳转） |
| `FalseNext` | string | `falseNext` | 条件为 false 时跳转的节点 ID（空 = 不跳转） |

**行为**（`engine/executor.go:513-540`）：
- 对条件表达式求值
- 根据结果选择 `TrueNext` 或 `FalseNext` 作为目标节点
- 目标节点 ID 为空时，不执行任何操作（相当于"顺序流出"）
- 执行目标节点后，如果返回 `errSkip`，静默处理（返回 nil）
- boolean 节点不调用 `nodeDelay()`（仅 action 节点调用延迟）

**注意**：`Condition` 字段在 `loop` 和 `boolean` 之间复用。两者语义一致（求值为 bool），且一个节点只会是一种类型，不会产生歧义。

**JSON 示例**：
```json
{
  "type": "boolean",
  "condition": "lua:has_role.lua",
  "trueNext": "startGame",
  "falseNext": "createRole"
}
```

### 4.6 switch 节点

**用途**：按 `cases` 顺序对每条条件求值，执行第一条命中分支；全部未命中走 `defaultNext`。

**字段**（`flow/flow.go:52-54,97-103`）：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"switch"` |
| `Cases` | `[]SwitchCase` | `cases` | 按顺序匹配的条件分支，每条含 `condition` / `next` |
| `DefaultNext` | string | `defaultNext` | 所有 case 未命中时跳转的节点 ID（空 = 正常结束） |

**行为**（`engine/executor.go:548-588`）：
- 单跳无 fall-through：命中第一条 case 即执行其 `next` 并返回，不再评估后续 case
- 命中 case 但 `next` 为空：debug 日志后结束分支，返回 nil
- 无 case 命中且配置了 `defaultNext`：执行默认分支
- 无 case 命中且未配置 default：**Warn**「分支静默结束」——压测里最易藏 bug 的路由落空场景，默认可见；若属预期，补一个 default 即可消除
- 分支目标返回 `errSkip` 时静默处理（返回 nil）
- 每条 case 的条件同样在加载期编译（`flow/prepare.go:68-73`）

**JSON 示例**：
```json
{
  "type": "switch",
  "cases": [
    { "condition": "state:level >= 80", "next": "highLevelBranch" },
    { "condition": "state:level >= 40", "next": "midLevelBranch" }
  ],
  "defaultNext": "lowLevelBranch"
}
```

### 4.7 weighted 节点

**用途**：按权重随机选择一个 option 执行。

**字段**（`flow/flow.go:61-62,77-81`）：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"weighted"` |
| `Options` | `[]WeightedOption` | `options` | 加权选项列表 |

**WeightedOption 结构**：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Node` | string | `node` | 目标节点 ID |
| `Weight` | int | `weight` | 权重值 |

**行为**（`engine/executor.go:607-644`）：
- `Options` 为空时直接返回 nil
- 负权重视为 0 并打 Warn（`engine/executor.go:614-619`）
- 权重之和 <= 0 是**配置错误而非合法跳过**：返回 `ErrWeightedNoBranch` 错误（`engine/executor.go:622-625`），静默跳过会让整条支路在压测里凭空消失
- 使用累积和算法随机选择一个 option；`r ∈ [0,total)` 保证循环必然命中，循环后无兜底分支（`engine/executor.go:628-643`）——「兜底执行最后一个 option」会把不变量破裂伪装成正常分支，已改为不可达断言错误
- 执行选中的节点，捕获 `errSkip` 时静默处理

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

### 4.8 wait 节点

**用途**：用户在流程中显式插入的等待步骤，与全局默认延迟完全独立。

**字段**（`flow/flow.go:64-68`）：

| 字段 | 类型 | JSON 键 | 说明 |
|------|------|---------|------|
| `Type` | string | `type` | 固定值 `"wait"` |
| `WaitMs` | int | `waitMs` | 固定等待时长（毫秒）；仅当 WaitMin/WaitMax 都 ≤ 0 时生效 |
| `WaitMin` | int | `waitMin` | 随机等待最小值（毫秒，含） |
| `WaitMax` | int | `waitMax` | 随机等待最大值（毫秒，含） |
| `Then` | string | `then` | 等待完成后执行的唯一后继节点 ID（可选） |

**行为**（`engine/executor.go:647-678`）：
- 优先级：`WaitMin/WaitMax`（随机模式）> `WaitMs`（固定模式）
- `WaitMin > 0 && WaitMax > 0` 时使用随机模式：等待 `[WaitMin, WaitMax]` 范围内的随机毫秒数
  - `WaitMin >= WaitMax` 时 Warn 并使用 `WaitMin` 作为固定值
- 仅 `WaitMin` 或 `WaitMax` 其中一个 > 0 时，打印警告并跳过
- `WaitMs > 0` 时等待固定毫秒数；`WaitMs < 0` 时打印警告并跳过
- 等待走 `ActionHandler.CooperativeSleep`：等待期间 drain 任务队列（跑 listen 回调）；ctx 取消时向上传播取消
- 等待成功或因参数无效被跳过后，`Then` 非空时执行对应后继节点
- context 取消或协作式休眠失败时直接返回，不执行 `Then`

**JSON 示例**：
```json
{ "type": "wait", "waitMs": 2000 }
{ "type": "wait", "waitMin": 1000, "waitMax": 3000 }
{ "type": "wait", "waitMs": 2000, "then": "nextAction" }
```

### 4.9 break 节点

**用途**：产生 `errBreak` 信号，中断最近的 `loop`。

**字段**：无额外字段。仅有 `type: "break"`。

**行为**：
- 在 `executeNode` 的 switch 中直接返回 `errBreak`（`engine/executor.go:136-137`）
- 信号通过 Go error 机制在节点树中向上冒泡
- 冒泡路径：`executeBreak -> executeSequence（透传） -> executeLoop（捕获）`
- 信号不会穿透 `executeLoop`，外层 sequence 不会感知到它

**JSON 示例**：
```json
{ "type": "break" }
```

### 4.10 continue 节点

**用途**：产生 `errContinue` 信号，跳过本次迭代剩余步骤，进入下一次迭代。

**字段**：无额外字段。仅有 `type: "continue"`。

**行为**：
- 在 `executeNode` 的 switch 中直接返回 `errContinue`（`engine/executor.go:138-139`）
- 信号冒泡到最近的 `executeLoop` 时被捕获，进入下一次迭代（仍评估后置退出条件）
- 冒泡路径与 `break` 相同，只是捕获方处理方式不同

**JSON 示例**：
```json
{ "type": "continue" }
```

## 5. 循环控制信号传播

### 5.1 信号定义

`engine/executor.go:23-28`：

```go
var (
    errBreak = errors.New("break")
    errContinue = errors.New("continue")
    errSkip = errors.New("skip")
)
```

这三个信号通过 Go 的 error 传播机制在节点树中冒泡。`errSkip` 由 `onError.strategy=skip` 产生，被 sequence/loop/boolean/switch/weighted 捕获后视为当前分支完成，不继续传播为失败（`engine/executor.go:20-22` 注释）。

### 5.2 传播规则

| 节点 | 对 errBreak 的处理 | 对 errContinue 的处理 | 对 errSkip 的处理 |
|------|--------------------|-----------------------|-------------------|
| `break` | 产生 errBreak | - | - |
| `continue` | - | 产生 errContinue | - |
| `executeSequence` | 透传（不捕获） | 透传（不捕获） | 捕获，跳过剩余子节点 |
| `executeBoolean` | 透传 | 透传 | 捕获，返回 nil |
| `executeSwitch` | 透传 | 透传 | 捕获，返回 nil |
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

每个 action 节点执行完后，框架自动等待的最小间隔。目的：避免压测机器人执行过快，模拟真实用户的操作节奏。来源：`TaskFlow.DefaultDelayMs`，节点级 `DelayMs` 可覆盖。`DefaultDelayMs` 为 0 时引擎兜底为 1000ms（`engine/executor.go:64-70`），保证漏配的流程每个 action 节点仍有 drain 窗口（listen 回调靠节点延迟 drain）。

**wait 节点（用户负责 -- 业务逻辑控制）**：

用户在 flow.json 中显式插入的等待步骤。目的：在特定业务节点之间插入固定等待，如等待服务器处理、等待动画等。与 defaultDelayMs 完全独立，两者叠加生效。

### 6.2 nodeDelay 方法

`engine/executor.go:680-692`：

```go
func (e *Executor) nodeDelay(ctx context.Context, node *flowdef.Node) error {
    ms := node.DelayMs
    if ms == 0 {
        ms = e.defaultDelayMs
    }
    if ms < 0 {
        return nil // 禁用延迟
    }
    return e.handler.CooperativeSleep(ctx, time.Duration(ms)*time.Millisecond)
}
```

**延迟优先级**：`node.DelayMs` > `TaskFlow.DefaultDelayMs`（0 时引擎默认 1000）

| DelayMs 值 | 行为 |
|------------|------|
| > 0 | 使用此值作为延迟毫秒数 |
| = 0 | 使用 `TaskFlow.DefaultDelayMs`（本身为 0 则 1000） |
| < 0 | 禁用延迟 |

**注意**：
- `nodeDelay` 仅在 `action` 节点执行完后调用。`boolean` / `switch` 等控制节点不触发延迟。
- 延迟走协作式休眠：等待期间持续 drain 任务队列（listen 回调），ctx 取消时向上传播取消。

## 7. Executor -- 图遍历

### 7.1 Executor 结构体

`engine/executor.go:30-39`：

```go
type Executor struct {
    flow           *flowdef.TaskFlow
    handler        ActionHandler
    defaultDelayMs int    // 解析自 flow.DefaultDelayMs，初始化后只读
    caller         string // 调用方标识（机器人账号），用于日志追踪
}
```

### 7.2 创建与运行

```go
func NewExecutor(flow *flowdef.TaskFlow, handler ActionHandler, caller string) *Executor
func (e *Executor) Run(ctx context.Context) error   // 从 "main" 节点开始执行
func (e *Executor) Flow() *flowdef.TaskFlow         // 返回流程图定义
```

`Run()` 从 `"main"` 节点开始执行流程，阻塞直到流程结束或 context 取消。退出日志分级：正常结束与 ctx 取消为 debug（取消是预期生命周期事件，500 个 robot 停止时不打 warn 刷屏），其它错误为 warn（`engine/executor.go:84-104`）。

### 7.3 executeNode 路由

```go
func (e *Executor) executeNode(ctx context.Context, nodeID string) error
```

按 nodeID 查找节点，按 `Type` 字段分发到对应的执行方法（`engine/executor.go:121-142`）。每步执行前检查 `ctx.Err()`。

路由规则：
- `NodeSequence` -> `executeSequence()`
- `NodeAction` -> `executeAction()`
- `NodeLoop` -> `executeLoop()`
- `NodeBoolean` -> `executeBoolean()`
- `NodeSwitch` -> `executeSwitch()`
- `NodeWeighted` -> `executeWeighted()`
- `NodeWait` -> `executeWait()`
- `NodeBreak` -> 直接返回 `errBreak`
- `NodeContinue` -> 直接返回 `errContinue`

### 7.4 ActionHandler 接口

`engine/executor.go:41-59`：

```go
type ActionHandler interface {
    ExecuteAction(ctx context.Context, actionDef *flowdef.ActionDef) error
    ExecuteCondition(condition *flowdef.CompiledCondition) bool
    RegisterListen(refs []flowdef.ListenRef) error
    CooperativeSleep(ctx context.Context, d time.Duration) error
}
```

由 `robot.robotActionHandler` 实现（`robot/robot.go:632,830,868,887`），负责具体的网络请求、Lua 脚本执行、条件判断、推送监听注册与协作式休眠：

- `ExecuteCondition` 执行加载期已经准备的 state 或 Lua 条件：`state:` 走 `CompiledCondition.EvalState`，`lua:` 经 `LuaScript()` 分流到 `executeLuaBoolean`（`robot/robot.go:829-836`）
- `RegisterListen` 批量注册持久化推送监听（注册本身不阻塞流程）。推送到达后的处理分两路，均不在网络 pump goroutine 内碰业务 LState：script 回调只投递到 Robot 任务队列，由执行器 goroutine 在等待窗口内串行执行；声明式 store 由 pump 直接写线程安全的 state.Store
- `CooperativeSleep` 协作式休眠：等待期间持续 drain 任务队列，不让「会等待」的点饿死推送回调；ctx 取消时返回 ctx.Err()

## 8. 节点字段速查表

Node 结构体见 `flow/flow.go:35-75`。

| 字段 | Go 类型 | JSON 键 | 适用节点 | 说明 |
|------|---------|---------|----------|------|
| `Type` | string | `type` | 所有 | 节点类型 |
| `Next` | `[]string` | `next` | sequence | 子节点 ID 列表（顺序执行） |
| `Body` | string | `body` | loop | 循环体节点 ID（单个） |
| `LoopCount` | int | `loopCount` | loop | 循环次数，<0 无限，=0 跳过，>0 执行 N 次 |
| `Condition` | string | `condition` | loop / boolean | loop：前置条件；boolean：分支条件 |
| `BreakCondition` | string | `breakCondition` | loop | 后置退出条件 |
| `TrueNext` | string | `trueNext` | boolean | 条件为 true 时的目标节点 |
| `FalseNext` | string | `falseNext` | boolean | 条件为 false 时的目标节点 |
| `Cases` | `[]SwitchCase` | `cases` | switch | 按序匹配的条件分支（condition/next） |
| `DefaultNext` | string | `defaultNext` | switch | 全部 case 未命中时的兜底目标 |
| `Options` | `[]WeightedOption` | `options` | weighted | 加权选项列表 |
| `Action` | string | `action` | action | 引用 actions 表中的动作名（空 = 纯监听节点） |
| `OnError` | `*OnErrorDef` | `onError` | action | 错误处理链路 |
| `ListenRefs` | `[]ListenRef` | `listenRefs` | action | 动作后注册的推送监听引用 |
| `WaitMs` | int | `waitMs` | wait | 固定等待时长（毫秒） |
| `WaitMin` | int | `waitMin` | wait | 随机等待最小值 |
| `WaitMax` | int | `waitMax` | wait | 随机等待最大值 |
| `Then` | string | `then` | wait | 等待完成后的唯一后继节点 ID |
| `DelayMs` | int | `delayMs` | action | 覆盖 defaultDelayMs |

`Node` 还持有加载期编译产物 `compiledCondition` / `compiledBreakCondition`（不序列化），`SwitchCase` 同样持有 `compiledCondition`（`flow/flow.go:73-74,102`）。

## 9. ActionDef -- 动作定义

### 9.1 结构体

`flow/flow.go:172-188`，14 个 JSON 字段：

```go
type ActionDef struct {
    Name        string                 `json:"-"`           // 运行时回填（actions map 的 key），不参与序列化
    Pattern     string                 `json:"pattern"`     // 动作模式
    Service     string                 `json:"service"`     // 目标服务名
    Route       any                    `json:"route"`       // 不透明路由，原样传给 protocol.EncodeTCP
    Script      string                 `json:"script"`      // Lua 脚本路径（lua 模式）
    Address     string                 `json:"address"`     // 连接地址（connect 模式），可用 state:key 形式
    C2SProto    string                 `json:"c2sProto"`    // 客户端消息 proto 全名
    S2CProto    string                 `json:"s2cProto"`    // 服务器响应 proto 全名
    Bindings    []binding.FieldBind    `json:"bindings"`    // C2S 字段绑定
    Store       []binding.StoreMapping `json:"store"`       // S2C 响应字段 -> 状态存储映射
    Timeout     int                    `json:"timeout"`     // 超时秒数（request/listen 模式）
    Keys        []string               `json:"keys"`        // clearState 要清除的 key 列表
    URL         string                 `json:"url"`         // HTTP 请求 URL（httpRequest 模式），支持 state: 前缀
    Method      string                 `json:"method"`      // HTTP 方法（httpRequest 模式）：POST(默认) / GET
    ContentType string                 `json:"contentType"` // HTTP 内容类型（httpRequest 模式）：json(默认) / form
}
```

### 9.2 14 种 Pattern

常量定义见 `flow/flow.go:121-136`。

**网络通信（TCP）**：

| Pattern 常量 | 值 | 说明 |
|---------------|----|------|
| `PatternTCPSend` | `"tcpSend"` | TCP 单向发送 |
| `PatternTCPRequest` | `"tcpRequest"` | TCP 请求-响应 |
| `PatternTCPConnect` | `"tcpConnect"` | TCP 连接建立 |
| `PatternTCPClose` | `"tcpClose"` | TCP 连接关闭 |
| `PatternTCPListen` | `"tcpListen"` | TCP 推送消息消费（事件等待 ListenRefs 预缓存） |

**网络通信（UDP）**：

| Pattern 常量 | 值 | 说明 |
|---------------|----|------|
| `PatternUDPSend` | `"udpSend"` | UDP 单向发送 |
| `PatternUDPRequest` | `"udpRequest"` | UDP 请求-响应 |
| `PatternUDPConnect` | `"udpConnect"` | UDP 连接建立 |
| `PatternUDPClose` | `"udpClose"` | UDP 连接关闭 |
| `PatternUDPListen` | `"udpListen"` | UDP 推送消息消费（事件等待 ListenRefs 预缓存） |

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

心跳不是 flow action：由 Robot 建连后读取对应连接 codec 配置里的 `heartbeat` 对象构造 `engine.HeartbeatConfig` 注册到连接（`robot/robot.go:1685,1760`，详见 §17）。

### 9.3 各 Pattern 执行行为

**tcpSend / udpSend**（`engine/action.go:1544`）：
1. 调用 `buildBody(def)` 构建 protobuf 消息体
2. 调用 `expectedRouteKey(proto, service, def.Route)` 计算路由键
3. 获取加密密钥
4. 调用 `protocolEncode`（内部 `adapter.EncodeTCP/UDP(route, body, secretKey)`）编码完整包
5. 调用 `protocolSend`（内部 `netSender.TCPSend/UDPSend(service, packet)`）发送
6. 编码返回 nil（resolver 解析不到该连接的 codec）时返回 `errcode.ErrEncodeFailed`

**tcpRequest / udpRequest**（`engine/action.go:1598-1700`）：
1. 同 tcpSend 的 1-4 步构建请求包（按 timingLevel 记录 Build/Encode 耗时）
2. 调用 `protocolRequest`（内部 `ActionNetSender.TCPRequest/UDPRequest(ctx, service, packet, routeKey, timeout...)`）等待响应，`def.Timeout > 0` 时覆盖默认超时
3. 请求层失败（超时/断连/发送失败）且非主动取消时记 `FailedRequests`（进 RTT Apdex 分母按 frustrated，`engine/action.go:1663-1672`）
4. `exchange.HeaderErr != 0` 时：`handleHeaderError` 解析响应体 + 调用 `adapter.DescribeError` 获取描述，构造 `errcode.NewActionError(errcode.ErrorCode(headerErr), ...)` 返回
5. 调用 `parseAndStoreResponse(def, respBody)` 解析 S2C proto 并存储字段
6. 返回发送字节数、接收字节数、计时拆解和错误

**tcpConnect / udpConnect**（`engine/action.go:948-982`）：
1. 调用 `resolveAddress(def.Address)` 解析地址（支持 `state:` 前缀）
2. 地址为空时返回 `errcode.ErrAddrEmpty`
3. 阻塞拨号经 `coopIO` 在后台 goroutine 执行（等待窗口 drain mailbox），调用 `netSender.ConnectTCP/UDP(service, addr)` 建立连接

**tcpClose / udpClose**：
1. 调用 `netSender.CloseTCP/UDP(service)` 关闭连接

**tcpListen / udpListen**（`engine/action.go:1704`）：
1. 计算超时（默认 `DefaultListenTimeoutSec = 60` 秒）
2. 计算路由键
3. 通过 `netSender.TCPListen/UDPListen(ctx, service, routeKey, timeout)` 等待已预注册监听：
   - 队列已有消息时立即返回
   - 新消息入队时由容量 1 的边沿通知唤醒
   - 同时监听 context 取消和 deadline
   - 收到响应后：检查 headerErr、解析存储、返回
4. 命中时按「帧到达时刻 vs 开始等待时刻」三态分类等待时长样本（`engine.ClassifyListenWait`，`engine/action.go:165-177`）；超时记 `ListenTimeouts` 单独成率

**httpRequest**（`engine/action.go:1030-1156`）：
1. 解析 URL（支持 `state:` 前缀），空 URL 返回 `errcode.ErrURLEmpty`
2. 确定 HTTP 方法（默认 POST）和内容类型（默认 json）
3. 根据 contentType 构建请求体：
   - `json`：将 bindings 转为 map（map 绑定按 string key 构造），JSON 序列化
   - `form`：将 bindings 编码为 `k=v&k2=v2` 的 url.Values；**不支持 map 绑定**（扁平编码无无歧义表示，直接报错指路 json）
4. HTTP Do 经 `coopIO` 在后台 goroutine 执行，往返计入 WireRTT
5. 非 2xx 状态码返回 `errcode.ErrHTTPStatus`（detail 只含 status 码与文本，body 截断进日志）
6. 成功且配置了 store 时解析 JSON 响应并存储

**setState**（`engine/action.go:1009-1028`）：
1. 遍历 bindings，对每个绑定：
   - 检查 Condition 表达式
   - 调用 `resolveStateValue` 解析值（map 绑定按 string key）
   - 跳过 nil 值
   - 使用 `Field` 作为 key 写入 state

**clearState**（`engine/action.go:996-1007`）：
1. 遍历 `Keys` 列表，逐一调用 `store.Delete(key)`
2. 加载期校验：`Keys` 不得包含机器人内置状态 `id` / `index` / `account`（`flow/prepare.go:10-30`），命中直接报错拒绝加载

**lua**（`robot/robot.go:775`）：
1. 由 `robotActionHandler.ExecuteAction` 特殊处理
2. 当前 Robot 主流程获取独占 LState
3. 同步执行脚本
4. 阻塞型 Lua API 只暂停当前主流程；connectionPump 与连接级心跳继续独立运行
5. 脚本 `return nil` 表示成功；`return err table` 时由 runtime 重建 `*errcode.ActionError` 透传（含 Code/Detail）；返回 number 等非法值 fail loud（报错）

**取消语义（全 pattern 统一）**：`ActionExecutor.Execute` 入口一次早退，有等待窗口的（request / listen / connect / http）把流程 ctx 送到真正的阻塞点。取消一律报 `errcode.ErrActionCanceled`（Unwrap 保留 `context.Canceled`），monitor 归类为 Canceled 而非 Timeout/Failure（`engine/action.go:379-392` 注释）。

## 10. FieldBind -- 字段绑定

### 10.1 结构体

`binding/field.go:29-53`：

```go
type FieldBind struct {
    Field         string         `json:"field"`         // 目标 proto 字段名
    Type          string         `json:"type"`          // 绑定类型
    Value         any            `json:"value"`         // fixed: 固定值
    Source        string         `json:"source"`        // 数据来源 state key
    Path          string         `json:"path"`          // 嵌套字段导航
    Values        []any          `json:"values"`        // 候选值列表
    Entries       []MapEntryBind `json:"entries"`       // map: key->子绑定条目
    Required      bool           `json:"required"`      // 缺失时报错
    Filters       []FilterDef    `json:"filters"`       // 过滤条件列表
    Min           int            `json:"min"`           // 随机数最小值
    Max           int            `json:"max"`           // 随机数最大值
    Precision     int            `json:"precision"`     // 浮点精度
    Length        int            `json:"length"`        // 字符串长度
    Count         int            `json:"count"`         // 选取数量
    Charset       string         `json:"charset"`       // 字符集别名或自定义字符集
    ExcludeSource string         `json:"excludeSource"` // 排除列表来源
    Optional      bool           `json:"optional"`      // 允许字段为空
    Wrap          bool           `json:"wrap"`          // 单值包装为切片
    StoreAs       string         `json:"storeAs"`       // 中间变量存储
    KeySource     string         `json:"keySource"`     // randomPickMap 的 key 来源
    Condition     string         `json:"condition"`     // 条件绑定（state:/lua: 前缀）
}
```

`MapEntryBind` 为 `{ Key any; Value FieldBind }`（`binding/field.go:23-26`）。

### 10.2 17 种 Binding Type

常量定义见 `binding/constants.go:5-23`。

**取值类**：

| 常量 | 值 | 说明 |
|------|----|------|
| `BindFixed` | `"fixed"` | 固定值（`Value` 字段） |
| `BindState` | `"state"` | 从 StateStore 读取（`Source` 为 key），隐式 required |
| `BindStateFirst` | `"stateFirst"` | 从 StateStore 列表取第一个元素，空列表返回 nil，隐式 required |

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

`Charset` 为空时默认 `alphanum`；其他非空字符串按自定义字符集字面量处理，例如 `"ABC-123_"`（`engine/action.go:63-69`）。

**辅助类**：

| 常量 | 值 | 说明 |
|------|----|------|
| `BindListSize` | `"listSize"` | 返回 state 列表长度（int） |
| `BindMap` | `"map"` | 用 `entries:[{key,value}]` 构造 proto map 字段；key 保持配置声明的原始类型（proto map key 允许整数/布尔），setState/HTTP json body 场景按 string key 归一（`engine/action.go:566-587`）。不支持嵌套 map |

### 10.3 绑定处理流程

`engine/action.go:506-545` 的 `bindFields`，每个绑定的处理顺序：

1. **Condition 检查**：condition 表达式求值为 false -> 跳过该绑定（`bindingConditionSatisfied`，字段上的 `lua:` 条件与节点条件共用 Robot 层同一个求值实现，由 `SetLuaConditionEvaluator` 注入，`engine/action.go:219-231`）
2. **nil 值处理**：`resolveFieldValue` 返回 nil 时：
   - `Optional=true` -> 静默跳过
   - `Required=true` 或隐式必需类型 -> 返回 `errcode.ErrBindField` 动作错误
   - 其余情况 -> 静默跳过
3. **StoreAs 写入**：值非 nil 且配置了 StoreAs -> 写入 state（即使 Field 为空也写入）
4. **空 Field 跳过**：值仍为 nil 或 Field 为空字符串 -> 跳过 proto 赋值
5. **proto 赋值**：调用 `Factory.SetField` 写入消息字段

`resolveFieldValue` 返回 error 表示绑定配置本身非法（未知 type、随机区间反向），必须打断动作——否则字段悄悄变 nil，压测数据看起来"成功"但内容是错的（`engine/action.go:628-632` 注释）。

### 10.4 Path 导航

`Path` 支持点分路径从嵌套 map/list 中提取值。例如 `"heroList[0].heroId"` 会先取 `heroList` 字段，再取索引 0，再取 `heroId` 字段。

`Path` 还支持用 `|` 分隔多条候选路径，按顺序尝试，返回第一个非 nil 的值。例如 `"mailUid|gid"` 会先尝试 `mailUid`，不存在则取 `gid`。

### 10.5 Wrap 包装

`Wrap=true` 时，将单个值包装为 `[]any{val}`，用于 repeated 字段赋单个元素的场景。

### 10.6 隐式必需类型

`binding.IsImplicitRequired`（`binding/constants.go:33-40`）：以下 binding type 即使不设置 `Required=true`，值缺失时也会报错：
- `state`, `stateFirst`, `stateRandom`, `stateRandomN`, `stateMapKey`, `stateMapValue`

`FieldBind.IsRequired()`（`binding/field.go:56-61`）：`Optional` 优先级最高，为 true 时永不视为必需。

### 10.7 Filters 过滤

`binding/filter.go:3-10`：

```go
type FilterDef struct {
    Path   string `json:"path"`   // 字段导航路径
    Op     string `json:"op"`     // 比较运算符
    Value  any    `json:"value"`  // 比较目标值（固定值）
    Source string `json:"source"` // 比较目标值（从 state 读取）
    Mode   string `json:"mode"`   // 多条过滤器的聚合模式：any / all / none
}
```

支持 12 种比较运算符（`state.CompareValues`，`state/store.go:675-765`）：`eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `contains`, `notContains`, `in`, `notIn`, `notNil`, `isNil`。

`Mode` 常量（`binding/constants.go:26-30`）：`FilterModeAny`（任一命中）/ `FilterModeAll`（全部命中）/ `FilterModeNone`（全部不命中），用于多条过滤器对候选列表的多值聚合。

## 11. StoreMapping -- 响应存储映射

```go
type StoreMapping struct {
    Field  string `json:"field"`  // S2C 响应中的字段名
    Setter string `json:"setter"` // 写入 StateStore 的 key
}
```

- `Field` 支持嵌套路径如 `"heroList[0].heroId"`
- `Field` 为空字符串时，存储整个响应对象

## 12. ListenRef -- 监听引用

`flow/flow.go:108-118`：

```go
type ListenRef struct {
    Route  any    `json:"route"`  // 不透明路由（与 ActionDef.Route 格式一致）
    Server string `json:"server"` // 连接名，格式：协议:服务名（如 "tcp:logic"）
    Listen string `json:"listen"` // 监听定义名称（引用 listens 表），空 = 仅缓存不回调
    QueueSize *int `json:"queueSize,omitempty"` // 监听缓存队列容量
}
```

运行时通过 `protocol.ExpectedRouteKey(route)` 计算实际的 routeKey 字符串。

**queueSize 语义**（`robot/robot.go:982-996` `effectiveListenQueueSize`）：
- 未写（nil）→ 默认 1，与历史单槽语义逐字节等价
- 显式 > 0 → 按该值预创建环形队列
- 显式 <= 0 → 配置错误，注册时报错，不静默 clamp（遵循「禁止兼容性兜底」）

用 `*int` 区分「未写」与「显式 0」。

**加载期校验**（`flow/prepare.go:77-84`）：`listenRefs[].listen` 非空时必须在 `listens` 表中有定义，否则整份流程加载失败——JSON Schema 查不到这种跨表引用，漏掉这层校验会让监听永远不触发而流程照跑不误。

## 13. ListenDef -- 监听回调定义

`flow/flow.go:190-195`：

```go
type ListenDef struct {
    S2CProto string                 `json:"s2cProto"` // 解析推送消息的 proto 全名
    Store    []binding.StoreMapping `json:"store"`    // 响应字段到 StateStore 的映射
    Script   string                 `json:"script"`   // Lua listen 回调脚本；与 Store 互斥
}
```

当前判别：
- `Script` 非空 -> 协作式 Lua 回调：connectionPump 命中推送时只复制消息 + 投递 Robot 任务队列，Lua on_message 由执行器 goroutine 在 await / 等待窗口的 select 内就地串行执行，不在网络 pump goroutine 内碰业务 LState（`robot/robot.go:1003` 注释）
- `S2CProto` 非空且 `Store` 非空 -> Go-store 回调（解析 proto + 存储字段，pump 直接写线程安全的 state.Store）
- 否则 -> 仅缓存到监听队列，由主流程事件等待消费

`Script` 与 `Store` 同时非空会在注册阶段直接报配置错误，避免同一推送双写 state（`robot/robot.go:1003` `validateListenDef`）。

## 14. 条件表达式

### 14.1 条件表达式前缀

`flow/flow.go:139-142`：

| 前缀常量 | 值 | 说明 |
|----------|----|------|
| `PrefixState` | `"state:"` | 状态存储条件表达式 |
| `PrefixLua` | `"lua:"` | Lua 脚本条件 |

缺前缀的非空条件在**加载期**直接报错：运行期无从判断作者本意是 state 比较还是 Lua 脚本，放过去只会让该分支恒为 false（`flow/condition_compile.go:112-139`，缺前缀报错在 :136-138）。

### 14.2 编译与求值的两阶段结构

条件不是运行期边解析边求值的解释器，而是：

1. **加载期编译**：`PrepareTaskFlow` 遍历所有节点 `condition` / `breakCondition` / `cases[].condition` 与各 `FieldBind.condition`（含 map entry 内的子绑定），经 `conditionCompiler.compile` 编译为不可变 `CompiledCondition`（`flow/prepare.go:33-135`）。同一文本的条件按 source 去重复用。`CompiledCondition` 不持有 Robot 或 state.Store，同一实例可以由多个 Robot 并发共享（`flow/condition_compile.go:66-73`）。
2. **运行期求值**：`CompiledCondition.EvalState(store)` 走 AST 求值（`flow/condition.go:38-69`）；`lua:` 条件经 `LuaScript()` 分流到 Robot 层的 `executeLuaBoolean`。

### 14.3 文法（严格类型，零关键字）

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

解析函数位于 `flow/condition_compile.go:219-374`（`parseOr` / `parseAnd` / `parseUnary` / `parseComparison` / `parseArithmetic` / `parseTerm` / `parseFactor`），产出扁平节点数组形式的 AST（conditionProgram）。

字面量只有数字（`123`、`1.5`）和带引号字符串（`"member"`）；裸标识符恒为 state 路径。**无 `true`/`false`/`nil` 关键字**——store 值域闭合且无指针，nil 只可能是「key 缺失」，由「缺失 → 错误」处理；存在性用按类型的非零比较表达（`!= 0` / `!= ""`）。

### 14.4 词法分析（`flow/condition_tokenizer.go`）

`tokenize(input)` 产出 token 序列（末尾 EOF 哨兵）：NUMBER、STRING、PATH、运算符、括号。

- 数字 `.` 后必须跟数字；拒绝科学计数/十六进制；整数字面量溢出报错，不退化为 float（`flow/condition_compile.go:376-389`）。
- 字符串 `"..."` 无转义；未闭合报错；`""` 合法。
- PATH 支持点分与 `[N]` 续段（state key 嵌套点分 + 数组下标）；空/非数字下标报错。
- 单字符 `=`/`|`/`&` 直接报错（常见笔误，`flow/condition_tokenizer.go:113-120`）。

### 14.5 短路与严格类型纪律

**短路**：`&&` 在左操作数 effective-false 时跳过右侧求值、`||` 在 effective-true 时跳过（`flow/condition.go:127-144` 的 evalBinary 分支）。

**严格类型**（`flow/condition_compare.go`）：
- 裸操作数（布尔上下文）必须 bool；数/串/missing → 记错误并按 false（`asBool`，`flow/condition_compare.go:71-77`）。
- `> >= < <=` 仅数值；`== !=` 仅同类型（number/string/bool），跨类型 → 记错误并按 false。
- 算术 `+ - * / %` 仅数值；`%` 仅整数；除法两边整型→整除、任一浮点→浮点除；不做字符串拼接。
- missing key、除零、浮点取模、uint64 越界参与整数运算 → 记错误并按 false。
- `[]byte`/list/map 非标量，任何操作 → 记错误并按 false。
- 数值比较用 `cmpNumbersExact`（`flow/condition_compare.go:79` 起）：两个整数按 int64/uint64 精确比较（正确处理混合符号与 uint64>MaxInt64 的玩家 ID，防浮点失真），任一浮点才退化为 float64 比较。
- `state.CompareValues` 不用于条件求值，仅 FilterDef 过滤器沿用（语义未变）。

**错误语义（local-false）**：出错的子表达式视为 effective-false 并记录首个错误；短路照常；顶层若有错误打一条 warn，但返回实际计算出的布尔结果（如 `missing || fallback`，fallback 为真时结果为 true）。仅当结果不是 bool（裸数值/字符串顶层、结构错误）才返回 false（`flow/condition.go:53-69`）。

## 15. ActionExecutor -- 声明式动作执行

### 15.1 结构体

`engine/action.go:196-210`：

```go
type ActionExecutor struct {
    netSender   ActionNetSender        // 网络发送委托，由 Robot 层实现
    store       *state.Store           // Robot 状态存储
    factory     *protox.Factory        // 动态 protobuf 消息工厂
    resolver    protocol.CodecResolver // 按 "<proto>:<service>" 解析每连接的 Go codec adapter
    timingLevel int                    // 计时细分级别：0=rtt, 1=codec, 2=full
    coopIO      func(job func()) error // 协作式阻塞 I/O 后端（Robot 调度器注入，nil 时同步执行）
    luaCondition func(script string) bool // 字段绑定 lua: 条件求值后端（Robot 层注入）
}
```

encode 侧（protocolEncode / ExpectedRouteKey / DescribeError）通过 resolver 按 `"<proto>:<service>"`（proto 由 pattern 推导，service=def.Service）Resolve 出该连接的 Go SchemaAdapter。Resolve nil 时 fail loud（`ErrEncodeFailed`），不静默兜底。

### 15.2 NetSender / ActionNetSender 接口

`engine/action.go:290-364`。`NetSender` 17 个方法，由 Robot 层的 `netSenderAdapter` 实现：

**发送/监听等待**：
- `TCPSend(service, packet) (int, error)`
- `UDPSend(service, data) (int, error)`
- `TCPListen(ctx, service, routeKey, timeout) (*NetExchange, error)` -- nil, nil 表示超时
- `UDPListen(ctx, service, routeKey, timeout) (*NetExchange, error)`
- `ConnectTCP(service, address) error`
- `ConnectUDP(service, address) error`
- `HTTPRequest(url, method, contentType, body) (*HTTPExchange, error)`

**连接管理**：
- `CloseTCP(service)`
- `CloseUDP(service)`

**监听**：
- `GetTCPListenResp(service, routeKey) *NetExchange`
- `GetUDPListenResp(service, routeKey) *NetExchange`
- `EnsureTCPListener(service, routeKey, queueSize)`
- `EnsureUDPListener(service, routeKey, queueSize)`

**密钥**：
- `GetTCPSecretKey(service) []byte`
- `SetTCPSecretKey(service, key)`
- `GetUDPSecretKey(service) []byte`
- `SetUDPSecretKey(service, key)`

`ActionNetSender`（`engine/action.go:341-364`）= `NetSender` + 两个取消感知的请求-响应方法：

- `TCPRequest(ctx, service, packet, routeKey, timeout...) (*NetExchange, error)`
- `UDPRequest(ctx, service, packet, routeKey, timeout...) (*NetExchange, error)`

拆两层而不是直接给 NetSender 全体方法加 ctx：请求-响应只有声明式动作在用（Lua 侧请求走 script.Waiter 协作式路径），而它是本层唯一「发出去后要等对端」的阻塞点，必须绑定流程 ctx 才能在任务停止时立刻退出在途请求；NetSender 其余能力被 Lua 运行时共用，保持原签名。实现方契约：ctx 取消时必须尽快返回，且返回的错误要能被上层识别为「取消」而非「超时」。

心跳不再是 flow action，由 Robot 建连后读取对应 codec 的 `heartbeat` 配置并注册到连接（详见 §17）。

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
| `protocolEncode(proto, service, route, body, key)` | 按 protocol 调用 EncodeTCP/UDP |
| `protocolSecretKey(protocol, service)` | 按 protocol 获取 TCP/UDP 密钥 |
| `protocolRequest(ctx, protocol, service, packet, routeKey, timeout...)` | 按 protocol 调用 TCP/UDP Request |
| `resolveAdapter(proto, service)` | 按 `<proto>:<service>` Resolve codec adapter |

### 15.5 响应处理

```
接收响应 -> parseAndStoreResponse(def, respBody)
         -> factory.Parse(s2cProto, body)
         -> storeResponse(storeMappings, fieldMap)
         -> navigatePath(fieldMap, field) 逐个字段存储到 StateStore
```

`handleHeaderError` 统一处理服务端返回的非零 headerErr：解析响应体 + 调用 `adapter.DescribeError` 获取描述 + 构造 `errcode.NewActionError(errcode.ErrorCode(headerErr), ...)`（`engine/action.go:1311`）。

### 15.6 计时拆解

`ActionTiming`（`engine/action.go:93-118`）承载单次动作的耗时拆解：请求级 `RequestTiming`（SendCost / WireRTT / DecodeWait / DecodeCost / DispatchToActionWait）、客户端 `ClientTiming`（Build / Encode / Send / DecodeWait / Decode / DispatchWait / ParseStore）、监听等待样本（ListenWaits / ListenReady / ListenTimeouts 三态，见 §9.3 tcpListen）。`timingLevel`（0=rtt / 1=codec / 2=full）控制记录哪些时间点。

RTT 的两个测点都取在贴近内核的位置：起点是 `AsyncWrite` 的写完成回调（数据真正交给内核），终点是 `RecvFrameAt`（帧在 gnet 入站缓冲里可读的时刻），避免协作式调度把施压机自身的排队算进服务端延迟。

## 16. ActionError -- 结构化错误

### 16.1 结构

`errcode/error.go:6-10`：

```go
type ActionError struct {
    Code   errcode.ErrorCode // 错误码（<100 框架错误，>=100 业务错误）
    Detail string            // 上下文描述
    cause  error             // 可选下层错误
}
```

ActionError 统一放在 `errcode` 包，实现 `monitor.CodedError` 接口供按 code 单维聚合。

### 16.2 错误格式

单一 code 维度，码段区分来源（<100 为框架错误，>=100 为业务/服务端错误），展示标签由 code 推导：

```
[#1] action=xxx service=logic           // 框架错误（code<100）
[#1004] desc: route=CreateTeam: ...     // 业务错误（code>=100）
```

### 16.3 构造与方法

| 方法 | 说明 |
|------|------|
| `NewActionError(code, detail, cause...)` | 创建结构化错误；可选第一个 cause 作为被包装的底层错误（`errcode/error.go:14-20`） |
| `Error() string` | `"[code] detail: cause"` 格式化输出 |
| `Unwrap() error` | 返回 cause，支持 `errors.Is/As` 链式判断 |
| `ErrorCode() uint64` | 返回数值错误码 |
| `ErrorDetail() string` | 返回上下文描述 |

### 16.4 流程配置哨兵错误

`engine/errors.go:11-16`：

| 错误 | 用途 |
|------|------|
| `ErrNodeNotFound` | 节点 ID 不存在 |
| `ErrUnknownNodeType` | 未知的节点 Type |
| `ErrActionNotFound` | 动作名不存在 |
| `ErrWeightedNoBranch` | weighted 节点权重之和 ≤ 0，无分支可选 |

`engine/errors.go:24-35` 的 `isCanceledError` 是全包唯一的取消判据：`normalizeActionCancel`（onError 短路）与 `execRequest`（是否记 frustrated 样本）共用，避免两处判据漂移。

## 17. 连接级声明式心跳

心跳不是 flow action，而是每条连接一份的 codec 配置（`<proto>_<service>_codec.json` 的 `heartbeat` 对象）。Robot 建连后读取该配置构造 `engine.HeartbeatConfig` 注册到 `network.Connection`（`robot/robot.go:1685,1760-1905`）。

**HeartbeatConfig**（`engine/heartbeat.go:58-69`）双模式 body 构造（互斥，由 codec schema 校验）：
- proto 模式：`C2SProto` 非空 → 每 tick 用 factory + Bindings 构造 proto body（复用 tcpSend 的 bindFields 解析）
- raw-binary 模式：`Fields` 非空 → 每 tick 按 `BuildHeartbeatBody` 小端打包（自研协议服/战斗同步）
- 空 body：两者皆无 → 静态心跳（轻量保活）

**HeartbeatField**（`engine/heartbeat.go:34-45`）-- raw 模式的单个字段：
- `Type`：u8/u16/u32/u64/i8/i16/i32/i64（小端整数）/ f32/f64（小端 IEEE754 浮点）
- `Source` 六值：`fixed`（固定值）/ `state`（读 state.Store）/ `stateCounter`（state 共享计数器自增）/ `counter`（私有计数器）/ `timestamp`（当前时间）/ `randomInt`（[Min,Max] 随机整数）；f32/f64 仅支持 fixed/state
- 属性：`value`（fixed 整型值，nil 报错不静默默认）/ `floatValue`（fixed 浮点值）/ `key`（state|stateCounter 的 state 键名）/ `start` / `step`（counter 初值与步长）/ `min` / `max`（randomInt 边界）/ `unit`（timestamp 的 ms|s，缺省 ms）

另有 `SkipWhenMissing`（raw 模式 state 源缺失时跳过本 tick 而非报错）与 `RequireSecretKey`（等待连接设置密钥后再启动心跳 timer）两个开关。

性能优化：注册时经 `CompileHeartbeatPlan` 把布局定型为「总长 + 动态槽位表」（`engine/heartbeat_plan.go`），每 tick 只覆写动态槽，零查表零 `append` 增长；`BuildHeartbeatBody` 作为 oracle 保留用于对拍。

## 18. 默认常量

`engine/action.go:30-33`：

| 常量 | 值 | 说明 |
|------|----|------|
| `DefaultRequestTimeoutSec` | 10 | tcpRequest/udpRequest 默认超时（秒） |
| `DefaultListenTimeoutSec` | 60 | tcpListen/udpListen 默认超时（秒） |
| `DefaultHeartbeatMs` | 3000 | 心跳默认间隔（毫秒） |
| `defaultDelayMs` 兜底 | 1000 | `TaskFlow.DefaultDelayMs` 为 0 时的引擎默认节点延迟（`engine/executor.go:64-70`） |

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
        { "field": "account", "type": "state", "source": "account" },
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
