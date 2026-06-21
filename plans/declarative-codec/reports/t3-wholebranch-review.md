# T3 Whole-Branch Review — 必修项修正报告

## 1. whole-branch review 3 维度结论摘要

声明式 codec 重构（T1+T4+T2+T3 全轨）whole-branch review 并行跑 3 维度：

| 维度 | 范围 | 结论 |
|------|------|------|
| **A. 并发 / Lua 运行时** | `network/`、`script/`、`robot/`、`heartbeat.go`、`connectionPump`、锁删除 | 无阻塞。T2-D 删锁 + connectionPump 独立运行 + 主流程阻塞语义正确；Lua 仅在主流程同步执行，连接收包与声明式心跳由 Go goroutine 独立推进，无竞态。 |
| **B. 文档 / 测试 / 范围一致性** | `CLAUDE.md`、`README.md`、`docs/`、`cmd/web/` 用户面表述 + 测试覆盖 | **1 阻塞 + 2 用户面必修**：阻塞 = 文档大量 stale codec.lua/error.lua 生产路径表述（用户面误导）；必修 = ListenEditor `'lua'` 死分支、RoleLinkedForm stale「后续版本提供」提示。 |
| **C. codec 引擎 / 适配器 / 网络热路径** | `adapter/codec_resolver.go`、`adapter/schema_adapter.go`、`codec/`、`network/gnet.go` 帧解析 | 无阻塞。CodecResolver 按 `<proto>:<service>` 解析 + SchemaAdapter 包装无状态引擎、同 codec 多连接复用，热路径零 Lua 调用、零额外开销。 |

**当前事实**（文档对齐基准）：协议编解码全 Go——`adapter/codec_resolver.go`（CodecResolver 按 `"<proto>:<service>"` 解析）+ `adapter/schema_adapter.go`（SchemaAdapter 包装 `codec/` 引擎）+ `conf/adapter/<proto>_<service>_codec.json`（每连接一份）+ 共享 `conf/adapter/errors.json`。`conf/adapter/codec.lua`/`error.lua` **仅保留为 T1 一致性测试 oracle**（非生产路径）。前端「协议配置」面板按连接编辑多份 codec.json。

---

## 2. 本次 fix 清单（merge-blocking / 用户面必修）

### Fix 1 — `CLAUDE.md`（4 处 stale → 全清）
- **L23**（`-adapter` flag 帮助）：「含 codec.lua 与可选 error.lua」→「含各 `*_codec.json` 与可选 `errors.json`」。
- **L39**（单机模式启动序列）：「Lua 协议适配器」→「声明式 codec 配置（`*_codec.json` + `errors.json`）」。
- **L57**（adapter 包描述）：「编解码通过 Lua 池调用 `codec.lua`」→「`CodecResolver` 按 `"<proto>:<service>"` 解析、`SchemaAdapter` 包装 `codec/` Go 引擎驱动，配置来自 `conf/adapter/<proto>_<service>_codec.json`（每连接一份）」。
- **L83**（配置文件段）：「`conf/adapter/codec.lua` 协议适配器脚本（7 个必需 Lua 函数）」→「`conf/adapter/<proto>_<service>_codec.json` 每连接一份的声明式 codec 配置；共享 `errors.json` 提供错误码描述。`codec.lua`/`error.lua` 仅保留为 T1 一致性测试的 oracle，非生产路径。」
- **L128**（UDP 加密关键约定）：「由 `codec.lua` 的 `encrypt.udpOffset` 配置，默认 11」→「由 `<proto>_<service>_codec.json` 的 `encrypt.offset.{encode,decode}` 单向配置（如 `udp:battle` 发送偏移 11、接收偏移 0）」。

