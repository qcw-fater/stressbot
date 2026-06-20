# T3 Batch-1 任务 C — 迁移 pipeline 消费方 + 重写 sync 内部 + 删全部旧 adapter API

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：B1-A（新 API）、B1-B（UI 消费方已迁）已完成。本任务是 **Batch-1 收尾**：把剩余 pipeline 消费方
> 迁到新模型、把基线 sync 内部从单文件改多文件、**删除全部旧 adapter API**。完成后整个工作树只剩新
> 多 codec 模型，旧 codec.lua/error.lua 前端路径零残留，Batch-1 可整体提交。

## 1. 任务定位

B1-A 加了新多 codec API，B1-B 迁了 UI 消费方（ResourcesDrawer/editorStore/index/RuntimeBar）。本任务迁
**剩余消费方**（`taskActions.ts` 上传、`taskResourceDiff.ts` 任务级 diff）+ 重写 `resourcesStore.ts` 的
**基线 sync 内部**（adapter 从单文件 codec.lua/error.lua 改为多文件 `*_codec.json`+`errors.json`）+
**删除全部旧 adapter API**（resourcesStore + baselineApi）。删完后旧符号全仓零调用 → 安全删定义。

## 2. 现状（先读码）

**先读** `cmd/web/src/services/taskActions.ts`（`createTask`/启动流，约 118-225：`getAdapterScript`/
`getErrorMapScript`/multipart `adapter/codec.lua`+`adapter/error.lua`/`markResourcesAsBaselineSynced`）、
`cmd/web/src/services/taskResourceDiff.ts`（`collectTaskResourceNames` 的 `adapters:['codec.lua']`、
`checkTaskResourcesAgainstBaseline` 的 adapter 循环用 `getAdapterScript`/`getErrorMapScript`/
`fetchBaselineAdapter`/`fetchBaselineErrorMap`）、
`cmd/web/src/services/resourcesStore.ts` 的 sync 内部（`getResource`/`writeBaselineResource`/`deleteResource`/
`setResourceBaseHash`/`syncResourcesFromBaseline`/`markResourcesAsBaselineSynced`/`fetchResourceBaseline`/
`applyConflictResolution`/`syncFileGroup`/`saveLastBaseline`/`LastBaselineIndex`，约 257-612）。

B1-A 已就绪的新 API（**用这些**）：`getCodecSchema/setCodecSchema/setCodecSchemaFromBaseline/clearCodecSchema/
listCodecFiles`、`getErrorMap/setErrorMap/setErrorMapFromBaseline/clearErrorMap`、`validateCodecSchema`、
`collectCodecSchemaErrors`；`baselineApi.ts` 的 `fetchBaselineCodecIndex/fetchBaselineCodec`。
后端契约（T4.3 + B1 前置）：multipart 字段 `adapter/<basename>`（basename=`<proto>_<service>_codec.json`
进 Codecs，`errors.json` 进 ErrorMap，旧 `codec.lua`/`error.lua` 被拒）；`GET /sbot/baseline/adapter/index.json`
返回 `string[]`（所有 `.json` 文件名）；`GET /sbot/baseline/adapter/{name}` 返回单文件文本。

## 3. 实现规格

### 3.1 `taskActions.ts` — 启动上传改多 codec

- 收集：`const codecs = await listCodecFiles();` + `const errorMap = await getErrorMap();`（替 `getAdapterScript`/`getErrorMapScript`）。
- **校验**：`codecs.length === 0` → 抛 `ApiError`（code `INVALID_ARGUMENT`，中文「缺少协议配置，请在「协议配置」面板导入或新建」），替旧「缺少协议适配器」。（flow 引用连接覆盖校验**本任务不做**——属 §3.5 增强，记入报告；agent 侧 resolver 在缺 codec 的连接 dial 时会 fail-loud 中文报错，是运行时兜底。）
- multipart：对每个 codec `fd.append('adapter/'+f.name, new Blob([f.content],{type:'application/json'}), f.name)`；
  有 errorMap 则 `fd.append('adapter/errors.json', new Blob([errorMap.content],{type:'application/json'}), 'errors.json')`。
  **删**旧 `adapter/codec.lua`/`adapter/error.lua` 两条 append。
