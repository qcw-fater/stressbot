# T3 Batch-1 任务 B — UI 消费方迁移到多 codec 模型（报告）

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）
> 任务源：`plans/declarative-codec/briefs/t3-b1b-ui-migration-brief.md`
> 状态：**DONE**

## 1. 实现要点

把前端 UI 从旧单文件 codec.lua 模型迁到新多文件 codec.json 模型，严格限制在 4 个 UI 文件内。
旧 API（`getAdapterScript`/`validateAdapter`/`fetchBaselineAdapter`/`getErrorMapScript`/`fetchBaselineErrorMap`/
`REQUIRED_ADAPTER_FUNCTIONS` 等）**定义保留不动**（B1-C 才删），只是这 4 个消费方不再调用。

### 1.1 `ResourcesDrawer.tsx`（核心重写）

「适配器」tab → **「协议配置」tab**，结构为**源码 JSON 视图**（Batch 2 才做结构化可视化）：

- **import 改造**：移除 `getAdapterScript/setAdapterScript/setAdapterScriptFromBaseline/clearAdapterScript/
  validateAdapter/getErrorMapScript/setErrorMapScript/setErrorMapScriptFromBaseline/clearErrorMapScript` 与
  `fetchBaselineAdapter/fetchBaselineErrorMap`；新增 `getCodecSchema/setCodecSchema/setCodecSchemaFromBaseline/
  clearCodecSchema/listCodecFiles/getErrorMap/setErrorMap/setErrorMapFromBaseline/clearErrorMap/validateCodecSchema/
  collectCodecSchemaErrors` 与 `fetchBaselineCodecIndex/fetchBaselineCodec`。`Segmented` 换成 `Select` + `Input` +
  `Modal`（新建/复制弹窗）。
- **删除旧物**：`ADAPTER_TEMPLATE`、`ERROR_MAP_TEMPLATE`、`CODEC_SPEC`、`ERROR_MAP_SPEC`、`SpecBlock`、
  `AdapterFileKey`、`ADAPTER_FILE_LABELS`，以及内嵌的「接口规范」二级 tab（codec.lua 函数契约已不再适用）。
- **新增 `CODEC_JSON_TEMPLATE`**（最小合法 schema，新建连接直接通过 `validateCodecSchema`）：
  - `version: 1`、`endianDefault: "le"`
  - `frame: { headerSize: 8, trailerSize: 0, lengthIncludesHeader: false, lengthIncludesTrailer: false }`
  - `header`：1 个 `role:"length"`（u32, offset 0）+ 2 个 `role:"route"`（`cmd` u8 offset 4、`act` u8 offset 5）
    + 1 个 `role:"value"`（`index` u16 offset 6, `source:{kind:"const",value:0}`）—— 物理区间不重叠、
    填满 headerSize=8（剩 offset 7 是 reserved 余量，未列字段不影响校验，因校验只要求声明的字段不越界不重叠）
  - `routeKeyTemplate: "{cmd}:{act}"`（占位指向真实 route 字段）
  - `pipeline: []`（空管线合法）
- **新增 `EMPTY_ERROR_MAP_TEMPLATE`**（空对象 `{}`），errors.json 未保存时载入。
- **连接选择器**（`Select`）：选项 = `listCodecFiles()` 解析出的连接名 `<proto>:<service>` + 固定项「错误码映射（共享）」
  （`__errors__` 哨兵值）。切选项即 `loadConn`。
- **新建/复制**：弹 `Modal` + `Input`，输入 `<协议>:<服务名>`。`validateConnName` 校验：proto∈{tcp,udp}、
  service 非空且不含 `:`/`_`、不与已存文件重名；非法/重名给中文报错并拒绝（**不静默兜底**）。
  新建用 `CODEC_JSON_TEMPLATE`，复制取当前选中连接的 `getCodecSchema` 内容；落库前**再过一次** `validateCodecSchema`
  （模板/副本本身必须合法才允许创建）→ `setCodecSchema(fileName, content)`。
- **删除**：`modal.confirm` 二次确认 → `clearCodecSchema(name)` → 重载列表 + 切到剩余第一项 → 刷新徽标。
- **源码编辑器**：Monaco `language="json"`、`automaticLayout: true`。实时校验当前 codec 内容（仅 codec，
  `__errors__` 不参与结构校验），有错时下方 `Alert` 列出前 8 条（warning，不阻塞输入）。
