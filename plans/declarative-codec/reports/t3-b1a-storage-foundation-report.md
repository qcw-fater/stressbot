# T3 Batch-1 任务 A — 多 codec 存储与校验基础（纯增量）报告

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）
> 任务定位：T3 Batch-1 第 1 个任务，**纯增量**——只新增类型 + 追加新函数 + 测试，不删旧、不迁移消费方。
> 需求来源：`plans/declarative-codec/briefs/t3-b1a-storage-foundation-brief.md`（逐字照做）。

## 1. 实现要点

### 1.1 新增类型 — `cmd/web/src/types/codec.ts`（新建，纯声明）
镜像 `codec/schema.go`（行 22-122），JSON 键名用 camelCase（与 Go json tag 一致），可选字段用 `?`。导出：
- 接口：`CodecSchema`（version/endianDefault/frame/header/routeKeyTemplate/pipeline）、`FrameSpec`、`Field`、`FlagBit`、`ValueSource`、`PipelineStep`、`StepOffset`、`StepProduce`、`OverSpec`、`StepCond`、`Guard`。
- 类型别名：`ErrorMap = Record<string, string>`（errors.json：code→中文）。
- 合法值集合（照搬 schema.go 126-162）：
  - `FIELD_TYPE_WIDTH`（type→固定宽度字节数，-1=bytes 需显式 size）+ 派生 `FIELD_TYPES`；
  - `FIELD_ROLES` / `PIPELINE_OPS` / `PRODUCE_REGIONS` / `OVER_KINDS` / `GUARD_OPS` / `ON_ERROR`（均 `as const` 元组）；
  - `VALUE_SOURCE_KINDS_SUPPORTED`（`Record<string, boolean>`，value=false 表示 v1 不支持，留 v1.1）。

### 1.2 追加存储 API — `cmd/web/src/services/resourcesStore.ts`（追加，**0 行删除**）
复用现有 `adapterStore`（同 DB/store，多文件 = 多 key），在 `clearErrorMapScript` 之后、`REQUIRED_ADAPTER_FUNCTIONS` 之前插入新段。

新增常量：`CODEC_FILE_SUFFIX='_codec.json'`、`ERRORS_JSON_KEY='errors.json'`，以及文件顶部的 codec 类型 import（`CodecSchema` + 8 个集合常量）。

新增函数：
| 函数 | 用途 | 关键语义 |
|---|---|---|
| `assertCodecFileName(name)` | 内部守卫 | 名字必须 `endsWith('_codec.json')`，否则抛中文 Error |
| `getCodecSchema(name)` | 取单份 codec | 含 `assertCodecFileName` |
| `setCodecSchema(name, content)` | 本地编辑 | `localResourceFile`（继承旧 baseHash）+ `notify()` |
| `setCodecSchemaFromBaseline(name, content)` | 基线写入 | `serverResourceFile`（baseHash=内容 hash）+ `notify()` |
| `clearCodecSchema(name)` | 删除单份 | `del` + `notify()` |
| `listCodecFiles()` | 列表 | `keys` 过滤 `endsWith('_codec.json')`，按 name 排序；**不含** `errors.json` |
| `getErrorMap()` | 取共享错误表 | key=`errors.json` |
| `setErrorMap(content)` | 本地编辑 | 同 setErrorMapScript 语义，key 改 `errors.json` |
| `setErrorMapFromBaseline(content)` | 基线写入 | baseHash=内容 hash |
| `clearErrorMap()` | 删除 | |
| `validateCodecSchema(content)` | **纯结构校验** | 纯函数、同步、聚合所有错误一次性返回（空=通过） |
| `collectCodecSchemaErrors()` | 批量聚合 | `listCodecFiles` → 逐份校验，错误前缀 `[{name}] ` |

所有写操作末尾 `notify()`（与现有 adapter 函数一致）。

### 1.3 `validateCodecSchema` 对 Go Validate 各规则的覆盖映射

逐条镜像 `codec/schema.go` 的 `Validate`（203-580），结构与文案与后端中文错误对齐（便于 B1-C 前端面板直接展示）。

