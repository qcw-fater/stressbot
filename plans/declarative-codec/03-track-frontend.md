# Track 3 — 前端：多 codec 文件编辑器 + 存储 + 校验

> 依赖：Track 1（codec schema 规范 + JSON Schema 草案）、Track 4（文件命名 `<proto>_<service>_codec.json`/`errors.json`、baseline 端点、multipart 字段名）
> 产出：资源面板「适配器」从单一 Lua 文本编辑改为**按连接的多份** `<proto>_<service>_codec.json`（+ 共享 `errors.json`）编辑/校验/同步/下发
> 约束：遵循 `CLAUDE.md` 前端规约（antd v5、UI 文本禁暴露技术术语、请求收拢到 `services/`、类型安全）

---

## 1. 现状参考（已读码，详见前端勘察）

> 命名警惕：`src/components/FlowEditor/codec/` 是 **flow.json 图编解码**，与协议 codec 无关，**不要动**（必要时备注以防混淆）。

| 关注点 | 文件 | 行 |
|---|---|---|
| 适配器存储 + CRUD + 校验 | `src/services/resourcesStore.ts` | 175-255（codec.lua/error.lua CRUD、`REQUIRED_ADAPTER_FUNCTIONS`、`validateAdapter`）；257-498（baseline 同步） |
| baseline 拉取 | `src/services/baselineApi.ts` | 62-69（`fetchBaselineAdapter`→`/adapter/codec.lua`、`fetchBaselineErrorMap`→`error.lua`） |
| 任务上传（multipart） | `src/services/taskActions.ts` | 118-149、197-225（要求 codec.lua 在 IDB；上传 `adapter/codec.lua`+可选 `adapter/error.lua`） |
| 任务级 diff | `src/services/taskResourceDiff.ts` | 15-27、48-53 |
| 资源面板 UI（主改造） | `src/components/modules/ResourcesDrawer.tsx` | 155-456（适配器 tab：Monaco `language="lua"`、模板、`CODEC_SPEC`、import `.lua`、save/clear） |
| baseline 冲突弹窗 | `src/components/modules/BaselineSyncModal.tsx` | 33-37、238（adapter 用 lua diff 语言） |
| 任务启动前校验 | `src/components/runtime/TaskStartModal.tsx` | 50、147-148、286-330、876-915 |
| 校验状态 | `src/components/FlowEditor/store/editorStore.ts` | 128-134、174-177（`adapterMissing`）；14-22、248（**死代码** `codecAdapter` panel） |
| 挂载校验 | `src/components/FlowEditor/index.tsx` | 90-106 |
| 资源按钮徽标 | `src/components/runtime/RuntimeBar.tsx` | 113-125、450-466 |
| 路由键伪匹配（可升级为真实计算） | `src/components/FlowEditor/listens/refsGraph.ts` | 83-96；`refsCheck.ts` 351/362/371 |
| Lua API 文档中的 adapter 模块（随后端删除而删） | `src/components/FlowEditor/lua/luaApiSpec.ts` | 716-770、989；`LuaApiPopover.tsx:19` |
| 类型 | `src/services/resourcesStore.ts` `ResourceFile`/`ResourceType` 23-30/275；`src/types/action.ts:108` `route?` | — |

---

## 2. 设计

### 2.1 存储层（resourcesStore.ts）

> **多文件模型（决策 #8）**：codec 不再是单一 `codec.lua`，而是**每连接一份** `<proto>_<service>_codec.json`（如 `tcp_logic_codec.json`/`tcp_battle_codec.json`/`udp_battle_codec.json`）+ 共享一份 `errors.json`。前端存储/编辑/同步/下发全部按「一组 codec 文件」处理，而非单文件。

