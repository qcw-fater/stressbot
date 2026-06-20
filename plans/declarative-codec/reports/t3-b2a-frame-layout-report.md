# T3 Batch-2 任务 A — 结构化编辑骨架 + 帧布局编辑器（报告）

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`
> Brief：`plans/declarative-codec/briefs/t3-b2a-frame-layout-brief.md`
> 状态：DONE（tsc exit 0；vitest 204 通过含新增 15；diff scope 限于 `codecEditor/` + `ResourcesDrawer.tsx`）

## 1. 实现要点

在 Batch 1 的「协议配置」tab（源码 Monaco 视图）旁加**结构化视图**（帧布局编辑器），两者以 content 字符串为单一数据源双向同步。新组件全部抽到独立目录 `cmd/web/src/components/modules/codecEditor/`，ResourcesDrawer.tsx 仅做视图切换接线。

## 2. raw-object 无损同步模型（codecEdit.ts）

核心：`JSON.parse` 保留全部键（含未知键）与原始键序，结构化编辑走「深拷贝 → 改对应位置 → serializeCodec」纯函数链，**不 mutate 入参、不重排整个文档**。

- `parseCodecForEdit(content): {raw, schema, error}`
  - raw = `JSON.parse` 原结果（lossless，保留未知键与原序）；
  - schema = 同一对象按 CodecSchema 宽松读（仅「对象」时给 typed 视图，字段缺失不报错——结构合法性交给 `validateCodecSchema`）；
  - 非法 JSON → raw/schema=null + 中文 error（**不静默兜底**，直接提示切源码）。
- `serializeCodec(raw)` = `JSON.stringify(raw, null, 2)`（确定性、保留键序）。
- `addHeaderField / updateHeaderField / removeHeaderField / moveHeaderField(raw, …)`：克隆 raw → 操作 `raw.header` 数组（按 index）→ serializeCodec 返回新字符串；`raw.header` 非数组或 index 越界时安全降级（返回原 content，不抛错）。
- `setCodecScalar(raw, path, value)`：path ∈ `{version, endianDefault, frame.headerSize, frame.trailerSize, frame.lengthIncludesHeader, frame.lengthIncludesTrailer}`；frame.* 嵌套写入，确保 `raw.frame` 是对象后再改。

所有 mutate helper **不 mutate 入参**（单测 `expect(raw!).toEqual(snapshot)` 断言）。

## 3. FrameLayoutEditor 结构

外壳（`FrameLayoutEditor.tsx`）组合 4 个子区，持有 `selectedIndex`（当前选中字段）：

1. **FrameScalars**（`FrameScalars.tsx`）：version（InputNumber）/ endianDefault（le|be Radio）/ headerSize / trailerSize（InputNumber）/ lengthIncludesHeader / lengthIncludesTrailer（Radio）→ 经 `setCodecScalar`。
2. **ByteStrip**（`ByteStrip.tsx`）+ `byteLayout.ts`：一条 `0..headerSize-1` 的彩色条带，每字段占 `[offset, offset+size)`，标 name/offset+size/type；点击区块选中对应字段（高亮 + outline）。**仅展示 + 选中**（代码注释标 DnD-resize 留后续，本 scope 决策）。越界/重叠段标红（`computeByteRanges` 计算）；trailerSize>0 时在 header 段后画灰色 trailer 区。偏移标尺（0/中/末）。
3. **HeaderFieldTable**（`HeaderFieldTable.tsx`）：列 name/offset/size/type(下拉 u8..bytes，选项带宽度提示)/endian(le|be|默认)/role(下拉 7 角色)/操作(↑↓移序/删除)。type 宽度即时提示用 `FIELD_TYPE_WIDTH`（size≠type 宽度时 warning 文案，不阻塞输入）。表底「+ 添加字段」（默认追加在 headerSize 处的 reserved u8）。行选中联动 RoleLinkedForm；bad 行左侧细红条。
4. **RoleLinkedForm**（`RoleLinkedForm.tsx`）：按 role 分支
   - `route`：只读提示（routeKey 模板编辑器在 B2-B）；
   - `flags`：命名位编辑器（bits:[{name,bit}]，bit∈[0,size×8) 客户端提示，增删）；
   - `checksumOut`：from 文本输入 + `<step>.<output>` 格式提示（pipeline 在 B2-B）；
   - `value`：source.kind 下拉（v1 const/route 可选，state/counter/timestamp 置灰标 v1.1）+ const→value 数字 / route→key 文本；
   - `errorCode`：提示（绑定后启用服务端错误码识别 + errors.json）；
   - `length`/`reserved`：无额外配置。

`codecEditor.css`：内联 input 轻样式（`.flet-input`）+ bad 行红条（`.flet-row-bad`）。

## 4. AdapterTab 视图切换接线（ResourcesDrawer.tsx）

仅改 `ResourcesDrawer.tsx` 的 `AdapterTab`：

- import 加 `Segmented`、`useMemo`、`parseCodecForEdit`、`FrameLayoutEditor`。
- 新 state `viewMode: 'struct' | 'source'`（默认 `struct`）。
- `const parsed = useMemo(() => parseCodecForEdit(content), [content])`。
- `isErrorsView = activeConn === '__errors__'`；`showStructView = !isErrorsView && viewMode === 'struct' && parsed.schema !== null`。
- 编辑器区上方：非 errors 视图时渲染 Segmented 切换（结构化 | 源码），errors 视图隐藏。
- `showStructView` 且 raw/schema 都有 → 渲染 `<FrameLayoutEditor raw schema onEdit={setContent} />`（scrollable 容器）。
- 否则降级显示 Monaco：当 codec 视图 + struct 模式 + parsed.error 非空时多显示一条 Alert「源码不是合法 JSON，请切到源码视图修正」+ error 明细。
- save/clear/import/liveErrors 逻辑**不变**（都基于 content；结构化编辑已通过 `onEdit=setContent` 写入 content）。

## 5. 测试用例（codecEdit.test.ts，15 条）

TDD：先写测试跑红再实现。

- **parseCodecForEdit**：合法 JSON → raw+schema+error=null；保留未知键（customMarker / 带空格的 trailingComment）；非法 JSON → 中文 error；解析为数组 → schema=null 但 raw 保留（不抛错）。
- **serializeCodec**：parse→serialize 字节级 round-trip（对齐 `JSON.stringify(JSON.parse(...),null,2)`）；保留原始键序（version < endianDefault < customMarker < frame）；2 空格缩进确定性。
- **header 增删改**：addHeaderField 追加到末尾 + 不 mutate 入参；updateHeaderField 局部 patch 保留该字段其他键；removeHeaderField 删除指定 index；moveHeaderField 上下移交换相邻 + 越界保持原序；header 非数组时安全降级（4 个操作都不抛错）。
- **setCodecScalar**：version/endianDefault 顶层；frame.headerSize/trailerSize/lengthIncludes* 嵌套（其他 frame 键保留）；不 mutate 入参。

## 6. 验证结果

```
$ npx tsc -b                       # exit 0，无输出
$ npm run test
Test Files  17 passed (17)
     Tests  204 passed (204)        # 含新增 codecEdit 15；既有 189 不回归