- `markResourcesAsBaselineSynced` 调用：签名从 `{protos, scripts, adapter, errorMap}` 改 `{protos, scripts, codecs, errorMap}`
  （见 3.3），传 `codecs`（数组）+ `errorMap`。

### 3.2 `taskResourceDiff.ts` — 任务级 diff 改多 codec

- `collectTaskResourceNames`：`adapters: ['codec.lua']` → `adapters: (await listCodecFiles()).map(f => f.name)`；
  errorMap 存在则 push `'errors.json'`（替旧 `'error.lua'`）。
- `checkTaskResourcesAgainstBaseline` 的 adapter 循环：`name === 'errors.json' ? getErrorMap() : getCodecSchema(name)`
  取本地；`name === 'errors.json' ? fetchBaselineCodec('errors.json') : fetchBaselineCodec(name)` 取基线（统一用
  `fetchBaselineCodec(name)` 即可，因后端按名透传）。删 `getAdapterScript`/`getErrorMapScript`/`fetchBaselineAdapter`/`fetchBaselineErrorMap`。

### 3.3 `resourcesStore.ts` — sync 内部重写（adapter → 通用文件组）

核心思路：adapter 现在有 index 端点 + 按名取/写，可**与 proto/scripts 一样走通用 `syncFileGroup`**，不再
单文件特殊分支。逐个改：

- `getResource('adapter', name)`：`name === 'errors.json' ? getErrorMap() : getCodecSchema(name)`。
- `writeBaselineResource('adapter', name, content)`：`name === 'errors.json' ? setErrorMapFromBaseline(content) : setCodecSchemaFromBaseline(name, content)`。
- `deleteResource('adapter', name)`：`name === 'errors.json' ? clearErrorMap() : clearCodecSchema(name)`。
- `fetchResourceBaseline('adapter', name)`：`return fetchBaselineCodec(name);`（统一，后端按名透传 errors.json 与 codec）。
- `setResourceBaseHash('adapter', name, ...)`：已是按名 `set(name, next, adapterStore)` 通用，**保持**（但要确认不再引用 CODEC_LUA_KEY/ERROR_LUA_KEY）。
- `syncResourcesFromBaseline`：删旧的「单 codec.lua + 单 error.lua fetch + reconcile」段；改为
  `const codecIndex = await fetchBaselineCodecIndex();` 后调
  `await syncFileGroup(codecIndex, 'adapter', adapterStore, `${BASELINE_PREFIX}/adapter/`, result);`
  （与 proto/scripts 完全同款——syncFileGroup 用上面改好的 getResource/writeBaselineResource/deleteResource/reconcileResourceWithServer）。
  注意：`syncFileGroup` 的 `urlPrefix + encodeURIComponent(name)` 会拼成 `/sbot/baseline/adapter/<name>`，与后端端点一致 ✓。
  `saveLastBaseline` 的 `adapter` 字段可由 `codecIndex`（string[]）填充（见下）。
- `markResourcesAsBaselineSynced`：签名改 `{ protos?, scripts?, codecs?, errorMap? }`（`adapter?: ResourceFile|null`
  → `codecs?: ResourceFile[]`）；内部对 `codecs` 数组每项 `setResourceBaseHash('adapter', f.name, hash)`，
  errorMap 仍 `setResourceBaseHash('adapter', 'errors.json', hash)`。
- `LastBaselineIndex` / `saveLastBaseline`：`adapter: boolean` → `adapter: string[]`（codec+errors 文件名清单，
  来自 `fetchBaselineCodecIndex()`）。`syncResourcesFromBaseline` 末尾 `saveLastBaseline({ proto, script, adapter: codecIndex })`。
  （这只是 localStorage 快照，形状自洽即可。）
