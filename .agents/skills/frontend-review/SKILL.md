---
name: frontend-review
description: Use when 审查 stressbot 前端 .tsx/.ts/.css、FlowEditor、onError/listenRefs/listens、antd v5、API 集中化、类型安全、状态管理、主题样式或前后端字段对齐相关变更时。
---

# stressbot 前端代码审查技能

## 技术栈

- **UI 框架**：React + TypeScript + antd v5
- **状态管理**：Zustand v5（9 个 store）
- **画布**：@xyflow/react v12
- **代码编辑器**：Monaco Editor
- **图表**：ECharts v6
- **浮动窗口**：react-rnd
- **构建**：Vite

---

## 审查原则

- **只读权限，禁止修改代码**
- **按严重程度分级**：🔴 必须修复 / 🟡 建议修复 / 🔵 可选优化
- **每条意见给出具体文件和行号**
- **跨 skill 协作**：若项目中存在前端设计相关 skill（如 `frontend-design`），应在审查时并行调用，同步检查 UI 布局、交互设计、视觉效果是否合理，发现可优化项一并纳入审查报告的 🟡/🔵 条目

---

## 一、antd v5 合规

### 1.1 废弃 API 禁用清单

以下 antd API 已在 v5 中废弃，禁止使用：

| 废弃 API | 替代方案 |
|----------|---------|
| `addonBefore` / `addonAfter` | `Space.Compact` + `<span>` 标签 |
| `destroyOnClose` | `destroyOnHidden` |
| `bordered`（Table/Descriptions/Card） | 移除该 prop 或使用 CSS 控制 |
| `strokeWidth`（Progress） | 使用 `size` prop |
| `visible`（Modal/Drawer/Popover） | `open` |

### 1.2 组件使用规范

| 检查点 | 说明 |
|--------|------|
| Tooltip 统一 | 禁止 HTML 元素的 `title=` 属性，统一用 antd `<Tooltip>`，深色主题自动跟随 |
| Modal/Drawer 关闭 | 用 `destroyOnHidden`（非 `destroyOnClose`） |
| 表单输入单位 | 不用 `addonBefore/After`，用 `Space.Compact` + `<span>` 包裹 Input + 单位标签 |
| 图标导入 | 从 `@ant-design/icons` 按需导入，不使用 `Icon` 通用组件 |

---

## 二、UI 文本规范

### 2.1 用户可见文本

| 规则 | 说明 |
|------|------|
| 不暴露技术术语 | "Agent" → "节点"，"Admin" → "服务器"，"IDB" → "本地存储"，"Lua" 技术术语在用户可见文本中避免 |
| 中文为主 | 面板标题、按钮文字、提示信息用中文；代码/命令/文件名保持英文原文 |
| 配置字段名保留英文 | flow.json 字段名（如 `onError.strategy`、`loopCount`、`listenRefs`、`listens`）在 UI 中保留英文原文，括号内附中文说明 |

### 2.2 提示与描述

| 检查点 | 说明 |
|--------|------|
| Tooltip 延迟 | 非关键提示加 `mouseEnterDelay={0.4}` 避免频繁弹出 |
| Alert 描述 | Alert 的 message 简短，详细说明放 description |
| 占位符 | Input placeholder 应给出具体示例，如 "如 192.168.1.1:8080 或 state:battleAddr" |

---

## 三、zIndex 与浮动窗口

### 3.1 浮动窗口层级

`floatingWindowStore` 管理单调递增的 `_nextZ` 计数器（起始 1000）。

| 检查点 | 说明 |
|--------|------|
| 窗口打开/聚焦 | 必须调用 `focusWindow(windowId)` 以获取最新 zIndex |
| ESC 关闭 | FloatingWindow 已内置 ESC 关闭逻辑（仅最顶层窗口响应） |
| 拖拽事件 | `onDragStart` 和 `onMouseDown` 中调用 `e.stopPropagation()` 防止嵌套窗口抢焦点 |

### 3.2 弹出层 zIndex

FloatingWindow 内的 Modal/Popover/Select 下拉等弹出层 zIndex 必须高于窗口基线：

