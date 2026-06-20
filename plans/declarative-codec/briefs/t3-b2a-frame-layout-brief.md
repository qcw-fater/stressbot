# T3 Batch-2 任务 A — 结构化编辑骨架 + 帧布局编辑器

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：Batch 1 已落地（`AdapterTab` 源码 JSON 编辑器、`content` 为单一数据源、`validateCodecSchema`、
> `types/codec.ts`）。本任务在源码视图旁加**结构化视图**（帧布局编辑器），两者以 JSON 为单一数据源双向同步。
> 后续 B2-B 加 Pipeline + RouteKey 编辑器（共享本任务的同步 helper）。

## 1. 任务定位

Batch 1 的「协议配置」tab 只有源码 JSON 视图。本任务加**结构化视图**：把「哪段字节是哪个字段」可视化配置——
header 字段表 + 字节条带（byte map）+ role 联动表单 + trailer。源码视图保留（高级用户直编），两视图切换；
**以 JSON 对象为单一数据源**，结构化视图是其受控编辑器。

## 2. 现状（先读码）

**先读** `cmd/web/src/components/modules/ResourcesDrawer.tsx`（重点 `AdapterTab`，约 158-662）：连接选择器、
`content: string` 状态（单一数据源）、源码 Monaco `language="json"`、`onSave`（validateCodecSchema 阻塞）、
`liveErrors`（每渲染 `validateCodecSchema(content)`）、`CODEC_JSON_TEMPLATE`、`connNameToFileName/fileNameToConnName`。
再读 `cmd/web/src/types/codec.ts`（CodecSchema/HeaderField 等类型 + 合法值集合 FIELD_TYPES/FIELD_ROLES 等）、
`cmd/web/src/services/resourcesStore.ts` 的 `validateCodecSchema`。
设计权威：`plans/declarative-codec/03-track-frontend.md` §2.2(a) 帧布局编辑器 + §2.2(e) 双视图同步。

ResourcesDrawer.tsx 已 1000+ 行；**新组件抽到独立目录** `cmd/web/src/components/modules/codecEditor/`，勿继续撑大 ResourcesDrawer。

## 3. 实现规格

### 3.1 同步 helper（新模块 `codecEditor/codecEdit.ts`，纯函数 + 单测）

**核心：无损 raw-object round-trip**（保留未知键与原始键序，结构化编辑不丢字段、不重排整个文档）：

```ts
import type { CodecSchema, Field } from '@/types/codec';

/** 把 content 解析为 raw 对象（保留全部键与序）+ typed 视图；非法 JSON → raw/schema=null + error。 */
export function parseCodecForEdit(content: string): {
  raw: Record<string, unknown> | null;   // JSON.parse 结果（lossless）
  schema: CodecSchema | null;            // 同一对象的 typed 视图（parse 成功时）
  error: string | null;                  // JSON 解析错误信息（中文）
};

/** 把 raw 对象序列化回 content（2 空格缩进，保留键序）——确定性输出。 */
export function serializeCodec(raw: Record<string, unknown>): string;

// header 字段增删改（直接改 raw.header 数组，返回新 content 字符串）
export function addHeaderField(raw, field: Field): string;
export function updateHeaderField(raw, index: number, patch: Partial<Field>): string;
export function removeHeaderField(raw, index: number): string;
export function moveHeaderField(raw, index: number, dir: -1 | 1): string;
// frame / endianDefault / trailer 等标量编辑
export function setCodecScalar(raw, path: 'version'|'endianDefault'|'frame.headerSize'|'frame.trailerSize'|'frame.lengthIncludesHeader'|'frame.lengthIncludesTrailer', value: number|string|boolean): string;
```

要点：
- `parseCodecForEdit`：`JSON.parse` 失败 → `{raw:null, schema:null, error:'协议配置不是合法 JSON：…'}`；成功 → raw=结果、schema=同对象按 CodecSchema 宽松读（字段缺失给默认/undefined，**不**因缺字段报错——结构校验仍由 `validateCodecSchema` 负责，这里只管「能否解析成对象」）。
- `serializeCodec`：`JSON.stringify(raw, null, 2)`——确定性、保留键序。
- 增删改 helper：克隆 raw → 改对应位置 → `serializeCodec` 返回新字符串（**纯函数，不 mutate 入参**）。header 字段在 raw 里是 `raw.header`（数组），按 index 操作。
- 单测（新 `codecEditor/__tests__/codecEdit.test.ts`）：round-trip（parse→serialize 字节相等，含未知键与原序保留）；非法 JSON → error；增删改/移动 header 字段后 serialize 稳定且只动预期部分；scalar 编辑。

### 3.2 帧布局编辑器（新组件 `codecEditor/FrameLayoutEditor.tsx`）

Props：`{ raw: Record<string,unknown>; schema: CodecSchema; onEdit: (nextContent: string) => void }`（`onEdit` 把新 content 回灌给 AdapterTab 的 setContent）。
内部：`schema` 读展示，修改经 codecEdit helper 生成新 content → `onEdit`。

