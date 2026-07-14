# setState / clearState 节点编辑器优化设计

## 背景

`setState` 和 `clearState` 的后端语义已经明确：

- `setState` 通过 `FieldBind.field` 指定目标路径，调用 `State.SetPath`；目标已存在时覆盖，不存在时创建。
- `clearState` 通过 `ActionDef.keys` 指定一组顶层状态，逐项调用 `State.Delete`。

现有前端没有围绕这两个语义提供专用编辑体验。`setState` 直接复用面向 C2S 消息字段的通用 `BindingsTable`，目标状态仍以 `field` 和 Proto 字段输入的形式出现；`clearState` 使用可自由输入的 tags Select。与此同时，条件、binding 来源等界面已经通过状态注册表提供可搜索的已知状态列表，状态动作没有复用该能力，导致填写繁琐且容易写错。

## 目标

1. `setState` 的每条写入直观表达为“目标状态 + 取值方式 + 值”，目标状态可从已有状态中选择，也可输入新名称。
2. `clearState` 保留批量清除能力，只允许从已知状态中选择，不再以自由文本新增 key。
3. `clearState` 永远不允许清除内置状态 `id`、`index`、`account`，前端和后端共同执行该约束。
4. `setState` 保留现有全部 17 种 binding 取值能力，但把不常用参数收进高级区域。
5. 保持 `ActionDef.bindings` 和 `ActionDef.keys` 的 JSON 契约不变，不引入迁移或兼容字段。
6. 视觉和操作方式与其他 pattern 的 binding 编辑器保持一致。

## 选定方案

采用“摘要卡片”布局。

每条 `setState` 写入继续使用与现有 `BindingsTable` 相同的 Collapse 交互：折叠时显示摘要，展开后编辑完整配置，右侧保留上移、下移和删除操作。该方案与其他 action pattern 的编辑习惯一致，也能在当前 720×560 的节点编辑浮窗中保持足够清晰的层次。

未采用的方案：

- 行内快速编辑：操作最直接，但与其他 pattern 的折叠式 binding 编辑风格差异较大，复杂 binding 展开后也容易形成不一致的行高。
- 状态列表 + 详情面板：适合大量条目，但在现有节点编辑浮窗中占用过多横向空间。

## 组件边界

### SetStateEditor

新增 `SetStateEditor`，专门编辑 `ActionDef.bindings`：

- 不改变 `FieldBind` 或序列化结构。
- 复用现有 binding 类型定义、类型分组、`BindingTypeForm`、`StateKeyInput`、`StateExprInput` 和移动/删除逻辑。
- 不再让 `setState` 经过面向消息字段的通用 `BindingsTable`。
- 其他使用 `bindings` 的 pattern 继续使用原组件，不受本次改动影响。

### ClearStateEditor

新增 `ClearStateEditor`，专门编辑 `ActionDef.keys`：

- 使用状态注册表生成可搜索的多选项。
- 候选项展示 key、来源和已知类型。
- 继续输出有序且去重的 `string[]`。
- 不提供创建未知 key 的入口。

### 状态候选数据

状态候选继续以 `stateRegistry` 为单一来源，覆盖：

- 内置状态；
- action 响应存储；
- listen 推送存储；
- 启动配置；
- binding 中间值；
- 流程实际引用脚本中写入的状态；
- 嵌套 setter 推导出的顶层状态。

为避免新组件重复异步加载脚本和拼装展示信息，将当前 `StateKeyInput` 内部的候选加载流程抽成 `useStateKeyOptions(currentBindings?)` hook。该 hook 返回完整 `StateKeyInfo[]`；`StateKeyInput`、`SetStateEditor` 和 `ClearStateEditor` 使用同一接口，来源标签和类型显示继续由现有纯函数生成。

## setState 交互

### 折叠摘要

每条写入显示：

```text
目标状态    取值方式    值或来源摘要
```

例如：

```text
rankedMatchStarted    固定值      true
battleId              已有状态    matchInfo.id
```