- **保存**（`onSave`）：
  - codec：`validateCodecSchema(content)` **有错则 `message.error` 拒绝落库**（阻塞），无错 `setCodecSchema`
    → 刷新徽标 → 成功提示。
  - errors.json：仅 `JSON.parse` 合法性校验，有错拒绝；合法则 `setErrorMap`。
- **导入**（`onUpload`，accept `.json`）：读文本 → codec 走 `validateCodecSchema` / errors 走 `JSON.parse`
  → 有错拒绝；合法 `setCodecSchema`/`setErrorMap` → 刷新列表/徽标。
- **清空**（`onClear`）：codec 走 `modal.confirm` → `clearCodecSchema` → 切剩余项/清空 → 刷新徽标；
  errors.json 直接 `clearErrorMap` 并载入空模板。
- **从基线载入**：`fetchBaselineCodecIndex()` → 逐个 `fetchBaselineCodec(name)` →
  `*_codec.json` 走 `setCodecSchemaFromBaseline`、`errors.json` 走 `setErrorMapFromBaseline`；
  失败中文提示。完成后重载列表 + 切首项 + 刷新徽标。
- **辅助函数**：`connNameToFileName`（`tcp:logic` → `tcp_logic_codec.json`）、`fileNameToConnName`（反向）、
  `validateConnName`。常量 `CODEC_FILE_SUFFIX='_codec.json'`、`ERRORS_JSON_KEY='errors.json'`、
  `CODEC_PROTOS=['tcp','udp']`。

### 1.2 `editorStore.ts`

- `adapterMissing: string[] | null` + `setAdapterMissing` → **`codecSchemaErrors: string[] | null`** +
  `setCodecSchemaErrors`（语义：codec schema 校验错误数组；null=未校验/空）。
- 删死代码 `codecAdapter` panel kind（`ActivePanel` 联合类型 + `DEFAULT_SIZES` 条目）。

### 1.3 `FlowEditor/index.tsx`（挂载校验）

- import `validateAdapter` → `collectCodecSchemaErrors`。
- 挂载 effect：`collectCodecSchemaErrors()` → `setCodecSchemaErrors(errors)`（替 `validateAdapter` +
  `setAdapterMissing`）。注释从「适配器校验 / codec.lua 必需函数」改为「协议配置校验 / *_codec.json schema」。

### 1.4 `RuntimeBar.tsx`（徽标）

- `adapterMissing` → `codecSchemaErrors`（`useShallow` 解构 + 徽标 `count` + `color` 三处）。
- tooltip：「适配器缺少 N 个必需函数」→「协议配置有 N 处问题」；备选「资源管理（proto / lua / 协议配置）」。
- 注释行清理：「设置 Popover 用到的 UI 状态 + 打开协议适配器面板的 setter」→「设置 Popover 用到的 UI 状态」。

## 2. `CODEC_JSON_TEMPLATE`（完整内容）

```json
{
  "version": 1,
  "endianDefault": "le",
  "frame": { "headerSize": 8, "trailerSize": 0, "lengthIncludesHeader": false, "lengthIncludesTrailer": false },
  "header": [
    { "name": "bodyLen", "offset": 0, "size": 4, "type": "u32", "endian": "le", "role": "length" },
    { "name": "cmd",     "offset": 4, "size": 1, "type": "u8",  "role": "route" },
    { "name": "act",     "offset": 5, "size": 1, "type": "u8",  "role": "route" },
    { "name": "index",   "offset": 6, "size": 2, "type": "u16", "endian": "le", "role": "value", "source": { "kind": "const", "value": 0 } }
  ],
  "routeKeyTemplate": "{cmd}:{act}",
  "pipeline": []
}
```

通过 `validateCodecSchema`（在 `submitCreate` 内落库前再校验一次确认）。header 字段物理区间
[0,4)、[4,5)、[5,6)、[6,8) 不重叠且都在 headerSize=8 内；size 与 type 宽度匹配（u32=4、u8=1、u16=2）。

## 3. 验证结果

### 3.1 `npx tsc -b`

```
（无输出，退出码 0）
```

