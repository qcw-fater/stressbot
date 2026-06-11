# Proto Map Binding 设计

## 背景

当前声明式动作通过 `FieldBind` 解析字段值，再调用 `protox.Factory.SetField` 写入动态 protobuf 消息。现有实现支持标量、repeated、普通嵌套 message 等字段，但没有专门处理 proto `map<K,V>` 字段。

前端 proto 解析已经能识别 `protobuf.MapField`，`ProtoField` 也包含 `kind: 'map'`、`mapKey`、`mapValue`。但 bindings 的字段选择使用 `ProtoPathInput`，它只显示 `f.type`，导致 `map<int32,int32>` 这类字段在选择器里看起来像普通 `int32` value 类型。

目标是支持固定 key + 动态 value 的声明式 proto map 构造，优先覆盖以下 Go 代码等价配置：

```go
&pb.GuildEditInfoC2S{
    Params: map[int32]int32{
        1: utils.RandBoolToInteger[int32](),
        2: utils.RandBoolToInteger[int32](),
        3: utils.RandRangeNumber[int32](1, 200),
    },
}
```

## 目标

1. 新增声明式 binding 类型 `map`。
2. 支持 map entry 使用固定 key，value 复用现有动态 binding 能力。
3. 支持 `map<int32,int32>` 写入动态 protobuf 消息。
4. 修复前端 bindings 字段选择中 map 类型展示。
5. 新字段全链路一致，不做旧格式迁移或兼容兜底。

## 非目标

1. 不支持随机 key、state key 或 key binding。
2. 不设计重复 key 策略；固定 key 重复时按 Go map 行为后写覆盖先写。
3. 不引入旧格式自动迁移。
4. 不扩展复杂 OR/NOT 过滤语义。
5. 不要求本阶段完整支持所有 message value map；优先支持 protobuf 允许的标量 key 与标量/现有可转换 value。

## 配置格式

新增 binding 类型：

```json
{
  "field": "params",
  "type": "map",
  "entries": [
    { "key": 1, "value": { "type": "randomInt", "min": 0, "max": 1 } },
    { "key": 2, "value": { "type": "randomInt", "min": 0, "max": 1 } },
    { "key": 3, "value": { "type": "randomInt", "min": 1, "max": 200 } }
  ]
}
```

`entries[].key` 是固定值。`entries[].value` 是一个值表达式 binding，复用现有 binding 类型，例如 `fixed`、`state`、`stateFirst`、`stateRandom`、`randomPick`、`randomInt`、`randomBool` 等。嵌套 value binding 的 `field` 字段不参与 proto 赋值，只表达该 entry 的 value 如何生成。

## 后端数据模型

`engine/flow.go` 增加：

```go
const BindMap = "map"

type MapEntryBind struct {
    Key   any       `json:"key"`
    Value FieldBind `json:"value"`
}
```

`FieldBind` 增加：

```go
Entries []MapEntryBind `json:"entries"`
```

`isRequired` / 隐式必需判断保持现有语义。`map` 外层 binding 是否必需由外层 `required` / `optional` 控制；entry value 的缺失由 entry value 自己的 `required` / `optional` 控制。

## 后端执行流程

`ActionExecutor.resolveFieldValue()` 增加 `BindMap` 分支：

1. 创建 `map[any]any`。
2. 遍历 `fb.Entries`。
3. 对每个 entry 调用现有 `resolveFieldValue(&entry.Value)` 生成 value。
4. 若 value 为 nil：
   - `entry.Value.Optional == true` 时跳过该 entry；
   - `entry.Value.Required == true` 或 entry value 类型为隐式必需时，返回可识别的绑定错误；
   - 默认跳过该 entry，因为 proto map 不能表达 nil value。
5. 将 `entry.Key -> value` 写入结果 map。
6. 返回结果 map，由外层 `bindFields()` 调用 `Factory.SetField(msg, fb.Field, value)`。

为保留错误上下文，当前 `resolveFieldValue()` 只返回 `any` 的结构需要调整。推荐最小调整是新增内部 helper，例如：

```go
func (ae *ActionExecutor) resolveFieldValueStrict(fb *FieldBind, actionName, fieldName string) (any, error)
```

外层 `bindFields()` 使用 strict 版本，现有解析逻辑迁入 strict 版本，原有行为保持。这样 map entry value 的 required 错误可以带 action、field、entry key 等中文 detail。

## proto map 写入

`protox.Factory.setNestedField()` 在最后一级字段赋值时增加 map 分支：

```go
if field.IsMap() {
    return setMapField(ref, field, value)
}
```

`setMapField()` 负责：