摘要同时显示已启用的高级属性标签，例如 required、optional、wrap、storeAs 和 condition。摘要只负责展示，不改变原字段含义。

### 展开内容

展开后按以下顺序显示：

1. **目标状态**
   - 使用可搜索、可自由输入的状态输入框。
   - 选择已有状态时显示来源和已知类型。
   - 输入未出现在候选中的名称时显示“新状态”，明确表示运行时会创建该状态。
   - 支持 `State.SetPath` 已支持的嵌套路径，例如 `matchInfo.id`。
   - 输入 `id`、`index` 或 `account` 时属于覆盖已有内置状态，不标记为新状态；本次设计不禁止 setState 覆盖内置状态。

2. **取值方式**
   - 保留全部 17 种 binding type。
   - 下拉选项按用户任务分组，并将固定值、已有状态、随机整数、随机小数、随机布尔和随机字符串放在靠前位置。
   - 改变类型时仍使用既有 action pruning/字段清理规则，不保留不属于新类型的陈旧字段。

3. **值配置**
   - 固定值、状态来源和简单随机参数直接显示。
   - 复杂列表、map、过滤和排除规则继续复用 `BindingTypeForm` 的现有编辑能力。

4. **高级设置**
   - required、optional、wrap、storeAs、condition 以及类型相关的路径、过滤等非主流程配置默认收起。
   - 只要存在已配置的高级属性，折叠标题显示配置数量，摘要显示对应标签，避免配置被隐藏后不可察觉。

### 条目管理

- “添加状态”新增 `{ type: "fixed", field: "" }`。
- 保留上移、下移和删除。
- 多条 binding 写入同一目标路径是合法但可疑的配置；界面不自动合并或改写，而由校验报告明确提示后面的写入会覆盖前面的值。

## clearState 交互

- 使用可搜索多选框，已选 key 以标签展示，可逐项移除。
- 只能从状态注册表中的已知 key 新增选择。
- 状态候选显示来源和已知类型。
- `id`、`index`、`account` 在候选中保留可见性，但禁用，并标注“内置状态不可清除”。
- 选择结果保持用户选择顺序写入 `keys[]`，组件自动避免重复选择。
- 当状态注册表没有可清除项时显示空状态说明，不退化成自由输入。

### 已导入的未知 key

历史或手写 JSON 可能含有当前状态注册表无法识别的 key。编辑器必须：

- 原样保留该 key，不因打开或编辑节点而静默删除；
- 在已选区域标注“当前流程未识别”；
- 允许用户主动移除；
- 不把它加入可再次选择的候选列表；
- 在校验报告中给出 warning，但不阻止导出。

## 校验规则

### setState

- bindings 为空：保留现有 `SETSTATE_NO_BINDINGS` warning。
- `field` 为空：对 setState 使用明确的“目标状态为空”错误，不再以通用“field 和 storeAs 都为空”表达。
- 多条 binding 的非空 `field` 完全相同：warning，说明按数组顺序执行，后项会覆盖前项。
- binding 类型及必填参数继续由现有递归 binding 校验负责。
- 嵌套数组路径是否越界依赖运行时数据，不做无法保证准确的静态判断。

### clearState

- keys 为空：保留现有 `ACTION_NO_KEYS` error。
- keys 含重复项：warning；编辑器新建配置不会产生重复项，但导入 JSON 需要被识别。
- keys 含未知状态：warning，且不自动删除。
- keys 含 `id`、`index` 或 `account`：error。

前端校验只负责及时反馈，不能作为安全边界。手写 JSON 可以绕过界面，因此后端必须执行相同的内置状态保护。

## 后端保护与原子性

定义统一的不可清除内置状态集合：

```text
id / index / account
```

`execClearState` 在删除任何状态之前先扫描完整 `keys`：

1. 发现任一内置 key 时返回明确的配置类 `ActionError`；
2. 不执行任何删除；
3. 只有完整列表通过检查后，才逐项删除普通 key。

