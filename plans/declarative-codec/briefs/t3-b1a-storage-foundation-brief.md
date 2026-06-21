# T3 Batch-1 任务 A — 多 codec 存储与校验基础（纯增量）

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 在 T3 Batch-1 内，本任务是**第 1 个**（前置 adapter-index 端点已另完成）。
> 后续 B1-B/C/D 会迁移所有消费方并删除旧 API；**本任务只新增、不动旧、不迁移消费方**。

## 1. 任务定位（为什么是纯增量）

T3 把前端「适配器」从单一 `codec.lua` 改为**按连接的多份** `<proto>_<service>_codec.json`（+ 共享
`errors.json`）。改存储 API 会波及所有消费方（`ResourcesDrawer`/`index.tsx`/`RuntimeBar`/`editorStore`/
`taskActions`/`taskResourceDiff`/`baselineApi`）——它们共享同一存储契约，无法逐个独立编译。

因此 Batch-1 用**「先增量、后迁移+删旧」**两段式：本任务（A）**纯新增**多文件存储 API + 类型 + 校验 +
baseline 端点封装，**完全不碰旧 API、不迁移任何消费方** → 工作树保持 `tsc -b` 绿；后续 B1-B/C/D 把
消费方逐个迁到新 API，最后 B1-D 删旧 API。整个 Batch 在批次末一次性提交（不在此任务提交）。

**这不是「兼容兜底」**：旧 API 在 B1-D 会被彻底删除，提交到 git 的是「只有新 API」的最终态；本任务的
「新旧并存」只是同一未提交批次内的重构分阶段，不向运行时引入任何 fallback/自动迁移。

## 2. 现状（已读码）

`cmd/web/src/services/resourcesStore.ts`：adapter 段用单 key `codec.lua`（`CODEC_LUA_KEY`）+ 单 key
`error.lua`（`ERROR_LUA_KEY`），暴露 `getAdapterScript/setAdapterScript/setAdapterScriptFromBaseline/
clearAdapterScript`、`getErrorMapScript/setErrorMapScript/setErrorMapScriptFromBaseline/clearErrorMapScript`、
`REQUIRED_ADAPTER_FUNCTIONS` + `validateAdapter()`（按 Lua 函数名 grep 校验，对新 JSON 模型无意义）。
DB `stressbot-resources-adapter`（单 object store `data`，`adapterStore = createStore(ADAPTER_DB,'data')`）。
`ResourceFile = { name, content: string, size, uploadedAt, baseHash? }`。

`cmd/web/src/services/baselineApi.ts`：`fetchBaselineAdapter()`→`/adapter/codec.lua`、
`fetchBaselineErrorMap()`→`/adapter/error.lua`（**旧**，本任务不动）。后端已有新端点（T4.3 + 本次前置）：
`GET /sbot/baseline/adapter/index.json`（列 `*_codec.json`+`errors.json` 文件名）、
`GET /sbot/baseline/adapter/{name}`（按文件名取单文件）。

**类型权威**：`codec/schema.go`（CodecSchema + Validate）。**先读它**（行 22-122 类型、126-162 合法值
集合、203-580 Validate 全规则），TS 类型与校验逐条对齐它，避免与后端漂移。

## 3. 新增类型 — `cmd/web/src/types/codec.ts`（新建）

镜像 `codec/schema.go` 的 Go 类型，**JSON 键名用 camelCase（与 Go json tag 一致）**。导出：
`CodecSchema`（version/endianDefault/frame/header/routeKeyTemplate/pipeline）、`FrameSpec`、
`Field`、`FlagBit`、`ValueSource`、`PipelineStep`、`StepOffset`、`StepProduce`、`OverSpec`、`StepCond`、
`Guard`。字段与 Go 结构体一一对应（含可选字段 `?`）。另外导出 `ErrorMap = Record<string, string>`
（code→中文，对应 `errors.json`）。

合法值集合（从 schema.go 126-162 照搬为 TS `const ... = [...] as const` 或 `Set`，供校验与后续 UI 下拉复用）：
`FIELD_TYPES`（u8/u16/u24/u32/u64/i8/i16/i24/i32/i64/f32/f64/bytes → 宽度）、`FIELD_ROLES`
（length/route/errorCode/flags/checksumOut/value/reserved）、`PIPELINE_OPS`（compress/encrypt/checksum/hash）、
`PRODUCE_REGIONS`、`OVER_KINDS`、`GUARD_OPS`、`ON_ERROR`（fail/keep）、`VALUE_SOURCE_KINDS`
（const/route 受支持；state/counter/timestamp 标 v1.1 不支持）。

## 4. 新增存储 API — `resourcesStore.ts`（追加，不删旧）

