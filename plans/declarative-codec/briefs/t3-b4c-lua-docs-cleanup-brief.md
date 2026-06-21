# T3 Batch-4 任务 C — Lua API 文档清理 + 旧术语（§3.8，T3 收尾）

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：T2-C2-Lua 已删后端 `loadAdapterModule`（业务 LState 不再注入 adapter 模块）；codec 全 Go。
> 本任务是 T3 最后一块：清前端残留的 adapter Lua 模块文档 + 旧术语。

## 1. 任务定位

后端 adapter Lua 模块（`loadAdapterModule` / `PreloadModule("adapter")`）在 T2-C2-Lua 已删，业务 Lua 不再有
adapter 模块。但前端 `luaApiSpec.ts` 仍文档化 `adapterModule`（Lua API 参考里列着已不存在的 adapter 函数），
`LuaApiPopover` 仍有 adapter 颜色项，部分文案仍提 `adapter.expected_route_key`（现由 Go codec 计算）。
本任务清掉这些残留，让前端 Lua API 文档与后端实际一致。

## 2. 现状（已 recon）

- `cmd/web/src/components/FlowEditor/lua/luaApiSpec.ts:674` `const adapterModule: LuaModule`（整块 adapter 模块文档）+ `:947` 在 modules 数组里引用它。
- 同文件 `:222` / `:276`：`tcp_request_route`/`udp_request_route` 的 detail 文案提 `adapter.expected_route_key 计算后用于 responseMap 匹配`——route 现由 Go codec.ExpectedRouteKey 计算，文案 stale。
- `cmd/web/src/components/FlowEditor/lua/LuaApiPopover.tsx:19` `adapter: 'geekblue'`（模块颜色映射里的 adapter 项）。
- `cmd/web/src/components/FlowEditor/lua/__tests__/luaApiSpec.test.ts`：若有断言 adapter 模块覆盖/存在，同步删。
- **先 grep** `cmd/web/src` + `cmd/web/README.md` 全量找 residual：`adapter` 模块文档、`adapter.expected_route_key`、`codec.lua`/`error.lua` 在**用户可见文案**（非历史注释）的残留。`resourcesStore.ts:189` / `codecStorage.test.ts:5` 是**历史背景注释**（解释重构由来），**保留**不算 stale。

## 3. 实现规格

### 3.1 luaApiSpec.ts
- **删 `adapterModule` 整块**（674 起）+ 从 modules 数组（947）移除引用。
- **清 stale `adapter.expected_route_key` 文案**（222/276）：改为不提 adapter，或改为「route 由协议配置（codec）计算后用于 responseMap 匹配」（UI 不暴露 codec 术语的话用「协议配置」；但 luaApiSpec 是给写 Lua 的高级用户看的，提「协议配置计算的 route」即可，去掉 adapter 字样）。
- grep 该文件其它 `adapter` 残留全清。

### 3.2 LuaApiPopover.tsx
- 删 `adapter: 'geekblue'` 颜色项（19）。若该映射其它地方依赖 adapter key，一并清。

### 3.3 测试同步
- `luaApiSpec.test.ts`：删/改任何断言 adapter 模块的用例（如「所有模块都文档化」若含 adapter，或专门 adapter 覆盖用例）。
- `tcp_request_route`/`udp_request_route` 若有 detail 断言，同步新文案。

### 3.4 README / 文案
- `cmd/web/README.md`：若有 codec.lua/error.lua/Lua 适配器说明，改为 codec.json/errors.json/协议配置；若无则补一句「协议配置（codec.json）」资源说明（计划 §3.8）。
- grep 确认无其它用户可见 stale 文案。

## 4. 全局约束（bind）

- **改动文件**：`luaApiSpec.ts` + `LuaApiPopover.tsx` + `luaApiSpec.test.ts` + `cmd/web/README.md`（+ grep 发现的其它 stale 文案文件）。**严禁动** 后端、services 生产逻辑、codecEditor、FlowEditor 校验/编辑器逻辑（纯文档/测试/文案清理）。
- **只清用户可见 stale 文案 + 已删模块的文档**；**保留历史背景注释**（resourcesStore:189 / codecStorage.test:5 等解释重构由来的注释）。
- **UI 文本不暴露技术术语**：面向用户的文案用「协议配置」；luaApiSpec 是 Lua API 参考（给写脚本的高级用户），可用必要技术词但去掉 adapter 字样。
- 类型安全；`ALL_ACTION_PATTERNS` 等常量不动；**不要 git commit**。
- 删 adapterModule 后 `npm run test` 必须绿（测试同步）。

## 5. 验证（全绿才算 DONE）

- `cd cmd/web && npx tsc -b` exit 0。
- `cd cmd/web && npm run test` 通过（luaApiSpec 测试同步后绿；既有 286 不回归）。
- `git grep -n "adapterModule\|loadAdapterModule\|adapter\.expected_route_key" cmd/web/src` → **空**（adapter 模块文档/stale 文案清零）。
- 自查 `git diff --stat`：改动限于上述文件。

## 6. 报告

写到 `plans/declarative-codec/reports/t3-b4c-lua-docs-cleanup-report.md`：实现要点、删了什么、文案改了什么、测试同步、grep 清零证据、tsc/test 结果、自审、贴 `git diff --stat`。

返回消息只含：① 状态；② 改动文件清单；③ 一行测试摘要；④ concerns。有歧义先问。
