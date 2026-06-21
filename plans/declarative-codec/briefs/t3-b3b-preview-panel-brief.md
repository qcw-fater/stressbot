# T3 Batch-3 任务 B — 实时预览面板（encode/decode）

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：B3-A 已落地 `services/codecApi.ts` 的 `previewCodec` + `PreviewRequest`/`PreviewResult`/`PreviewField` 类型。
> 本任务做编辑辅助预览面板，调后端真实 Go 引擎（`POST /sbot/codec/preview`）跑一次 encode/decode。

## 1. 任务定位

结构化编辑器已能配 codec.json，但用户难肉眼确认「这个 schema 编出来的帧长啥样 / 这帧解出来是啥」。
本任务加**预览面板**：encode（输入 route+body+key → 出帧 hex + 字段解释）/ decode（粘帧+key → 出字段 +
routeKey + headerErr + body），走 `previewCodec`（后端真实引擎，杜绝前端重写 codec 漂移）。
**纯编辑辅助**：不写 IDB、不改 baseline、不参与任务下发。

## 2. 现状（先读码）

**先读** `cmd/web/src/services/codecApi.ts`（B3-A 的 `previewCodec(req:PreviewRequest):Promise<PreviewResult>`，
**HTTP 200 即使 result.error 非空也返回 result**）、`cmd/web/src/types/codec.ts`（PreviewRequest/PreviewResult/
PreviewField + CodecSchema/Field）、`cmd/web/src/components/modules/codecEditor/codecEdit.ts`
（`parseCodecForEdit`——AdapterTab 已有 parsed.raw/schema）、`cmd/web/src/components/modules/ResourcesDrawer.tsx`
（AdapterTab 结构化视图渲染处 + activeConn）、`codec/preview.go:24-57`（Preview 语义：encode 必填 route/bodyHex、
decode 必填 frameHex、keyHex 可空、route 值 int/float/string 数值化取整）。
设计权威：`plans/declarative-codec/03-track-frontend.md` §2.2(d) + §3.4。

## 3. 实现规格

### 3.1 预览面板（新组件 `codecEditor/PreviewPanel.tsx`）

Props：`{ raw: Record<string,unknown>; schema: CodecSchema; transport: 'tcp'|'udp'; onEdit?: never }`
（raw 作 preview 的 schema 入参；schema 读 route 字段清单；transport 来自连接名 proto）。

- **模式切换**：`encode | decode`（Segmented/Radio）。
- **encode**：
  - route 字段输入：从 `schema.header` 取所有 `role:"route"` 字段，每个一个值输入（数字/文本；preview 端数值化取整）→ 组装 `route: { [fieldName]: value }`。
  - `bodyHex`（hex 文本框，placeholder「body hex」）。
  - `keyHex`（hex 文本框，可空）。
  - 「预览」按钮 → `previewCodec({ schema: raw, mode:'encode', transport, route, bodyHex, keyHex })`。
  - 结果：`frameHex`（hex 串，可复制）+ `fields`（PreviewField[]：name/value/offset/size 列表）。
- **decode**：
  - `frameHex`（hex 文本框，粘整帧）。
  - `keyHex`（可空）。
  - 「预览」按钮 → `previewCodec({ schema: raw, mode:'decode', transport, frameHex, keyHex })`。
  - 结果：`fields`（逐字段 name/value）+ `routeKey` + `headerErr`（errorCode 值，非 0 标红提示）+ `bodyHex`。
- **错误处理**：`PreviewResult.error` 非空 → Alert 中文展示（schema 编译失败 / 坏 hex / 未知 mode 等）。
  `previewCodec` 自身抛错（网络/非 2xx）→ catch 后 Alert 中文「预览失败：…」。**不**用 toast（结果区常驻更合适）。
- **触发方式**：**手动「预览」按钮**（不自动每次按键触发——避免抖动/频繁请求/错误闪烁）。可加 loading 态。
- schema 非法（liveErrors 非空）时仍允许点预览（后端会返回 error，用户据此修 schema）。
- 结果区空态：「点「预览」查看编解码结果」。

### 3.2 AdapterTab 接线

结构化视图（parsed.schema 有时）在 PipelineEditor/RouteKeyEditor 下方挂 `<PreviewPanel>`
（可放 Collapse「预览」默认折叠，避免占太多纵向空间）。`transport` 由 `activeConn` 推导
（`'tcp:logic'`→`'tcp'`，即连接名首个 `:` 前的 proto）。errors.json 视图不显示预览。

## 4. 全局约束（bind）

- **改动文件**：新 `codecEditor/PreviewPanel.tsx`（+ 必要小子组件）+ `ResourcesDrawer.tsx`（结构化视图挂 PreviewPanel + transport 推导）。**严禁动** services/（codecApi 已就绪）、types/、codecEdit、帧布局/pipeline/routeKey 组件、后端。
- **请求收拢 services**：组件不直接 fetch，全走 `codecApi.previewCodec`。
- **纯编辑辅助**：预览结果不落 IDB、不改 baseline、不进任务下发（不调任何 set* / save）。
- **禁止兼容性兜底**：错误原样中文提示，不 `??` 兜、不本地模拟 codec。
- **手动触发**（不自动按键预览）。
- **UI 文本不暴露技术术语**：用「预览」「编码/解码」「帧」「字段」「路由键」；hex/route 是必要配置术语可保留。
- transport 由连接名推导（单 transport codec）。
- 类型安全；**不要 git commit**。

## 5. 验证（全绿才算 DONE）

- `cd cmd/web && npx tsc -b` exit 0。
- `cd cmd/web && npm run test` 通过（既有 228 不回归；若有可抽的纯函数如 route 字段提取/transport 推导，加单测；UI 交互靠 tsc + 代码自洽）。
- 自查 `git diff --stat`：改动限于 `codecEditor/PreviewPanel.tsx`（+ 子组件）+ ResourcesDrawer.tsx。

## 6. 报告

写到 `plans/declarative-codec/reports/t3-b3b-preview-panel-report.md`：实现要点、encode/decode 表单 + 结果区、
错误处理、手动触发理由、transport 推导、AdapterTab 接线、测试、tsc/test 结果、自审、贴 `git diff --stat`。

返回消息只含：① 状态；② 改动文件清单；③ 一行测试摘要；④ concerns。有歧义先问。