- [ ] IndexedDB：单一 `codec.lua` key → **多个** `<proto>_<service>_codec.json` key（按连接）；`error.lua` → `errors.json`（单份）。DB 名 `stressbot-resources-adapter` 可沿用。
- [ ] `ResourceFile.content` 仍是字符串（存 JSON 文本）；新增类型 `CodecSchema`（见 §2.4）仅用于校验/编辑，落库仍序列化为字符串。每个连接 codec 文件各一条 `ResourceFile`。
- [ ] `getAdapterScript/setAdapterScript/...`（单文件）→ 按文件名读写的 `getCodecSchema(file)/setCodecSchema(file,...)` + `listCodecFiles()`（枚举所有 `*_codec.json`）。`getErrorMap/setErrorMap` 仍单份。
- [ ] 删 `REQUIRED_ADAPTER_FUNCTIONS`（7 个 Lua 函数名）；`validateAdapter()` 改为 `validateCodecSchema()`（见 §2.3），对每份 codec 文件分别校验。
- [ ] `syncResourcesFromBaseline` / `markResourcesAsBaselineSynced` / 三方合并：`ResourceType` 的 `'adapter'` 保留，但内部从单一 `codec.lua` 改为枚举多份 `*_codec.json` + `errors.json`；`localStorage` 的 `adapter:boolean` 标记沿用。

### 2.2 资源面板 UI（ResourcesDrawer.tsx，最大改动）——**可视化结构化编辑器**

适配器 tab 改造为「可视化编辑器为主、JSON 源码为辅」的双视图。这是本轨道工作量最大、价值最高的部分：把「哪段帧代表哪个字段」全部可视化配置。

> **连接选择器（多文件）**：tab 顶部先选「哪条连接」（`tcp:logic`/`tcp:battle`/`udp:battle` 等，列表来自已存的 `*_codec.json` 文件，支持新建/复制/删除一条连接的 codec）；选中后下方编辑器编辑该连接对应的 codec 文件。新建时按 `<proto>:<service>` 命名，落库为 `<proto>_<service>_codec.json`。「复制」便于把 `tcp:logic` 的配置克隆给 `tcp:battle` 再微调（避免每条从零写）。

#### (a) 帧布局编辑器（header 字段表 + 字节条带）

- [ ] **字节条带（byte map）**：一条 `0 .. headerSize-1` 的可视化字节带，每个字段占一段彩色区块（类似 Wireshark/struct 视图），标注 `name/offset/size/type`；点击区块选中、可视化调整跨度。`trailer` 若启用同样展示。
- [ ] **字段表**：每行一个 header 字段，列含 `name`、`offset`、`size`、`type`（下拉：`u8/u16/u24/u32/u64 / i* / f32/f64 / bytes`）、`endian`（`le/be`，默认随 `endianDefault`）、`role`（下拉：`length/route/errorCode/flags/checksumOut/value/reserved`）。
- [ ] **角色联动表单**（选不同 role 显示不同配置）：
  - `route`：参与 `routeKeyTemplate` 的占位名提示；
  - `flags`：编辑命名位 `bits:[{name,bit}]`（位编辑器）；
  - `checksumOut`：选 `from`，格式为 `<step>.<output>`（下拉列出各 pipeline 步声明的 `produces` 产物，如 `enc.bcc`）；
  - `value`：选 `source.kind`，**v1 仅 `const`/`route` 可选**；`state`/`counter`/`timestamp` 在下拉中置灰并标注「v1.1」（与后端 `Validate` 拒绝一致）；
  - `errorCode`：提示「绑定后启用服务端错误码识别与 errors.json」。
- [ ] **校验联动**：必有 1 个 `length`、≥1 个 `route`；字段不越界/不重叠；`from` 指向存在的产物；实时红线提示（见 §2.3）。

#### (b) Pipeline 编辑器

- [ ] 有序步骤卡片列表，可增删/拖拽排序；每张卡片：`op`（`compress/encrypt/checksum/hash`）、`algo`（下拉，来自前端只读算法清单，见 §2.7-bis）、`params`（按算法动态字段，如 `xor_carry_rol` 的 `rol`）、`flag`（下拉绑定到 flags 命名位）、`when`（结构化条件：`minBodyLen/onlySmaller/requireKey/guards[]/appliesWith`）、`onError`（`fail`默认 / `keep`）。
  - encrypt：`keyLen` + **单向偏移** `offset.{encode,decode}`（两个独立输入，UI 标注「发/收偏移可不同，如 UDP 发=11 收=0」；每份 codec 单 transport，故无需 tcp/udp 区分）+ `produces`（声明派生产物，如 `{name:bcc, algo:xor8, region:ciphered}`）；
  - 独立 checksum/hash：`over`（`bodyPlain/bodyFinal/header/frame/range`）。
- [ ] 卡片顺序即 encode 顺序；UI 标注「decode 自动反序」。

#### (c) RouteKey 模板编辑器

