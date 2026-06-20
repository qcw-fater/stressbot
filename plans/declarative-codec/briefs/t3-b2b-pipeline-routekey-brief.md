# T3 Batch-2 任务 B — Pipeline 编辑器 + RouteKey 模板编辑器

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：B2-A 已落地（`codecEditor/codecEdit.ts` raw 无损同步 + FrameLayoutEditor + AdapterTab 结构化/源码视图切换）。
> 本任务加结构化视图的另两块：**Pipeline 编辑器**（管线步骤卡片）+ **RouteKey 模板编辑器**。共享 B2-A 的 codecEdit。

## 1. 任务定位

B2-A 的结构化视图已有帧布局编辑器。本任务补全结构化视图：Pipeline（encode 管线步骤的可视化编辑）+ RouteKey
模板。完成后结构化视图覆盖 codec.json 的全部主要结构（header/frame 已在 B2-A；pipeline + routeKey 在本任务）。

**algo 下拉 + 动态 params + 实时预览属 §3.4（Batch 3）**——本任务的 Pipeline 编辑器 **algo 用文本输入、
params 用通用键值表**（Batch 3 接 `GET /sbot/codec/algorithms` 后改为下拉 + 按算法动态 params + 加 encode/decode
预览）。代码注释标明此 staging。

## 2. 现状（先读码）

**先读** `cmd/web/src/components/modules/codecEditor/codecEdit.ts`（B2-A 的 parseCodecForEdit/serializeCodec/
header 增删改/移动 + setCodecScalar——本任务**扩展**它，加 pipeline 步骤与 routeKeyTemplate 的 helper）、
`FrameLayoutEditor.tsx`（结构 + onEdit 模式参考）、`ResourcesDrawer.tsx`（AdapterTab 结构化视图渲染处，本任务在其下加 Pipeline + RouteKey）、
`cmd/web/src/types/codec.ts`（PipelineStep/StepOffset/StepProduce/OverSpec/StepCond/Guard + PIPELINE_OPS/PRODUCE_REGIONS/OVER_KINDS/GUARD_OPS/ON_ERROR 合法值集合）、
`resourcesStore.ts` 的 `validateCodecSchema`（pipeline 校验规则对照）。
设计权威：`plans/declarative-codec/03-track-frontend.md` §2.2(b) Pipeline + §2.2(c) RouteKey。

## 3. 实现规格

### 3.1 扩展 codecEdit（`codecEditor/codecEdit.ts`，纯函数 + 单测）

沿用 B2-A 的 raw 无损 + 克隆不 mutate 模式，新增：
```ts
export function addPipelineStep(raw, step?: Partial<PipelineStep>): string;     // 追加到 raw.pipeline
export function updatePipelineStep(raw, index: number, patch: Partial<PipelineStep>): string;
export function removePipelineStep(raw, index: number): string;
export function movePipelineStep(raw, index: number, dir: -1 | 1): string;
export function setRouteKeyTemplate(raw, template: string): string;             // raw.routeKeyTemplate
```
- `raw.pipeline` 非数组时按空数组处理（add 创建数组）。新 step 默认 `{op:'compress', name:'', algo:''}`（name 必填，校验交给 validateCodecSchema）。
- 单测（追加到 `codecEdit.test.ts` 或新文件）：pipeline 增删改/移动 + routeKeyTemplate 编辑 → serialize 稳定、不 mutate 入参、不丢未知键/原序。

### 3.2 Pipeline 编辑器（新组件 `codecEditor/PipelineEditor.tsx`）

Props 同 B2-A FrameLayoutEditor：`{ raw, schema, onEdit }`。内部读 `schema.pipeline`（PipelineStep[]）展示，修改经 codecEdit helper → onEdit。

- **有序步骤卡片列表**：每张卡可 ↑↓ 移序、删除；列表底「+ 添加步骤」。卡片顺序即 encode 顺序；顶部标注「decode 自动反序」。
- **每张卡的字段**（按 `op` 显示相关项）：
  - 通用：`name`（必填输入）、`op`（下拉 compress/encrypt/checksum/hash，用 PIPELINE_OPS）、`algo`（**文本输入**——Batch 3 改下拉；注释标明）、`onError`（下拉 fail/keep，空视 fail）、`flag`（下拉：选项 = header 所有 `role:"flags"` 字段的命名位 name 并集；可空）、`when`（结构化条件子表单，见下）。
  - `op=encrypt` 额外：`keyLen`（数字）、`offset`（两个独立输入 `offset.encode` / `offset.decode`，UI 标注「发/收偏移可不同，如 UDP 发=11 收=0」）、`produces`（产物列表子表单）。
  - `op=checksum`/`hash`（独立步，无 flag 绑定时）：`over`（kind 下拉 bodyPlain/bodyFinal/header/frame/range 用 OVER_KINDS；range 时显示 rangeStart/rangeEnd 数字）；也可有 `produces`。
  - `params`：**通用键值表**（行 = key(文本)/value(文本，存时数字串转 number 否则 string)；增删）——Batch 3 改按算法动态字段。
