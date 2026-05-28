# ActionPlan / BindingPlan / StorePlan 预编译优化计划

## 背景

当前流程配置主要以解释执行方式运行：机器人每次执行 action 时读取 `ActionDef`，解析 binding/store 字段路径，查找 proto 字段，执行字段读写和 state 存取。高并发、长流程或大量 binding/store 场景下，这些配置解析会进入热路径，增加 CPU、内存分配和 GC 压力。

预编译计划的目标是：加载 `flow.json` 后，把稳定不变的配置解析为运行时可直接执行的数据结构，减少 action 热路径中的字符串解析、descriptor 查找和重复分支判断。

## 目标

- 将固定配置解析从运行时前移到加载期。
- 降低 action 执行热路径中的 CPU 和分配。
- 提前暴露 proto 名、字段路径、store 路径等配置错误。
- 不改变压测行为、发包模型、随机逻辑、state 读写语义和错误码语义。

## 非目标

- 不重写流程图执行器。
- 不改变 Lua 动作执行模型。
- 不提前计算随机值、state 值、filter 结果等运行时数据。
- 不做旧路径 fallback；如果实现该优化，应保持单一路径，避免兼容性兜底。

## 核心概念

### StorePlan

`StoreMapping` 的运行时版本。加载期将响应字段路径编译为 accessor，运行时直接从响应 proto 读取并写入 state。

示例：

```json
{ "field": "roleInfo.level", "setter": "roleLevel" }
```

编译后：

```text
StorePlan
  Source = compiled accessor(roleInfo.level)
  Setter = roleLevel
```

运行时：

```text
value := Source.Get(resp)
state.Set("roleLevel", value)
```

### BindingPlan

`BindingDef` 的运行时版本。优先只编译目标字段路径，保留现有取值逻辑。

示例：

```json
{ "field": "playerId", "type": "state", "source": "roleId" }
```

编译后：

```text
BindingPlan
  Type = state
  Source = roleId
  Target = compiled accessor(playerId)
```

运行时仍按 binding type 从 state/random/list/filter 取值，但写 proto 时不再重复解析字段路径。

### ActionPlan

`ActionDef` 的运行时版本。汇总 pattern、service、route、proto descriptor、BindingPlan、StorePlan 等信息。

示例：

```text
ActionPlan(Login)
  Pattern = tcpRequest
  Service = logic
  C2S = descriptor(Game.LoginC2S)
  S2C = descriptor(Game.LoginS2C)
  Bindings = [...BindingPlan]
  Stores = [...StorePlan]
```

## 推荐实施顺序

### 第一阶段：StorePlan

优先编译响应存储路径。

原因：

- 范围小，风险低。
- 只影响响应字段读取和 state 写入。
- 可以减少响应处理中的字段路径解析和 proto 反射查找。
- 配置错误可以在加载期提前暴露。

建议实现：

1. 在 engine 或 protox 层新增字段 accessor 编译能力。
2. 对每个带 `s2cProto` 的 action 编译 `store[].field`。
3. `ActionExecutor` 响应处理阶段使用 `StorePlan` 读取字段。
4. 添加单元测试覆盖：
   - 标量字段
   - 嵌套 message 字段
   - repeated/list 字段
   - 字段不存在
   - nil/空响应

### 第二阶段：BindingPlan

编译请求字段写入路径。

建议先只预编译目标字段路径：

- `binding.field` → compiled target accessor
- binding type、source、filters、wrap、optional 等逻辑先保留原实现

注意：

- 随机、state、listSize、stateRandomN 等必须运行时求值。
- 过滤器比较依赖运行时数据，不能提前算结果。
- repeated/map/nested 写入逻辑需要单独测试。

### 第三阶段：ActionPlan

将 action 级配置整体编译。

可编译内容：

- pattern 枚举化
- service 字符串保留
- route 预转换为运行时结构
- c2s/s2c proto descriptor 预查找
- bindings/store 替换为 plan
- listen refs 可预校验存在性

这阶段可以让 `ActionExecutor` 主要消费 `ActionPlan`，减少运行时对原始 `ActionDef` 的依赖。

## 字段 accessor 设计建议

建议提供一个小而明确的接口：

```go
type FieldAccessor interface {
    Get(msg proto.Message) (any, error)
    Set(msg proto.Message, value any) error
    Path() string
}
```

加载期通过 proto descriptor 编译路径：

```go
CompileAccessor(desc protoreflect.MessageDescriptor, path string) (FieldAccessor, error)
```

运行时 accessor 不应再做：

- strings.Split
- 大小写兜底查找
- descriptor 全量扫描

如果字段不存在，应在编译期报错。

## 行为变化

预期行为不变，但错误暴露时机改变：

- 以前：机器人执行到对应 action 才报字段/类型错误。
- 以后：加载 flow / 构建任务时直接报错。

这是可接受且更好的行为，但前端校验后续应同步尽量提前提示。

## 风险点

- proto 字段名大小写和 json_name/name 的匹配规则必须与现有行为一致。
- repeated/map/nested 字段写入容易引入行为差异。
- BindingPlan 不应缓存运行时值。
- Lua action 不应强行纳入 plan，最多校验脚本存在。
- 不要为了兼容旧实现写 plan 失败 fallback 到旧解释执行路径。

## 验证方式

- 单元测试：字段 accessor、StorePlan、BindingPlan。
- 回归测试：现有 flow.json 能加载并执行。
- benchmark：对比 action 构建消息和 store 响应处理的耗时/分配。

建议 benchmark 指标：

```text
BenchmarkBuildRequestMessage
BenchmarkApplyStoreMappings
BenchmarkExecuteActionHotPath
```

关注：

- ns/op
- B/op
- allocs/op

## 建议结论

该优化是合理的，但应作为性能重构分阶段推进。建议先实现 StorePlan，确认收益和行为一致后，再推进 BindingPlan，最后才做完整 ActionPlan。