```tsx
// 正确模式
const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
// Modal
<Modal styles={{ mask: { zIndex: popupZ }, wrapper: { zIndex: popupZ + 1 } }} />
// Popover
<Popover overlayStyle={{ zIndex: popupZ + 10 }} />
```

| 检查点 | 说明 |
|--------|------|
| Modal zIndex | 必须通过 `styles.mask.zIndex` 和 `styles.wrapper.zIndex` 设置，不依赖默认值 |
| Popover/Select | 必须设置 `overlayStyle.zIndex` 或 `popupStyle.zIndex` |
| 顶层 ConfigProvider | FlowEditor 入口的 `<ConfigProvider>` 已设置 `zIndexPopup: nextZ + 100` |

---

## 四、API 集中化

### 4.1 禁止组件直接调用 fetch

| 检查点 | 说明 |
|--------|------|
| 组件内 fetch | 禁止。所有 HTTP 请求收拢到 `services/` 层 |
| 域 API 模块 | `agentsApi.ts`、`tasksApi.ts`、`metricsApi.ts`、`logsApi.ts`、`historyApi.ts` 等 |
| 基线 API | `baselineApi.ts` 负责 conf/ 资源读取（允许直接 fetch） |
| 核心 wrapper | `api.ts` 提供 `getJson`/`postJson`/`putJson`/`del`/`postMultipart`/`getText` |

### 4.2 错误处理

| 检查点 | 说明 |
|--------|------|
| 统一错误 | `api.ts` 的 `request()` 统一抛 `ApiError`（含 code/status/details） |
| 组件层 | 用 `AntApp.useApp()` 的 `message.error()` 展示，不自行 alert |
| 网络错误 | `NETWORK_ERROR` 由 `api.ts` 统一包装，组件不单独判断 |

---

## 五、类型安全与后端对齐

### 5.1 类型定义位置

| 类型 | 文件 | 说明 |
|------|------|------|
| FlowNode / TaskFlow | `types/flow.ts` | 镜像 Go `engine/flow.go` |
| ActionDef / FieldBind / BindingType | `types/action.ts` | 镜像 Go `engine/action.go` |
| ListenDef | `types/listen.ts` | 对应 Go `ListenDef`（前端重命名为 Listen） |

### 5.2 对齐检查

| 检查点 | 说明 |
|--------|------|
| JSON tag 一致 | 前端 type 字段名必须与 Go 的 json tag 完全一致 |
| 联合类型 | `ActionPattern`、`BindingType`、`NodeType` 等联合类型必须与 Go 常量一致 |
| 可选字段 | Go `omitempty` 的字段在前端对应 `?:` 可选 |
| 前端扩展字段 | `description` 等 UI-only 字段需确认 Go 端能忽略；`listenRefs[].listen` 是 Go/TS 对齐字段，不是前端扩展；禁止引入 `callback` / `listenCallbacks` 别名 |
| FilterDef 运算符 | Go 只支持 `eq`/`neq`/`gt`/`gte`/`lt`/`lte`/`contains`/`in`/`notNil`/`isNil`；前端 spec 不得声明 Go 未实现的运算符 |

### 5.3 编解码层

`codec/flowToJson.ts` + `codec/jsonToFlow.ts` 负责 JSON ↔ 内部模型的转换：

| 检查点 | 说明 |
|--------|------|
| 字段命名 | `flowToJson.ts` + `jsonToFlow.ts` 应保持 `listens`、`listenRefs`、`onError` 原名透传/清理，不做 `callbacks` / `listenCallbacks` 兼容映射 |
| 空值处理 | 空字符串/0/空数组不导出到 JSON（对齐 Go omitempty） |
| 循环引用 | 避免在 codec 层引入 store 依赖 |

---

## 六、状态管理

### 6.1 Store 职责

