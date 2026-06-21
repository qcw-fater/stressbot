# T3 Batch-4 任务 B — routeKey 真实计算（§3.7）执行报告

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）
> Brief：`plans/declarative-codec/briefs/t3-b4b-routekey-real-brief.md`
> 状态：**DONE**

## 1. 实现要点

routeKey 从「总伪 JSON 排序」升级为 **server 感知的真实计算**：

- server 在 templates Map 有 template → `computeRouteKey(template, route)`；
- server 无 codec → 伪 routeKey 降级，**收集到 `missingCodecServers`** 供 refsCheck 显式产 `ROUTEKEY_CODEC_MISSING` warning（不静默伪 key）；
- codec 有但 route 缺占位字段（computeRouteKey → null）→ 伪 routeKey 降级（flow 数据问题，查重仍可用，不产 codec warning）。

既有 listen 校验语义不变（去重/预注册/dangling/refCount），只是 routeKey 更准 + 无 codec 时多一条 warning。

## 2. computeRouteKey / loadRouteKeyTemplates

`cmd/web/src/components/FlowEditor/listens/routeKeyResolver.ts`（新）：

- **`computeRouteKey(template: string, route: unknown): string | null`** — 纯函数。占位正则 `\{([A-Za-z_]\w*)\}`（与 `resourcesStore.validateCodecSchema` 的 `ROUTE_KEY_PLACEHOLDER_RE` 一致），`String(value)` 代入。route 非对象 / 占位字段缺失（含 null/undefined）→ null。
- **`pseudoRouteKey(route: unknown): string`** — 旧伪实现（key 排序稳定 JSON），作为降级 + `listenTemplateDefaults.ts` 展示摘要。
- **`loadRouteKeyTemplates(): Promise<Map<string,string>>`** — `listCodecFiles()` → 逐份 `JSON.parse` 取 `routeKeyTemplate`（字符串）+ `codecFileNameToConnName` 得 server。坏 JSON / 无 template / 非字符串 跳过（不抛）。
- **`resolveRouteKeyForServer(server, route): string`** — refsGraph 与 refsCheck 共用的 server 感知解析（保证查重 key 一致）。

## 3. 接入方案：**cache**（非透传）

### 选择 + 理由

`validateFlow` / `buildRefsGraph` 的调用方（`ValidationReport.tsx:31` / `Toolbar.tsx:79` / `flowStore.ts:141,526` / `taskActions.ts:85`）中 **3/4 在 React `useMemo` / zustand store action 中同步调用**，渲染期无法 `await loadRouteKeyTemplates()`。逐个改 async（Suspense / effect 重算）会扩散到 3 个组件 + store，超出本任务改动范围且有 UI 回归风险。故按 brief 授权的 fallback 方案采用 **模块级 cache**。

### 失效条件（已实现，不靠手动）

- `refreshRouteKeyTemplates()`（async，in-flight promise 复用）；
- `getRouteKeyTemplatesSync()`（sync，未加载返回空 Map → 全降级 + ROUTEKEY_CODEC_MISSING，安全）；
- `index.tsx` mount effect：`refreshRouteKeyTemplates()` + `subscribeResources(() => refreshRouteKeyTemplates())`——codec 文件增删改（resourcesStore.notify）自动刷新 cache。无需手动失效。
- `__resetRouteKeyTemplateCacheForTest()` 供测试隔离。

## 4. Warning 机制（不静默伪 key）

`refsGraph.buildRefsGraph` 在遍历 listenRefs 时收集 `missingCodecServers`（server 非空且 IDB 无对应 codec template 的 server 集合），随 RefsGraph 返回。`refsCheck.validateFlow` 据 `graph.missingCodecServers` 每个 server 产一条 `ROUTEKEY_CODEC_MISSING` warning：

```
连接 tcp:logic 的协议配置缺失或有误，listen 去重使用近似匹配，请先在协议配置面板修复
```

文案用「连接」「协议配置」（不暴露 codec/schema 术语，符合 UI 文案约定）。

