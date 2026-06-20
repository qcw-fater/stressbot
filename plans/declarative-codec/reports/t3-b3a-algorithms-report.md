# T3 Batch-3 任务 A — 算法清单接线 + Pipeline algo 下拉/动态 params 报告

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`，未 commit）。
> 需求：`plans/declarative-codec/briefs/t3-b3a-algorithms-brief.md`。
> 状态：**DONE**。

## 1. 实现要点

### 1.1 类型追加（`cmd/web/src/types/codec.ts`）

镜像后端权威定义（`codec/registry.go:172-186` + `codec/preview.go:24-41` + `admin/codec_handlers.go:29-37`），camelCase 与 Go json tag 对齐：

- `AlgoParam { name; type: 'int'|'string'|'bool'|'bytes'; default?; description? }`
- `AlgoMeta { name; op: 'cipher'|'compress'|'checksum'|'hash'; description?; params?: AlgoParam[] }`
  - 注释标注 **encrypt↔cipher 映射 gotcha**（`PipelineStep.op='encrypt'` ↔ `AlgoMeta.op='cipher'`）。
- `PreviewField { name; value: number; offset: number; size: number }`
- `PreviewResult { mode: 'encode'|'decode'; frameHex?; bodyHex?; routeKey?; headerErr?; fields?: PreviewField[]; error? }`
  - 注释强调编辑器语义：**HTTP 200 即使 error 非空也照常返回**。
- `PreviewRequest { schema: unknown; mode; transport?; route?; bodyHex?; keyHex?; frameHex? }`
  - `schema` 用 `unknown` 承载（编辑器 content 的 JSON.parse 结果），由后端二次反序列化。

### 1.2 服务封装（新 `cmd/web/src/services/codecApi.ts`）

```ts
export async function fetchCodecAlgorithms(): Promise<AlgoMeta[]>;   // GET  /codec/algorithms
export async function previewCodec(req: PreviewRequest): Promise<PreviewResult>; // POST /codec/preview
```

- **复用 `services/api.ts` 的 `getJson` / `postJson`**（自动拼 `API_PREFIX='/sbot'`、处理非 2xx、网络异常包装为 `ApiError`）——**组件不直接 fetch**，全走 codecApi。
- `fetchCodecAlgorithms`：调 `getJson<AlgoMeta[]>('/codec/algorithms')`；非 2xx / 网络异常 / 响应非数组 → 抛中文 Error（前缀「算法清单加载失败：…」）。
- `previewCodec`：调 `postJson<PreviewResult>('/codec/preview', req)`；**HTTP 200 即使 result.error 非空也照常返回 result**（postJson 只在非 2xx 抛）；非 2xx → 抛中文 Error（前缀「预览失败：…」）。
- 不写兼容兜底、不 `??` fallback。

### 1.3 纯函数 `algosForStepOp`（新 `cmd/web/src/components/modules/codecEditor/algosForStepOp.ts`）

```ts
export function algosForStepOp(algos: AlgoMeta[], stepOp: string): AlgoMeta[];
```

- `STEP_OP_TO_ALGO_OP` 映射表：`encrypt→cipher`、`compress→compress`、`checksum→checksum`、`hash→hash`（+ 容忍 `cipher→cipher`）。
- 未知 stepOp → 空数组（不抛、不兜底）。保持清单原顺序（不重排）。
- 无 React 依赖，便于纯函数单测。

### 1.4 PipelineEditor algo 下拉 + 动态 params（`cmd/web/src/components/modules/codecEditor/PipelineEditor.tsx`）

**算法清单加载**（module-level cache + `useCodecAlgorithms` hook）：
- module-level `algoCache: { loaded, algos }` + `inflight: Promise | null`（并发多实例去重，整会话只发一个请求）。
- PipelineEditor 挂载时调 `fetchCodecAlgorithms()` 一次；成功 → 写 cache + setState；失败 → `message.error('算法清单加载失败：…')` + 空下拉。
- **禁止本地伪清单兜底**：失败后 cache 标记 `loaded:true, algos:[]`，下拉空（符合 plan §3.4）。

**algo 下拉替文本输入**：
- `Select`（`showSearch`），选项 = `algosForStepOp(algorithms, step.op)`，显示 `algo.name` + description 作 optionRender 次行 + tooltip。
- 占位文案随步 op 动态：该 op 无可用算法时显示「无可用算法」。
- encrypt↔cipher 映射在 `algosForStepOp` 内完成，PipelineEditor 调用方无感知。

**动态 params（`ParamsDynamic` 子组件替原 `ParamsSubform` 通用键值表）**：
- 入参：`step` + `algo`（选中算法元数据，可能 undefined）+ `onPatch`。
- algo 无 params（空/缺）→ 整个 params 区不渲染（return null）。
- 按 `algo.params: AlgoParam[]` 逐项渲染：`int→InputNumber`、`string→Input`、`bool→Switch`、`bytes→hex 文本输入`（placeholder 提示「hex」）。
- 值读自 `step.params[name]`，写回经 `onPatch({ params: { ...paramsObj, [name]: value } })`（保留其它 param 键）。
- 字段无值时用 `AlgoParam.default` 作 placeholder（惰性，不强制写入；清空 int 传 undefined，序列化丢弃该键）。
- **algo 元数据外的残留键不显示、不删除**（保留在 raw step.params，切源码可见，不静默丢弃）。

**清理 staging 注释**：删 B2-B 在 PipelineEditor 里「algo 文本输入 / params 键值表为 staging」的两处注释 + 顶部 docstring 同步更新。

## 2. 失败处理（无伪清单兜底）

- 加载失败路径：`fetchCodecAlgorithms` reject → `useCodecAlgorithms` catch → `message.error('算法清单加载失败：…')` + `setAlgos([])` → algo 下拉空 + 动态 params 区不渲染（selectedAlgo=undefined）。
- cache 标记 `loaded:true` → 同会话不再自动重试（避免反复弹错；用户可刷新页面重试）。
- 不引入任何本地算法常量、不 `??` 兜。

## 3. 测试（TDD：先红后绿）

### 3.1 新增单测

- `services/__tests__/codecApi.test.ts`（7 tests）：mock 全局 fetch，断言
  - `fetchCodecAlgorithms`：GET `/sbot/codec/algorithms`（经 API_PREFIX 拼接）、返回 AlgoMeta[]、空清单返回 []、HTTP 非 2xx 抛中文「算法清单加载失败」、网络异常抛中文。
  - `previewCodec`：POST `/sbot/codec/preview`、请求体 JSON 化含 schema/mode/bodyHex、**HTTP 200 且 result.error 非空时照常返回 result（不抛）**、HTTP 非 2xx 抛中文「预览失败」。
- `components/modules/codecEditor/__tests__/algosForStepOp.test.ts`（7 tests）：encrypt→cipher、compress→compress、checksum→checksum、hash→hash、未知 op 空、空清单空、保持原顺序。

### 3.2 验证结果

```
npx tsc -b        → exit 0（无类型错误）
npm run test      → Test Files 19 passed (19) / Tests 228 passed (228)
                    （既有 214 + 新增 14，无回归）