- [ ] 编辑 `routeKeyTemplate`（如 `{cmd}:{act}`），实时校验占位名都对应 `role:"route"` 字段，并展示样例 routeKey。

#### (d) 实时预览（强烈建议，验证利器）

- [ ] **encode 预览**：输入样例 `route`(cmd/act…)+`body`(hex/文本)+`key`(hex)，实时调用前端 codec 模拟器（见 §2.7-bis）输出帧 hex，并按字段着色标注，让用户肉眼确认布局正确。
- [ ] **decode 预览**：粘贴一帧 hex，解析出各字段值 + routeKey + headerErr + body，验证 schema 与真实抓包一致。

#### (e) 源码视图（次级）

- [ ] 「源码」子 tab：Monaco `language="json"` + Track 1 的 JSON Schema 做补全/红线，供高级用户直接编辑；与结构化视图双向同步（以 JSON 为单一数据源，结构化视图是其受控编辑器）。
- [ ] import 接受 `.json`；`error.lua`→`errors.json` 编辑用一个简单 `code→中文` 表格 + JSON 源码视图。
- [ ] 删除旧 `ADAPTER_TEMPLATE`/`CODEC_SPEC`（Lua 函数契约文案）；新增 `CODEC_JSON_TEMPLATE`（总纲 §3.1 示例）与字段/算法说明面板。
- [ ] save：当前选中连接的结构化或源码任一改动 → 序列化为该连接的 `<proto>_<service>_codec.json` → `validateCodecSchema` → 落库（按文件名）。

### 2.3 校验模型（editorStore + index + RuntimeBar）

- [ ] `validateCodecSchema(content)`：① 合法 JSON；② 必填 `headerSize/fields/pipeline`；③ 各 field offset+size≤headerSize；④ pipeline `algo` 在前端已知算法集合内（与 Track 1 注册表对齐，前端维护一份只读清单）；⑤ `produces` 指向存在的 `pipelineOut` 字段。返回结构化错误数组（替代旧"缺失函数名"）。
- [ ] `editorStore.adapterMissing` 改为 `codecSchemaErrors: string[]`（或保留名字、改语义）；`setAdapterMissing` 同步重命名。
- [ ] `FlowEditor/index.tsx:90-106` 挂载校验改调 `validateCodecSchema`。
- [ ] `RuntimeBar.tsx:450-466` 徽标/tooltip 文案：从「缺少适配器函数」改为「协议配置有误/缺失」（UI 文本不暴露 codec/schema 技术术语，用「协议配置」）。
- [ ] 删除死代码 `editorStore` 的 `codecAdapter` panel kind（14-22、248）与 `RuntimeBar.tsx:113` 过时注释。

### 2.4 类型（src/types/）

- [ ] 新增 `src/types/codec.ts`：`CodecSchema`/`HeaderField`/`PipelineStep`/`CompressCond`/`EncryptCond` 等（镜像 Track 1 Go 类型，TS 侧）。
- [ ] `services/index.ts` 导出更新（41-42）。

### 2.5 baseline / 任务上传（baselineApi + taskActions + taskResourceDiff）

> 文件名/端点以 Track 4 为准；下列为前端侧改动。

- [ ] `baselineApi.ts:62-69`：`fetchBaselineAdapter`→ 枚举并逐份 `GET {BASELINE_PREFIX}/adapter/<proto>_<service>_codec.json`；`fetchBaselineErrorMap`→ `errors.json`。
- [ ] `taskActions.ts:118-225`：硬校验改为「IDB 至少有一份 `*_codec.json` 且 flow 引用的连接均有对应文件」；multipart 字段 `adapter/codec.lua`（单）→ `adapter/<proto>_<service>_codec.json`（多）、可选 `adapter/error.lua`→`adapter/errors.json`；错误文案更新（145 行的「协议适配器」面板提示对齐新 UI）。
- [ ] `taskResourceDiff.ts:15-53`：`adapters` 改为枚举所有 `*_codec.json`（+`errors.json`）；baseline 逐份取。
- [ ] `BaselineSyncModal.tsx:238`：adapter 类型的 DiffEditor `language` 由 `lua`→`json`；标签「Adapter」可改「协议配置」。
- [ ] `TaskStartModal.tsx`：冲突弹窗随 BaselineSyncModal 改动生效；资源摘要（808-829）可补充展示「协议配置」。

### 2.6 路由键真实计算（增强，可选）