子区：
- **frame 标量**：headerSize / trailerSize / lengthIncludesHeader / lengthIncludesTrailer / endianDefault（le/be 单选）。编辑 → `setCodecScalar`。
- **字节条带（byte map）**：一条 `0..headerSize-1` 的彩色条带，每个 header 字段占 `[offset, offset+size)` 一段彩色区块，标注 `name/offset+size/type`；点击区块 → 选中该字段（高亮字段表对应行）。**仅展示 + 选中**（不做拖拽改跨度——改跨度走字段表输入；本 scope 决策，见任务定位）。trailer 若 trailerSize>0，在 header 条带后再画一段灰色 trailer 区。字段越界/重叠时该段标红（视觉提示，错误明细复用 liveErrors）。
- **header 字段表**：每行一字段，列 `name / offset / size / type(下拉 u8..bytes) / endian(le/be/默认) / role(下拉 length/route/errorCode/flags/checksumOut/value/reserved) / 操作(↑↓移序、删除)`。表底「+ 添加字段」。编辑 → `updateHeaderField`/`addHeaderField`/`removeHeaderField`/`moveHeaderField`。
- **role 联动表单**（选中字段时下方展开，按 role 显示不同输入）：
  - `route`：只读提示「参与 routeKeyTemplate 占位」（routeKey 编辑器在 B2-B）。
  - `flags`：命名位编辑器（`bits:[{name,bit}]`：表格 name/bit，增删；bit∈[0,size*8) 客户端提示）。
  - `checksumOut`：`from` 输入（占位 `<step>.<output>`；下拉选项 = 各 pipeline 步的 produces 产物，pipeline 在 B2-B 才有编辑器，此处先给文本输入 + 提示格式）。
  - `value`：`source.kind` 下拉（**v1 仅 const/route 可选**；state/counter/timestamp 置灰标「v1.1」）+ const 时 `value` 数字输入、route 时 `key` 输入。与后端 `Validate` 拒绝一致。
  - `errorCode`：提示「绑定后启用服务端错误码识别与 errors.json」。
  - `length`/`reserved`：无额外配置。
- 字段 type 的宽度约束（u8=1…u64=8、bytes 需显式 size）在客户端给即时提示（用 `types/codec.ts` 的 FIELD_TYPES 宽度表），但不阻塞输入（最终校验交给 validateCodecSchema）。

### 3.3 AdapterTab 接线（视图切换）

在 AdapterTab 的编辑器区上方加**视图切换**（Segmented 或 Tabs）：`结构化 | 源码`。
- 默认「结构化」。`const parsed = useMemo(() => parseCodecForEdit(content), [content])`。
- 结构化视图：`parsed.error` 非空（非法 JSON 或正在手编源码）→ 显示提示「源码不是合法 JSON，请切到源码视图修正」+ 仍显示源码 Monaco（降级）。`parsed.schema` 有 → 渲染 `<FrameLayoutEditor raw={parsed.raw} schema={parsed.schema} onEdit={setContent} />`。
- 源码视图：保留现有 Monaco（不变）。
- `onEdit`（结构化编辑）→ `setContent(nextContent)` → content 变 → parsed 重算 → 结构化视图刷新 + liveErrors 重算 + 源码视图（切换后）一致。**单一数据源 = content 字符串**。
- errors.json 选中时（`activeConn==='__errors__'`）：不显示结构化视图（只源码），视图切换隐藏。
- save/clear/import/live 校验逻辑**不变**（它们都基于 content；结构化编辑已把变更写入 content）。

## 4. 全局约束（bind）

- **新文件目录** `cmd/web/src/components/modules/codecEditor/`（codecEdit.ts + FrameLayoutEditor.tsx + 子组件 + 测试）+ 改 `ResourcesDrawer.tsx`（AdapterTab 接线：视图切换 + 渲染 FrameLayoutEditor）。**严禁动** services/、types/、其他组件（B2-B 才加 Pipeline/RouteKey）。
- **无损 round-trip**：raw-object 保留未知键与原序；结构化编辑只动预期部分，不重排整个文档。
- **禁止兼容性兜底**：不 `??` 兜错误；非法 JSON 直接提示切源码，不静默。
- **UI 文本不暴露技术术语**：面板用「帧布局」「字段」「字节」等可懂词；字段表列名可用 type/role/offset/size（配置术语，必要）；**不**在面板文案出现 codec/schema（变量名可保留）。
- 复用 `validateCodecSchema`（校验）与 `types/codec.ts`（类型 + 合法值集合）；**不要**复制合法值表。
- **字节条带 scope**：仅展示 + 点击选中，不做拖拽改跨度（改跨度走字段表）；代码注释标明 DnD-resize 留后续。
- 类型安全；请求收拢（本任务无网络）。**不要 git commit**。

## 5. 验证（全绿才算 DONE）

- `cd cmd/web && npx tsc -b` exit 0。
- `cd cmd/web && npm run test` 通过（新增 codecEdit round-trip/mutate 单测；既有 189 不回归）。
- 自查 `git diff --stat`：改动限于 `codecEditor/` 新文件 + `ResourcesDrawer.tsx`。

## 6. 报告

写到 `plans/declarative-codec/reports/t3-b2a-frame-layout-report.md`：实现要点、codecEdit 同步模型（raw lossless）、FrameLayoutEditor 结构（字段表/字节条带/role 联动/frame 标量）、AdapterTab 视图切换接线、测试用例、tsc/test 结果、自审、贴 `git diff --stat`。

返回消息只含：① 状态；② 改动文件清单；③ 一行测试摘要；④ concerns。有歧义先问。
