# T3 Batch-3 任务 B — 实时预览面板（encode/decode）报告

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`
> 任务来源：`plans/declarative-codec/briefs/t3-b3b-preview-panel-brief.md`
> 状态：**DONE**

## 1. 实现要点

新增「预览面板」组件 `PreviewPanel`，调后端真实 codec 引擎（`POST /sbot/codec/preview`，
经 `services/codecApi.ts` 的 `previewCodec`）跑一次 encode/decode，**纯编辑辅助**（不落 IDB、
不改 baseline、不进任务下发）。挂在 AdapterTab 结构化视图的 RouteKeyEditor 下方，
Collapse「预览（编码/解码）」默认折叠。

三个新文件 + ResourcesDrawer 接线：

- `cmd/web/src/components/modules/codecEditor/PreviewPanel.tsx`（组件）
- `cmd/web/src/components/modules/codecEditor/previewHelpers.ts`（纯函数 helper）
- `cmd/web/src/components/modules/codecEditor/__tests__/previewHelpers.test.ts`（20 个单测）
- `cmd/web/src/components/modules/ResourcesDrawer.tsx`（+20 行接线）

## 2. encode/decode 表单 + 结果区

### encode 模式
- **路由字段输入**：从 `schema.header` 取所有 `role:"route"` 字段（`collectRouteFields`），
  每字段一个 `Input`（placeholder「0」），值组装成 `route: { [name]: number|string }`
  （`buildRouteMap`：空白剔除、纯整数串转 number、其它保留 string 与后端
  `normalizeRouteMap` + `routePreviewFloorInt` 对齐）。无 route 字段时提示。
- **body hex**：`Input.TextArea`（monospace，autoSize）。
- **key hex**：同上，可空（空串→`undefined`）。
- 「预览」按钮 → `previewCodec({ schema: raw, mode:'encode', transport, route, bodyHex, keyHex })`。
- **结果区**：`frameHex`（带复制按钮）+ 字段解释表（PreviewField[] name/value/offset/size）。

### decode 模式
- **帧 hex**：`Input.TextArea`（粘贴完整帧）。
- **key hex**：可空。
- 「预览」按钮 → `previewCodec({ schema: raw, mode:'decode', transport, frameHex, keyHex })`。
- **结果区**：`routeKey`（code 样式）+ `headerErr`（**非 0 标红 Alert**，展示 errorCode 值）
  + 字段解释表 + `bodyHex`（带复制按钮）。

### 结果区空态
未触发时显示 Empty「点「预览」查看编解码结果」。

## 3. 错误处理（双通道，禁止兼容兜底）

- **PreviewResult.error 非空**（schema 编译失败 / 坏 hex / 未知 mode 等，HTTP 200）：
  红色 Alert 直接展示 `result.error`（后端中文）。
- **previewCodec 自身抛错**（网络 / 非 2xx）：catch 后红色 Alert 展示
  `预览失败：${message}`（codecApi 已包中文前缀）。
- 不用 toast（结果区常驻更合适，避免错误一闪即逝）。
- schema 非法（liveErrors 非空）时**仍允许点预览**——后端回填中文 error，用户据此修 schema。
- 切换模式时清空上次结果（`setResult(null)` + `setReqError(null)`），避免显示另一种模式的过期输出。

## 4. 手动触发理由

**不自动每次按键触发**，改用手动「预览」按钮：
- 避免输入过程中频繁请求（抖动）；
- 避免半成品输入时错误反复闪烁（用户体感差）；
- 避免后端被空/坏 hex 等无效输入轰炸。
- 按钮带 `loading` 态，用户感知「正在跑」。

## 5. transport 推导

`deriveTransport(connName)`：连接名 `<proto>:<service>` 取首个 `:` 前的 proto，
仅识别 tcp/udp；无冒号 / 未知 proto / null / undefined → 回退 `'tcp'`
（与后端 `preview.go` 空串/非法→tcp 语义对齐）。在 AdapterTab 由 `activeConn` 推导后传入。

## 6. AdapterTab 接线

`ResourcesDrawer.tsx` 结构化视图（`showStructView && parsed.raw && parsed.schema`）的
`RouteKeyEditor` 下方挂 `<Collapse>`，children 为 `<PreviewPanel raw={parsed.raw}
schema={parsed.schema} transport={deriveTransport(activeConn)} />`。
- errors.json 视图（`isErrorsView`）不显示预览（结构化视图整体不渲染）。
- 源码视图（`viewMode==='source'`）也不显示预览（同上）。
- 仅新增 antd `Collapse` 导入 + PreviewPanel/deriveTransport 导入 + 20 行 JSX。

## 7. 测试

新增 `previewHelpers.test.ts`，20 个纯函数单测：

- `deriveTransport`（6 个）：`tcp:logic`→tcp、`udp:battle`→udp、无冒号→tcp、未知 proto→tcp、
  空/null/undefined→tcp、冒号在首位→tcp。
- `collectRouteFields`（6 个）：收集 role:"route" 保序、无 route 字段→空、跳过空名、
  同名去重保序、schema=null→空、header 非数组安全降级。
- `buildRouteMap`（8 个）：纯整数串→number、负数串→number、空串剔除、空白串剔除、
  非数字串保留 string、混合 trim、空对象、`1e3` 不按整数解析保留 string。

## 8. 验证结果

- `cd cmd/web && npx tsc -b`：**exit 0**（无错误）。
- `cd cmd/web && npm run test`：**248 passed**（20 文件）——既有 228 不回归 + 新增 20。

## 9. 自审（自查清单）

- [x] 改动限于新 PreviewPanel.tsx + previewHelpers.ts（+ 子组件 HexInput/HexOutput/FieldsTable
      均内联在 PreviewPanel.tsx 中）+ ResourcesDrawer.tsx；未动 services/types/codecEdit/
      帧布局/pipeline/routeKey 组件/后端。
- [x] 请求收拢 services（走 `previewCodec`，组件不直接 fetch）。
- [x] 纯编辑辅助：不调 set*/save，不落 IDB。
- [x] 禁止兼容兜底：错误原样中文，不本地模拟 codec。
- [x] 手动触发（不自动按键）。
- [x] UI 文案「预览/编码/解码/帧/字段/路由键」；hex/route 必要术语保留；未暴露 Agent/Admin/IDB。
- [x] transport 连接名推导。
- [x] 类型安全（tsc 绿）。
- [x] 未 git commit。

## 10. git diff --stat（本任务范围）

```
 cmd/web/src/components/modules/ResourcesDrawer.tsx                              | 20 ++++++++++++
 cmd/web/src/components/modules/codecEditor/PreviewPanel.tsx                      | (新文件)
 cmd/web/src/components/modules/codecEditor/previewHelpers.ts                     | (新文件)
 cmd/web/src/components/modules/codecEditor/__tests__/previewHelpers.test.ts      | (新文件)
```

> 注：`git diff --stat` 全量输出里还含 B3-A 既有未 commit 的 PipelineEditor.tsx /
> types/codec.ts / algosForStepOp.ts / codecApi.ts 等——那些是 B3-A 任务产物，本任务未触碰。
> 本任务唯一改动的已跟踪文件是 ResourcesDrawer.tsx（+20 行）。
