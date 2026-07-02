# Action onError 错误链路设计

## 背景

当前 action 节点通过 `errorStrategy` 处理失败，能力接近简单 `try/catch`：只能在失败后选择继续、跳过或中断。声明式 action 与 Lua action 的执行外壳已经统一后，失败处理也需要从单字段策略扩展为一条清晰、可复用、可观察的错误链路。

本设计只作用于 action 节点，不新增全局错误处理，不新增独立 error 节点，也不做错误码到不同 handler 的映射。

## 目标

- 用 `onError` 替代 `errorStrategy`，表达 action 失败后的完整处理链路。
- 支持按错误码忽略可接受失败，但保留 monitor 中的真实失败样本。
- 支持连接普通节点作为错误 handler，handler 可复用且不污染主流程 `next`。
- 支持局部 retry，retry 只重试当前 action，不转换为普通 flow loop。
- 保持声明式 action 与 Lua action 的统一外壳：每次 attempt 都独立记录 monitor。
- 删除旧 `errorStrategy`，不提供自动迁移、fallback 或兼容读取路径。

## 非目标

- 不设计全局、sequence、loop 级别的错误处理。
- 不设计 code -> handler -> strategy 的错误码映射表。
- 不新增 `error` 节点类型。
- 不让 retry 参与主流程图跳转。
- 不改变 monitor 聚合公式和错误码模型。

## 数据模型

Action 节点删除旧字段：

```json
"errorStrategy": "abort"
```

Action 节点新增：

```json
{
  "type": "action",
  "action": "enterRoom",
  "delayMs": 1000,
  "listenRefs": [],
  "onError": {
    "ignoreCodes": [1001, 1002],
    "handler": "cleanupRoom",
    "retry": {
      "maxRetries": 2,
      "retryDelayMs": 500
    },
    "strategy": "abort"
  }
}
```

Go 模型建议：

```go
type Node struct {
    // ... existing fields
    OnError *OnErrorDef `json:"onError,omitempty"`
}

type OnErrorDef struct {
    IgnoreCodes []errcode.ErrorCode `json:"ignoreCodes,omitempty"`
    Handler     string              `json:"handler,omitempty"`
    Retry       *RetryDef           `json:"retry,omitempty"`
    Strategy    string              `json:"strategy,omitempty"`
}

type RetryDef struct {
    MaxRetries   int `json:"maxRetries,omitempty"`
    RetryDelayMs int `json:"retryDelayMs,omitempty"`
}
```

`strategy` 支持：

| 值 | 语义 |
| --- | --- |
| 空 / `resume` | 错误链路结束后继续原流程 |
| `skip` | 返回内部 `errSkip`，由 sequence/loop/boolean/weighted 捕获 |
| `abort` | 包装并返回 `ActionError`，中断流程 |

不保留 `goto`。错误 handler 连线已经表达“执行某个普通节点”，但它是调用边，不是普通主流程跳转。

## 执行语义

完整流程：

```text
执行 action
  ├─ 成功
  │    ├─ 注册 listenRefs
  │    ├─ 执行 delayMs
  │    └─ 返回 nil
  │
  └─ 失败
       ├─ context.Canceled / DeadlineExceeded
       │    └─ 直接返回，不走 onError，不执行 delayMs
       │
       ├─ ErrActionCanceled
       │    └─ 映射为 context.Canceled，不走 onError，不执行 delayMs
       │
       ├─ 命中 onError.ignoreCodes
       │    ├─ warn 日志
       │    ├─ monitor 保留原始 failure
       │    ├─ 注册 listenRefs
       │    ├─ 执行 delayMs
       │    └─ 返回 nil
       │
       └─ 进入 onError 链路
            ├─ 执行 onError.handler 子流程
            ├─ 如果还能 retry：等待 retryDelayMs 后重新执行当前 action
            └─ retry 耗尽：执行 delayMs 后按 strategy 收束
```

## retry 语义

`retry` 放在 `onError` 中，因为它只在失败链路中生效。但实现仍然位于 `engine.Executor.executeAction` 内部，是当前 action 的局部 attempt loop。

`maxRetries` 表示额外重试次数：

```text
maxRetries = 0 → 最多执行 1 次
maxRetries = 2 → 最多执行 3 次：原始 1 次 + 重试 2 次
```

伪代码：

```go
retriesUsed := 0
maxRetries := onErrorMaxRetries(node)

for {
    err := e.handler.ExecuteAction(ctx, actionDef)
    if err == nil {
        return e.finishActionSuccess(ctx, node, actionDef)
    }

    if cancelErr := normalizeActionCancel(err); cancelErr != nil {
        return cancelErr
    }

    if e.isIgnoredActionError(node, err) {
        return e.finishActionAccepted(ctx, node, actionDef, err)
    }

    if err := e.executeOnErrorHandler(ctx, node); err != nil {
        return err
    }

    if retriesUsed < maxRetries {
        retriesUsed++
        if err := e.retryDelay(ctx, node.OnError.Retry); err != nil {
            return err
        }
        continue
    }

    e.nodeDelay(ctx, node)
    return applyOnErrorStrategy(onErrorStrategy(node), func() error {
        return NewActionError(errcode.ErrExecFailed, "action="+node.Action, err)
    })
}
```