## 5. 既有校验不回归

- refsCheck 的 pre-reg 校验（R14）改用 `resolveRouteKeyForServer`（与 buildRefsGraph 同解析器，key 一致）；
- 既有 55 条 refsCheck 测试 + 6 条 refsGraph 测试全绿——这些测试不预置 codec（cache 空 → 全伪 key 降级，行为与改动前等价，只是多产 ROUTEKEY_CODEC_MISSING 警告；但既有测试断言的都是 error/具体 warning code，不被 ROUTEKEY_CODEC_MISSING 触发）；
- `listenTemplateDefaults.ts` 仍 import `routeKey`（展示摘要用），保留导出作 passthrough 到 `pseudoRouteKey`。

## 6. 测试（TDD：先红后绿）

- `routeKeyResolver.test.ts`（16 tests，新）— computeRouteKey（单/多占位、缺字段→null、非对象→null、无占位原样、数值字符串值、占位重复、null/undefined 值→null）；pseudoRouteKey（键序稳定、不同值不同 key、空对象）；loadRouteKeyTemplates（mock listCodecFiles：正常 Map、跳过坏 JSON、跳过无 template、跳过非字符串 template、空列表）。
- `routeKeyIntegration.test.ts`（4 tests，新）— ROUTEKEY_CODEC_MISSING 在 codec 缺失时产、注入 template 后不产、DUPLICATE_REGISTER 命中真实 routeKey（'1:2' 而非伪 JSON）、computeRouteKey→null 时降级伪 key 且不产 ROUTEKEY_CODEC_MISSING。

## 7. tsc / test 结果

```
$ npx tsc -b
（exit 0，无输出）

$ npm run test
Test Files  22 passed (22)
     Tests  286 passed (286)
```

新增 4 条集成测试后总数从 282 → 286。

## 8. 自审

- ✓ 改动限 refsGraph.ts + refsCheck.ts（仅 routeKey 段）+ index.tsx（mount effect）+ 新 routeKeyResolver.ts + 2 个新测试；
- ✓ 未动 services/（只复用 listCodecFiles / codecFileNameToConnName）、codecEditor/、types/、后端；
- ✓ 不静默伪 key（codec 缺失 → 显式 ROUTEKEY_CODEC_MISSING warning）；
- ✓ 不 `??` 兜错（computeRouteKey null → 伪 key 是数据问题合理降级，非兜错）；
- ✓ 类型安全（tsc 0 error）；
- ✓ UI 文案中文、不暴露 codec/schema（用「协议配置/连接」）；
- ✓ 未 git commit；
- ✓ TDD：computeRouteKey/loadRouteKeyTemplates 先写测试。

## 9. git diff --stat（本任务相关文件）

```
 cmd/web/src/components/FlowEditor/index.tsx        |  12 +-
 cmd/web/src/components/FlowEditor/listens/refsGraph.ts | 61 +++++-
 cmd/web/src/components/FlowEditor/validation/refsCheck.ts | （仅 routeKey 段：import 改、ROUTEKEY_CODEC_MISSING warning、2 处 routeKey→resolveRouteKeyForServer）
?? cmd/web/src/components/FlowEditor/listens/routeKeyResolver.ts
?? cmd/web/src/components/FlowEditor/listens/routeKeyResolver.test.ts
?? cmd/web/src/components/FlowEditor/validation/routeKeyIntegration.test.ts
```

> 注：工作树中 refsCheck.ts / refsGraph.ts 的 diff 还含 B4a（heartbeat/queueSize/script，tasks #419-424 已完成）的未提交改动，非本任务产出。本任务在 refsCheck.ts 的实际新增仅为上述 routeKey 段（已 grep 确认）。

## 10. Concerns

