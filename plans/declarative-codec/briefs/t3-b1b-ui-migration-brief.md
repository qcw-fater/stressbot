# T3 Batch-1 任务 B — 迁移 UI 消费方到多 codec 模型（ResourcesDrawer 源码编辑器 + 校验接线）

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：**B1-A 已完成**——新 API 已就绪（`resourcesStore.ts` 的 `getCodecSchema/setCodecSchema/
> setCodecSchemaFromBaseline/clearCodecSchema/listCodecFiles`、`getErrorMap/setErrorMap/setErrorMapFromBaseline/
> clearErrorMap`、`validateCodecSchema`、`collectCodecSchemaErrors`；`baselineApi.ts` 的
> `fetchBaselineCodecIndex/fetchBaselineCodec`；`types/codec.ts`）。旧 API 仍存在（B1-C 才删）。
> 本任务**只迁移 UI 消费方**，不动 pipeline 消费方、不动 sync 内部、不删旧 API。

## 1. 任务定位

B1-A 已新增多 codec 存储/校验/baseline API（纯增量）。本任务把**前端 UI 消费方**从旧单文件 codec.lua 模型
迁到新多文件 codec.json 模型：资源面板「适配器」tab 改为「协议配置」tab（**源码 JSON 编辑器**为主视图，
按连接选择/新建/复制/删除多份 `*_codec.json` + 共享 `errors.json`），并把挂载校验与徽标从「缺失 Lua 函数」
改为「codec schema 错误」。

**Batch 1 的 ResourcesDrawer 是「源码 JSON 视图」**（Monaco `language="json"`）——结构化可视化编辑器
（帧布局表/字节条带/pipeline 卡片）是 **Batch 2（§3.3-visual）**，本任务不做。源码视图以 JSON 对象为
单一数据源，Batch 2 的结构化视图会挂在这同一数据源上（双向同步），故本任务先把 JSON 数据源 + 连接选择器
+ 校验落地。

## 2. 现状（先读码）

**先读** `cmd/web/src/components/modules/ResourcesDrawer.tsx`（adapter tab：约 40-60 import、155-360
handler、对应 JSX），理解当前结构：Monaco `language="lua"` 编辑 codec.lua + error.lua、`ADAPTER_TEMPLATE`、
`CODEC_SPEC`、save/clear/import、`getAdapterScript/setAdapterScript/setAdapterScriptFromBaseline/
clearAdapterScript/validateAdapter`、`getErrorMapScript/setErrorMapScript/...`、
`fetchBaselineAdapter/fetchBaselineErrorMap`。

再读 `cmd/web/src/components/FlowEditor/store/editorStore.ts`（`adapterMissing`/`setAdapterMissing`，
约 133-134/176-177；另有死代码 `codecAdapter` panel kind 约 14-22/248——**本任务一并删**）、
`cmd/web/src/components/FlowEditor/index.tsx`（挂载校验约 90-106，调 `validateAdapter`）、
`cmd/web/src/components/runtime/RuntimeBar.tsx`（徽标约 113-125/450-466，读 `adapterMissing`）。

## 3. 实现规格

### 3.1 ResourcesDrawer.tsx ——「协议配置」tab（核心）

- **连接选择器**（tab 顶部）：列出 `listCodecFiles()`，每项显示 `<proto>:<service>`（由文件名
  `<proto>_<service>_codec.json` 解析：去 `_codec.json` 后缀，首个 `_` 换 `:`）。提供：
  - **新建**：输入 `<proto>:<service>` → 文件名 `<proto>_<service>_codec.json`；校验名合法（proto∈{tcp,udp}、
    service 非空、整体匹配 `<proto>_<service>_codec.json` 且不与已存重名）；用 `CODEC_JSON_TEMPLATE`
    （见下）初始化 → `setCodecSchema`。名非法/重名给中文报错，**不静默兜底**。
  - **复制**：把当前选中连接克隆为新连接名（同新建命名校验）→ `getCodecSchema(src)` → `setCodecSchema(dst, content)`。
  - **删除**：`clearCodecSchema(name)`（二次确认）。
- **源码编辑器**（选中连接后）：Monaco `language="json"`。编辑当前 codec 文本。
  - **保存**：`validateCodecSchema(content)` → 有错则在编辑器下方列出中文错误（不阻塞保存？**阻塞**：有错
    时保存按钮置灰或保存时报错并拒绝落库——选其一，**禁止**把已知非法 schema 静默落库）；无错 →
    `setCodecSchema(name, content)` → 刷新 `codecSchemaErrors`（调 `collectCodecSchemaErrors`）。
  - **导入**：选 `.json` 文件 → 读文本 → `validateCodecSchema` → 同保存校验 → `setCodecSchema`。
  - **从基线载入**（按钮）：`fetchBaselineCodecIndex()` → 对每个 `*_codec.json` 名 `fetchBaselineCodec(name)`
    → `setCodecSchemaFromBaseline(name, text)`；`fetchBaselineCodec('errors.json')` → `setErrorMapFromBaseline(text)`。
    失败给中文提示，不静默。
