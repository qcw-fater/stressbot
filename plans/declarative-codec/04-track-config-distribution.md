# Track 4 — 配置加载、分发与 conf 迁移

> 依赖：Track 1（`codec.LoadSchema`/`LoadErrorMap` 签名、`<proto>_<service>_codec.json`/`errors.json` 文件格式）
> 产出：standalone / Admin / Agent 全链路把单一 `codec.lua`+`error.lua` 替换为**每连接一份** `<proto>_<service>_codec.json` + 共享 `errors.json`；提供人工迁移产物；一刀切删旧文件
> 约束：无兼容垫片（`MEMORY.md`）；配置与代码同 commit 更新

---

## 1. 现状参考（已读码）

| 链路 | 文件 | 行 | 现状 |
|---|---|---|---|
| standalone CLI flag | `cmd/agent/main.go` | 88（`-adapter`）、408-416（`resolveStandalonePaths`） | adapter 目录，默认 `<conf>/adapter` |
| standalone 加载 | `cmd/agent/main.go` | 196-206（`NewLuaAdapter(poolSize, codec.lua, error.lua?)`）、296（`NewDialer(adp)`）、282-302（Manager）、368（`adp.Close()`） | 加载 codec.lua + 可选 error.lua |
| agent 本地兜底 | `agent/config.go` | 112（`AdapterScript:"conf/adapter/codec.lua"`） | 下载无 codec 时回退 |
| agent 任务执行 | `agent/task_runner.go` | 106-123（下载 configFiles）、125-141（加载 adapter）、186（Dialer）、231-237（Manager） | 优先 `{confDir}/adapter/codec.lua` |
| Admin 上传 | `admin/handlers.go` | 476-494（multipart `adapter/codec.lua`+`adapter/error.lua`） | 存 `TaskConfig.AdapterScript/ErrorMapScript`（`[]byte`） |
| Admin 任务 config 清单 | `admin/handlers.go` | 810-815 | configFiles 含 `adapter/codec.lua`[+`error.lua`] |
| Admin agent 下载端点 | `admin/handlers.go` | 622-697（`GET /sbot/tasks/{id}/config/{path...}`） | 逐文件 HTTP |
| Admin baseline 落盘 | `admin/handlers.go` | 1505-1515（`writeBaselineFiles`） | 写 `conf/adapter/*` |
| Admin baseline HTTP | `admin/handlers.go` | 96-97、1559-1564 | `/sbot/baseline/adapter/*` |
| Admin 类型 | `admin/types.go` | 97-100（`AdapterScript`/`ErrorMapScript []byte`） | — |
| 参考产物（迁移源） | `conf/adapter/codec.lua`、`conf/adapter/error.lua` | — | 待转换并删除 |
| 路径解析单测 | `cmd/agent/main_test.go` | 26-68 | `resolveStandalonePaths` |

---

## 2. 设计要点

- **文件名（每连接一份，决策 #8）**：`codec.lua` → **每条连接一份** `<proto>_<service>_codec.json`（如 `tcp_logic_codec.json`/`tcp_battle_codec.json`/`udp_battle_codec.json`，必需）；`error.lua` → `errors.json`（可选，可被多连接共享，错误码描述与 transport 无关）。目录仍 `conf/adapter/`，CLI flag `-adapter` 含义不变（目录）。
- **加载**：`NewLuaAdapter(...)` 调用点替换为「**枚举连接 → 各读自己的 `<proto>_<service>_codec.json`**」：对每条声明连接 `codec.LoadSchema(<dir>/<proto>_<service>_codec.json)` + 共享 `codec.LoadErrorMap(<dir>/errors.json)`（可选）→ `adapter.NewSchemaAdapter(schema, errorMap)`。Track 1 提供这些函数。
- **连接 → codec 显式映射**：默认每连接一份 codec 文件；config 显式声明「`server` 串 → codec 文件」映射。多连接可显式指向同一文件去重（loader 对同一文件路径 dedup、编译一次、共享同一无状态实例）。loader 从 config/flow 枚举声明连接（`server` 串），为每条连接显式填入 `CodecResolver` map；flow 引用未声明连接或 resolver 缺映射时 fail loud。
- **分发**：Admin 仍逐文件下发；字段名/路径从 `adapter/codec.lua`（单文件）改为 `adapter/<proto>_<service>_codec.json`（多文件）+ `adapter/errors.json`。`TaskConfig` 的 codec 字段从「单脚本 `[]byte`」改为「文件名→`[]byte` 的 map」以容纳多文件。
- **任务包形态**：继续按目录文件分发 `adapter/*_codec.json`、`adapter/errors.json`，不引入嵌套 JSON 字段包装，减少 Admin/Agent 任务包抽象改动。
- **一刀切**：同 commit 删 `codec.lua`/`error.lua`、提交各连接 `*_codec.json`/`errors.json`，并更新全链路文件名引用。已存历史任务/baseline 失效（已确认接受）。

---

## 3. 任务清单

### 3.0 加载顺序与 连接→codec 映射契约