retry 不实现为普通 `loop` 节点、隐藏节点、handler 反向连线或 robot 层自动重试。原因是 retry 需要和 `handler`、`strategy`、`delayMs`、`listenRefs`、取消语义精确协作，而且每次 attempt 都必须经过统一 action 外壳并独立记录 monitor。

每次失败 attempt 都进入完整错误链路：

```text
第 1 次失败 → handler → retryDelayMs → retry
第 2 次失败 → handler → retryDelayMs → retry
第 3 次失败 → handler → delayMs → strategy
```

## handler 语义

`onError.handler` 是普通节点 ID：

```json
"onError": {
  "handler": "cleanupRoom"
}
```

handler 可以指向任意普通节点类型，包括 action、sequence、boolean、weighted、loop、wait。

画布中 action 节点提供特殊 error handle：

```text
[action: enterRoom] --error--> [sequence: cleanupRoom]
```

保存时不写入普通 `next`，只写入：

```json
"onError": {
  "handler": "cleanupRoom"
}
```

handler 是调用边：handler 执行完成后返回原 action 的错误链路，继续判断 retry 或 strategy。

handler 子流程失败时直接向上传播错误，不继续 retry，也不再套原 action 的 strategy。handler 内部如果有 action 节点，这些 action 可以使用自己的 `onError`。

`errSkip` 在 handler 内视为 handler 正常完成，避免 handler 内部使用 skip 策略时被当作错误传播。

## ignoreCodes 语义

`ignoreCodes` 是错误链路入口的接受旁路：

```json
"onError": {
  "ignoreCodes": [1001, 1002]
}
```

命中后：

- 打 warn 日志。
- 不执行 handler。
- 不执行 retry。
- 不执行 strategy。
- 流程语义上视为可接受，继续原流程。
- monitor 保留 action 的原始 failure 样本和错误码分布。

这表示“该错误对流程可接受”，不是“动作成功”。

## delay 语义

保留两个 delay：

- `delayMs`：action 节点生命周期结束后的节奏控制。
- `retryDelayMs`：错误链路内部，确认 retry 后、重新执行 action 前的等待。

执行规则：

```text
成功：listenRefs → delayMs → continue
ignoreCodes 命中：warn → listenRefs → delayMs → continue
失败且会 retry：handler → retryDelayMs → retry
失败且不 retry：handler → delayMs → strategy
```

失败后如果还要 retry，不执行 `delayMs`，只执行 `retryDelayMs`，避免等待时间叠加和语义混乱。

`retryDelayMs` 使用 `ActionHandler.CooperativeSleep`，等待期间继续 drain actor 任务队列；ctx 取消时直接返回取消。

## listenRefs 语义

`listenRefs` 仍然属于 action 被流程接受后的注册逻辑。

执行时机：

- action 最终成功。
- action 失败但命中 `ignoreCodes`。

不执行时机：

- 普通失败 attempt。
- 失败后准备 retry。
- retry 耗尽后走 strategy。
- context canceled。
- ErrActionCanceled。

监听注册失败属于 action 成功后的后处理失败，不进入 handler / retry / ignoreCodes。它执行 `delayMs` 后按当前节点的 `onError.strategy` 收束。原因是 action 本体已经成功，重试原 action 可能产生重复业务副作用。

## 日志

普通失败进入 onError 链路时记录失败日志，字段包含：

- caller
- action
- pattern
- onErrorStrategy
- handler
- retryUsed
- maxRetries
- errorCode
- errorDetail

`ignoreCodes` 命中时必须 warn：

```text
[ENGINE] 动作错误码已忽略，流程继续
```

retry 前建议 warn：

```text
[ENGINE] 动作失败后准备重试
```

retry 耗尽后按 strategy 分级：

- `abort`：error
- `skip` / `resume` / 空：warn

取消类错误保持 debug，不进入 onError。

## 后端改造范围

### engine/flow.go

- 删除 `Node.ErrorStrategy`。
- 新增 `Node.OnError`、`OnErrorDef`、`RetryDef`。
- 保留策略常量时，将命名语义从 errorStrategy 调整为 onError strategy；如果常量名仍复用，需要同步更新注释。

### engine/executor.go

重构 `executeAction`，建议拆分：

```go
func (e *Executor) executeAction(ctx context.Context, node *Node) error
func (e *Executor) finishActionSuccess(ctx context.Context, node *Node, actionDef *ActionDef) error
func (e *Executor) finishActionAccepted(ctx context.Context, node *Node, actionDef *ActionDef, err error) error
func (e *Executor) registerActionListens(ctx context.Context, node *Node, actionDef *ActionDef) error
func (e *Executor) executeOnErrorHandler(ctx context.Context, node *Node) error
func (e *Executor) retryDelay(ctx context.Context, retry *RetryDef) error
func applyOnErrorStrategy(strategy string, abortErr func() error) error
```