| Go Validate 规则（schema.go 行号） | TS 实现 | 覆盖测试 |
|---|---|---|
| 解析失败（`LoadSchema`/JSON） | `JSON.parse` 失败 → `['codec 配置不是合法 JSON：<原因>']`；非对象 → `['codec 配置不是合法 JSON 对象']` | 坏 JSON |
| version===1（213-215） | ✓ | version=2 |
| endianDefault∈{le,be}（216-218） | ✓ | endianDefault=middle |
| headerSize>0（219-221） | ✓ | headerSize=0 |
| trailerSize>=0（222-224） | ✓ | trailerSize=-1 |
| routeKeyTemplate 非空（225-227） | ✓（trim） | routeKeyTemplate='   ' |
| 字段名唯一+非空（238-245） | ✓ | 字段名重复 |
| offset>=0（246-248） | ✓ | offset=-1 |
| size>0（249-251） | ✓ | size=0 |
| 物理区间⊆[0,headerSize)（252-254） | ✓ | size 越界 |
| type 合法 + 定宽 size==width（255-266） | ✓（用 `FIELD_TYPE_WIDTH`） | u32 size=3；未知 type u128；bytes |
| endian∈{le,be}（268-270） | ✓ | endian=middle |
| role∈`FIELD_ROLES`（271-273） | ✓ | role=mystery |
| 物理区间不重叠（276-296） | ✓（按 start 排序相邻比较） | cmd offset 挪到 5 与 errCode 重叠 |
| length 恰好 1 个（319-323） | ✓（0 / >1 分报） | 缺 length；2 个 length |
| route>=1（324-326） | ✓ | cmd/act 都改 reserved |
| flags bits 合法（332-354：bit∈[0,size*8)、不重复、命名非空不重） | ✓ | bit=9 超出；bit 重复 |
| checksumOut from 匹配 `<step>.<output>`（356-364） | ✓（`CHECKSUM_FROM_RE`） | from='no-dot-format' |
| value source.kind 未知/不支持（366-379） | ✓（`VALUE_SOURCE_KINDS_SUPPORTED`） | kind=mystery 未知；kind=state 不支持 |
| routeKeyTemplate 占位指向 route 字段（385-396） | ✓（`ROUTE_KEY_PLACEHOLDER_RE`） | `{nonexistent}` |
| pipeline step name 唯一非空（413-422） | ✓ | enc 改名为 gz |
| op∈`PIPELINE_OPS`（429-431） | ✓ | op=scramble |
| algo 非空（432-434） | ✓ | algo='' |
| onError∈{fail,keep}（435-439） | ✓ | onError=retry |
| produces name 唯一 + region 合法（441-452） | ✓ | region=galaxy |
| encrypt offset.encode/decode>=0（455-466） | ✓ | encode=-1 |
| over.kind∈`OVER_KINDS` + range 区间合法（468-470 / 555-566） | ✓ | over.kind=planet；range end<start |
| when.appliesWith 指向存在 step（568-573） | ✓ | appliesWith='ghost-step' |
| guard op∈`GUARD_OPS`（574-579） | ✓ | op=matches |
| flag 引用必须存在 + 同一 flag 至多被一个 step 绑定（504-513） | ✓ | flag='nonexistent-flag' |
| 带 when 的 step 必须绑定 flag（516-521） | ✓ | gz 去掉 flag |
| checksumOut.from 的 step 必须存在 + produce 必须存在（534-552） | ✓ | from='ghost.bcc'（step 不存在）；from='enc.nonexistent'（produce 不存在） |

**algo 是否在注册表中**这一条**本任务不做**（已在源码注释标明）——前端只校验 algo 非空，算法注册表由 §3.4 后端预览/`GET /sbot/codec/algorithms` 端点权威。

### 1.4 追加 baseline 封装 — `cmd/web/src/services/baselineApi.ts`（追加，**0 行删除**）
| 函数 | 端点 |
|---|---|
| `fetchBaselineCodecIndex(): Promise<string[]>` | `GET /sbot/baseline/adapter/index.json`（沿用 `fetchJson`，空/失败返回 `[]`） |
| `fetchBaselineCodec(name): Promise<string \| null>` | `GET /sbot/baseline/adapter/{encodeURIComponent(name)}`（沿用 `fetchText`，404/失败返回 null） |

旧 `fetchBaselineAdapter`/`fetchBaselineErrorMap` **完全不动**（B1-D 删）。

## 2. 新增 API 清单（汇总）

**`src/types/codec.ts`**（纯类型，无运行时副作用）：
`CodecSchema` / `FrameSpec` / `Field` / `FlagBit` / `ValueSource` / `PipelineStep` / `StepOffset` / `StepProduce` / `OverSpec` / `StepCond` / `Guard` / `ErrorMap` + `FIELD_TYPE_WIDTH` / `FIELD_TYPES` / `FIELD_ROLES` / `PIPELINE_OPS` / `PRODUCE_REGIONS` / `OVER_KINDS` / `GUARD_OPS` / `ON_ERROR` / `VALUE_SOURCE_KINDS_SUPPORTED`。

**`resourcesStore.ts` 追加**：
`getCodecSchema` / `setCodecSchema` / `setCodecSchemaFromBaseline` / `clearCodecSchema` / `listCodecFiles` / `getErrorMap` / `setErrorMap` / `setErrorMapFromBaseline` / `clearErrorMap` / `validateCodecSchema` / `collectCodecSchemaErrors`。