| Store | 职责 |
|-------|------|
| `flowStore` | 核心业务数据：TaskFlow + React Flow 可视化状态 + 校验 |
| `editorStore` | 编辑器 UI 状态：面板、选中、主题、调试 |
| `floatingWindowStore` | 浮动窗口生命周期：开关、焦点、位置、zIndex |
| `runtimeStore` | 运行时状态机 + 轮询数据 + agent 列表 |
| `protoStore` | Proto 加载状态机 |
| `resourcesStore` | IndexedDB 资源管理 + 基线同步 |

### 6.2 Store 使用规范

| 检查点 | 说明 |
|--------|------|
| 选择器性能 | 用 `useShallow` 避免对象引用变化导致不必要的重渲染 |
| 最小订阅 | 只订阅需要的字段，不订阅整个 store |
| 派生状态 | 能从已有 state 计算的不另存（用 `useMemo`） |
| 副作用 | 状态变更后的副作用（网络请求、IDB 写入）放在调用方，不放在 store 的 set 里 |
| 持久化 | 需跨 session 保持的状态用 `persist` middleware 或直接操作 IDB |

---

## 七、主题与样式

### 7.1 CSS 变量

所有颜色/间距/阴影必须通过 CSS 变量引用（`tokens.css`），禁止硬编码。

**常用变量**：

| 类别 | 变量 |
|------|------|
| 文字 | `--text-primary` / `--text-secondary` / `--text-tertiary` |
| 背景 | `--bg-canvas` / `--bg-panel` / `--bg-elevated` / `--container-bg` |
| 边框 | `--border-color` |
| 节点色 | `--node-{type}` / `--node-{type}-bg` / `--node-{type}-border-active` |
| 语义色 | `--color-success` / `--color-error` / `--color-warning` / `--color-blue` / `--color-purple` / `--color-orange` |
| 间距 | `--space-xs`(4px) / `--space-sm`(8px) / `--space-md`(12px) / `--space-lg`(16px) / `--space-xl`(24px) |

### 7.2 暗色主题

| 检查点 | 说明 |
|--------|------|
| 变量覆盖 | 暗色主题通过 `[data-theme='dark']` 覆盖变量，组件不应感知当前主题 |
| antd 主题 | FlowEditor 入口 ConfigProvider 统一配置 antd 暗色 token |
| Monaco 主题 | `theme === 'dark' ? 'vs-dark' : 'light'` |

### 7.3 行内样式规范

| 检查点 | 说明 |
|--------|------|
| 颜色值 | 禁止硬编码 `#xxx`/`rgb()`/`rgba()`，用 CSS 变量 |
| 例外 | `borderRadius`、`width`、`height` 等非主题属性可以硬编码 |
| 复杂样式 | 超过 3 个属性或需要伪类/媒体查询的样式应抽到 .css 文件 |

---

## 八、组件模式

### 8.1 文件组织

| 规则 | 说明 |
|------|------|
| 功能目录 | 每个功能模块独立目录：`editors/ActionEditor/`、`listens/`、`panels/` |
| 就近原则 | 相关的 store、样式、测试放在同一目录 |
| 共享组件 | 跨模块复用的放 `shared/` 子目录 |
| 默认导出 | 组件用 `export function`，不用 `export default` |

### 8.2 组件设计

| 检查点 | 说明 |
|--------|------|
| Props 类型 | 必须定义 `interface XxxProps`，不用 inline `{ }: { ... }` |
| 回调命名 | `onChange` / `onXxx`，不用 `handleXxx` 作为 prop 名 |
| 受控模式 | 表单组件始终受控（`value` + `onChange`） |
| Fragment | 无需额外 DOM 包裹时用 `<>...</>` |
| key prop | 列表渲染必须用稳定唯一 key，禁止用 index |

### 8.3 React 性能

| 检查点 | 说明 |
|--------|------|
| memo | 纯展示组件用 `React.memo`；频繁更新的表单组件不用 |
| useMemo/useCallback | 仅在计算成本高或传递给 memo 子组件时使用 |
| useEffect 依赖 | 必须声明完整依赖数组；空数组 `[]` 仅用于首次加载 |
| 状态提升 | 多个子组件共享的状态提升到最近公共父组件 |

---

## 九、FlowEditor 特有规则

### 9.1 画布节点

