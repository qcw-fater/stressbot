# Wait 节点 Then 后继设计

## 目标

让用户可以从 wait 节点右侧锚点直接连接一个普通节点。该连接保存为 `wait.then`，并在运行时表示“等待完成后执行这个节点”，而不是仅用于画布展示。

## 配置契约

`then` 是 wait 节点专用的可选字符串字段，值为后继节点 ID：

```json
{
  "type": "wait",
  "waitMs": 1000,
  "then": "next_action"
}
```

- 每个 wait 节点最多配置一个 `then`。
- 未配置或为空字符串时，wait 保持叶节点行为。
- `then` 只能引用 `nodes` 中存在的普通流程节点，不能指向监听卡片或当前 wait 节点自身。
- 旧配置不包含 `then`，加载和执行行为保持不变。
- `sequence.next` 继续使用字符串数组，不受该字段影响。

## 前端交互

wait 节点保留右侧 `out` 锚点。

1. 从 `out` 拖到普通节点时，将目标节点 ID 写入源 wait 的 `then`。
2. 已存在 `then` 时再次连线，新目标替换旧目标，画布上始终只有一条 then 边。
3. 删除 then 边时清空源 wait 的 `then`。
4. 删除目标节点时，所有指向该节点的 `then` 引用一并清空。
5. 只读模式继续禁止创建和删除连接。
6. 连接监听卡片或自身时拒绝更新，并显示中文提示。

JSON 转换层根据 `wait.then` 生成 `sourceHandle: "out"` 的派生边。边使用独立类型 `waitThen`，颜色继承 wait 节点主题色。导出时仅在 `then` 为非空字符串时写入 JSON。

## 后端执行语义

Go 端 `engine.Node` 增加 `Then string \`json:"then"\``，只在 wait 节点中使用。

执行 wait 节点时：

1. 按现有固定时长或随机时长规则计算等待时间。
2. 有有效等待时间时执行协作式休眠。
3. 休眠成功后，如果 `Then` 非空，则通过同一个执行器调用对应节点。
4. 未配置有效等待时间时维持现有“跳过等待”行为，但仍继续执行 `Then`；配置校验负责提示无效等待参数。
5. 上下文取消、休眠错误或后继节点错误原样向上传递，不继续执行其他后继。
6. 后继节点产生的 `break`、`continue`、`skip` 等控制信号保持现有传播规则。

这种语义允许 wait 独立作为入口或分支目标，也允许它位于 sequence 中。若 sequence 同时在 wait 后显式列出同一目标，目标会执行两次，这是配置本身的真实语义。

## 校验与可达性

前端校验增加以下行为：

- `then` 指向不存在节点时复用 `NODE_REF_NOT_FOUND`。
- `then` 指向自身时复用 `NODE_SELF_REF`。
- 可达性遍历将 `wait.then` 视为普通有向边。
- 当某个 sequence 的相邻两项为 `waitId, targetId`，且该 wait 的 `then` 也等于 `targetId` 时，报告 `WAIT_THEN_DUPLICATE_SEQUENCE_NEXT` 警告，提示目标将执行两次。

间接循环的处理保持与现有 sequence、boolean、switch 等引用一致，本次不引入新的全图循环限制。

## 代码边界

预计修改范围：

- `engine/flow.go`：新增 Go 字段。
- `engine/executor.go`：等待成功后执行 `Then`。
- `engine/*_test.go`：覆盖后继执行、取消和错误传播。
- `cmd/web/src/types/flow.ts`：同步 `then?: string`。
- `cmd/web/src/components/FlowEditor/FlowCanvas.tsx`：连接创建与边删除。
- `cmd/web/src/components/FlowEditor/codec/jsonToFlow.ts`：生成 then 边。
- `cmd/web/src/components/FlowEditor/codec/flowToJson.ts`：导出 then 字段。
- `cmd/web/src/components/FlowEditor/validation/refsCheck.ts`：引用、重复执行和可达性校验。
- `cmd/web/src/components/FlowEditor/store/flowStore.ts`：删除节点时清理 then 引用。
- 对应 Vitest 与 Go 测试文件。

不修改 API 层、数据库、任务调度、配置下载协议或其他节点的执行语义。

## 测试策略

前端测试覆盖：

- JSON 加载能生成 `waitThen` 边。
- JSON 导出保留合法 `then`，省略空值。
- 创建连接写入或替换 `then`，删除连接清空 `then`。
- 删除目标节点清空引用。
- 自引用、缺失引用和重复执行配置产生预期校验结果。
- 可达性通过 `then` 继续遍历后继。

后端测试覆盖：

- 固定等待完成后执行后继。
- 无有效等待时直接执行后继。
- 上下文在等待期间取消时不执行后继。
- 后继错误和控制信号向上传递。
- 未配置 `Then` 的 wait 行为保持不变。

完成后执行项目规定的 `go build ./...`、`npx tsc -b` 和 `npm run test`。涉及后端执行器变更，额外运行相关 Go 单元测试。

## 验收标准

- 用户从 wait 右侧连接普通节点后，连线立即出现并可保存、重新加载。
- 运行流程时，`then` 关系在等待结束后触发目标节点一次；父级 sequence 中额外列出的同一目标仍按其自身配置执行。
- 替换或删除连接后，运行时不再访问旧目标。
- 无 `then` 的历史 flow.json 无需迁移。
- 前后端字段名均为 `then`，没有 `nextNode` 等别名。