**`baselineApi.ts` 追加**：`fetchBaselineCodecIndex` / `fetchBaselineCodec`。

## 3. 测试

### 3.1 测试文件
- `cmd/web/src/services/__tests__/validateCodecSchema.test.ts` — **39 用例**：1 PASS 样本（真实 `conf/adapter/tcp_logic_codec.json` → 空错误）+ 38 畸形构造，每个断言命中关键中文子串（非「数组非空」蒙混）。覆盖 base / header / routeKeyTemplate / pipeline / pipeline↔header 引用 五大类。
- `cmd/web/src/services/__tests__/codecStorage.test.ts` — **11 用例**：
  - codec 多文件 round-trip（baseHash 继承、文件名校验、listCodecFiles 排序且不含 errors.json、clearCodecSchema）；
  - errors.json round-trip（set/get/setFromBaseline 写 baseHash/clear）；
  - baseline 封装（fetchBaselineCodecIndex 命中 URL+空返回 []、404 返回 []；fetchBaselineCodec 命中 URL+encodeURIComponent、404 返回 null）。

  存储测试用 `vi.mock('idb-keyval', ...)` 提供内存 Map（Node 环境无 IndexedDB），隔离多 store；fetch 用 `vi.stubGlobal('fetch', ...)` + `Response` mock。

### 3.2 命令输出

```
$ cd cmd/web && npx tsc -b ; echo "exit=$?"
（无输出）
exit=0

$ cd cmd/web && npx vitest run src/services/__tests__/codecStorage.test.ts src/services/__tests__/validateCodecSchema.test.ts
 ✓ src/services/__tests__/validateCodecSchema.test.ts (39 tests) 20ms
 ✓ src/services/__tests__/codecStorage.test.ts (11 tests) 39ms
 Test Files  2 passed (2)
      Tests  50 passed (50)

$ cd cmd/web && npm run test   # 全量
 Test Files  1 failed | 14 passed (15)
      Tests  1 failed | 174 passed (175)
```

**唯一失败的 `luaApiSpec.test.ts`（`register_tcp_heartbeat` 未在 spec 中）是预先存在、与本任务无关**——已用 `git stash` 在干净工作树上复跑确认（同样的 1 failed / 14 passed）。本任务的 50 个新用例全绿、其余 124 个旧用例无回归。

## 4. `git diff --stat`（证明只动了 3 个源文件 + 2 个测试，纯增量）

```
$ git diff --stat -- cmd/web/src/
 cmd/web/src/services/baselineApi.ts    |  18 ++
 cmd/web/src/services/resourcesStore.ts | 521 +++++++++++++++++++++++++++++++++
 2 files changed, 539 insertions(+)

$ git status --short -- cmd/web/
 M cmd/web/src/services/baselineApi.ts
 M cmd/web/src/services/resourcesStore.ts
?? cmd/web/src/services/__tests__/codecStorage.test.ts
?? cmd/web/src/services/__tests__/validateCodecSchema.test.ts
?? cmd/web/src/types/codec.ts
```

**旧 API 零改动校验**：
```
$ git diff -- cmd/web/src/services/resourcesStore.ts cmd/web/src/services/baselineApi.ts | grep '^-' | grep -v '^---'
（空——没有任何删除/修改行，纯追加）
```

即：旧 adapter 函数（`getAdapterScript`/`setAdapterScript`/`setAdapterScriptFromBaseline`/`clearAdapterScript`/`getErrorMapScript`/`setErrorMapScript`/`setErrorMapScriptFromBaseline`/`clearErrorMapScript`/`validateAdapter`/`REQUIRED_ADAPTER_FUNCTIONS`/`CODEC_LUA_KEY`/`ERROR_LUA_KEY`）与旧 `fetchBaselineAdapter`/`fetchBaselineErrorMap` 原封不动；消费方（`ResourcesDrawer`/`index.tsx`/`RuntimeBar`/`editorStore`/`taskActions`/`taskResourceDiff`）**零触碰**。

## 5. 自审

