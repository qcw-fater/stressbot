# T3 Batch-3 任务 A — 算法清单接线 + Pipeline algo 下拉/动态 params

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：B2-B 的 PipelineEditor（algo 现为文本输入、params 为通用键值表——staging）。后端端点已就绪
> （T4.2：`GET /sbot/codec/algorithms` + `POST /sbot/codec/preview`）。本任务把 algo 改下拉 + 动态 params。
> 后续 B3-B 加实时预览面板（共享本任务的 codecApi）。

## 1. 任务定位

B2-B 的 Pipeline 编辑器 algo 用文本输入、params 用通用键值表（staging，因算法清单属 §3.4）。本任务接
`GET /sbot/codec/algorithms` 拿到算法元数据，把 algo 改为**下拉**（按步的 op 过滤）+ params 改为**按算法
动态字段**（AlgoParam 驱动）。新建 `services/codecApi.ts` 封装两个端点（preview 端点本任务只封装、
UI 在 B3-B 用）。

## 2. 现状（先读码）

**先读** `cmd/web/src/components/modules/codecEditor/PipelineEditor.tsx`（algo 文本输入 + ParamsSubform
通用键值表——本任务改）、`codecEdit.ts`（updatePipelineStep 写 algo/params）、`cmd/web/src/types/codec.ts`
（PipelineStep + PIPELINE_OPS；本任务**追加**算法/预览类型）、`cmd/web/src/services/baselineApi.ts`
（fetchText/fetchJson helper 模式参考）、`cmd/web/src/services/resourcesStore.ts`（无直接关联，但服务层风格参考）。
后端形状权威：`codec/registry.go:172-229`（AlgoParam/AlgoMeta/Algorithms）、`codec/preview.go:24-57`
（PreviewField/PreviewResult/Preview）、`admin/codec_handlers.go`（HTTP 请求响应）。

## 3. 实现规格

### 3.1 类型（追加到 `cmd/web/src/types/codec.ts`）

镜像 Go（camelCase 与 json tag 一致）：
```ts
export interface AlgoParam { name: string; type: 'int'|'string'|'bool'|'bytes'; default?: unknown; description?: string; }
export interface AlgoMeta { name: string; op: 'cipher'|'compress'|'checksum'|'hash'; description?: string; params?: AlgoParam[]; }
export interface PreviewField { name: string; value: number; offset: number; size: number; }
export interface PreviewResult {
  mode: 'encode'|'decode'; frameHex?: string; bodyHex?: string; routeKey?: string;
  headerErr?: number; fields?: PreviewField[]; error?: string;
}
export interface PreviewRequest {
  schema: unknown;              // codec.json 对象（当前编辑的 content 解析结果）
  mode: 'encode'|'decode'; transport?: 'tcp'|'udp';
  route?: Record<string, unknown>; bodyHex?: string; keyHex?: string; frameHex?: string;
}
```

### 3.2 服务封装（新 `cmd/web/src/services/codecApi.ts`）

```ts
export async function fetchCodecAlgorithms(): Promise<AlgoMeta[]>;      // GET /sbot/codec/algorithms
export async function previewCodec(req: PreviewRequest): Promise<PreviewResult>; // POST /sbot/codec/preview
```
- 用现有 `BASELINE_PREFIX`? 否——这俩端点是 `/sbot/codec/...`（admin API，非 baseline）。沿用项目 API 前缀
  约定（看 `services/api.ts` 或 `env.ts` 的 API base；admin 端点前缀通常是 `/sbot` 或同源）。先读
  `services/api.ts`/`env.ts` 确认 admin API base 怎么取，保持一致（**组件禁止直接 fetch**，全走 codecApi）。
- `fetchCodecAlgorithms`：GET，返回 `AlgoMeta[]`；HTTP 非 2xx 或解析失败 → 抛中文 Error。
- `previewCodec`：POST JSON；**HTTP 200 即使 result.error 非空**（编辑器语义）——只把 body 当 PreviewResult 返回；
  HTTP 非 2xx（400 坏 schema/坏 JSON）→ 抛中文 Error（含 body 错误信息）。