- `reconcileResourceWithServer` / `compareResourceThreeWay` / `applyConflictResolution` / `syncFileGroup` /
  `subtractSyncResult` / `hasSyncDiff` 等**通用逻辑不动**（它们已 type/name 参数化）。
- `migrateLegacyResources`（proto 旧 DB 迁移）**不动**。

### 3.4 删除全部旧 adapter API（确认零调用后）

先 `git grep -n` 全仓确认下列旧符号**零调用**（B1-B 已迁 UI、本任务 3.1/3.2/3.3 已迁其余），再删**定义**：
- `resourcesStore.ts` 删：`CODEC_LUA_KEY`、`CODEC_BASELINE_URL`、`ERROR_LUA_KEY`、`getAdapterScript`、
  `setAdapterScript`、`setAdapterScriptFromBaseline`、`clearAdapterScript`、`getErrorMapScript`、
  `setErrorMapScript`、`setErrorMapScriptFromBaseline`、`clearErrorMapScript`、`REQUIRED_ADAPTER_FUNCTIONS`、
  `validateAdapter`。
- `baselineApi.ts` 删：`fetchBaselineAdapter`、`fetchBaselineErrorMap`。
- 删后 `git grep -rn "getAdapterScript\|setAdapterScript\|clearAdapterScript\|validateAdapter\|REQUIRED_ADAPTER_FUNCTIONS\|CODEC_LUA_KEY\|ERROR_LUA_KEY\|getErrorMapScript\|setErrorMapScript\|fetchBaselineAdapter\|fetchBaselineErrorMap\|CODEC_BASELINE_URL" cmd/web/src` 应**全空**（含测试文件——若有测试引用旧函数，一并更新/删除）。

## 4. 全局约束（bind）

- **改动文件**：`taskActions.ts`、`taskResourceDiff.ts`、`resourcesStore.ts`（sync 内部 + 删旧 API 定义）、
  `baselineApi.ts`（删旧函数）。**严禁动** B1-B 已迁的 4 个 UI 文件（除非它们的 import 因删旧 API 而需微调——
  但 B1-B 应已不 import 旧符号，若有残留 import 需清理，属本任务范畴）。也严禁动 `types/codec.ts`（B1-A 产物）。
- **禁止兼容性兜底**：不写 codec.lua→codec.json 迁移、不用 `??` fallback、旧 API 一刀切删除（不保留 shim）。
- **请求收拢 services/**；UI 文本不暴露技术术语（错误文案用「协议配置」）。
- **不要 git commit**。
- `syncFileGroup` 复用要对齐 proto/scripts 的语义（added/unchanged/conflicts/removed 三方判断），不要新造并行逻辑。

## 5. 验证（全绿才算 DONE）

- `cd cmd/web && npx tsc -b` 退出 0。
- `cd cmd/web && npm run test` 通过（若有测试覆盖 taskActions/taskResourceDiff/resourcesStore sync，同步更新；
  确认删旧 API 后无测试红）。
- `git grep -rn` 旧符号清单（见 3.4）在 `cmd/web/src` **全空**。
- 自查 `git diff --stat`：改动文件限于上述 4 个（+ 可能测试）。
- 抽查 `syncResourcesFromBaseline` 新实现：用 mock 或人工 trace 确认 adapter 走 `syncFileGroup` 后，
  added/unchanged/conflicts/removed 与 proto 一致（基线有 IDB 无→added；都有且同→unchanged；都改且不同→conflict；等）。

## 6. 报告

写到 `plans/declarative-codec/reports/t3-b1c-pipeline-sync-cleanup-report.md`：实现要点、taskActions/taskResourceDiff
迁移点、sync 内部重写（adapter 走 syncFileGroup 的前后对照）、`markResourcesAsBaselineSynced`/`LastBaselineIndex`
签名变化、删除的旧 API 清单、`tsc -b`/`npm run test` 结果、旧符号 grep 全空证据、自审、贴 `git diff --stat`。

返回消息只含：① 状态；② 改动文件清单；③ 一行测试摘要；④ concerns。有歧义先问。
