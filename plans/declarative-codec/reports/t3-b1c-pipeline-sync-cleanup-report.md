# T3 Batch-1 任务 C 收尾报告 — 迁移 pipeline 消费方 + 重写 sync 内部 + 删全部旧 adapter API

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 任务来源：`plans/declarative-codec/briefs/t3-b1c-pipeline-sync-cleanup-brief.md`。
> 依赖：B1-A（新 API）、B1-B（UI 消费方已迁）。本任务完成后 Batch-1 工作树只剩新多 codec 模型。

## 1. 状态

**DONE**。`tsc -b` 退出 0；`npm run test` 175/175 通过；旧 adapter 符号全仓 grep 零命中。
未 `git commit`（按要求）。flow 引用→连接覆盖校验按 brief §3.5 指示**未做**（记入 §6 concerns）。

## 2. 改动文件清单（仅 4 个 service 文件）

| 文件 | 改动 |
|---|---|
| `cmd/web/src/services/taskActions.ts` | 上传收集 + 校验文案 + multipart + markResourcesAsBaselineSynced 调用 |
| `cmd/web/src/services/taskResourceDiff.ts` | collectTaskResourceNames + adapter 循环全量改多 codec |
| `cmd/web/src/services/resourcesStore.ts` | sync 内部重写 + 删旧 adapter/error API + 删 REQUIRED_ADAPTER_FUNCTIONS/validateAdapter + 签名调整 |
| `cmd/web/src/services/baselineApi.ts` | 删 fetchBaselineAdapter/fetchBaselineErrorMap |

> 工作树里另有 B1-A/B1-B 遗留的未提交改动（4 个 UI 文件 / luaApiSpec 测试 / types/codec.ts / 2 个新测试），
> 不属本任务范畴，本任务未触碰。无新增文件。

## 3. 实现要点

### 3.1 `taskActions.ts`

- **收集**：`Promise.all([listProto(), listScript(), listCodecFiles(), getErrorMap()])` 替旧的
  `getAdapterScript()/getErrorMapScript()`。
- **校验**：`codecs.length === 0` → `ApiError`（code `INVALID_ARGUMENT`，文案改「缺少协议配置，请在「协议配置」面板导入或新建」），
  替旧「缺少协议适配器」。**不**做 flow 引用连接覆盖校验（brief §3.5，记 §6）。
- **multipart**：每个 codec `fd.append('adapter/'+f.name, Blob(application/json), f.name)`；
  有 errorMap 则 `fd.append('adapter/errors.json', Blob(application/json), 'errors.json')`。
  删旧 `adapter/codec.lua` + `adapter/error.lua`。
- **markResourcesAsBaselineSynced**：调用签名从 `{protos,scripts,adapter,errorMap}` 改为
  `{protos,scripts,codecs,errorMap}`，传 `codecs` 数组。

### 3.2 `taskResourceDiff.ts`

- **collectTaskResourceNames**：`adapters` 从硬编码 `['codec.lua']` 改为 `(await listCodecFiles()).map(f=>f.name)`；
  若 `getErrorMap()` 存在则 push `'errors.json'`。
- **adapter 循环**：本地 `name==='errors.json' ? getErrorMap() : getCodecSchema(name)`；
  基线统一 `fetchBaselineCodec(name)`（后端按名透传 errors.json 与 codec）。
  删 `getAdapterScript/getErrorMapScript/fetchBaselineAdapter/fetchBaselineErrorMap` 全部调用。
- 文件头 import 同步更新。

### 3.3 `resourcesStore.ts` — sync 内部重写（adapter → 通用文件组）

核心思路：adapter 有了 index + 按名取写后，可**与 proto/scripts 同款走通用 `syncFileGroup`**，单文件特殊分支全部删除。

**前后对照（关键 6 个内部函数 + 2 个签名）：**