- [ ] 统一 standalone / agent task runner 的加载顺序：读取 config → 枚举声明连接（`server` 串）及其 codec 文件映射 → 逐连接读取 `adapter/<proto>_<service>_codec.json`（+共享 `adapter/errors.json`）→ `schema.Validate` → `adapter.NewSchemaAdapter` → 为每条连接显式填入 `CodecResolver` → 加载 proto → 加载 flow → 校验 flow 中 `server` 引用均可 Resolve。
- [ ] 连接列表只从运行配置中声明的「`server` 串 → codec 文件」映射枚举，不从 flow 动态推断；flow 引用未声明连接是配置错误。
- [ ] 默认每连接一份 codec 文件；多连接显式指向同一文件时 loader 按文件路径 dedup（编译一次、共享实例），但每条连接仍须在 resolver map 中逐项显式登记。
- [ ] `CodecResolver` 构建失败要在启动/任务开始阶段 fail loud，不推迟到首个 action 发送时才暴露。
- [ ] Admin 仍不执行任务 codec 加载；但保存/下发前可做 JSON 语法级校验。深度 schema 校验由 standalone/agent runner 与 codec preview 端点负责。

> **config 声明格式（建议）**：在运行 config 的网络/服务段为每条连接显式声明其 codec 文件，例如
> ```json
> "codecs": {
>   "tcp:logic":  "tcp_logic_codec.json",
>   "tcp:battle": "tcp_battle_codec.json",
>   "udp:battle": "udp_battle_codec.json"
> }
> ```
> 缺省可约定「未显式声明时按 `<proto>_<service>_codec.json` 推断文件名」，但**文件缺失即 fail loud**，不回退到其它连接的 codec。具体落点（config 段名/与现有 service 声明的关系）由 T4 实现时定，但「显式映射 + 缺失报错 + 可共享」三条不变。

### 3.1 迁移产物（与 Track 1 协作）

- [ ] 手工把 `conf/adapter/codec.lua` 按连接拆/转为各连接 codec 文件：`tcp_logic_codec.json`、`tcp_battle_codec.json`、`udp_battle_codec.json`（总纲 §3.1 为单 transport 目标内容；`udp:battle` 的 `offset={encode:11,decode:0}`，TCP 两份 `offset={encode:0,decode:0}`），并用 Track 1 对拍测试逐连接验证字节级一致。
- [ ] 手工把 `conf/adapter/error.lua` 的 `errors` 表转为 `conf/adapter/errors.json`（`{"code":"中文"}`，711 行映射逐条搬运，可写一次性脚本辅助但产物入库；一份共享）。
- [ ] 删除 `conf/adapter/codec.lua`、`conf/adapter/error.lua`（同切换 commit）。

### 3.2 standalone（cmd/agent）

- [ ] `cmd/agent/main.go:196-206`：`NewLuaAdapter` → 枚举连接、逐份 `codec.LoadSchema(<dir>/<proto>_<service>_codec.json)` + 可选共享 `LoadErrorMap(<dir>/errors.json)` → 各 `adapter.NewSchemaAdapter`。
- [ ] 枚举 standalone config 中声明的连接（`server` 串 → codec 文件映射），构建 `CodecResolver`；同一文件 dedup 为同一 schema adapter 实例。
- [ ] `ManagerConfig.Adapter` 接线改为 `ManagerConfig.CodecResolver`；`Dialer` 不再持 server-level fallback adapter。
- [ ] `:368` `adp.Close()`：Go adapter 的 `Close()` 为 no-op，保留无害（或删）；dedup 后多连接共享的实例只 close 一次或让 Close 幂等。
- [ ] `:88`/`:408-416` `-adapter`/`resolveStandalonePaths`：含义不变（目录）；如有对 `codec.lua` 文件名的硬引用则改为按连接枚举 `*_codec.json`。
- [ ] `cmd/agent/main_test.go:26-68`：若断言文件名则更新，并补一例 `-adapter` 目录下按连接解析 `<proto>_<service>_codec.json`/`errors.json`。

### 3.3 agent 模式（agent/）

- [ ] `agent/config.go:112`：本地兜底路径从单一 `conf/adapter/codec.lua` 改为「`conf/adapter/` 目录 + 按连接枚举 `*_codec.json`」；如字段正名则同步为目录/映射形态。
- [ ] `agent/task_runner.go:106-123`：下载的 configFiles 路径随 Admin 改为 `adapter/<proto>_<service>_codec.json`（多文件）、`adapter/errors.json`。
- [ ] `agent/task_runner.go:125-141`：加载逻辑改逐连接 `<proto>_<service>_codec.json`(+共享 `errors.json`)→`NewSchemaAdapter`，并按任务 config 中声明连接构建 `CodecResolver`。
- [ ] Agent 不从 flow 反推连接；任务 config 缺连接 codec 或 flow 引用未声明连接，任务启动阶段失败并上报明确错误。

### 3.4 Admin（admin/）

