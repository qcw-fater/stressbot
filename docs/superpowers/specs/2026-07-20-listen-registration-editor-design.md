# 监听注册编辑器重设计

## 目标

让用户在 listen 节点中直接配置每条监听注册的目标连接、route 模板字段和队列容量，并与 action 节点的监听注册保持同一数据源和一致交互。

固定属性保持单行、紧凑、易扫读。动态 route 字段不再撑宽整张表或造成某一列独自换行，而是使用窄横向编辑轨道；复杂场景可打开细长的非模态浮动窗口。

## 配置契约

不修改后端结构和 `flow.json` 契约。队列容量继续属于具体 `ListenRef`：

```json
{
  "server": "tcp:logic",
  "route": { "cmd": 12, "act": 3 },
  "listen": "battle_push",
  "queueSize": 128
}
```

- `queueSize` 未写时运行时默认容量为 1。
- `queueSize` 显式写入时必须大于 0，沿用现有 `LISTEN_QUEUE_INVALID` 校验。
- 同一 listen 被多个 action 节点注册时，每条 `ListenRef` 独立保存连接、route 和队列容量。
- listen 与 action 编辑器都直接更新 `nodes[nodeId].listenRefs[refIndex]`，不复制业务数据。
- `ListenDef` 继续只描述 silent、declarative 或 lua 消费行为。

## 表格结构

action 节点的监听注册表格固定为：

1. 监听
2. 目标连接
3. route
4. 队列容量
5. 操作

listen 节点的反向引用表格固定为：

1. 动作节点
2. 目标连接
3. route
4. 队列容量
5. 操作

两张表保持单行结构和一致控件尺寸。route 列固定为 `150px`，仅占约两个常见字段的宽度。整表最小宽度为 `600px`；窗口更窄时才使用表格级横向滚动。

## route 横向编辑轨道

route 单元格由可滚动轨道和固定浮动窗口按钮组成：

- 轨道使用 `display: flex`、`flex-wrap: nowrap` 和 `overflow-x: auto`，字段永不换行。
- 浮动窗口按钮位于轨道右侧的固定区域，不参与滚动。
- 所有 route 单元格预留相同的细滚动条高度；即使某行无需滚动，行高也保持一致。
- 不截断或隐藏字段。用户通过鼠标、触控板或 `Shift + 滚轮` 横向访问后续字段。
- 不将普通纵向滚轮强制转换为横向滚动，避免破坏表格浏览习惯。
- 键盘焦点进入轨道外字段时，组件显式将该字段横向滚入最近的可视区域。

### 阅读态

字段默认显示为紧凑文本，例如：

```text
cmd=12   act=3   roomId=8401
```

字段名使用次级文本色，值使用主要文本色。默认不显示输入框边框，保证列表仍以阅读和比较为主。

### 原位编辑

- 点击具体字段值时，仅该字段切换为带字段名前缀的紧凑输入框。
- `Enter` 或失焦提交，`Escape` 撤销并恢复阅读态。
- 输入框宽度保持稳定；长值在输入框内部滚动，不能改变单元格宽度或表格行高。
- 每次只允许同一 route 轨道中的一个字段进入编辑态。
- 字段非法时保留编辑态，使用错误边框和 Tooltip 展示中文错误，不在表格内增加错误文本。
- 提交后直接更新对应 `ListenRef.route`，action 与 listen 面板同步重渲染。

## 细长浮动窗口

route 单元格右侧始终提供浮动打开按钮。浮动编辑器直接复用现有 `FloatingWindow`，不是 Modal：

- 无遮罩，底层 action/listen 编辑器保持可见、可滚动和可操作。
- 标题栏可拖动，窗口支持缩放、zIndex 聚焦和最顶层 `Escape` 关闭。
- 默认尺寸约 `560 x 112px`，最小尺寸约 `360 x 96px`。
- 标题显示监听名和目标连接，例如 `matchPoll · tcp:logic · route 模板字段`。
- 窗口体只有一行紧凑字段输入，不使用底部操作栏。
- 字段使用 `flex-wrap: nowrap` 横向排列；窗口宽度不足时仅窗口体横向滚动。
- 字段修改立即更新同一 `ListenRef`，不增加保存按钮。
- route 模板缺失或加载失败时，在窗口体显示紧凑错误状态和 Tooltip。

每个 action/listen 编辑面板只维护一个 route 浮动窗口。打开另一条注册时，窗口切换到新目标并置顶，避免为没有持久 ID 的 `ListenRef` 创建多个难以跟踪的窗口。

