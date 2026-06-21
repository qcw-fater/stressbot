# T3 Batch-4 任务 C 报告 — Lua API 文档清理 + 旧术语（§3.8，T3 收尾）

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`
> Brief：`plans/declarative-codec/briefs/t3-b4c-lua-docs-cleanup-brief.md`
> 状态：**DONE**（全绿验证通过）

## 1. 实现要点

后端 `loadAdapterModule` 在 T2-C2-Lua 已删（业务 LState 不再注入 adapter 模块），本任务清理前端残留：
让 `luaApiSpec.ts`（Monaco completion/hover/signature 三处共享的事实源）不再文档化已不存在的 adapter Lua 函数；
`LuaApiPopover` 删掉孤儿颜色项；两处 `_request_route` detail 文案不再提 `adapter.expected_route_key`（route 现由 Go
codec.ExpectedRouteKey 计算）；README 补一条协议配置资源说明。测试同步，历史背景注释保留。

## 2. 删了什么 / 改了什么

### 2.1 `cmd/web/src/components/FlowEditor/lua/luaApiSpec.ts`（-56 行）
- **删整块 `const adapterModule: LuaModule`**（原 674–728）：5 个 adapter 函数文档（encode_tcp / encode_udp /
  decode_tcp / decode_udp / expected_route_key）整块移除——这些函数在 T2-C2-Lua 之后业务 Lua 不再可调用。
- **从 `LUA_MODULES` 导出数组移除 `adapterModule` 引用**（原 947）。现数组 7 模块（robot/network/proto/utils/json/log/share）。
- **清 stale `adapter.expected_route_key` 文案**（原 222 / 276，tcp_request_route / udp_request_route 的 detail）：
  - 旧：`response_route 经 adapter.expected_route_key 计算后用于 responseMap 匹配`
  - 新：`response_route 由协议配置（codec）计算 route 后用于 responseMap 匹配`
  - 不再暴露 adapter 字样，保留 route/responseMap 技术词（luaApiSpec 给写 Lua 的高级用户）。

### 2.2 `cmd/web/src/components/FlowEditor/lua/LuaApiPopover.tsx`（-1 行）
- 删 `MODULE_COLOR` 里的 `adapter: 'geekblue'`（原 19）。该映射其余 key（robot/network/proto/utils/json/log）不依赖 adapter。

### 2.3 `cmd/web/src/components/FlowEditor/lua/__tests__/luaApiSpec.test.ts`（拆 1 → 2 用例）
- 原 `it('utils / json / log / adapter 模块都存在')` 断言 adapter 存在，现已失效。拆为：
  1. `it('utils / json / log 模块都存在')` —— 去掉 adapter。
  2. **新增** `it('adapter 模块已随声明式 codec 重构移除（业务 Lua 不再注入 adapter）')` —— 正向断言
     `getLuaModule('adapter')` 为 `undefined`，且 `LUA_MODULES` 不含名为 `'adapter'` 的模块（防回归）。
- 其余 15 个 luaApiSpec 用例（robot/network/proto 覆盖、函数唯一性、renderSignature/renderDoc 格式）未动，全绿。

### 2.4 `cmd/web/README.md`（+1 行）
- 「配置依赖」列表原本无 codec 资源说明。按 brief §3.4「无则补一句」补：
  `- conf/adapter/*_codec.json / conf/adapter/errors.json —— 协议配置（codec.json）：声明式编解码（帧布局 /
   pipeline / routeKey 模板）+ 错误码描述；在资源抽屉「协议配置」Tab 内编辑，由 Go 端 codec 在运行时加载`
- README 全文无 codec.lua / error.lua / Lua 适配器说明（grep 确认），无需删改；只补。

## 3. 测试同步

- `luaApiSpec.test.ts` 原 16 个 it → 现 17 个 it（拆分 + 新增正向防回归断言）。
- 全套：`npm run test` = **22 files / 287 passed**（先前 286；+1 来自新断言）。
- tsc：`npx tsc -b` exit 0。

## 4. grep 清零证据

```
$ git grep -n "adapterModule\|loadAdapterModule\|adapter\.expected_route_key" cmd/web/src
$ echo $?
1
```

退出码 1 = 0 matches，**清零**。

`cmd/web/src/components/FlowEditor/lua/luaApiSpec.ts` 内 `adapter`（大小写不敏感）全文 grep 同样 0 matches——
detail 文案改写后整文件不再出现 adapter 字样。

## 5. 边界判断（未改的相邻残留，留作记录）

按 brief §4「严禁动 services 生产逻辑 / codecEditor / FlowEditor 校验/编辑器逻辑」，以下 grep 命中的相邻
`adapter` 用法**刻意保留**（非本任务范围，且属合法用法或历史背景注释）：

- `services/resourcesStore.ts:189` / `services/__tests__/codecStorage.test.ts:5` —— brief 明确点名要保留的历史
  背景注释（解释 T3 重构由来：codec.lua → codec.json）。
- `services/baselineApi.ts` / `services/taskActions.ts` / `services/taskResourceDiff.ts` / `services/env.ts` ——
  这些是 adapter 资源类型的**内部命名 / URL 路径 / form-data field key**（`adapter/index.json`、`adapter/` 前缀、
  ResourceType='adapter'），不是用户可见文案，也非 adapter Lua 模块文档；属 services 生产逻辑，禁动。
- `components/modules/ResourcesDrawer.tsx` / `components/modules/BaselineSyncModal.tsx` —— 用户可见 Tab 标签已是
  「协议配置」（ResourcesDrawer.tsx:145），BaselineSyncModal 的 `adapter: { text: 'Adapter' }` 是资源类型标签、
  非 Lua 模块文档，且属资源管理 UI（非 luaApiSpec 范畴），按 brief「改动限于 4 文件」未动。
- `components/FlowEditor/editors/ActionEditor/DeclarativeForm.tsx:106` —— UI hint `引擎透传给 adapter.codec`：
  这是 FlowEditor 编辑器逻辑（brief §4 明列「严禁动 FlowEditor 校验/编辑器逻辑」），且 `adapter.codec` 是内部
  字段路径术语、非 adapter Lua 模块文档，非本任务 stale 清理对象。留。
- `components/FlowEditor/listens/routeKeyResolver.ts:5` —— 历史背景注释（解释旧 Lua adapter → 声明式 codec 演进），
  同 resourcesStore:189 类，保留。
- `services/__tests__/validateCodecSchema.test.ts:15`、`components/modules/codecEditor/*` —— codec 测试路径与
  codecEditor 内部注释，属禁动范围。

## 6. 自审

- [x] 改动限于 brief §4 指定的 4 文件（luaApiSpec.ts / LuaApiPopover.tsx / luaApiSpec.test.ts / README.md），
      未触 services / codecEditor / FlowEditor 编辑/校验逻辑。
- [x] `git grep` 三模式（adapterModule / loadAdapterModule / adapter.expected_route_key）在 `cmd/web/src` 清零。
- [x] `luaApiSpec.ts` 全文无 adapter 字样。
- [x] 历史背景注释（resourcesStore:189 / codecStorage.test:5 / routeKeyResolver.ts:5）保留。
- [x] 常量 `ALL_ACTION_PATTERNS` 未动；未碰后端；未 git commit。
- [x] tsc exit 0；npm run test 287 passed（+1 防回归断言）。
- [x] luaApiSpec detail 文案去 adapter 字样但保留必要技术词（route / responseMap / codec），符合
      「luaApiSpec 给写 Lua 的高级用户，可用必要技术词但去掉 adapter」。
- [x] README 用「协议配置」面向用户词汇，附「协议配置」Tab 路径指引。

## 7. tsc / test 结果

```
$ cd cmd/web && npx tsc -b
EXIT=0

$ cd cmd/web && npm run test
 Test Files  22 passed (22)
      Tests  287 passed (287)
EXIT=0
```

## 8. `git diff --stat`（本任务 4 文件）

```
 cmd/web/README.md                                  |   1 +
 .../components/FlowEditor/lua/LuaApiPopover.tsx    |   1 -
 .../FlowEditor/lua/__tests__/luaApiSpec.test.ts    |  11 +++-
 .../src/components/FlowEditor/lua/luaApiSpec.ts    | 61 +---------------------
 4 files changed, 12 insertions(+), 62 deletions(-)
```

> 注：`git diff --stat`（全量）还显示 DeclarativeForm.tsx / PatternSelector.tsx / actionPrune.ts /
> ListenEditor.tsx / refsCheck.ts / types/*.ts 等文件——这些是 Batch-3（心跳/queueSize）与 T2-D 的既存未提交
> 改动，**非本任务触碰**。本任务实际改动严格限于上述 4 文件。