- [ ] `admin/handlers.go:476-494`：multipart 接收 `adapter/` 下**多个** `*_codec.json`（按上传文件名透传）+ `adapter/error.lua`→`adapter/errors.json`；codec 改为按文件名收集到 map，不再假定单一 `codec.lua`。
- [ ] `:810-815`：任务 configFiles 清单路径同步改，枚举 `adapter/*_codec.json` 多文件。
- [ ] `:1505-1515` `writeBaselineFiles`：落盘改为遍历多份 `*_codec.json`。
- [ ] `:96-97`/`:1559-1564` baseline HTTP 路由：`/sbot/baseline/adapter/<proto>_<service>_codec.json`、`errors.json`（按 `{path...}` 透传即可）。
- [ ] `:622-697` 下载端点：路径透传，通常无需改（按 `{path...}`），核实多文件均可下载。
- [ ] `admin/types.go:97-100`：`AdapterScript []byte`（单脚本）→ `Codecs map[string][]byte`（文件名→内容）；`ErrorMapScript`→`ErrorMap`（仍单份）。
- [ ] Admin **不执行任务 codec**（现状如此），任务创建/保存只存/发文件；可做 JSON 语法校验，但不要求连接 config 构建 resolver。
- [ ] Admin 任务包继续按文件路径分发，不改成嵌套 adapter JSON 字段；agent 下载后落到 `conf/adapter/*_codec.json`、`conf/adapter/errors.json` 对应目录。
- [x]（**已确认，跨 T3**）在 Admin 新增两个只读端点：`POST /sbot/codec/preview`（用 Track 1 `NewSchemaAdapter` 跑一次 encode/decode，无副作用）与 `GET /sbot/codec/algorithms`（返回 Track 1 导出的算法元数据）。两者纯计算、不入库、不下发。

### 3.5 文档

- [ ] `CLAUDE.md` adapter 段、`docs/adapter-layer.md`、`plans/refactor-adapter-layer.md`：更新为声明式 codec（或标注历史）。
- [ ] `conf/config.json` 注释/README：如有 adapter 说明，更新为 `codec.json`。

---

## 4. 切换顺序（与 T2/T3 合流）

1. T1 落地（新引擎 + 迁移产物各连接 `*_codec.json`/`errors.json` 入库，旧 `.lua` 暂留）。
2. T4 先落文件名/加载函数/任务包路径的双端准备，但不删旧文件；standalone/agent 可通过新 loader 枚举连接构建 `CodecResolver`。
3. T2 后端切到 `CodecResolver` + Go adapter（loader 用本轨道的 `LoadSchema`/`LoadErrorMap`，resolver 按 `server` 串显式映射连接）。
4. T3 前端切到 `codec.json` / `errors.json` 编辑、上传、任务创建与 flow 校验。
5. **合流切换 commit**：Admin/agent/standalone 文件名全切 + 删 `codec.lua`/`error.lua` + 删 `NewLuaAdapter`/`adapter/lua_adapter.go`/`adapter/robot_adapter.go`/`lua_crypto.go`/`lua_zlib.go` 中已无人用的 Lua 绑定（Go 算法逻辑已被 Track 1 迁走）。该 commit 单独、可回滚。

---

## 5. 验收

- [ ] standalone：`go run ./cmd/agent -config conf/config.json` 用 `codec.json` 正常启动压测，日志无 adapter 相关错误。
- [ ] standalone：config 中每条声明连接（`server` 串）都能在 `CodecResolver` 中 Resolve；flow 引用未声明连接时启动失败且错误清晰。
- [ ] agent 模式：Admin 下发各连接 `*_codec.json`(+`errors.json`)，agent 下载并以 Go adapter + `CodecResolver` 启动任务成功。
- [ ] 前端创建任务 → Admin 存储/落盘/baseline → agent 下载，全链路多 codec 文件名一致、无 `.lua` codec 残留。
- [ ] 任务包仍按 `adapter/*_codec.json`、`adapter/errors.json` 文件路径分发，Admin/Agent 对路径大小写与目录一致。
- [ ] `conf/adapter/` 下只剩各连接 `*_codec.json`(+`errors.json`)；`codec.lua`/`error.lua` 已删。
- [ ] 全仓 `grep "codec.lua"`/`"error.lua"`/`NewLuaAdapter`：零残留（注释/历史 plan 除外）。

---

## 6. 风险

| 风险 | 缓解 |
|---|---|
| 多链路文件名漏改导致下发后找不到文件 | 以本轨道清单逐项核对；合流 commit 后跑 standalone + agent 双模式端到端 |
| 连接枚举不完整 / codec 文件漏配导致运行时才发现 codec 缺失 | loader 从 config 声明的「`server`→codec 文件」显式构建 resolver，逐文件存在性校验，并在加载 flow 后校验所有 `server` 引用均可 Resolve，启动阶段 fail loud |
| `errors.json` 逐条搬运出错 | 写一次性转换脚本从 `error.lua` 生成 `errors.json`，再人工抽查；产物入库 |
| 历史任务失效引起线上困惑 | 已确认接受；发布说明提示「需重新上传协议配置」 |