| 检查点 | 说明 |
|--------|------|
| 节点类型 | 8 种：sequence / action / loop / boolean / weighted / wait / break / continue |
| 节点组件 | `nodes/` 目录，统一用 `NodeShell` 包裹 |
| 节点 badge | 状态标记（abort/skip/listen/preview）用 CSS class + 颜色区分 |
| Handle | action 节点左入右出（右侧仅 listen 语义） |

### 9.2 编辑器面板

| 检查点 | 说明 |
|--------|------|
| 节点编辑 | `NodeEditorDrawer` 按节点类型分发到对应 Editor |
| Action 编辑 | `ActionEditor` → `DeclarativeForm` 或 `LuaScriptField` |
| 节点级字段 | `onError`（含 `strategy` / `ignoreCodes` / `handler` / `retry`）、`listenRefs`、`delayMs` 放在 Collapse 折叠面板中；`optional` 是 binding 字段，不是节点/action 字段 |
| Bindings/Store | 用 `Collapse` 风格的列表编辑器，标题含数量 "(N)" |

### 9.3 校验

| 检查点 | 说明 |
|--------|------|
| 实时校验 | `validation/refsCheck.ts` 在 store 变化时自动执行 |
| 校验覆盖 | 节点引用完整性、动作存在性、`onError.strategy` 合法值、`listenRefs` 引用/queueSize/server、service 非空 |
| 新增字段同步 | 后端新增字段时，`types/` + `refsCheck.ts` + 对应编辑器组件须同步更新 |

---

## 十、第三方库 API 合规

审查时若不确定某个 API 是否为最新用法，应查阅对应库的官方文档。

### 10.1 @xyflow/react v12

| 废弃 / 旧模式 | 替代方案 |
|----------|---------|
| `reactflow` 包名 | `@xyflow/react` |
| `node.width` / `node.height`（实测尺寸） | `node.measured.width` / `node.measured.height` |
| `onEdgeUpdate` | `onReconnect` |
| `parentNode` | `parentId` |
| `xPos` / `yPos` | `positionAbsoluteX` / `positionAbsoluteY` |
| `getTransformForBounds` | `getViewportForBounds` |
| `project()` | `screenToFlowPosition()` |
| `nodeInternals` | `nodeLookup` |

| 检查点 | 说明 |
|--------|------|
| 不可变更新 | 节点/边的变更必须创建新对象，禁止原地 mutate |
| Handle 连接 | 用 `isValidConnection` prop 或 `useHandleConnections` hook 验证连接合法性 |
| 暗色模式 | 使用 `colorMode="dark"` 或 `colorMode="inherit"`，不自行处理背景色 |
| 自定义节点 | 通过 `nodeTypes` 注册，组件接收 `NodeProps`；用 `useNodesData` 访问相邻节点数据 |

### 10.2 Zustand v5

| 废弃 / 旧模式 | 替代方案 |
|----------|---------|
| `import shallow from 'zustand/shallow'` | `import { useShallow } from 'zustand/react/shallow'` |
| `create<State>((set) => ...)` 无双括号 | `create<State>()((set) => ...)` 柯里化形式 |
| `createContext` from core | `import { createContext } from 'zustand/context'` |
| `default export` | 全部使用 named import |

| 检查点 | 说明 |
|--------|------|
| 选择器 | 对象选择器必须用 `useShallow` 避免引用变化导致无限重渲染 |
| 最小订阅 | 只订阅需要的字段，不订阅整个 store |
| 派生状态 | 能从已有 state 计算的不另存（用 `useMemo`） |
| 副作用 | 网络请求、IDB 写入等放在调用方，不放在 store 的 `set` 里 |

### 10.3 @monaco-editor/react v4