- **纯增量** ✓：只新增 + 追加，0 删除行（`git diff` 验证）。旧 API 与消费方未触碰。
- **无兼容兜底** ✓：无 codec.lua→codec.json 自动迁移、无 `??` fallback、新字段全链路一致。
- **类型权威对齐** ✓：`types/codec.ts` 字段与 `codec/schema.go` json tag 一一对应；`validateCodecSchema` 逐条镜像 Go Validate，文案与后端中文错误对齐（B1-C 前端可直接展示）。
- **algo 注册表校验不做** ✓：源码注释标明归 §3.4 后端，前端只校验非空。
- **请求收拢到 services/** ✓：新 baseline 函数在 `baselineApi.ts`，组件不直接 fetch（本任务无组件改动）。
- **禁止 git commit** ✓：未提交。
- **TDD** ✓：先写测试（含具体中文断言）→ 跑红 → 实现 → 跑绿；过程中发现并修正了一处测试自身 bug（`enc.unknown-output` 因含 `-` 不匹配 `<step>.<output>` 正则，改用 `enc.nonexistent` 才能命中「produce 不存在」分支——这反过来验证了 TS 校验与 Go 正则一致）。

## 6. 约束遵循

| 约束 | 遵循 |
|---|---|
| 纯增量，不动旧 adapter 函数、不迁移消费方 | ✓（`git diff` 0 删除） |
| 禁止兼容兜底 | ✓ |
| 类型权威 = `codec/schema.go` | ✓（逐条对齐） |
| `validateCodecSchema` 纯结构校验、聚合返回、不校验 algo 注册表 | ✓（注释标明） |
| 新 baseline 函数放 `baselineApi.ts` | ✓ |
| 不 git commit | ✓ |
| 按 TDD：先红后绿，断言具体中文子串 | ✓ |

## 7. DONE 判定

- `npx tsc -b` exit 0 ✓
- 50 个新测试全绿 ✓
- 旧 124 个测试无回归 ✓（唯一失败的 `luaApiSpec` 预先存在、与本任务无关，已干净工作树复跑确认）
- 旧 API 与所有消费方零改动 ✓（`git diff --stat` + grep 删除行均证明）

## 测试严谨性修正（后续）

> Review 记录的 Minor（测试严谨性）+ 1 个非 hermetic 测试，逐项修正。纯测试/注释修正，不动生产逻辑；不加兼容兜底；未 commit。

### 改动清单

#### 1. `cmd/web/src/services/__tests__/validateCodecSchema.test.ts`

- **① alias 统一**：删除文件末尾 `validCodec()` helper（与 `validSchema()` 等价、仅 `return validSchema()`），全部用例统一改用 `validSchema()`。
- **② 隔离「物理区间越界」用例**：原用例把 `header[0]`（bodyLen, u32）的 `size` 改成 100，**同时**触发「type u32 的 size 必须为 4」和「越界」两条错误，断言只查「越界」——错误源不唯一。改为把 `header[6]`（bcc, u8, offset=11, size=1）的 `offset` 挪到 12（≥ headerSize=12），`offset+size=13 > 12` 只触发「越界」分支，type u8 的 size 约束不被破坏（仍 size=1）。断言保持查中文「越界」子串。
- **③ 补 flags 命名位两个分支用例**（对照 `codec/schema.go` `validateFlagBits` 行 346-349）：
  - `flags 命名位名称为空`：把 `header[5].bits[0].name` 清空 → 断言命中「名称为空」。
  - `flags 命名位名称重复`：两个不同 bit（0 与 2）取同名 → 断言命中「命名位 ... 重复」。

#### 2. `cmd/web/src/services/resourcesStore.ts`

- **⑥ 删孤儿注释**：删掉 `migrateLegacyResources` 之前的 `// === 基线回写 ===` 注释块（已无对应实现——`saveLastBaseline` 是索引快照，不是内容回写）。逻辑零改动。

#### 3. `cmd/web/src/components/FlowEditor/codec/codec.test.ts:149`（非 hermetic 修正）

- 原「导出 JSON 写到磁盘，结构稳定」用例把产物写到 `path.resolve(__dirname, '../../../../../tmp_codec_export.json')` = tracked 文件 `cmd/tmp_codec_export.json`，每次跑测试翻动该 tracked 文件。
- 改为写到系统临时目录 `path.join(os.tmpdir(), \`stressbot-codec-export-${process.pid}-${Date.now()}.json\`)`，新增 `import * as os from 'node:os'`。「写盘 → 读回 → 比对」语义不变。tracked 文件不再被测试翻动。

### 验证

- `cd cmd/web && npx tsc -b` → exit 0 ✓
- `cd cmd/web && npx vitest run src/services/__tests__/validateCodecSchema.test.ts src/components/FlowEditor/codec/codec.test.ts` → **50 passed**（含 2 个新增 flags 用例）✓
- `cd cmd/web && npm run test`（全量）→ **177 passed**（原 175 + 新增 2，无回归）✓
- **非 hermetic 验收**：跑完测试后 `git status --porcelain cmd/tmp_codec_export.json` → **输出为空**（tracked 文件不再被翻动）✓

```
$ git status --porcelain cmd/tmp_codec_export.json
（空）
```