- **`when` 子表单**（`StepCond`）：`minBodyLen`(数字) / `onlySmaller`(开关) / `requireKey`(开关) / `appliesWith`(下拉：已有 step.name 列表) / `guards[]`(表格 field/op(用 GUARD_OPS)/value(数字)，增删)。带 when 的步会触发「需绑定 flag」的客户端提示（与 validateCodecSchema 一致）。
- **`produces` 子表单**（`StepProduce[]`）：每行 name/algo/region(下拉 PRODUCE_REGIONS)，增删。
- 字段非法值（如 op 不合法）给即时提示但不阻塞（最终校验交 validateCodecSchema）。

### 3.3 RouteKey 模板编辑器（新组件 `codecEditor/RouteKeyEditor.tsx`）

Props `{ raw, schema, onEdit }`：
- `routeKeyTemplate` 输入框（如 `{cmd}:{act}`）→ `setRouteKeyTemplate`。
- **实时校验**：解析模板里所有 `{name}` 占位，逐个检查是否对应某个 `role:"route"` 的 header 字段；未知占位红色列出（与 validateCodecSchema 的 routeKeyTemplate 校验一致）。
- **展示 route 字段清单**：列出当前所有 `role:"route"` 字段名，提示「这些可用作占位」。
- **样例 routeKey**：把占位替换为各 route 字段的样例值（如用字段名或 `0` 占位）展示一个示例串（仅提示性，如 `{cmd}:{act}` → `<cmd>:<act>` 或 `cmd:act`）。

### 3.4 AdapterTab 接线

在结构化视图（`parsed.schema` 有时）的 FrameLayoutEditor **下方**追加 `<PipelineEditor .../>` 与 `<RouteKeyEditor .../>`（同一 onEdit=setContent、raw、schema）。errors.json 视图不受影响（仍只源码）。视图切换、save/clear/import/liveErrors 不变。

## 4. 全局约束（bind）

- **改动文件**：扩展 `codecEditor/codecEdit.ts`（+ pipeline/routeKey helper + 测试）、新增 `codecEditor/PipelineEditor.tsx` + `RouteKeyEditor.tsx`（+ 必要小子组件）、改 `ResourcesDrawer.tsx`（结构化视图加两块）。**严禁动** services/、types/、B2-A 的 FrameLayoutEditor/ByteStrip/HeaderFieldTable/RoleLinkedForm（除非抽共享小子组件——优先新建不改动既有）。
- **复用 codecEdit 的 raw 无损模式**（克隆不 mutate、serialize 稳定、不丢键/原序）；合法值集合用 types/codec.ts 的 PIPELINE_OPS/PRODUCE_REGIONS/OVER_KINDS/GUARD_OPS/ON_ERROR，**勿复制**。
- **algo 文本输入 + params 键值表是 staging**：注释标明 Batch 3（§3.4）接 `GET /sbot/codec/algorithms` 后改下拉 + 动态 params + 加预览。勿在此任务拉算法清单。
- **禁止兼容性兜底**：非法值即时提示但不静默兜底；最终校验交 validateCodecSchema。
- **UI 文本不暴露技术术语**：用「管线」「步骤」「路由键模板」等；列名 op/algo/flag 等配置术语必要保留。
- 类型安全；**不要 git commit**。

## 5. 验证（全绿才算 DONE）

- `cd cmd/web && npx tsc -b` exit 0。
- `cd cmd/web && npm run test` 通过（codecEdit 新增 pipeline/routeKey helper 单测；既有 204 不回归）。
- 自查 `git diff --stat`：改动限于 `codecEditor/`（codecEdit.ts 扩展 + 新组件 + 测试）+ ResourcesDrawer.tsx。

## 6. 报告

写到 `plans/declarative-codec/reports/t3-b2b-pipeline-routekey-report.md`：实现要点、codecEdit 扩展、PipelineEditor（卡片字段 + per-op 可见性 + when/produces/over 子表单）、RouteKeyEditor（占位校验 + route 字段清单 + 样例）、AdapterTab 接线、algo/params staging 说明、测试用例、tsc/test 结果、自审、贴 `git diff --stat`。

返回消息只含：① 状态；② 改动文件清单；③ 一行测试摘要；④ concerns。有歧义先问。
