# stressbot · Web 流程编辑器

stressbot 压测工具的可视化流程编辑器前端，类似 UE 蓝图的节点式 DAG 编辑体验。完整设计见 [`docs/design-web-editor.md`](../docs/design-web-editor.md)。

## 快速开始

```bash
cd web
npm install
npm run dev          # 启动 dev server（http://localhost:5173）
npm run build        # 生产构建
npm run type-check   # TypeScript 类型校验
npm run lint         # ESLint
npm run test         # Vitest 单元测试（22 个，覆盖 codec / refsGraph / refsCheck）
```

## 功能一览

| 模块 | 入口 | 说明 |
| --- | --- | --- |
| 主画布 | `FlowCanvas` | 8 种节点（sequence / action / loop / boolean / weighted / wait / break / continue）+ 5 种边 |
| 节点属性 | 双击节点 → `NodeEditorDrawer` | 控制流节点 5 种独立编辑器 + ActionEditor |
| ActionEditor | action 节点双击 | 11 pattern × 17 binding type 完整编辑（含 nested 递归） |
| Callback 子系统 | Toolbar → "Listens" | 三态形态（silent / declarative / lua）+ 反向引用 + 重复注册检测 |
| 监听注册 | ActionEditor 内嵌 | route + server + callback 三列 + 批量粘贴 JSON |
| Proto 浏览器 | Toolbar → "Proto" | 启动时全量加载 `conf/proto/`，搜索 message + 字段表 |
| 模板库 | Toolbar → "模板库" | IndexedDB 存放 action / callback 模板，跨流程复用 |
| 校验 | Toolbar → "校验" | 15 条规则（节点引用 / proto 校验 / 重复注册 / 孤儿） |
| Undo / Redo | `Ctrl+Z` / `Ctrl+Shift+Z` | 仅作用于业务字段，不影响视图 |
| 编辑稿持久化 | LocalStorage | 自动 debounce 写盘，刷新页面恢复 |
| JSON 预览 | Toolbar → "JSON 预览" | Monaco 只读 + 复制 / 下载 |
| 监控槽位 | `metricsProvider` prop | 节点内显示进入数 / 平均耗时 / 失败率 |

## 配置依赖

启动后编辑器默认从同仓库 `conf/` 加载：

- `conf/flow/flow.json` —— 流程定义（开发环境通过 Vite 中间件挂载到 `/conf/flow/flow.json`）
- `conf/proto/*.proto` —— 消息定义；启动时全量解析后供 ActionEditor 字段补全
- `conf/scripts/*.lua` —— Lua 脚本
- `conf/adapter/*_codec.json` / `conf/adapter/errors.json` —— 协议配置（codec.json）：声明式编解码（帧布局 / pipeline / routeKey 模板）+ 错误码描述；在资源抽屉「协议配置」Tab 内编辑，由 Go 端 codec 在运行时加载

无需复制，dev server 通过 `confMountPlugin` 直接读 `../conf/`，并自动生成 `proto/index.json` / `scripts/index.json` 供前端枚举。

生产环境另有两条加载路径：

1. Go monitor HTTP API：`/api/proto/files`、`/api/proto/file/:name`
2. 用户手动上传 `.proto` 文件作为兜底

## 目录结构

```
src/
├── components/
│   └── FlowEditor/                    # 主组件（外部用 <FlowEditor />）
│       ├── index.tsx
│       ├── FlowCanvas.tsx
│       ├── nodes/                     # 8 种节点 + 通用 Shell + MetricsBadge
│       ├── edges/                     # 5 种边（seq/branch/weight/loopBody/listen）
│       ├── editors/                   # 控制流编辑器 + ActionEditor
│       │   ├── ActionEditor/          # PatternSelector + Declarative/LuaForm + BindingsTable + StoreTable + ProtoFieldPicker
│       │   └── shared/                # ConditionInput / DelayInput / NodeIdSelect
│       ├── callbacks/                 # Callback 子系统（CallbackPanel/Editor/Card + ListenRefsTable + RouteEditor + refsGraph）
│       ├── codec/                     # jsonToFlow / flowToJson / dagreLayout
│       ├── store/                     # flowStore / editorStore / persistDraft / undoRedo
│       ├── proto/                     # ProtoLoader / ProtoRegistry / ProtoBrowser
│       ├── library/                   # 模板库（IndexedDB）
│       ├── validation/                # 15 条校验规则 + 报告抽屉
│       ├── panels/                    # Toolbar / NodePalette
│       └── preview/                   # JsonPreviewModal
├── pages/
│   └── EditorPage.tsx                 # 全屏页（Vite dev 入口）
├── types/                             # 镜像 engine/flow.go 的 TS 类型
└── styles/                            # tokens.css（全色板）+ global.css
```

## codec 圆周等价性

`flowToJson(jsonToFlow(flow))` 必须保持业务字段语义不变。`src/components/FlowEditor/codec/codec.test.ts` 用真实的 `conf/flow/flow.json`（89 节点 / 70 动作 / 18 回调）做 round-trip：

```bash
npm run test -- codec.test
# Test Files  1 passed (1)
#      Tests  6 passed (6)

# 进一步在前端编辑器中导入导出结果，查看校验报告
npm run test -- codec.test
# Test Files  1 passed (1)
```

## 作为组件使用

```tsx
import { FlowEditor } from './components/FlowEditor';

<FlowEditor
  initialFlow={taskFlow}        // 可选；不传则尝试从 LocalStorage / /conf/flow/flow.json 恢复
  initialLayout={layoutJson}    // 可选；视觉位置元数据
  autoLoadDefault={false}       // 关闭自动 fetch
  onSave={(flow, layout) => fetch('/api/flow', { method: 'POST', body: JSON.stringify({flow, layout}) })}
  metricsProvider={(nodeId) => realtimeMetrics[nodeId]}
/>
```

## 设计参考

- `docs/design-web-editor.md` —— 完整设计文档（架构 / 节点视觉 / 验证规则 / 实施 Roadmap）
- `docs/redesign-flow-nodes.md` —— 节点 / 动作 / 回调的 JSON schema（与 `engine/flow.go` 对齐）
- `engine/flow.go` —— 后端权威数据模型（前端类型镜像）