- **errors.json 编辑器**（次级，同 tab 内独立小区块）：Monaco `language="json"`（或简单 code→中文 表格，
  **源码 JSON 视图优先**，表格留 Batch 2）。`getErrorMap/setErrorMap`。保存前 `JSON.parse` 校验合法。
- **删除旧物**：`ADAPTER_TEMPLATE`（Lua）、`CODEC_SPEC`（Lua 函数契约文案）、codec.lua/error.lua 的
  Monaco（`language="lua"`）编辑器、所有 `getAdapterScript/setAdapterScript/.../validateAdapter/
  getErrorMapScript/setErrorMapScript/.../fetchBaselineAdapter/fetchBaselineErrorMap` 调用。
- **新增 `CODEC_JSON_TEMPLATE`**：一份**最小合法** CodecSchema（version=1、endianDefault="le"、frame
  {headerSize 合理, trailerSize:0}、header 含 1 个 role:"length" + ≥1 个 role:"route" 字段、
  routeKeyTemplate 用该 route 字段、pipeline 可空 `[]` 或含一个示例）。用它新建连接能直接通过
  `validateCodecSchema`。可参考 `conf/adapter/tcp_logic_codec.json` 的结构（但模板要最小化、通用）。

### 3.2 editorStore.ts

- `adapterMissing: string[] | null` + `setAdapterMissing` → **重命名** `codecSchemaErrors: string[] | null`
  + `setCodecSchemaErrors`（语义：codec schema 校验错误数组，null=未校验/空）。
- **删死代码** `codecAdapter` panel kind（约 14-22 类型、248 用法）——与协议 codec 概念无关的遗留。

### 3.3 FlowEditor/index.tsx（挂载校验）

- 挂载 effect：`validateAdapter()` → 改 `collectCodecSchemaErrors()` → `setCodecSchemaErrors(errors)`。

### 3.4 RuntimeBar.tsx（徽标）

- 读 `codecSchemaErrors`（替 `adapterMissing`）；tooltip 「适配器缺少 N 个必需函数」→「协议配置有 N 处问题」
  （**UI 文本用「协议配置」，不暴露 codec/schema/adapter 术语**）；徽标数 = `codecSchemaErrors?.length ?? 0`；
  顺手清约 113 行过时注释。

## 4. 全局约束（bind）

- **仅迁移这 4 个 UI 文件**：`ResourcesDrawer.tsx`、`editorStore.ts`、`FlowEditor/index.tsx`、`RuntimeBar.tsx`
  （+ 可能的小共享常量）。**严禁动** `taskActions.ts`、`taskResourceDiff.ts`、`resourcesStore.ts` 的 sync
  内部与旧 API 定义、`baselineApi.ts` 旧函数（那些是 B1-C）。旧 API（`getAdapterScript`/`validateAdapter`/
  `getErrorMapScript`/`fetchBaselineAdapter`/…）**保持定义**（B1-C 删），只是本任务的 4 个文件不再调用它们。
- **禁止兼容性兜底**：不写 codec.lua→codec.json 迁移、不用 `??` 兜错误、新字段全链路一致。
- **UI 文本不暴露技术术语**：面板/按钮/tooltip 用「协议配置」「连接」等，**不**出现 codec/schema/adapter/
  codec.json（开发变量名可保留 codec）。
- **请求收拢 services/**：组件不直接 fetch；baseline 载入走 `baselineApi` 的新函数。
- **类型安全**：用 `types/codec.ts` 的类型（如需解析 JSON 成 `CodecSchema`）。
- **已知非法 schema 不静默落库**：保存/导入时 `validateCodecSchema` 有错必须拦下并提示。
- **不要 git commit**。

## 5. 验证（全绿才算 DONE）

- `cd cmd/web && npx tsc -b` 退出 0。
- `cd cmd/web && npm run test` 通过（含既有 ResourcesDrawer/editorStore/RuntimeBar 相关测试若有；若有测试
  断言旧的 adapterMissing/validateAdapter 行为，同步更新为 codecSchemaErrors/collectCodecSchemaErrors）。
- 自查：`git diff --stat` 证明只动了上述 4（+小常量）文件；`git grep -n "getAdapterScript\|validateAdapter\|fetchBaselineAdapter\|getErrorMapScript\|adapterMissing\|setAdapterMissing"` 在这 4 文件内**零命中**（旧符号已迁出），但**不在 taskActions/taskResourceDiff/resourcesStore/baselineApi 里删**（B1-C 才删）。

## 6. 报告

写到 `plans/declarative-codec/reports/t3-b1b-ui-migration-report.md`：实现要点、ResourcesDrawer 新结构
（连接选择器 + 源码编辑器 + errors 区 + 模板/载入/保存/校验流）、editorStore/index/RuntimeBar 改动、
`CODEC_JSON_TEMPLATE` 内容、`tsc -b`/`npm run test` 结果、自审、贴 `git diff --stat` + grep 证明 scope。

返回消息只含：① 状态；② 改动文件清单；③ 一行测试摘要；④ concerns。有歧义先问。