### Fix 2 — `README.md`（5+ 处 stale → 全清）
- **L18**（目录树）：「协议适配器接口 + Lua 桥接」→「协议适配器接口 + 声明式 codec 引擎（CodecResolver / SchemaAdapter + codec/ Go 编解码、帧分割、错误码映射）」。
- **L35-36**（目录树 adapter 段）：`codec.lua` 单文件 → `<proto>_<service>_codec.json` + `errors.json` 两项。
- **L400-432**（第二部分 Adapter 接口 + 「Lua 脚本要求」整节）：
  - Adapter 接口表去除「零 Lua 调用 / Lua 状态池」字眼，补一段说明实现已全 Go 化（CodecResolver + SchemaAdapter）。
  - 「## Lua 脚本要求（7 个必需函数）」整节删除，改为「## 声明式 codec 配置（`<proto>_<service>_codec.json`）」描述 header/frame/routeKeyTemplate/pipeline/encrypt.offset/errors.json，并在 blockquote 中标 `codec.lua`/`error.lua` 为旧版 oracle。
- **L411**（DescribeError 行）：「需 `error.lua`」→「读取共享 `errors.json`」。
- **L608-694**（standalone / agent-config 示例）：
  - standalone 删除 `adapter: { script: "conf/adapter/codec.lua", poolSize: 4 }` 旧块，补 blockquote 说明 codec 走 CodecResolver。
  - agent 删除 `adapterScript: "conf/adapter/codec.lua"` 字段，补说明 codec 随任务资源下发、不再读 adapterScript。
- **L763-765**（资源类型表）：`codec.lua` / `error.lua` 两行 → 「声明式 codec 配置 `<proto>_<service>_codec.json`」+「错误码描述 `errors.json`」。
- **L765**（Adapter tab 描述）：「7 函数校验 + 接口规范说明」→「按连接编辑多份 codec.json + errors.json；结构化视图（帧布局/管线/路由键模板编辑器）+ 源码 Monaco 切换 + 预览 + schema 校验」。
- **L1039**（错误码映射注）：「可通过 `error.lua` 映射」→「可通过共享 `errors.json` 映射」。

### Fix 3 — `cmd/web/src/components/FlowEditor/listens/ListenEditor.tsx`（删死分支）
- **L166**：`activeKey={selectedKind === 'lua' ? 'silent' : selectedKind}` → `activeKey={selectedKind}`。
- 理由：B4-A 已禁 script 入口、T2-A2.2 禁 `ListenDef.script`，`'lua'` 形态不可达（Tabs 只暴露 silent/declarative）。`switchKind` 体与 `stash`/`ListenKind` 类型保留 `'lua'` 用于旧数据 round-trip，不在本次 scope（严禁动测试/其它逻辑）。

### Fix 4 — `cmd/web/src/components/modules/codecEditor/RoleLinkedForm.tsx`（改 stale 提示）
- **L41**（route role 提示）：「模板编辑器在后续版本提供」→「模板编辑见下方『路由键模板』卡片」。
- **L56**（checksumOut 提示）：「pipeline 编辑器在后续版本提供」→「见下方『管线』卡片」。
- **L5-7**（docblock 头）：同步更新两条描述（B2-B 已交付 RouteKeyEditor / PipelineEditor，卡片在同面板 FrameLayoutEditor 之下并列：PipelineEditor 卡片标题「管线」、RouteKeyEditor 卡片标题「路由键模板」）。

### Fix 5 — `docs/visual-flow-editor.md`（branch-modified，2 处 stale）
- **L142**（组件树注释）：「codec.lua 编辑器」→「声明式 codec.json 编辑器（每连接一份 + 共享 errors.json）」。
- **L866**（IDB 资源表）：`codec.lua / error.lua` →「各 `*_codec.json` + 共享 `errors.json`」。

> 其余 `docs/`（adapter-layer / error-code-system / flow-node-system / monitoring-system）已在 branch 内重写或正确标注「测试 oracle / 历史迁移参考 / 旧实现真值」，命中项均属允许的历史背景注释。`agent-implementation` / `admin-implementation` / `resource-sync` / `frontend-api` / `distributed-architecture` / `superpowers/specs` 的 stale 表述非本 branch 引入（不在 master..HEAD diff），whole-branch review scope 外。

---

## 3. grep 用户面 stale 清零证据