| 符号 | 旧（单文件） | 新（多文件，按 name） |
|---|---|---|
| `getResource('adapter', name)` | `name===ERROR_LUA_KEY ? getErrorMapScript() : getAdapterScript()` | `name===ERRORS_JSON_KEY ? getErrorMap() : getCodecSchema(name)` |
| `writeBaselineResource('adapter', name, content)` | `setErrorMapScriptFromBaseline / setAdapterScriptFromBaseline` | `setErrorMapFromBaseline / setCodecSchemaFromBaseline(name, ...)` |
| `deleteResource('adapter', name)` | `del(ERROR_LUA_KEY/CODEC_LUA_KEY, adapterStore)` | `del(name, adapterStore)`（通用，name 透传） |
| `fetchResourceBaseline('adapter', name)` | `fetchFileText(CODEC_BASELINE_URL)` / `error.lua` | `fetchBaselineCodec(name)`（统一） |
| `setResourceBaseHash('adapter', name, ...)` | 已是按名 `set(name, next, adapterStore)`，**保持**（不再引用旧 KEY 常量） |
| `syncResourcesFromBaseline` adapter 段 | 单 fetch codec.lua + error.lua + 2 段 reconcile（特殊路径） | `fetchBaselineCodecIndex()` + `syncFileGroup(codecIndex,'adapter',adapterStore,'.../adapter/',result)`（与 proto/scripts 同款） |
| `markResourcesAsBaselineSynced` 签名 | `{protos?,scripts?,adapter?:ResourceFile\|null,errorMap?}` | `{protos?,scripts?,codecs?:ResourceFile[],errorMap?}`；内部对 `codecs` 数组每项 `setResourceBaseHash('adapter', f.name, hash)` |
| `LastBaselineIndex.adapter` | `boolean`（adapterText !== null） | `string[]`（codecIndex = fetchBaselineCodecIndex() 结果） |

- `syncFileGroup` 用改好的 `getResource/writeBaselineResource/deleteResource/reconcileResourceWithServer`，
  URL `urlPrefix + encodeURIComponent(name)` 拼成 `/sbot/baseline/adapter/<name>`，与后端端点一致 ✓。
- 通用 3-way 逻辑（`reconcileResourceWithServer`/`compareResourceThreeWay`/`applyConflictResolution`/
  `syncFileGroup`/`subtractSyncResult`/`hasSyncDiff`）**全部未动**（已 type/name 参数化）。
- `migrateLegacyResources`（proto 旧 DB 迁移）未动。
- 新增 `import { fetchBaselineCodecIndex, fetchBaselineCodec } from './baselineApi';`（单向，无循环依赖）。

**人工 trace（mock added/unchanged/conflicts/removed）**：syncFileGroup 对每个基线 name——IDB 无→`writeBaselineResource` 写入并 `result.added.push`；都有且 `localHash===serverHash`→unchanged；baseHash===serverHash && localHash!==serverHash→localOnlyChanged（保留）；baseHash===localHash && serverHash!==localHash→serverOnlyChanged（自动采用）；双方都改且不同→conflict；服务器删但本地有→removed（按 baseHash 区分 serverRemovedOnly 自动删 / removedConflict 冲突）。与 proto/scripts 完全一致，无并行新逻辑。

### 3.4 删除的旧 API 清单

**`resourcesStore.ts` 删除**：
- 常量：`CODEC_LUA_KEY`、`ERROR_LUA_KEY`、`CODEC_BASELINE_URL`
- 函数：`getAdapterScript`、`setAdapterScript`、`setAdapterScriptFromBaseline`、`clearAdapterScript`、
  `getErrorMapScript`、`setErrorMapScript`、`setErrorMapScriptFromBaseline`、`clearErrorMapScript`
- 常量：`REQUIRED_ADAPTER_FUNCTIONS`
- 函数：`validateAdapter`

**`baselineApi.ts` 删除**：
- `fetchBaselineAdapter`
- `fetchBaselineErrorMap`

**禁止兼容性兜底**：无 codec.lua→codec.json 迁移、无 `??` fallback、无 shim 保留。