复用现有 `adapterStore`（同一 DB/store，多文件 = 多 key）。新增常量与函数：

```ts
const CODEC_FILE_SUFFIX = '_codec.json';
const ERRORS_JSON_KEY = 'errors.json';

// 每连接一份 codec（key = 文件名，如 'tcp_logic_codec.json'）
export async function getCodecSchema(name: string): Promise<ResourceFile | undefined>;
export async function setCodecSchema(name: string, content: string): Promise<ResourceFile>;
export async function setCodecSchemaFromBaseline(name: string, content: string): Promise<ResourceFile>;
export async function clearCodecSchema(name: string): Promise<void>;
/** 列出所有 *_codec.json（不含 errors.json），按 name 排序。 */
export async function listCodecFiles(): Promise<ResourceFile[]>;

// 共享错误表（单份，key = 'errors.json'）
export async function getErrorMap(): Promise<ResourceFile | undefined>;
export async function setErrorMap(content: string): Promise<ResourceFile>;
export async function setErrorMapFromBaseline(content: string): Promise<ResourceFile>;
export async function clearErrorMap(): Promise<void>;
```

实现要点：
- `setCodecSchema` 用 `localResourceFile(name, content, await getCodecSchema(name))`（与现有 `setAdapterScript`
  同款 baseHash 继承语义）；`setCodecSchemaFromBaseline` 用 `serverResourceFile`（写 baseHash=当前 hash）。
- `listCodecFiles`：`keys(adapterStore)` 过滤 `String(k).endsWith('_codec.json')`，逐个 `get`，按 `name` 排序。
  **不要**把 `errors.json` 混入。
- error-map 四函数与现有 `getErrorMapScript` 等同构，仅 key 改 `errors.json`。
- 所有写操作末尾调 `notify()`（与现有 adapter 函数一致）。
- **文件名校验**：`getCodecSchema/setCodecSchema/clearCodecSchema` 对 `name` 做 `endsWith('_codec.json')`
  守卫，不合法抛中文 `Error`（防误把 errors.json 当 codec 存）。`getErrorMap/setErrorMap` 不需 name 参数。

## 5. 新增校验 — `validateCodecSchema`（resourcesStore.ts 或 codec.ts）

```ts
/** 对单份 codec.json 文本做结构校验，返回中文错误数组（空=通过）。纯函数、同步。 */
export function validateCodecSchema(content: string): string[];
```

**逐条镜像 `codec/schema.go` 的 `Validate`（203-580）**——它本身是纯结构校验（不执行算法），TS 可忠实移植：
- 解析：`JSON.parse` 失败 → `['codec 配置不是合法 JSON：<原因>']`。
- base：`version===1`；`endianDefault∈{le,be}`；`frame.headerSize>0`；`frame.trailerSize>=0`；
  `routeKeyTemplate` 非空。
- header：字段名唯一且非空；`offset>=0`、`size>0`；`[offset,offset+size)⊆[0,headerSize)`；type 在
  `FIELD_TYPES` 内且定宽类型的 size 等于宽度（bytes 需 size>0）；endian（若给）∈{le,be}；role 在
  `FIELD_ROLES` 内；**物理区间不重叠**（按 start 排序后相邻比较，与 Go 一致）；role 统计：**恰好 1 个
  length**、**≥1 个 route**；flags 字段的 bits 合法（bit∈[0,size*8)、不重复、命名不空不重）；checksumOut
  的 from 匹配 `<step>.<output>` 正则；value 的 source.kind（若有）未知/不支持分别报错。
- routeKeyTemplate：每个 `{name}` 占位必须指向某个 `role:"route"` 字段。
- pipeline：step.name 唯一非空；op 在 `PIPELINE_OPS`；algo 非空；onError（若给）∈{fail,keep}；produces
  名唯一 + region 在 `PRODUCE_REGIONS`；encrypt offset.encode/decode>=0；over.kind 合法（range 区间合法）；
  when.appliesWith 指向存在的 step、guards.op 合法。
- pipeline↔header 引用：flag 引用必须存在于某 flags 字段命名位、且同一 flag 至多被一个 step 绑定；**带
  when 的 step 必须绑定 flag**；checksumOut.from 的 `<step>` 存在且其 produces 含 `<output>`。

聚合所有错误一次性返回（与 Go `errCollector` 语义一致：不要遇到一个就停）。

**algo 是否在注册表中**这一条本任务**不做**（前端只读算法清单在 §3.4 从 `GET /sbot/codec/algorithms`
拉取）——`validateCodecSchema` 只校验 algo 非空，不校验 algo 是否注册。在代码注释里标明「algo 注册表
校验由 §3.4 后端预览/算法清单端点权威，前端此处仅校验非空」。