### 3.2 `npm run test`

```
Test Files  15 passed (15)
     Tests  175 passed (175)
  Duration  6.11s
```

既有测试无断言 `adapterMissing`/`validateAdapter` 行为（grep 全仓 `*.test.{ts,tsx}` 零命中），
无需更新测试。

## 4. 自审

- **scope**：`git diff --stat` 仅 4 个目标文件（见下）。`taskActions.ts`/`taskResourceDiff.ts`/
  `resourcesStore.ts`/`baselineApi.ts`（旧函数）**未动**——旧 API 定义保留（见下 grep 证据）。
- **旧符号迁移**：4 文件内 `git grep "getAdapterScript\|validateAdapter\|fetchBaselineAdapter\|
  getErrorMapScript\|adapterMissing\|setAdapterMissing"` **零命中**。
- **旧 API 保留**：`resourcesStore.ts` 的 `getAdapterScript`/`validateAdapter`、`baselineApi.ts` 的
  `fetchBaselineAdapter` 仍在原位（B1-C 删）。
- **禁止兼容性兜底**：无 codec.lua→codec.json 迁移、无 `??` 兜错误；命名非法/重名直接中文报错拒绝。
- **已知非法 schema 不静默落库**：保存、导入、新建、复制四处均在落库前 `validateCodecSchema`（codec）/
  `JSON.parse`（errors），有错 `message.error` 拒绝。
- **UI 文本不暴露技术术语**：面板名「协议配置」、按钮「新建/复制/删除/从基线载入/保存/清空/导入」、
  tooltip「协议配置有 N 处问题」「资源管理（proto / lua / 协议配置）」——无 codec/schema/adapter/codec.json
  面向用户；开发变量名（`codecSchemaErrors`、`CODEC_JSON_TEMPLATE` 等）保留。
- **请求收拢 services/**：baseline 载入走 `fetchBaselineCodecIndex/fetchBaselineCodec`，无直接 fetch。
- **类型安全**：用 `ResourceFile`（resourcesStore 导出）；未直接 import `CodecSchema`（编辑器以文本为
  单一数据源，`validateCodecSchema` 内部 JSON.parse + 类型断言）。

### 4.1 `git diff --stat`（4 目标文件）

```
 cmd/web/src/components/FlowEditor/index.tsx        |  12 +-
 .../src/components/FlowEditor/store/editorStore.ts |  14 +-
 cmd/web/src/components/modules/ResourcesDrawer.tsx | 767 +++++++++++++--------
 cmd/web/src/components/runtime/RuntimeBar.tsx      |  14 +-
 4 files changed, 493 insertions(+), 314 deletions(-)
```

### 4.2 scope grep 证据

```
$ git grep -n "getAdapterScript\|validateAdapter\|fetchBaselineAdapter\|getErrorMapScript\|adapterMissing\|setAdapterMissing" \
    -- cmd/web/src/components/modules/ResourcesDrawer.tsx \
       cmd/web/src/components/FlowEditor/store/editorStore.ts \
       cmd/web/src/components/FlowEditor/index.tsx \
       cmd/web/src/components/runtime/RuntimeBar.tsx
（exit=1，零命中）

$ git grep -n "export async function validateAdapter\|export async function getAdapterScript\|export async function fetchBaselineAdapter" \
    -- cmd/web/src/services/resourcesStore.ts cmd/web/src/services/baselineApi.ts
cmd/web/src/services/baselineApi.ts:63:export async function fetchBaselineAdapter(): Promise<string | null> {
cmd/web/src/services/resourcesStore.ts:191:export async function getAdapterScript(): Promise<ResourceFile | undefined> {
cmd/web/src/services/resourcesStore.ts:764:export async function validateAdapter(): Promise<string[]> {
（旧 API 定义仍在原位）
```

## 5. 注意事项 / 后续

- 本任务为 **Batch 1 源码 JSON 视图**；**Batch 2** 才在 JSON 数据源上挂结构化可视化编辑器（帧布局表/
  字节条带/pipeline 卡片，双向同步到同一 JSON 文本）。
- B1-C 将删除旧 API 定义与本任务 4 文件外残留的旧符号引用（如 `taskActions`/`taskResourceDiff` 内的
  adapter 路径）。