## 4. 验证结果

### 4.1 `tsc -b`
```
EXIT=0
```

### 4.2 `npm run test`（vitest run）
```
Test Files  15 passed (15)
     Tests  175 passed (175)
EXIT=0
```
（含 `codecStorage.test.ts` 11 / `validateCodecSchema.test.ts` 39 / `scriptSync.test.ts` 8 / `refsCheck.test.ts` 37 全绿，
无测试红。本任务未新增/改测试——taskActions/taskResourceDiff 的 sync 逻辑由既有 codecStorage/scriptSync 覆盖足够。）

### 4.3 旧符号全仓 grep 证据
```
$ git grep -rn "getAdapterScript\|setAdapterScript\|clearAdapterScript\|validateAdapter\|REQUIRED_ADAPTER_FUNCTIONS\|CODEC_LUA_KEY\|ERROR_LUA_KEY\|getErrorMapScript\|setErrorMapScript\|setErrorMapScriptFromBaseline\|clearErrorMapScript\|setAdapterScriptFromBaseline\|fetchBaselineAdapter\|fetchBaselineErrorMap\|CODEC_BASELINE_URL" -- cmd/web/src
（无输出）
---grep exit=1---
```
零命中 ✓。

### 4.4 `git diff --stat`（services 目录，4 个改动文件）
```
 cmd/web/src/services/baselineApi.ts      |  19 +-
 cmd/web/src/services/resourcesStore.ts   | 588 ++++++++++++++++++++++++++-----
 cmd/web/src/services/taskActions.ts      |  17 +-
 cmd/web/src/services/taskResourceDiff.ts |  19 +-
 4 files changed, 537 insertions(+), 106 deletions(-)
```
> 注：`resourcesStore.ts` 588 行 diff 含 B1-A 的新增（多 codec API 段、validateCodecSchema 等）叠加 B1-C 的删除，
> 因 Batch-1 整批未提交、相对 HEAD 累计。B1-C 净改动 = 删 ~120 行旧 adapter/error API + 改 sync 内部 6 函数。

## 5. 自审

- [x] 旧符号 grep 零命中（含测试）。
- [x] taskActions multipart 字段名严格 `adapter/<basename>` + `adapter/errors.json`，content-type `application/json`。
- [x] taskResourceDiff adapter 循环 errors.json/codec 分支正确，基线统一 `fetchBaselineCodec(name)`。
- [x] syncResourcesFromBaseline adapter 段与 proto/scripts 完全同款走 `syncFileGroup`。
- [x] `markResourcesAsBaselineSynced` 签名 `{codecs,errorMap}` 与 taskActions 调用一致。
- [x] `LastBaselineIndex.adapter` 形状自洽（`string[]`，来自 `fetchBaselineCodecIndex`）。
- [x] 无循环依赖（`resourcesStore → baselineApi` 单向）。
- [x] 无 `??` fallback、无 codec.lua 迁移 shim。
- [x] `tsc -b` 0 / `npm run test` 175/175。
- [x] 未 `git commit`。
- [x] 未触碰 B1-B 4 个 UI 文件 / types/codec.ts（除确认无残留旧 API import）。

## 6. Concerns / 遗留

1. **flow 引用→连接 codec 覆盖校验未做**（brief §3.5 明确不做）。当前 `codecs.length === 0` 仅兜底「一份 codec 都没有」，
   但若 flow 引用了连接 `xyz` 而用户未上传 `xyz` 对应的 codec 文件，前端不拦截。运行时由 agent resolver 在 dial 时
   fail-loud（中文报错）兜底。属增强项，建议后续 Batch 2 处理。
2. **Batch-1 整批未提交**：4 个 UI 文件 / luaApiSpec / types/codec.ts / 2 新测试是 B1-A/B1-B 遗留未提交，与本任务一起
   构成 Batch-1 完整改动。`git diff --stat` 数字含累计，提交前需复核 B1-A/B1-B 的改动是否已就绪。