| 检查点 | 说明 |
|--------|------|
| Worker 配置 | Vite 项目必须配置 `self.MonacoEnvironment.getWorker` + `loader.config({ monaco })`，否则控制台报错 |
| 主题 | `beforeMount` 中调用 `monaco.editor.defineTheme()`，或直接传 `theme` prop（`vs-dark` / `light`） |
| 生命周期 | `onMount(editor, monaco)` 获取 editor 实例引用；`useMonaco()` 首次渲染返回 `null`，必须 null-check |
| 多模型 | `path` prop 区分模型；`defaultValue`/`defaultLanguage` 仅首次创建生效，后续更新用 `value`/`language` |
| 内存泄漏 | 组件卸载时模型自动 dispose（除非 `keepCurrentModel={true}`）；长生命周期组件注意手动 dispose |

### 10.4 ECharts v6

| 检查点 | 说明 |
|--------|------|
| 初始化 | `echarts.init(dom, theme?, opts?)`，必须在容器有尺寸后调用 |
| 销毁 | 组件卸载时必须调用 `chart.dispose()` 防止内存泄漏 |
| 响应式 | 容器尺寸变化时调用 `chart.resize()`（`useEffect` cleanup 或 `ResizeObserver`） |
| 主题 | 暗色模式下通过 `registerTheme` 注册自定义暗色主题，或传递暗色 option |
| 空数据 | 数据为空时显示占位文字或空状态，不渲染空白画布 |

### 10.5 react-rnd v10

| 废弃 / 旧模式 | 替代方案 |
|----------|---------|
| `z` prop（控制层级） | `style={{ zIndex: N }}` |
| 旧版回调签名 | `onResizeStop(e, direction, ref, delta, position)` |

| 检查点 | 说明 |
|--------|------|
| 受控 vs 非受控 | 浮动窗口用受控模式（`size` + `position` props + `onDragStop`/`onResizeStop`） |
| 层级管理 | z-index 通过 `style.zIndex` 设置，由 `floatingWindowStore` 统一管理 |
| 边界约束 | `bounds="parent"` 或指定选择器，防止窗口拖出可视区域 |
| 拖拽焦点 | `onDragStart` / `onMouseDown` 中 `e.stopPropagation()` 防止嵌套窗口抢焦点 |

---

## 十一、代码复用性与组件拆分

### 11.1 拆分信号

以下信号表明组件需要拆分：

| 信号 | 说明 |
|------|------|
| 文件超过 300 行 | 通常意味着组件承担了过多职责 |
| 多个 `useEffect` | 副作用逻辑可能应提取为自定义 hook |
| 深层嵌套 JSX | 3 层以上嵌套表明应提取子组件 |
| 重复 UI 模式 | 相同的"标题 + 列表 + 操作按钮"结构出现在多处 |
| Props 过多 | 超过 6 个 props 通常意味着组件做了太多事 |
| 同一目录下多个相似组件 | 可能需要统一接口或提取共享基础组件 |

### 11.2 拆分策略

| 场景 | 策略 |
|------|------|
| 重复的 UI 片段 | 提取为 `shared/` 目录下的共享组件，定义清晰的 Props 接口 |
| 重复的状态逻辑 | 提取为自定义 hook（如 `useFormState`、`useAsyncLoad`） |
| 大型表单 | 按区块拆分为子表单组件，父组件协调数据 |
| Modal + 触发按钮 | 提取为独立组件，通过 `open`/`onClose` 控制 |
| 列表 + 列表项 | 列表项提取为独立组件（尤其含复杂交互时） |
| 工具函数 | 放入 `utils/` 或相关模块的 `helpers.ts`，不留在组件内 |

### 11.3 拆分原则

| 原则 | 说明 |
|------|------|
| 单一职责 | 每个组件/hook 只做一件事 |
| 就近放置 | 只在一个模块内使用的组件放在该模块目录下；跨模块使用的放 `shared/` |
| 接口稳定 | 提取共享组件时设计稳定的 Props 接口，避免频繁改动传播 |
| 不过度抽象 | 三行相似代码不需要抽象；只有确认会重复使用时才提取 |
| 向上提取 | 当多个兄弟组件共享逻辑时，提取到最近公共父组件或 hook，不立即引入 context |

### 11.4 审查检查项