- [ ] `refsGraph.ts:83-96` `routeKey()`：现在是「JSON 排序」伪匹配；声明式 codec 下 routeKey 是确定的（`routeKeyTemplate` + route 字段）。可在前端按 `codec.json` 的 `routeKeyTemplate` 真实计算 routeKey，提升 listen 去重/校验准确度。`refsCheck.ts` 同步。

### 2.7-bis 前端算法清单 与 实时预览（单一真相源取舍）

可视化编辑器的算法下拉与实时预览需要"知道有哪些算法、各算法长什么样"。为**避免前端重复实现 codec 导致与 Go 引擎漂移**，采取：

- **算法清单（仅元数据）**：前端维护一份**只读清单**——每个算法的 `name` + `op` 分类 + 参数 schema（如 `xor_carry_rol` 有 `rol:int`）。仅用于下拉与参数表单，**不含实现**。该清单由 Track 1 注册表导出（建议后端提供 `GET /sbot/codec/algorithms` 返回算法元数据，前端拉取；或约定一份静态 JSON 与后端同源维护）。新增算法时此清单同步更新。
- **实时预览（encode/decode 模拟）**：**推荐走后端预览端点**，调用真实 Go 引擎，杜绝逻辑漂移：
  - 新增 `POST /sbot/codec/preview`（Admin），入参 `{ schema, mode: "encode"|"decode", route, bodyHex, keyHex, frameHex }`，出参 `{ frameHex / fields + routeKey + headerErr + bodyHex, error? }`。
  - 该端点用 Track 1 的 `adapter.NewSchemaAdapter` 跑一次（无网络、无副作用）。**跨切**：实现归 Track 4(Admin 路由) + Track 1(引擎)，前端调用归本轨道。
  - 备选（不推荐）：前端 TS 重写一份 codec 模拟器——加密类算法（aes/rc4/xxtea）在 TS 实现量大且易与 Go 漂移，仅在无法加预览端点时考虑，且只覆盖 `none/xor*` 等简单算法。
- [x] **已确认**：新增 `POST /sbot/codec/preview` 与 `GET /sbot/codec/algorithms`（Admin，实现见 T4；引擎见 T1）。前端预览/算法清单一律走后端真实引擎，杜绝漂移。

### 2.7 Lua API 文档（随后端删 adapter 模块）

- [ ] `luaApiSpec.ts:716-770`：删除 `adapter` 模块条目（后端 `loadAdapterModule` 已删）；`LuaApiPopover.tsx:19` 移除 `adapter` 颜色项；`__tests__/luaApiSpec.test.ts` 同步。

---

## 3. 实施切片（按顺序落地）

### 3.1 资源存储与文件名切换

- [ ] IndexedDB 仍沿用 `stressbot-resources-adapter`，但内部 key 从 `codec.lua` / `error.lua` 改为 `codec.json` / `errors.json`。
- [ ] `resourcesStore.ts` 中 `getAdapterScript` / `setAdapterScript` / `clearAdapterScript`（单文件）正名为按文件名读写的 `getCodecSchema(file)` / `setCodecSchema(file,...)` / `clearCodecSchema(file)` + `listCodecFiles()`；如为降低改动量暂保留旧函数名，内部也必须只读写新 key、不做旧 key fallback。
- [ ] `ResourceType` 的 `'adapter'` 可保留作为资源分类，但 UI 文案统一展示为「协议配置」。
- [ ] 删除 `REQUIRED_ADAPTER_FUNCTIONS` 与 Lua 函数校验，改为对每份 `*_codec.json` 调 `validateCodecSchema(content)`。
- [ ] baseline 同步、三方合并、资源差异仍复用现有流程，只替换为多文件名枚举与 diff language。

### 3.2 类型与校验模型

- [ ] 新增 `src/types/codec.ts`，镜像 Track 1 Go schema：`CodecSchema`、`HeaderField`、`PipelineStep`、`PipelineWhen`、`PipelineProduce`、`FrameConfig`、`ErrorMap` 等。
- [ ] `validateCodecSchema` 分两层：前端结构校验（JSON、必填、offset/size、role 数量、pipeline 引用）+ 后端预览/保存前深校验（调用 Admin preview/validate 能力时返回真实 Go 校验错误）。
- [ ] `editorStore.adapterMissing` 正名为 `codecSchemaErrors` 或等价语义；RuntimeBar/TaskStartModal 文案改为「协议配置缺失/有误」。
- [ ] 删除 FlowEditor 中过时 `codecAdapter` panel 死代码，避免与协议 codec 概念混淆。
- [ ] UI 文本避免出现 codec/schema/adapter 等技术术语；开发变量名可保留 codec 以和代码语义一致。