- 单测（新 `services/__tests__/codecApi.test.ts`）：mock fetch，断言 URL/方法/请求体/响应解析 + 非 2xx 抛错 +
  preview 200-with-error 仍返回 result（不抛）。

### 3.3 PipelineEditor algo 下拉 + 动态 params

- **算法清单加载**：在 AdapterTab 或 PipelineEditor 挂载时调一次 `fetchCodecAlgorithms()`，缓存到 state
  （或一个轻量 module-level cache + subscribe）。失败 → `message.error('算法清单加载失败：…')` +
  algo 下拉显示空 + 错误态；**禁止本地伪算法清单兜底**（plan §3.4 明确）。
- **algo 下拉**：替掉文本输入。选项 = 算法清单里 **op 匹配当前步 op** 的算法。
  - **关键映射 gotcha**：`PipelineStep.op`∈{compress,encrypt,checksum,hash}，`AlgoMeta.op`∈{cipher,compress,checksum,hash}。
    **step.op==='encrypt' ↔ AlgoMeta.op==='cipher'**（其余三个同名）。下拉过滤必须做这个映射，否则 encrypt
    步选不到算法。抽一个纯函数 `algosForStepOp(algos, stepOp): AlgoMeta[]` 并单测（encrypt→cipher、compress→compress 等）。
  - 下拉显示 algo.name（+ description 作 tooltip/次行）。
- **动态 params**：替掉通用键值 ParamsSubform。选中 algo 后，按该 algo 的 `params: AlgoParam[]` 渲染字段：
  - `int` → InputNumber；`string` → Input；`bool` → Switch；`bytes` → hex 文本输入（placeholder「hex」）。
  - 值读自 `step.params[param.name]`，写回经 updatePipelineStep patch `params`（保留其它 param 键）。
  - 字段无值时用 `AlgoParam.default` 作 placeholder/初始（不强制写入，惰性同 B2-A value source）。
  - algo 无 params（params 为空/缺）→ 不显示 params 区。
  - 若 `step.params` 有 algo 元数据之外的键（手编残留），动态表单不显示它们但不删除（保留在 raw；用户切源码可见）——**不静默丢弃**。

### 3.4 清理 staging 注释

删 B2-B 在 PipelineEditor 里「algo 文本/params 键值为 staging，Batch 3 改下拉/动态」的注释（已落地）。

## 4. 全局约束（bind）

- **改动文件**：`types/codec.ts`（追加类型）、新 `services/codecApi.ts` + 测试、`PipelineEditor.tsx`（algo 下拉 + 动态 params，可能新建一个 `ParamsDynamic.tsx` 子组件）、可能 `ResourcesDrawer.tsx`（若算法清单在 AdapterTab 加载）。**严禁动** B2-A 既有帧布局组件、codecEdit（除非必要微调）、后端。
- **请求收拢 services/**：组件不直接 fetch，全走 codecApi。
- **禁止兼容性兜底**：算法清单加载失败→提示+空下拉，**不**本地伪清单；不 `??` 兜。
- **encrypt↔cipher 映射**必须正确（gotcha）。
- 复用 types/codec.ts 合法值集合；类型与 Go json tag 对齐。
- UI 文案不暴露技术术语（algo 是配置术语必要保留；面板用「算法」「参数」）。
- 类型安全；**不要 git commit**。

## 5. 验证（全绿才算 DONE）

- `cd cmd/web && npx tsc -b` exit 0。
- `cd cmd/web && npm run test` 通过（codecApi mock 单测 + algosForStepOp 映射单测；既有 214 不回归）。
- 自查 `git diff --stat`：改动限于上述文件。

## 6. 报告

写到 `plans/declarative-codec/reports/t3-b3a-algorithms-report.md`：实现要点、codecApi 封装、algo 下拉 +
encrypt↔cipher 映射、动态 params（按 type 渲染）、失败处理（无伪清单兜底）、测试、tsc/test 结果、自审、贴 `git diff --stat`。

返回消息只含：① 状态；② 改动文件清单；③ 一行测试摘要；④ concerns。有歧义先问（尤其 admin API base 前缀取法）。
