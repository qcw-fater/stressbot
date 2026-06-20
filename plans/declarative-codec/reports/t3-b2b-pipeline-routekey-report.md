# T3 Batch-2 任务 B 报告 — Pipeline 编辑器 + RouteKey 模板编辑器

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`，未 commit）。
> 依赖：B2-A 已落地（codecEdit raw 无损 + FrameLayoutEditor + AdapterTab 结构化/源码视图切换）。

## 1. 实现要点

补全 AdapterTab 结构化视图的另两块：**管线（Pipeline）编辑器** + **路由键模板（RouteKeyTemplate）编辑器**。两者共享 B2-A 的 codecEdit raw 无损同步模式与 `onEdit=setContent` 数据流。结构化视图现覆盖 codec.json 全部主要结构（header/frame 已在 B2-A；pipeline + routeKey 在本任务）。

## 2. codecEdit 扩展（`codecEditor/codecEdit.ts`）

沿用 B2-A「深拷贝 → 改对应位置 → serializeCodec」的克隆不 mutate 模式，新增 5 个纯函数：

| helper | 行为 |
|---|---|
| `addPipelineStep(raw, step?)` | 追加到 `raw.pipeline` 末尾；默认 `{op:'compress', name:'', algo:''}`；`raw.pipeline` 非数组时按空数组处理（add 创建数组）|
| `updatePipelineStep(raw, index, patch)` | 局部 patch 合并，保留该步其他键；越界安全降级（返回原 content 序列化）|
| `removePipelineStep(raw, index)` | 删除指定 index；越界安全降级 |
| `movePipelineStep(raw, index, dir)` | 上移(-1)/下移(+1)；越界保持原序 |
| `setRouteKeyTemplate(raw, template)` | 写 `raw.routeKeyTemplate`；保留未知键 |

新增内部 helper `cloneWithPipeline`（类比 `cloneWithHeader`）。所有 helper 均不 mutate 入参、serialize 稳定、不丢未知键/原序。

## 3. PipelineEditor（`codecEditor/PipelineEditor.tsx`）

**外层**：卡片列表，顶部标注「卡片顺序 = encode 顺序（decode 自动反序）」，底部「+ 添加步骤」。

**单步卡片字段**（按 op 显示相关项）：

- 通用行：`name`（必填文本）/ `op`（下拉 PIPELINE_OPS）/ `algo`（**文本输入——staging**）/ `onError`（下拉 fail|keep，空视 fail）/ `flag`（下拉 = 所有 role:"flags" 字段命名位 name 并集，可空）/ ↑↓移序 / 删除。
- op 非法时卡片内红色即时提示（不阻塞编辑）。
- **encrypt**：`keyLen` / `offset.encode（发）` / `offset.decode（收）`（两独立输入，标注「发/收偏移可不同，如 UDP 发=11 收=0」）。
- **checksum|hash**：`OverSubform`（kind 下拉 OVER_KINDS；range 时 rangeStart/rangeEnd 数字）。
- `ParamsSubform`：**通用键值表（staging）**——行 key(文本)/value(文本，纯数字串转 number 否则 string)；增删。
- `ProducesSubform`：produces 行 name/algo/region(下拉 PRODUCE_REGIONS)；增删。
- `WhenSubform`：开关启用；`minBodyLen` / `onlySmaller` / `requireKey` / `appliesWith`(下拉已有 step.name) / `guards[]`(field/op[GUARD_OPS]/value 增删)；带 when 但未绑 flag 时红色提示「带 when 的步骤必须绑定 flag」（与 validateCodecSchema 一致）。关闭 when 时传 `when: undefined`，JSON.stringify 自动丢弃该键。

per-op 可见性：`isEncrypt`（encrypt 专属）/ `isStandaloneDigest`（checksum|hash 显示 over）。合法值集合全部复用 types/codec.ts 的 PIPELINE_OPS/ON_ERROR/OVER_KINDS/PRODUCE_REGIONS/GUARD_OPS（未复制）。

## 4. RouteKeyEditor（`codecEditor/RouteKeyEditor.tsx`）

- `routeKeyTemplate` 输入框 → `setRouteKeyTemplate`。
- 实时校验：占位正则（与 resourcesStore.ts 的 ROUTE_KEY_PLACEHOLDER_RE 对齐）提取所有 `{name}`，逐个比对 role:"route" 字段名；未知占位红色 Alert 列出（不阻塞编辑）。
- route 字段清单：列出所有 role:"route" 字段，Tag 形式展示 `{name}`（提示「可用 route 字段」）。
- 样例 routeKey：已知占位替换为字段名，未知占位原样保留 `{name}`（用函数 replacement 避免 `$` 特殊语义）。

## 5. AdapterTab 接线（`ResourcesDrawer.tsx`）

结构化视图（`showStructView && parsed.raw && parsed.schema`）原 FrameLayoutEditor 外层包一层 `<Space direction="vertical" size={12}>`，在其下追加 `<PipelineEditor>` + `<RouteKeyEditor>`，同一 `onEdit={setContent}` / `raw={parsed.raw}` / `schema={parsed.schema}`。

- errors.json 视图不受影响（仍只源码，`isErrorsView` 时 showStructView=false）。
- 视图切换、save/clear/import/liveErrors 逻辑全部不变（未触及）。
- ResourcesDrawer.tsx 实际改动（除 B2-A 既有行外）：+2 import（PipelineEditor/RouteKeyEditor）+ 1 个 `<Space>` 包裹块。

## 6. algo / params staging 说明

- **algo 文本输入**：本任务 algo 用文本输入而非下拉。Batch 3 §3.4 接 `GET /sbot/codec/algorithms` 端点后改为下拉。PipelineEditor 代码注释已标明。
- **params 通用键值表**：本任务 params 用通用 key/value 表（value 纯数字串转 number）。Batch 3 改为按算法动态字段。ParamsSubform 代码注释已标明。
- 本任务**未**拉取算法清单，符合 brief 约束。

## 7. 测试

新增 10 个单测追加到 `codecEditor/__tests__/codecEdit.test.ts`（pipeline 增删改/移动 7 个 + routeKeyTemplate 2 个 + serialize 稳定 1 个），覆盖：
- addPipelineStep 默认值 + partial 透传 + 不 mutate 入参 + 未知键保留。
- updatePipelineStep 局部 patch 保留其他键 + 越界安全降级。
- removePipelineStep + movePipelineStep 上/下移/越界保持原序。
- pipeline 非数组时 add 创建数组。
- serialize 稳定（未知键 + 原序不丢）。
- setRouteKeyTemplate 不 mutate + 未知键保留 + 空串可设。

按 TDD 流程：先写测试跑红（10 failed），再实现 helper 跑绿。

## 8. 验证结果

- `cd cmd/web && npx tsc -b` → **exit 0**（无错误）。
- `cd cmd/web && npm run test` → **17 files / 214 tests passed**（codecEdit 25 tests 含新增 10 个；既有全部不回归）。

## 9. 自审

- 改动严格限于 `codecEditor/`（codecEdit.ts 扩展 + 新增 PipelineEditor.tsx/RouteKeyEditor.tsx + codecEdit.test.ts）+ `ResourcesDrawer.tsx`。未动 services/、types/、B2-A 既有 FrameLayoutEditor/ByteStrip/HeaderFieldTable/RoleLinkedForm。
- 复用 codecEdit raw 无损模式（克隆不 mutate、serialize 稳定、不丢键/原序）。
- 合法值集合全部来自 types/codec.ts，未复制。
- 无兼容性兜底：非法值（op/over.kind 非法、未知占位）即时提示不静默；最终校验交 validateCodecSchema。
- 类型安全（tsc 绿）。
- 未 git commit。

## 10. git diff --stat

```
 cmd/web/src/components/modules/ResourcesDrawer.tsx | 86 ++++++++++++++++++----
 1 file changed, 71 insertions(+), 15 deletions(-)
```

注：codecEditor/ 整个目录在 B2-A 起即为未跟踪（untracked），本任务在该目录内扩展 codecEdit.ts + codecEdit.test.ts 并新增 PipelineEditor.tsx / RouteKeyEditor.tsx。ResourcesDrawer.tsx 的 86/71/15 计数包含 B2-A 既有但未提交的工作树改动（Segmented/useMemo/viewMode/parsed/showStructView 等），本任务的实际新增仅为：2 行 import（PipelineEditor/RouteKeyEditor）+ 1 个 `<Space>` 包裹块（含两个新组件挂载）。

codecEditor/ 目录当前文件：
```
codecEditor/
  ByteStrip.tsx        (B2-A)
  FrameLayoutEditor.tsx (B2-A)
  FrameScalars.tsx      (B2-A)
  HeaderFieldTable.tsx  (B2-A)
  RoleLinkedForm.tsx    (B2-A)
  PipelineEditor.tsx    ← 本任务新增
  RouteKeyEditor.tsx    ← 本任务新增
  byteLayout.ts         (B2-A)
  codecEdit.ts          ← 本任务扩展（+5 helper）
  codecEditor.css       (B2-A)
  __tests__/
    codecEdit.test.ts   ← 本任务扩展（+10 tests）
```