```

## 7. 自审（对照 brief 约束）

- ✅ 无损 round-trip：raw 保留未知键与原序（单测断言键序）；mutate helper 不重排整个文档、不丢键。
- ✅ 禁止兼容兜底：非法 JSON 直接提示切源码，不静默；非对象 schema 不假装成 CodecSchema。
- ✅ UI 文案：面板用「帧布局/字段/字节/帧布局参数」；列名 type/role/offset/size/endian/version 保留（配置术语必要）；面板文案未出现 codec/schema（变量名保留）。
- ✅ 复用 validateCodecSchema + types/codec.ts（FIELD_TYPES/FIELD_ROLES/FIELD_TYPE_WIDTH/VALUE_SOURCE_KINDS_SUPPORTED）——未复制合法值表。
- ✅ 字节条带 scope：仅展示 + 点击选中，未做拖拽改跨度；注释标 DnD-resize 留后续。
- ✅ 类型安全：tsc -b exit 0（strict + noUnusedLocals/Parameters）。
- ✅ 新文件全在 `codecEditor/`；仅改 ResourcesDrawer.tsx；未动 services/、types/、其他组件。
- ✅ 未 git commit。

## 8. git diff --stat

```
 cmd/web/src/components/modules/ResourcesDrawer.tsx | 80 ++++++++++++++++++----
 1 file changed, 65 insertions(+), 15 deletions(-)
```

新增（untracked）`cmd/web/src/components/modules/codecEditor/`：

```
ByteStrip.tsx
FrameLayoutEditor.tsx
FrameScalars.tsx
HeaderFieldTable.tsx
RoleLinkedForm.tsx
byteLayout.ts
codecEdit.ts
codecEditor.css
__tests__/codecEdit.test.ts
```

## selectedIndex 移动跟随修正

**问题（B2-A review Minor）**：`FrameLayoutEditor.tsx` 持有 `selectedIndex`，但 `HeaderFieldTable.tsx` 的 ↑↓ 按钮只调 `onEdit(moveHeaderField(...))`，未更新选中态——移动后高亮仍指向原 index，视觉上选中了被交换过来的另一字段。删除字段已正确处理（`selectedIndex === idx → onSelect(null)`），移动字段缺失。

**改动文件**：`cmd/web/src/components/modules/codecEditor/HeaderFieldTable.tsx`

**改动行**：↑↓ 按钮的 `onClick`（原 153、162 行；现 ~153–172 行）。

- 上移按钮 `onClick`：`onEdit(moveHeaderField(raw, idx, -1))` 之后追加 `if (idx - 1 >= 0) onSelect(idx - 1);`
- 下移按钮 `onClick`：`onEdit(moveHeaderField(raw, idx, 1))` 之后追加 `if (idx + 1 < fields.length) onSelect(idx + 1);`

**选中跟随语义**：移动成功后选中态更新到字段移动后的新 index（上移 → idx-1，下移 → idx+1），保持高亮跟随被移动的字段。越界分支（`idx-1 < 0` / `idx+1 >= length`）跳过 `onSelect`——这与 `moveHeaderField` 的越界「返回原序」语义一致（且按钮本身在边界已 `disabled`，此处为防御性判断）。

**验证**：
- `cd cmd/web && npx tsc -b` → EXIT=0
- `cd cmd/web && npm run test` → 17 文件 / 214 用例全过（既有不回归；高亮无单测，靠 tsc + 代码自洽）

**未改动**：`FrameLayoutEditor.tsx`（仍只持有 `selectedIndex` + 透传 `onSelect`）、`codecEdit.ts`、其他文件、逻辑分支。未执行 git commit。