- **cache vs 透传**：选 cache，理由见 §3——validateFlow 调用方 3/4 sync，无法 await IDB。失效靠 `subscribeResources` 自动刷新（codec 文件变更触发）。未初始化时安全降级（空 Map + ROUTEKEY_CODEC_MISSING）。
- **调用方改动范围**：仅 `index.tsx` 加一个 mount effect（加载 + subscribe）。未改 ValidationReport.tsx / Toolbar.tsx / flowStore.ts / taskActions.ts（透传方案需改这些，cache 方案不需要）。
- cache 首次加载完成前（mount effect 的 `refreshRouteKeyTemplates` resolve 前）的首次 validateFlow 会全降级 + 产 ROUTEKEY_CODEC_MISSING；加载完成后由 zustand/React 重渲染自然重算。属可接受的一拍延迟（proto 加载等同样模式）。

## 11. stale-warning 修正（B4-B review Important）

**问题**：§10 上一段「加载完成后由 zustand/React 重渲染自然重算」的结论实际不成立。routeKey 模板 cache 加载（`refreshRouteKeyTemplates()`）只更新 `routeKeyResolver.ts` 的**模块级**状态，**没推任何信号进 React/zustand**。`ValidationReport.tsx` 的 `useMemo(() => validateFlow(flow), [flow])` 依赖只有 `[flow]`，cache 加载时 `flow` 引用不变 → useMemo 不重算 → `ROUTEKEY_CODEC_MISSING` warning 一直显示，直到用户下次编辑 flow 才消失。（proto 模式靠 `protoStore.set({status:'ready'})` 触发 zustand 重渲染；routeKey 缺这个信号。）

**修法（对齐 proto 模式的「store 变更驱动 React 重算」思路）**：

1. `store/editorStore.ts`：加 `routeKeyTemplatesVersion: number`（初始 0）+ `bumpRouteKeyTemplatesVersion: () => void`（bump +1）。
2. `index.tsx`：mount effect 里 `await refreshRouteKeyTemplates()` 完成后调 `bumpRouteKeyTemplatesVersion()`；`subscribeResources` 回调里 refresh 完成后也 bump（codec CRUD 后 cache 刷新 → 触发重算）。
3. `ValidationReport.tsx` / `Toolbar.tsx`：把 `routeKeyTemplatesVersion`（经 `useEditorStore` selector 取）加进各自 `validateFlow` 的 useMemo 依赖。
4. `flowStore.ts` / `taskActions.ts` 的 `validateFlow` 调用：**不加依赖**——前者是 zustand store action（每次 mutation 直接调，非 useMemo 缓存，读的是同一模块级 cache），后者是任务提交前的同步校验（一次调用即取最新 cache），二者不存在「cache 变更不触发重算」的 React useMemo 问题。

**效果**：cache 加载/刷新后 bump version → ValidationReport / Toolbar 的 useMemo 重算，warning 自动随 codec 状态刷新（不再卡到下次 flow 编辑）；codec 真缺失时仍正确显示 ROUTEKEY_CODEC_MISSING。

**改动文件**（`git diff` 限于以下 4 个文件 + 本报告）：

- `cmd/web/src/components/FlowEditor/store/editorStore.ts`（+11：接口 +1 字段 +1 action，初始值 +1 setter）
- `cmd/web/src/components/FlowEditor/index.tsx`（routeKey mount effect 内 refresh 完成后 bump）
- `cmd/web/src/components/FlowEditor/validation/ValidationReport.tsx`（useMemo 依赖 +routeKeyTemplatesVersion）
- `cmd/web/src/components/FlowEditor/panels/Toolbar.tsx`（同上）

**验证**（全绿）：
- `cd cmd/web && npx tsc -b` → exit 0。
- `cd cmd/web && npm run test` → **22 test files / 286 tests 全通过**（既有 286 不回归）。
- grep 自查：`useMemo(() => validateFlow(...), [...])` 两处（ValidationReport.tsx:34、Toolbar.tsx:81）依赖数组均已含 `routeKeyTemplatesVersion`；`flowStore.ts:141/526` 与 `taskActions.ts:85` 非 React useMemo 调用点，无需加依赖。