1. 接受 `map[any]any` 和 `map[string]any`。
2. 写入前清空已有 map，保持与 repeated 字段 `setRepeatedField()` 一致的“整体替换”语义。
3. 用 `field.MapKey()` 将 key 转成 `protoreflect.MapKey`。
4. 用 `field.MapValue()` 将 value 转成 `protoreflect.Value`。
5. 对转换失败返回中文错误，包含字段名、key、目标类型。

key 转换覆盖 protobuf map 允许的 key 类型：`int32`、`int64`、`uint32`、`uint64`、`sint32`、`sint64`、`fixed32`、`fixed64`、`sfixed32`、`sfixed64`、`bool`、`string`。value 转换复用现有 `toFieldValue` / 标量转换逻辑。

## 前端类型与编辑器

`cmd/web/src/types/action.ts`：

- `BindingType` 增加 `'map'`。
- `ALL_BINDING_TYPES` 增加 `'map'`。
- 新增 `MapEntryBind`。
- `FieldBind` 增加 `entries?: MapEntryBind[]`。

`BindingTypeForm` 增加 `map` 表单：

- 显示 entries 列表。
- 每行包含：
  - key 输入：复用 `JsonDraftInput`，允许数字、布尔、字符串。
  - value type 选择：复用现有 binding type 选项，但禁止 value.type 为 `map`，避免本阶段引入递归 map 嵌套。
  - value 参数区：复用或抽取现有 `BindingTypeForm` 的值参数渲染逻辑，以“值表达式模式”隐藏 field、storeAs、condition、required/optional/wrap 等外层 proto 字段控件。
- 提供添加、删除 entry 操作。

`ProtoPathInput` 的类型显示改为识别 map：

```tsx
const typeLabel = f.kind === 'map' ? `map<${f.mapKey},${f.mapValue}>` : shortType(f.type);
```

bindings field 选择器中 `params` 应显示为 `map<int32,int32>`。

## JSON 清理与校验

`flowToJson.cleanFieldBind()` 递归保留并清理 `entries`：

- 外层 `type: 'map'` 保留 `entries`。
- 每个 entry 保留 `key` 与清理后的 `value` binding。
- value binding 不输出无意义的 `field`。

前端引用校验补充：

1. `type: 'map'` 必须包含至少一个 entry。
2. entry 必须有 `key`。
3. entry value 必须有合法 `type`。
4. entry value 的 `type` 不能为 `map`。
5. entry value 内部的 state source、path、filters 等继续走现有校验。

## 错误处理

后端错误信息使用中文：

- map 字段收到非 map 值：`字段 params 是 map，绑定值需要 map 类型`
- key 类型转换失败：`字段 params 的 map key=xxx 无法转换为 int32`
- value 类型转换失败：`字段 params 的 map value(key=1) 转换失败: ...`
- entry value 缺失且 required：`action=GuildEditInfo field=params mapKey=1 value 缺失`

map entry 默认 nil value 跳过，避免向 proto map 写入不可表达的 nil。

## 测试计划

后端：

1. 构造含 `map<int32,int32>` 的测试 proto，验证 `Factory.SetField` 能写入并 `GetField` / 反序列化读回。
2. 覆盖 `map<string,int32>` 或 `map<int64,string>`，验证 key 转换。
3. 覆盖错误 key 类型，断言中文错误包含字段名与目标类型。
4. 覆盖 `ActionExecutor` 的 `type: map`：固定 key + `randomInt` value 生成 map。
5. 覆盖 nil optional entry 被跳过，required entry 报错。

前端：

1. `ProtoPathInput` map 字段显示 `map<int32,int32>`。
2. `flowToJson` 导出保留 `entries` 且递归清理 value binding。
3. `BindingTypeForm` 可以添加、编辑、删除 map entries。
4. refs 校验能发现空 entries、缺失 key、缺失 value type。

项目验证：

```powershell
go build ./...
```

前端改动验证：

```powershell
npx tsc -b
npm run test
```

## 示例

`GuildEditInfoC2S` 的声明式动作示例：

```json
{
  "pattern": "tcpRequest",
  "service": "logic",
  "route": { "cmd": 1, "act": 1 },
  "c2sProto": "Game.GuildEditInfoC2S",
  "s2cProto": "Game.GuildEditInfoS2C",
  "bindings": [
    {
      "field": "params",
      "type": "map",
      "entries": [
        { "key": 1, "value": { "type": "randomInt", "min": 0, "max": 1 } },
        { "key": 2, "value": { "type": "randomInt", "min": 0, "max": 1 } },
        { "key": 3, "value": { "type": "randomInt", "min": 1, "max": 200 } }
      ]
    }
  ]
}
```

该配置等价于每次动作构造一个新的 `params` map，key 固定为 1、2、3，value 在每次执行时动态生成。