### 3.3 协议配置可视化编辑器

- [ ] `ResourcesDrawer.tsx` 适配器 tab 改为「协议配置」tab，顶部连接选择器（多份 `*_codec.json`，可新建/复制/删除），下方主视图是当前连接的结构化编辑器，源码 JSON 为次级视图。
- [ ] 帧布局编辑器：header/trailer 字节条带 + 字段表 + role 联动表单；实时校验字段越界/重叠、length/route 必需性、checksumOut.from 引用。
- [ ] Pipeline 编辑器：步骤卡片支持排序、增删、op/algo/params/flag/when/onError；encrypt 支持单向 `{encode,decode}` offset 与 produces。
- [ ] RouteKey 模板编辑器：校验占位名均来自 `role:"route"` 字段，展示样例 routeKey。
- [ ] 源码视图：Monaco `language="json"`，结构化视图与源码视图双向同步，以 JSON 对象为单一数据源。
- [ ] `errors.json` 编辑器：提供 code→中文表格视图 + JSON 源码视图；保存为字符串落库。

### 3.4 后端算法清单与实时预览接线

- [ ] 新增 `services/codecApi.ts` 或并入现有服务层，封装 `GET /sbot/codec/algorithms` 与 `POST /sbot/codec/preview`；组件禁止直接 fetch。
- [ ] 算法下拉从 `codec/algorithms` 获取元数据；失败时给出「算法清单加载失败」提示，不使用本地伪实现兜底。
- [ ] encode/decode 预览调用后端真实 Go 引擎；入参为当前 schema JSON、mode、route/body/key/frame；错误原样转成中文提示。
- [ ] 预览只用于编辑辅助，不写入 IDB、不修改 baseline、不参与任务下发。

### 3.5 baseline / 任务上传 / 任务启动

- [ ] `baselineApi.ts` 路径改为逐份 `/adapter/<proto>_<service>_codec.json`、`/adapter/errors.json`。
- [ ] `taskActions.ts` 启动前硬校验 IDB 中 flow 引用的每条连接都有对应 `*_codec.json`；multipart 字段改为多份 `adapter/<proto>_<service>_codec.json` 与可选 `adapter/errors.json`。
- [ ] `taskResourceDiff.ts` / `BaselineSyncModal.tsx` / `TaskStartModal.tsx` 的 adapter 多文件名、diff language、资源摘要文案同步为 JSON/协议配置。
- [ ] 不上传旧 `codec.lua` / `error.lua`；也不在前端尝试从旧 key 自动迁移，符合一刀切策略。

### 3.6 flow 配置编辑器同步 T2 新字段/动作

- [ ] Flow 类型增加 `ListenRef.queueSize?: number`；校验规则：未填合法，显式 `<=0` 报错。
- [ ] ListenDef 编辑/校验禁用 `script` callback；UI 中不再提供脚本 callback 配置入口，只保留缓存 listen 与 `s2cProto + store`。
- [ ] action pattern 增加 `tcpHeartbeat` / `udpHeartbeat`；表单字段支持 `service`、`intervalMs`、`route`、`c2sProto`、`bindings`、`skipWhenMissing`。
- [ ] heartbeat bindings UI 只开放 `fixed` / `state` / `counter` / `timestamp`；隐藏或禁用随机类、map/list 类、Lua 条件。
- [ ] flow 校验中识别 heartbeat action：`intervalMs>0`、service 必填、route 必填、binding type 子集合法。
- [ ] RuntimeBar / TaskStartModal 的校验摘要同时展示 flow 新字段错误与协议配置错误。

### 3.7 路由键真实计算与 listen 校验增强

- [ ] 基于当前 `codec.json` 的 `routeKeyTemplate` 与 route 字段，在前端实现 routeKey 计算，仅用于 flow/listen 去重与提示。
- [ ] `refsGraph.ts` / `refsCheck.ts` 从 JSON 排序伪 key 改为真实 routeKey；若协议配置缺失或有误，则降级为“不执行 listen 去重校验并提示先修复协议配置”，不做静默 fallback。
- [ ] 重复 listenRef 检查同步 T2 语义：同一 server+routeKey 下 queueSize 或 listen/store 模式冲突时报错。