```

## 4. 自审

- [x] 类型镜像后端 json tag（camelCase），encrypt↔cipher 映射 gotcha 在 AlgoMeta 注释 + algosForStepOp 注释双标注。
- [x] 请求收拢 services：codecApi 复用 api.ts 的 getJson/postJson，组件不直接 fetch。
- [x] 禁止兼容兜底：清单失败→提示+空，不伪清单；无 `??` fallback。
- [x] encrypt↔cipher 映射正确（单测覆盖四种 stepOp + 未知 op）。
- [x] 动态 params：按 type 渲染控件正确；algo 元数据外残留键保留在 raw（不删）；default 作 placeholder 惰性。
- [x] 删 B2-B staging 注释（两处 + docstring）。
- [x] 改动文件限于 brief §4 白名单；未动 codecEdit（除无）、帧布局组件、后端。
- [x] 类型安全：tsc -b 绿；UI 文案用「算法」「参数」（algo 是必要配置术语保留）。

## 5. git diff --stat

```
 cmd/web/src/components/modules/codecEditor/PipelineEditor.tsx         | 273 +++++++++++++++------
 cmd/web/src/types/codec.ts                                            |  82 +++++++
 2 files changed, 286 insertions(+), 69 deletions(-)

新增（untracked）：
 cmd/web/src/components/modules/codecEditor/algosForStepOp.ts
 cmd/web/src/components/modules/codecEditor/__tests__/algosForStepOp.test.ts
 cmd/web/src/services/codecApi.ts
 cmd/web/src/services/__tests__/codecApi.test.ts
```

（git status --short 确认：cmd/web 下仅上述 6 个文件变动；codecEdit.ts / 帧布局组件 / 后端零改动。LF→CRLF warning 仅 Windows 换行规范化，无内容影响。）

## 6. Concerns

1. **清单加载失败后不自动重试**：cache 一旦标记 loaded（含失败态），同会话不再发请求。符合「拉一次」语义与无兜底约束，但用户需刷新页面恢复。若后续需要「点按钮重试」，可暴露一个 `reloadAlgorithms()` 并把 `algoCache.loaded` 重置为 false——本任务范围未要求，未做。
2. **algo 残留值显示**：若 `step.algo` 是算法清单外的手编残留值，下拉（showSearch）可能不显示该值（antd Select 无匹配 option）。残留值保留在 raw 不丢（切源码可见），但 UI 上该步可能看起来 algo 为空。与 brief「不静默丢弃」一致（值仍在），仅显示层未做 fallback 渲染——brief 未要求。
3. **preview 端点本任务只封装**：UI（实时预览面板）在 B3-B 接，codecApi 的 previewCodec 已就绪待用。