### 顶层文档（CLAUDE.md / README.md / cmd/web/README.md）
```
$ grep -rn "codec\.lua\|error\.lua\|adapterScript" CLAUDE.md README.md cmd/web/README.md
CLAUDE.md:83:- `conf/adapter/<proto>_<service>_codec.json` — ... `codec.lua`/`error.lua` 仅保留为 T1 一致性测试的 oracle，非生产路径。
README.md:427:> 旧版 `codec.lua`（7 个必需函数）/ `error.lua` 仅保留为 T1 一致性测试的 oracle，不再参与生产编解码路径。
README.md:653:> codec 配置：... `codec.lua`/`error.lua` 仅保留为 T1 一致性测试 oracle，不再参与生产路径。
README.md:690:Agent 模式 ... Agent 侧经 `CodecResolver` 解析，不再读取 `adapterScript`。
```
**4 处命中均为明确标注「oracle / 旧版 / 不再读取」的历史背景注释，无生产路径误导。用户面 stale = 0。**

### 前端 cmd/web（.ts/.tsx）
```
$ grep -rn "codec\.lua\|error\.lua\|adapterScript\|协议适配器" cmd/web/src/**/*.tsx cmd/web/src/**/*.ts
cmd/web/src/services/__tests__/codecStorage.test.ts:5: * 新 codec 文件 key 形如 `<proto>_<service>_codec.json`，与旧 `codec.lua`/`error.lua` 不冲突。
cmd/web/src/services/resourcesStore.ts:189:// T3 声明式 codec 重构：把前端适配器从单一 codec.lua 升级为按连接的多份 ...
```
**2 处命中均为测试/重构迁移注释（描述「从 codec.lua 升级」「与旧 codec.lua 不冲突」），属允许的历史背景。用户面 stale = 0。**

### branch-modified docs（adapter-layer / error-code-system / flow-node-system / monitoring-system / visual-flow-editor）
- visual-flow-editor.md：2 处已修复（见 Fix 5）。
- adapter-layer.md：每处 codec.lua/error.lua/LuaAdapter 均标「测试 oracle / 历史迁移参考 / 旧实现真值 / 不再参与生产编解码」。
- flow-node-system.md:850：`// 协议适配器` 为 `adapter.Adapter` 字段类型注释（适配器接口本身），非 codec.lua 生产表述。
- error-code-system.md / monitoring-system.md：无命中。

---

## 4. 验证结果（全绿）

| 验证项 | 命令 | 结果 |
|--------|------|------|
| Go 编译 | `go build ./...` | **EXIT=0** |
| 前端类型检查 | `cd cmd/web && npx tsc -b` | **EXIT=0** |
| 前端单元测试 | `cd cmd/web && npm run test` | **287 passed (22 files), EXIT=0** — 与基线一致，无回归 |
| 用户面 stale grep | 见 §3 | **0 生产路径误导**（仅保留明确标注的 oracle/迁移注释） |

---

## 5. 改动文件清单

| 文件 | 类型 | 改动 |
|------|------|------|
| `CLAUDE.md` | 文档 | 5 处 stale codec.lua/error.lua → 声明式 codec.json 模型 |
| `README.md` | 文档 | 5+ 处 stale（目录树/Adapter 接口/Lua 脚本要求节/config 示例/资源表/错误映射注/Adapter tab 描述）→ 全清 |
| `cmd/web/src/components/FlowEditor/listens/ListenEditor.tsx` | 前端 | 删 Tabs `activeKey` 三元里的 `'lua'` 死分支（1 行） |
| `cmd/web/src/components/modules/codecEditor/RoleLinkedForm.tsx` | 前端 | 改 2 条 stale「后续版本提供」提示 + 同步 docblock（3 处） |
| `docs/visual-flow-editor.md` | 文档 | 2 处 stale（组件树注释 + IDB 资源表）→ 声明式 codec.json |

**未触碰**：后端 Go / services / codecEditor 逻辑 / 其它编辑器 / 测试逻辑 / docs 中 branch-untouched 的预存 stale（whole-branch review scope 外）。

**未 git commit**（按约束）。