| 检查点 | 说明 |
|--------|------|
| 重复代码 | 两处以上相同的 UI 逻辑（尤其是表单、Modal、列表操作），应提取共享组件 |
| 大组件 | 单文件 > 300 行的组件，评估是否可按职责拆分 |
| 内联逻辑 | 组件内超过 10 行的数据处理/格式化逻辑，应提取为独立函数或 hook |
| 硬编码列表 | 重复的选项列表（如 `onError.strategy` 选项）应提取为常量或配置 |
| 紧耦合 | 组件直接依赖另一个组件内部实现细节，应通过 Props 解耦 |

---

## 十二、前后端接口审查

当变更涉及前后端数据交互时（新增/修改 API 调用、新增表单字段、修改类型定义），必须逐字段审查接口一致性。

### 12.1 请求字段审查

| 检查点 | 说明 |
|--------|------|
| 字段覆盖 | 前端发送的每个字段，后端对应的 handler 必须有读取逻辑；后端期望的必填字段，前端必须提供 |
| 多余字段 | 前端发送但后端不读取的字段应删除；后端已删除但前端仍发送的也应清理 |
| 缺失字段 | 后端新增了字段但前端未提供输入方式的，需补充 UI 控件或确认有合理默认值 |
| 字段命名 | `FormData.append(key)` 的 key 必须与后端 `r.FormValue(key)` / `r.MultipartField(key)` 完全一致 |

### 12.2 响应字段审查

| 检查点 | 说明 |
|--------|------|
| 消费完整性 | 后端返回的每个 JSON 字段，前端是否有对应的消费逻辑；未被使用的响应字段确认是冗余还是遗漏 |
| 类型匹配 | 前端 TypeScript 类型定义必须与后端 Go struct 的 json tag 完全一致（含 omitempty） |
| 枚举对齐 | 后端新增的枚举值（如 TaskState、ActionPattern），前端的联合类型必须同步更新 |

### 12.3 格式与编码审查

| 检查点 | 说明 |
|--------|------|
| 时间格式 | 前端序列化格式（如 `dayjs.toISOString()`）必须与后端解析格式（如 `time.Parse(time.RFC3339, ...)`）兼容 |
| 数值精度 | 前端 `InputNumber` 的 min/max 范围应与后端验证一致；浮点字段注意精度丢失 |
| 空值语义 | `null` / `undefined` / `""` / `0` 在前后端是否有不同的语义；Go `omitempty` 对零值的行为 |
| 枚举值传递 | 前端 Select 选项的 value 必须与后端期望的字符串完全一致 |

### 12.4 审查方法

1. 找到前端调用点（`services/` 层的 API 函数），追踪所有 `FormData.append` / JSON body 字段
2. 找到后端 handler（`admin/handlers.go` 或 `agent/http_server.go`），追踪所有 `r.FormValue` / JSON unmarshal 字段
3. 逐字段对照：名称、类型、是否必填、默认值、格式
4. 对类型定义文件（`types/*.ts` ↔ Go struct）做同样的对照

---

## 十三、注释审查

注释和代码必须同步维护。过时注释比没注释更有害——它让读者以为代码做了某件事，实际做的可能完全不同。

### 13.1 必须删除的注释

| 类型 | 说明 | 示例 |
|------|------|------|
| 过时注释 | 代码已改但注释没更新，描述的行为与实际不符 | 注释写"5s 轮询"但实际已改为 10s |
| 多余注释 | 重述代码本身已经表达的逻辑 | `count++; // count 加 1`、`const name = 'test'; // 设置名称为 test` |
| 变更遗留 | 提交信息、TODO 已完成、临时调试标记 | `// TODO: 后续优化`（已优化完但注释还在）、`// console.log(...)` |
| 被注释掉的代码 | 大段被注释掉的旧逻辑，git 历史已保留 | `// const oldMethod = () => { ... }` |
| 文件头注释与实际不符 | 文件顶部描述的职责与当前内容不匹配 | 文件头写"日志面板"但实际已改为通用监控面板 |

### 13.2 需要补充注释的地方