因此 `keys: ["battleId", "id"]` 必须整条动作失败，`battleId` 仍保留。该规则避免部分成功造成难以诊断的中间状态。

保护集合应位于可被验证逻辑和执行逻辑共同使用的明确边界，避免前后端或不同后端入口复制出不一致的魔法字符串。现有配置错误码段 41–50 中编号 50 尚未分配，因此新增专用框架错误码 `ErrStateConfig = 50`（注册名 `STATE_CONFIG`），用于状态动作的非法配置；不复用含义不符的 URL、HTTP 或 heartbeat 错误码。

## DeclarativeForm 接入

`DeclarativeForm` 的 pattern 分支调整为：

- `setState`：渲染 `SetStateEditor`，不再渲染通用 `BindingsTable`。
- `clearState`：渲染 `ClearStateEditor`，替换当前 tags Select。
- 其他 pattern：保持现有渲染路径。

pattern 顶栏、模板按钮、setState 预览、节点名称、描述、onError、delayMs 和监听注册均保持现状。

## 文案原则

用户可见文本使用状态动作语义，不暴露实现字段名：

- `bindings（State 写入绑定）` → `设置状态`；
- `field` → `目标状态`；
- `type` → `取值方式`；
- `keys（要清除的 state key 列表）` → `选择要清除的状态`；
- `storeAs` 等确需保留的高级术语放在高级区域并附中文说明。

UI 中继续使用“状态”，不使用 StateStore 等实现术语。

## 测试与验收

### 前端组件测试

覆盖：

1. setState 从候选中选择已有状态。
2. setState 输入不存在的状态并标记为新状态。
3. setState 输入内置状态时识别为已有状态。
4. 折叠摘要显示目标状态、取值方式和值/来源。
5. 全部 17 种 binding type 仍可选择并进入对应编辑器。
6. 已配置高级属性时摘要标签和高级配置数量正确。
7. 条目上移、下移和删除保持 bindings 顺序。
8. clearState 搜索、多选、移除和选择顺序正确。
9. clearState 的 `id`、`index`、`account` 可见但不可选择。
10. 已导入未知 key 被保留并可主动移除。
11. 无可清除候选时显示说明且不能自由输入。

### 前端校验测试

覆盖：

1. setState 空目标状态。
2. setState 重复目标状态。
3. clearState 空 keys。
4. clearState 重复 key。
5. clearState 未知 key。
6. clearState 分别包含 `id`、`index`、`account`。

### 后端测试

覆盖：

1. 普通状态可单个和批量清除。
2. `id`、`index`、`account` 分别被拒绝。
3. 合法 key 与内置 key 混合时不删除任何状态。
4. 拒绝时返回结构化配置类错误。
5. 重复普通 key 不导致异常，执行结果与删除一次相同。

### 验证流程

完成实现后执行：

1. `go build ./...`
2. `cd cmd/web && npx tsc -b`
3. 后端相关 Go 单元测试。
4. `cd cmd/web && npm run test`
5. 在 FlowEditor 中分别打开 setState 和 clearState 节点，确认 720×560 默认窗口与缩窄窗口下无横向溢出。
6. 导入含未知 key、重复 key、内置 key 的配置，确认编辑器不静默改写且校验报告符合上述规则。
7. 运行含 clearState 的最小流程，确认普通清除成功、内置状态保护具有原子性。

## 明确不做

- 不改变 `ActionDef.bindings`、`FieldBind` 或 `ActionDef.keys` 的 JSON 结构。
- 不迁移或自动修复旧配置。
- 不修改其他 pattern 的通用 `BindingsTable` 交互。
- 不减少 setState 可用的 binding 类型。
- 不增加运行时状态实时浏览器。
- 不让 clearState 自由输入未知 key。
- 不禁止 setState 覆盖内置状态。
- 不对运行时数组路径越界做不可靠的静态推断。