### 3.8 Lua API 文档与旧术语清理

- [ ] `luaApiSpec.ts` 删除 `adapter` 模块文档与示例；`LuaApiPopover.tsx` 删除 adapter 颜色项；测试同步。
- [ ] 资源面板、启动弹窗、baseline 弹窗、README 中所有 `codec.lua` / `error.lua` / Lua 适配器说明改为按连接的多份 `*_codec.json` / 共享 `errors.json` / 协议配置。
- [ ] `cmd/web/README.md` 补充「协议配置」资源说明。

## 4. 任务清单（汇总）

- [ ] 存储层 `resourcesStore.ts` 改造（key/URL/校验/同步）。
- [ ] `src/types/codec.ts` 新类型（镜像 Track 1 Go 类型）。
- [ ] **可视化编辑器**（本轨道核心，§3.3）：帧布局表 + 字节条带 + 角色联动表单 + pipeline 卡片 + routeKey 模板 + 源码视图（Monaco JSON 同步）。
- [ ] **实时预览 + 算法清单**（§3.4，走 Admin 后端端点）。
- [ ] `validateCodecSchema` + editorStore/index/RuntimeBar 校验接线 + 删死代码。
- [ ] `baselineApi.ts` / `taskActions.ts` / `taskResourceDiff.ts` / `BaselineSyncModal.tsx` / `TaskStartModal.tsx` 文件名 + 字段 + 语言更新。
- [ ] FlowEditor 同步 T2 新字段/动作：`ListenRef.queueSize`、禁用 `ListenDef.script`、`tcpHeartbeat` / `udpHeartbeat`、heartbeat binding 子集。
- [ ] 删 `luaApiSpec.ts` adapter 模块 + popover + 测试。
- [ ] `refsGraph.ts` 真实 routeKey 计算与 listen 冲突校验增强。
- [ ] `cmd/web/README.md` 补充「协议配置（codec.json）」资源说明。

---

## 5. 验收

- [ ] `npx tsc -b` 无类型错误；`npm run test`（Vitest）通过。
- [ ] 协议配置 tab 能按连接新建/复制/编辑/导入/保存/清除多份 `*_codec.json` 与共享 `errors.json`，畸形 JSON/schema 有清晰中文报错。
- [ ] 结构化编辑器与源码 JSON 双向同步；字段表、pipeline、routeKeyTemplate、errors 表格保存后内容稳定。
- [ ] 算法清单与实时预览均走后端端点；预览失败不影响保存，但有明确提示。
- [ ] 任务启动能正确把 flow 引用连接对应的多份 `*_codec.json`(+`errors.json`) 经 multipart 上传；baseline 拉取/三方合并/冲突弹窗对各 JSON 正常。
- [ ] FlowEditor 能编辑/校验 `ListenRef.queueSize`、`tcpHeartbeat` / `udpHeartbeat`；不再提供 `ListenDef.script` callback 配置入口。
- [ ] listen routeKey 去重按 `codec.json` 真实模板计算；协议配置缺失时给出提示，不静默用 JSON 排序伪 key。
- [ ] 全前端无残留对 `codec.lua`/`error.lua`/adapter Lua 模块的引用。
- [ ] UI 文本不暴露技术术语（用「协议配置」而非 codec/schema/adapter）。

---

## 6. 风险

| 风险 | 缓解 |
|---|---|
| 前后端文件名/端点漂移 | 以总纲 §3 + Track 4 钉死的 `codec.json`/`errors.json` 与端点为准，本轨道只引用 |
| 前端算法清单与后端注册表不同步 | 算法清单从 `GET /sbot/codec/algorithms` 获取；前端不维护算法实现 |
| 校验过严挡住合法 schema | 前端校验只做"结构性"检查；语义最终以后端 `schema.Validate` 为准（任务启动若后端拒绝则提示） |
| 可视化编辑器一次性过大 | 按实施切片先落资源 key/上传/源码 JSON 校验，再落结构化 editor 与预览；保证每步可编译可回归 |
| routeKey 真实计算依赖协议配置 | 协议配置缺失/错误时停止 listen 去重增强并提示修复，不静默 fallback 到伪 key |