| 场景 | 说明 |
|------|------|
| 非显而易见的约束 | 魔数、特殊阈值、硬编码的业务规则（如 `5 * 1024 * 1024` 需注释"超过 5 MB 跳过"） |
| 不直观的 workaround | 为规避某个库 bug 或浏览器兼容问题而写的特殊处理，需注释原因和关联 issue |
| 复杂的算法/条件 | 多层嵌套的三元表达式、复杂的位运算、反向逻辑（`!== false` 等） |
| 副作用说明 | 某段代码改变了外部状态、触发了副作用，且调用方不一定能直观看出 |
| 临时方案 | 不得已的临时方案或已知缺陷，需注释原因和预期修复方式 |

### 13.3 审查检查项

| 检查点 | 说明 |
|--------|------|
| 注释与代码一致 | 逐条检查注释描述的行为是否与下方代码完全匹配 |
| 无冗余注释 | 注释不应该只是用自然语言复述代码，应解释"为什么"而非"是什么" |
| 无遗留标记 | `// HACK`、`// FIXME`、`// TODO` 等标记需确认是否已处理；已处理的删除 |
| 无注释掉的代码 | 用 git 管理历史，不要用注释保留旧代码 |
| 关键逻辑有注释 | 复杂分支、魔数、workaround、性能优化相关的代码应有注释说明意图 |

---

## 十四、审查报告模板

```
## Frontend Review: <变更描述>

### 概要
- 变更范围：<文件数> 个文件
- 涉及模块：<FlowEditor / monitoring / runtime / services / types>
- 风险等级：<低/中/高>

### 一、antd 合规
- <是否有废弃 API？是否有原生 tooltip？>

### 二、UI 文本
- <是否暴露了技术术语？提示是否清晰？>

### 三、zIndex / 浮动窗口
- <弹出层 zIndex 是否正确？是否遗漏 popupStyle？>

### 四、API / 类型
- <是否有组件直接 fetch？类型是否与后端对齐？>

### 五、前后端接口
- <请求字段是否全覆盖？响应字段是否全消费？时间/枚举格式是否匹配？>

### 六、样式
- <是否硬编码颜色？是否使用 CSS 变量？>

### 七、注释
- <是否有过时/多余注释？复杂逻辑是否缺少注释？>

### 八、🔴 必须修复
1. [文件:行号] 问题 → 建议方式

### 九、🟡 建议修复
1. [文件:行号] 问题 → 建议方式

### 十、🔵 可选优化
1. [文件:行号] 建议
```

```
## Frontend Review: <变更描述>

### 概要
- 变更范围：<文件数> 个文件
- 涉及模块：<FlowEditor / monitoring / runtime / services / types>
- 风险等级：<低/中/高>

### 一、antd 合规
- <是否有废弃 API？是否有原生 tooltip？>

### 二、UI 文本
- <是否暴露了技术术语？提示是否清晰？>

### 三、zIndex / 浮动窗口
- <弹出层 zIndex 是否正确？是否遗漏 popupStyle？>

### 四、API / 类型
- <是否有组件直接 fetch？类型是否与后端对齐？>

### 五、前后端接口
- <请求字段是否全覆盖？响应字段是否全消费？时间/枚举格式是否匹配？>

### 六、样式
- <是否硬编码颜色？是否使用 CSS 变量？>

### 七、🔴 必须修复
1. [文件:行号] 问题 → 建议方式

### 八、🟡 建议修复
1. [文件:行号] 问题 → 建议方式

### 九、🔵 可选优化
1. [文件:行号] 建议
```

---

## 快速审查清单

1. **废弃 API**：`addonBefore/After` / `destroyOnClose` / `bordered` / `strokeWidth` / `visible` → 零容忍
2. **原生 tooltip**：HTML `title=` → 替换为 antd `<Tooltip>`
3. **UI 文本**：不暴露 Agent/Admin/IDB 等技术术语
4. **API 集中化**：组件内无 `fetch()`
5. **zIndex**：FloatingWindow 内弹出层必须设 zIndex
6. **类型对齐**：前端类型与 Go json tag 一致
7. **接口审查**：请求字段全覆盖、响应字段全消费、时间/枚举格式匹配、无多余/缺失字段
8. **样式变量**：颜色用 CSS 变量，不硬编码
9. **状态管理**：`useShallow` 选择器，最小订阅
10. **校验同步**：新增后端字段时前端三处同步（types + refsCheck + editor）
11. **库 API 合规**：@xyflow/react / zustand / Monaco / ECharts / react-rnd 使用最新 API
12. **代码复用**：>300 行组件评估拆分，重复逻辑提取共享组件/hook
13. **注释审查**：过时/多余/被注释掉的代码必须清理；复杂逻辑和 workaround 必须有注释