## 数据流与同步

action 表格根据当前数组下标生成新的 `listenRefs` 数组，再调用 `updateNode(nodeId, { listenRefs })`。

listen 编辑器通过 `buildRefsGraph` 获取 `{ nodeId, refIndex, ref }`，修改连接、route 或队列容量后，同样回写对应 action 节点的 `listenRefs` 数组。

两处订阅 Zustand 中的同一节点数据，因此所谓同步是单一数据源更新后的自然重渲染，不增加 effect、事件总线或双向复制逻辑。

浮动窗口目标使用 `{ nodeId, refIndex }` 定位。action 表格执行重排或删除时同步修正当前目标下标；目标行被删除时关闭窗口，避免写入错误引用。

## 监听模板

`ListenTemplateDefaultRef` 保留可选 `queueSize`：

- 保存监听模板时推断 `server`、`route` 和 `queueSize`。
- 多条引用任一字段不同时标记歧义；未写 `queueSize` 与显式写入 1 按运行时语义视为相同。
- 从模板创建 listen 节点并连接 action 时，将默认 `queueSize` 一并写入新 `ListenRef`。
- 历史模板没有 `queueSize` 时继续使用缺省容量 1，无需迁移本地数据。

## 错误与空状态

- 未选择目标连接时，route 轨道显示紧凑的“请先选择目标连接”。
- 协议配置加载中显示小型加载状态。
- 连接不存在、`routeKeyTemplate` 缺失或字段非法时，使用状态图标和 Tooltip，不撑高表格行。
- route 模板没有占位字段时显示“无需填写”。
- listen 没有引用时继续显示孤儿提示；没有 `ListenRef` 时不伪造队列配置。
- 批量导入继续接受 `ListenRef[]`，复用现有流程校验。

## 组件边界

- `TargetConnectionSelect` 继续作为 action/listen 表格共用的连接选择控件。
- `RouteEditor` 的纵向表单形态继续服务普通声明式 action，不全局改造。
- 新增共享的 route 轨道组件，负责阅读态、单字段编辑、横向滚动和浮动打开入口。
- 新增共享的细长 route 浮动编辑器，内部复用 route 字段解析和更新逻辑。
- `ListenRefsTable` 与 `BackrefList` 只负责各自第一列、操作列和 `ListenRef` 定位，不各自实现 route 交互。
- 不引入新的业务数据层，也不把引用关系迁移进 `ListenDef`。

## 测试策略

前端测试覆盖：

- listen 编辑器修改队列容量后，正确更新 action 节点的 `listenRefs[refIndex].queueSize`。
- action 与 listen 表格使用相同的 route 轨道和固定列结构。
- route 轨道保持单行、预留滚动条高度，并在多字段时产生横向溢出。
- 点击字段进入单字段编辑态；`Enter`、失焦和 `Escape` 分别提交或撤销。
- 非法字段值保留编辑态且不写入 route。
- 聚焦轨道外字段时将其滚入可视区域。
- 浮动按钮始终固定可见；打开后显示当前注册的全部字段。
- 浮动窗口编辑会更新同一 `ListenRef`，action/listen 两侧立即同步。
- 重排、删除活动注册时正确修正或关闭浮动窗口目标。
- 监听模板默认注册信息克隆、推断和画布连线时保留 `queueSize`。

完成后运行 `npx.cmd tsc -b`、`npm.cmd run test` 和 `go build ./...`。浏览器验证 action/listen 两种表格、单字段原位编辑、横向滚动、浮动窗口和双向同步。

## 验收标准

- 用户无需打开 action 节点即可在 listen 节点配置目标连接、route 和队列容量。
- action/listen 两张表的固定属性始终保持单行和一致行高。
- route 列约两个字段宽度，不再迫使节点编辑器显著变宽。
- 用户可以水平滑动查看全部 route 字段，并直接编辑当前字段。
- 用户可以打开细长、非模态、可拖拽和可缩放的 route 浮动窗口。
- 在任一入口修改后，另一编辑器立即显示同一值，保存和重载后数据不丢失。
- 历史 `flow.json` 和监听模板无需迁移，缺省队列容量仍为 1。

## 范围外

- 不修改网络监听队列、覆盖策略、注册冲突规则或 Go 运行时。
- 不把队列容量提升为 `ListenDef` 全局属性。
- 不改变一个 listen 被多个 action 节点引用的能力。
- 不重做动作编辑器其他折叠面板、错误处理或声明式 action 表单。