删除或替换：

```go
applyErrorStrategy
logActionFailure(..., strategy string, ...)
```

日志字段中删除 `errorStrategy`，改为 `onErrorStrategy`。

### robot 层

retry 不下沉到 `robotActionHandler.ExecuteAction`。

robot 层继续保持“一次调用 = 一次 action attempt = 一次 monitor 样本”。声明式与 Lua action 的统一外壳不需要因为 `onError.retry` 改变。

### monitor 层

不改变 monitor 核心逻辑。

要求：

```text
重试两次失败一次成功 → TotalActions=3, FailureCount=2, SuccessCount=1
ignoreCodes 命中 → FailureCount+1，但流程继续
```

## 前端改造范围

### 类型

删除：

```ts
errorStrategy?: string
```

新增：

```ts
onError?: {
  ignoreCodes?: number[]
  handler?: string
  retry?: {
    maxRetries?: number
    retryDelayMs?: number
  }
  strategy?: 'resume' | 'skip' | 'abort'
}
```

### Action 配置面板

新增“错误处理”折叠区：

- 忽略错误码 `ignoreCodes`
- 错误处理节点 `handler`
- 重试 `maxRetries` / `retryDelayMs`
- 最终策略 `strategy`

用户只想替代旧 `errorStrategy` 时，只需要配置：

```json
"onError": {
  "strategy": "abort"
}
```

无需创建 handler 节点。

### 画布连线

Action 节点增加特殊 error handle。

加载：

```text
node.onError.handler 非空 → 生成特殊 error edge
```

保存：

```text
特殊 error edge → node.onError.handler
```

该 edge 不进入普通 `next`。

### 节点展示

Action 节点显示 onError badge，例如：

```text
onError · retry:2 · ignore:2 · handler · abort
```

避免错误链路隐藏在折叠面板内不可见。

## 校验规则

后端和前端校验都应覆盖：

- `ignoreCodes` 只能是正整数错误码。
- `ignoreCodes` 不允许包含 `0`。
- `handler` 非空时必须引用存在节点。
- `handler` 不允许等于当前节点 ID。
- `retry.maxRetries >= 0`。
- `retry.retryDelayMs >= 0`。
- `strategy` 只能是空、`resume`、`skip`、`abort`。
- 保存时不生成空 `onError` 对象。

不做复杂环检测。普通 flow 已有 loop 表达能力，handler 指向复杂子图是允许的。

## 配置与文档更新

需要全链路替换旧字段：

- `conf/flow/*.json`
- 前端默认模板
- 测试 fixture
- 文档和示例中的 `errorStrategy`
- 校验提示文案

不提供旧字段兼容读取，不提供自动迁移函数。

## 测试计划

### engine

更新或新增 `engine/executor_action_test.go`：

- ctx 继续传给 action handler。
- `context.Canceled` 不走 onError，不执行 delay。
- `ErrActionCanceled` 映射为 `context.Canceled`，不执行 delay。
- 普通失败按 `onError.strategy=abort` 包装 `ErrExecFailed`。
- `onError.strategy=skip` 返回 `errSkip`。
- strategy 为空 / resume 时返回 nil。
- `ignoreCodes` 命中后返回 nil，执行 delay。
- `ignoreCodes` 命中后不执行 handler，不 retry。
- retry 成功：第一次失败、第二次成功，最终 nil，action 调用 2 次。
- retry 耗尽：`maxRetries=2` 时 action 调用 3 次。
- retry delay 次数正确。
- handler 每次失败后都执行。
- handler 失败直接返回，不继续 retry。
- listenRefs 只在最终成功 / ignore 时注册。
- listenRefs 注册失败不进入 retry。

### robot

保持现有 action 外壳测试。可新增说明性测试，确保 robot 层没有 retry 聚合逻辑。

### monitor

保持现有 collector 测试。通过集成测试或 robot 层测试验证 retry 多 attempt 不合并样本。

### 前端

- 类型检查：`npx tsc -b`
- 画布加载 / 保存：`onError.handler` 与特殊 error edge 双向转换。
- 表单校验：ignoreCodes、retry、strategy、handler 引用。
- Vitest 更新旧 `errorStrategy` 断言。

## 验收标准

- 仓库内不再出现有效配置字段 `errorStrategy`。
- action 节点通过 `onError` 表达 ignore、handler、retry、strategy。
- handler 连线在画布可见，但不污染普通 `next`。
- retry 是 `executeAction` 内部局部循环，不是普通 flow loop。
- 每次 retry attempt 都独立进入 robot action 外壳并记录 monitor。
- `ignoreCodes` 命中时流程继续，monitor 仍记录 failure。
- 取消类错误不走 onError，不执行 delay。
- 无旧字段 fallback、自动迁移或 `??` 兼容兜底。