---

## 动态积累（审查时追加）

此节记录审查过程中发现的实际案例、已知 bug 模式、最新 API 变更等。
每次使用此 skill 审查前端代码后，应将有价值的新发现追加到对应小节。

### 已知 Bug 模式

- **Monaco find widget tooltip 闪烁**：`.workbench-hover-container` hover 弹层盖在按钮上触发 mouseover/mouseout 死循环。必须 `display: none !important` 彻底隐藏，`pointer-events: none` 无效（Monaco 会重建容器）
- **Monaco find widget 按钮错位**：全局 `box-sizing: border-box` 影响 Monaco 内部按钮。只对 `.find-widget .button` 单独恢复 `content-box`，不能全局重置
- **flex 布局高度首帧不准**：`useLayoutEffect` 在首次 mount 时可能拿到不准确的 `clientHeight`（antd Table DOM 未稳定）。改用 `ResizeObserver` 观察容器
- **FloatingWindow 内弹出层被遮挡**：Modal/Popover/Select 下拉的 zIndex 默认值（1050）低于浮动窗口基线（1000+）。必须显式设置 `styles.mask.zIndex` / `overlayStyle.zIndex`。注意：仅 FlowEditor 内部有 ConfigProvider 设置 `zIndexPopup`，EditorPage 级别的 FloatingWindow（如 LogsTab、NotepadTab）内的 Modal 必须自行设置 zIndex
- **antd `bordered` 废弃 API 仍残留**：`<Descriptions bordered>` / `<List bordered>` 在多个文件中仍存在。antd v5 已废弃，应移除 prop 或用 CSS 控制

### 已废弃 API 追踪

- antd v5：`addonBefore/After` → `Space.Compact` + `<span>`；`destroyOnClose` → `destroyOnHidden`；`bordered` → 移除或 CSS；`strokeWidth` → `size`；`visible` → `open`
- @xyflow/react v12：`onEdgeUpdate` → `onReconnect`；`parentNode` → `parentId`；`project()` → `screenToFlowPosition()`；`getTransformForBounds` → `getViewportForBounds`
- react-rnd v10：`z` prop → `style={{ zIndex: N }}`
- zustand v5：`import shallow from 'zustand/shallow'` → `import { useShallow } from 'zustand/react/shallow'`

### 最佳实践沉淀

- [Monaco 嵌入 antd] 优先检查全局 box-sizing 影响和 hover 容器遮挡，不要全量覆盖 Monaco 内部样式
- [组件高度] 依赖 flex 剩余空间时用 `ResizeObserver` 替代 `useLayoutEffect` + 手动依赖
- [浮动窗口弹出层] 所有 FloatingWindow 内的 Modal/Popover/Select 必须通过 `floatingWindowStore._nextZ + 100` 设置 zIndex。特别注意非 FlowEditor 模块（monitoring、notepad）的 FloatingWindow 内 Modal 需自行设置
- [ECharts 颜色] ECharts option 中的颜色也应尽量使用 CSS 变量（通过 `getComputedStyle` 读取），保持暗色主题一致性。已有文件（DashboardTab）部分采用了 CSS 变量，应统一
- [API 集中化特例] `ProtoLoader.ts` 中 `loadFromHttp()` 已导入 `baselineApi` 函数但仍直接 `fetch()`，审查 service 导入但未使用的矛盾
- [组件拆分优先级] FlowCanvas.tsx (959行) > TaskStartModal.tsx (695行) > ResourcesDrawer.tsx (626行) > BindingTypeForm.tsx (600行) 是最需拆分的四个组件