另提供聚合辅助（供 B1-B 的 index.tsx 挂载校验用）：
```ts
/** 读取所有 *_codec.json，逐份校验，汇总错误（每条带文件名前缀）。 */
export async function collectCodecSchemaErrors(): Promise<string[]>;
```
实现：`listCodecFiles()` → 对每份 `validateCodecSchema(f.content)`，错误前缀 `[{name}] `。

## 6. 新增 baseline 封装 — `baselineApi.ts`（追加，不删旧）

```ts
/** 基线 adapter 文件名清单（*_codec.json + errors.json）。 */
export async function fetchBaselineCodecIndex(): Promise<string[]>;
/** 基线单份 codec/errors 文件内容（name = tcp_logic_codec.json / errors.json）。 */
export async function fetchBaselineCodec(name: string): Promise<string | null>;
```
实现：`fetchBaselineCodecIndex`→`GET /sbot/baseline/adapter/index.json`（沿用现有 `fetchJson` helper，
空时返回 `[]`）；`fetchBaselineCodec(name)`→`GET /sbot/baseline/adapter/{encodeURIComponent(name)}`
（沿用 `fetchText`，失败/404 返回 null）。**不动**旧 `fetchBaselineAdapter`/`fetchBaselineErrorMap`
（B1-D 删）。

## 7. 验证（必须全绿才算 DONE）

- `cd cmd/web && npx tsc -b` 退出 0（新类型 + 新函数无类型错误；旧代码未动）。
- `cd cmd/web && npm run test`（Vitest）通过；**为新函数写测试**：
  - `validateCodecSchema`：用 `conf/adapter/tcp_logic_codec.json` 真实内容 → 空错误；构造畸形（坏 JSON、
    headerSize<=0、字段越界/重叠、缺 length、缺 route、routeKeyTemplate 占位不指向 route、pipeline step
    name 重复、checksumOut.from 指向不存在 step）→ 各命中对应中文错误。**断言要具体**（包含关键中文
    子串），不能只断言「数组非空」。
  - `getCodecSchema/setCodecSchema/listCodecFiles/clearCodecSchema` + error-map 四函数：用 `idb-keyval`
    的内存/真实 store round-trip（参考既有 resourcesStore 测试若有；无则在 vitest 里用真实
    `createStore` 临时 DB，测后清理）。`listCodecFiles` 断言不含 `errors.json`。
  - `fetchBaselineCodecIndex/fetchBaselineCodec`：若项目有 API mock 测试范式则照做；否则至少类型与
    URL 拼接有断言（可 mock `fetch`）。

## 8. 全局约束（bind）

- **纯增量**：只新增 `src/types/codec.ts` + 在 `resourcesStore.ts`/`baselineApi.ts` 追加新函数/常量 +
  测试。**严禁**删除或修改旧 adapter 函数（`getAdapterScript`/`setAdapterScript`/`validateAdapter`/
    `REQUIRED_ADAPTER_FUNCTIONS`/`getErrorMapScript`/...）、**严禁**迁移任何消费方
  （`ResourcesDrawer`/`index.tsx`/`RuntimeBar`/`editorStore`/`taskActions`/`taskResourceDiff`）——那些是
  B1-B/C/D。本任务结束后旧路径仍照常工作。
- **禁止兼容性兜底**：不写 codec.lua→codec.json 自动迁移、不用 `??` fallback、新字段全链路一致。
  （本任务的「新旧并存」是批次内分阶段，非运行时 fallback。）
- **请求收拢到 services/**：新 baseline 函数放 `baselineApi.ts`，组件不直接 fetch（本任务无组件改动）。
- **类型安全**：`types/codec.ts` 字段与 Go json tag 逐一对齐；可选字段用 `?`。
- **UI 文本不暴露技术术语**：本任务错误文案可含 codec/schema（属校验信息，非 UI 面板文案），后续
  面板文案由 B1-C 改「协议配置」。
- **不要 git commit**（项目规则：implementer 不自动提交）。
- 前端换行：本项目无 autocrlf 强制，但仍以 `npx tsc -b` 与 `npm run test` 为权威。

## 9. 报告

写到 `plans/declarative-codec/reports/t3-b1a-storage-foundation-report.md`：实现要点、新增 API 清单、
`validateCodecSchema` 对 Go Validate 各规则的覆盖映射、测试用例与命令输出、`tsc -b`/`npm run test`
结果、自审发现、**确认旧 API 与所有消费方未被触碰**（贴 `git diff --stat` 证明只动了 3 个源文件 +
测试）。

返回给我只含：① 状态（DONE/DONE_WITH_CONCERNS/BLOCKED）；② 改动文件清单；③ 一行测试摘要；④ concerns。

有歧义先问再动手。
