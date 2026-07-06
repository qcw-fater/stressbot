# Switch 节点设计

## 背景

当前流程节点已经支持 `boolean` 二分支、`weighted` 随机分支、`sequence` 顺序执行和 `loop` 循环控制。多条件业务分流虽然可以用多个 `boolean` 节点串联实现，但当分支超过 2 个时，画布会出现大量中间判断节点，`flow.json` 也会变得啰嗦。

新增 `switch` 节点的目标是把“按多个条件选择一个后续流程”的场景收敛到一个节点中。它不引入新的条件 DSL，而是复用现有条件表达式能力。

## 目标

- 用一个节点表达 `if / else-if / else` 风格的多条件分支。
- 每个 case 是一条完整条件表达式，对应一个后续流程节点。
- 条件按顺序判断，命中第一个后停止继续判断。
- `defaultNext` 可选但推荐；未配置时编辑器给 warning，不阻断保存或运行。
- 复用现有 `state:` / `lua:` / `&&` / `||` / `!` / 括号条件表达式。

## 非目标

- 不提供独立的“按单一变量取值匹配”模式。
- 不支持 `fallthrough`。
- 不静态判断多个 case 条件是否互斥。
- 不做旧配置自动迁移或兼容兜底。

## JSON 模型

在 `Node` 上新增：

```json
{
  "type": "switch",
  "description": "按角色状态选择后续流程",
  "cases": [
    {
      "condition": "state:roleLevel >= 10",
      "next": "advanced_flow",
      "description": "高等级流程"
    },
    {
      "condition": "state:roleLevel >= 1",
      "next": "normal_flow",
      "description": "普通流程"
    }
  ],
  "defaultNext": "fallback_flow"
}
```

字段含义：

- `cases`: 按顺序匹配的分支列表。
- `cases[].condition`: 条件表达式，语法与现有 boolean/loop 条件一致。
- `cases[].next`: 条件命中后执行的节点 ID。
- `cases[].description`: 可选说明，仅用于编辑器展示和配置可读性。
- `defaultNext`: 可选默认分支；所有 case 都未命中时执行。

## 执行语义

`switch` 节点执行流程：

1. 进入节点后按数组顺序遍历 `cases`。
2. 对每个 case 调用现有条件判断能力。
3. 第一个返回 true 的 case 被选中。
4. 执行该 case 的 `next` 节点。
5. 后续 case 不再判断。
6. 如果没有 case 命中：
   - 配置了 `defaultNext`：执行默认分支。
   - 未配置 `defaultNext`：当前 switch 节点正常结束。

case 子流程返回 `errSkip`、`errBreak`、`errContinue`、上下文取消或普通错误时，沿用现有节点执行错误传播规则，不在 switch 内做特殊吞吐。

## B 方案覆盖方式

“按同一个变量取值分流”不需要独立模型，可以通过 A 方案表达：

```json
{
  "type": "switch",
  "cases": [
    { "condition": "state:errorCode eq 1001", "next": "handle_login_expired" },
    { "condition": "state:errorCode eq 2001", "next": "handle_resource_not_enough" }
  ],
  "defaultNext": "handle_unknown_error"
}
```

这样可以避免 `source` / `branches` / `cases` 两套结构并存，同时仍然支持范围判断、组合条件和 Lua 条件。

## 前端交互

FlowEditor 中新增 `switch` 节点类型：

- 左侧 1 个输入 handle。
- 右侧每个 case 1 个输出 handle。
- 右侧额外显示 `default` 输出行。
- case 行展示条件摘要、目标节点和可选描述。
- `defaultNext` 未配置时显示 warning 状态，但允许保存。

节点视觉示意：

```text
┌────────────────────────────┐
│ switch: choose_next         │
├────────────────────────────┤
│ 1. roleLevel >= 10      ───▶│
│ 2. roleLevel >= 1       ───▶│
│ default                 ───▶│
└────────────────────────────┘
```

编辑器表单提供：

- case 增删改和排序。
- 每个 case 的条件输入，复用现有 `ConditionInput`。
- 每个 case 的目标节点选择。
- 可选 case 说明。
- `defaultNext` 目标节点选择。

## 连线行为

- 从 case handle 拖线到节点时，写入对应 `cases[index].next`。
- 从 `default` handle 拖线到节点时，写入 `defaultNext`。
- 删除 case 时，同时删除该 case 的输出边。
- 调整 case 顺序时，handle 顺序和匹配顺序同步变化。
- 画布导入导出保持 `flow.json` 字段顺序和语义一致。

## 校验规则

新增校验：

- `cases` 为空：error。
- `cases[].condition` 为空：error。
- `cases[].condition` 语法错误：error。
- `cases[].next` 为空或不存在：error。
- `defaultNext` 填写但目标不存在：error。
- `defaultNext` 缺失：warning。

不校验：

- case 条件是否互斥。
- 多个 case 是否指向同一节点。
- default 分支是否一定可达。

## 后端影响

后端 engine 需要：

- 在节点类型常量中新增 `switch`。
- 在 `Node` 结构上新增 `Cases []SwitchCase` 和 `DefaultNext string`。
- 新增 `SwitchCase` 结构，包含 `Condition`、`Next`、`Description`。
- 在 `executeNode` 中分发到 `executeSwitch`。
- `executeSwitch` 复用现有条件求值能力，按顺序执行第一个命中分支。
- 更新 Go 单元测试，覆盖命中第一条、跳过后续、走 default、无 default 正常结束、条件错误和子节点错误传播。

## 前端影响

前端需要同步：

- 类型定义加入 `switch`、`cases`、`defaultNext`。
- codec 导入导出支持新字段。
- 节点面板新增 Switch 节点入口。
- 画布新增 SwitchNode 渲染。
- 节点编辑器新增 switch 表单。
- refs/validation/graph 相关逻辑识别 case next 和 defaultNext。
- 测试覆盖 JSON 转换、引用校验和基础编辑行为。

## 配置示例

```json
{
  "nodes": {
    "choose_guild_flow": {
      "type": "switch",
      "description": "按社团状态选择流程",
      "cases": [
        {
          "condition": "lua:is_guild_leader.lua",
          "next": "guild_leader_flow",
          "description": "会长流程"
        },
        {
          "condition": "lua:has_guild.lua",
          "next": "guild_member_flow",
          "description": "成员流程"
        }
      ],
      "defaultNext": "guild_join_flow"
    }
  }
}
```

## 验证计划

实现后按项目要求验证：

1. `go build ./...`。
2. `cd cmd/web && npx tsc -b`。
3. `cd cmd/web && npm run test`。
4. 在前端编辑器打开包含 switch 的 `flow.json`，确认校验报告只出现预期 warning/error。
5. 涉及后端执行后，运行 `go run ./cmd/agent -config conf/config.json` 做 2~5 分钟验证。
6. 审查日志，确认无异常错误或告警。

## 决策

采用 A 方案：`switch` 是顺序条件匹配节点。B 方案通过 A 方案的条件表达式自然覆盖，不新增独立配置模型。`defaultNext` 可选但推荐，缺失时前端提示 warning。