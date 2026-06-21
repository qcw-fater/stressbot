# T4.3 报告 — Admin 多 codec 分发（上传/下载/baseline/configFiles/TaskConfig）

> 状态：DONE。`go build ./...` 全仓绿、`go vet ./admin/...` 干净、`go test ./admin/... -count=1` 13 项全过。

## 范围

把 admin 的 codec 资源分发从「单 `codec.lua` + `error.lua`」改为「每连接多份 `*_codec.json` + 共享 `errors.json`」。仅改 `admin/`，不动 `agent/`、`cmd/`、`robot/` 等。旧 `AdapterScript`/`ErrorMapScript` 字段保留以维持 agent 编译，T2 切 agent 后删除。

## TaskConfig 字段策略

`admin/types.go` `TaskConfig`（L89-108）：
- **新增** `Codecs map[string][]byte json:"codecs,omitempty"`（L105，key=文件名如 `tcp_logic_codec.json`）+ `ErrorMap []byte json:"errorMap,omitempty"`（L107）。
- **保留** 旧 `AdapterScript`/`ErrorMapScript []byte`（L99/L102，加注释说明 T4.3 后 admin 不再写入、待 T2 删除）。admin 内部不再读/写旧字段；agent/task_runner 仍读它们（未动），故 `go build ./...` 全绿。
- **无旧→新迁移代码**、**无默认 codec 兜底**。

## 改动点（逐处，含当前实际行号）

### `admin/types.go`
- L97-107：`TaskConfig` 加 `Codecs`/`ErrorMap`；旧字段加迁移注释保留。

### `admin/handlers.go`

1. **baseline 路由**（L96-97）：把 `GET /sbot/baseline/adapter/codec.lua` + `GET /sbot/baseline/adapter/error.lua` 两条合并为 `GET /sbot/baseline/adapter/{name}`（路径透传，多文件均可读）。

2. **multipart 上传**（L480-517）：删掉 `r.FormFile("adapter/codec.lua")` / `r.FormFile("adapter/error.lua")` 单文件接收；改为遍历 `r.MultipartForm.File`，收集所有 `adapter/` 前缀 field：
   - field 名去 `adapter/` 前缀得 basename；
   - basename == `errors.json` → `cfg.ErrorMap`；
   - basename 以 `_codec.json` 结尾 → `cfg.Codecs[basename]`；
   - basename 为 `codec.lua`/`error.lua`（旧字段）或非约定名 → 显式忽略并 warn（无兜底）。

3. **下载端点 `handleGetTaskConfig`**（L655-728）：
   - 删 `case "adapter/codec.lua"` / `case "adapter/error.lua"` 两个旧分支，合并为 `case "adapter/codec.lua", "adapter/error.lua": http.NotFound`（旧字段产物 404）。
   - `default` 分支前置 `adapter/` 前缀匹配：`errors.json` → `task.Config.ErrorMap`；其余 basename 命中 `task.Config.Codecs` 返回该 codec。proto/lua 兜底逻辑不变。

4. **configFiles 清单**（L828）：把原先 inline 拼接 flow/proto/scripts/adapter 的逻辑替换为 `configFiles := buildConfigFiles(&task.Config)`。

5. **`buildConfigFiles` 新增辅助函数**（L1534-1554）：flow/proto/scripts/各 `*_codec.json`/`errors.json`。adapter 下走 `Codecs` map + `ErrorMap`，**不列** `codec.lua`/`error.lua`。分发逻辑与测试共用同一份规则（单一事实源）。

6. **baseline 落盘 `writeBaselineFiles`**（L1518-1531）：删 `cfg.AdapterScript`/`cfg.ErrorMapScript` 落盘分支；改为遍历 `cfg.Codecs` 落盘各 `*_codec.json` + `cfg.ErrorMap` 落盘 `errors.json`。

7. **baseline HTTP 读 handler**：删 `handleBaselineAdapter`/`handleBaselineErrorMap` 两个固定文件 handler；新增 `handleBaselineCodecFile`（L1596-1599），复用 `serveBaselineFile(w, r, "conf/adapter", "name")`，按 `{name}` 透传多文件。

## 测试（RED → GREEN）

新增 `admin/codec_distribution_test.go`（4 个用例），用 T1.6 产物 `conf/adapter/{tcp_logic,tcp_battle,udp_battle}_codec.json` + `errors.json` 作输入字节：

- `TestCodecDist_UploadPopulatesMultiCodec`：multipart 上传 4 文件 → `TaskConfig.Codecs` 含 3 个 `*_codec.json`、`ErrorMap` 非空；且旧 `AdapterScript`/`ErrorMapScript` 为 nil（admin 只写新字段）。
- `TestCodecDist_ConfigFilesListsMultiCodec`：`buildConfigFiles` 清单含各 `adapter/*_codec.json` + `adapter/errors.json`，**不含** `adapter/codec.lua`/`adapter/error.lua`。
- `TestCodecDist_BaselineWriteAndReadRoundTrip`：`writeBaselineFiles` 落盘后磁盘有各 `*_codec.json`+`errors.json`，无 `codec.lua`/`error.lua`；baseline HTTP（`/sbot/baseline/adapter/{name}`）能读回各文件。
- `TestCodecDist_DownloadServesMultiFiles`：`handleGetTaskConfig` 能取到多个 `adapter/*_codec.json` 与 `adapter/errors.json`，内容为合法 JSON。

测试基础设施：`setupCodecDistServer` 构造最小 `&AdminServer{tasks: NewTaskStore(tmpdir/data)}`，chdir 到 `t.TempDir()`（隔离 `conf/*` 落盘），`ReplaceLogger(zap.NewNop())`（避免 handler 日志在未初始化 zap 上 panic），cleanup 还原 cwd+logger。`codecDistRepoRoot` 在 init 期解析仓库根绝对路径，确保 chdir 后仍能读 T1.6 输入文件。

## 验证

- `go build ./...` → 无输出（全仓绿，agent 不受影响）。
- `go vet ./admin/...` → 无输出。
- `go test ./admin/... -count=1` → `ok stressbot/admin`，13 项全过（含 T4.2 的 9 个 codec preview/algorithms 用例）。

## admin/ 内 `codec.lua`/`error.lua` 残留引用核查

剩余引用全部是：迁移注释、显式旧字段拒绝守卫（`handlers.go:489-490` 拒收旧上传、`handlers.go:675` 旧下载 404）、测试断言（确认旧文件不出现在 configFiles/baseline）。**无任何活跃旧逻辑**。

`AdapterScript`/`ErrorMapScript` 在 admin 内仅剩：`types.go` 字段定义（含迁移注释）+ 测试断言确认其为 nil。无 admin 逻辑读/写它们。

## Self-review（对照 brief 验收）

- [x] TaskConfig 新增 `Codecs`/`ErrorMap`；旧字段保留（agent 编译不破）。
- [x] multipart/configFiles/baseline/upload/download 全走多文件；admin 内无 `codec.lua`/`error.lua` 活跃引用。
- [x] 无旧→新迁移代码；无默认 codec 兜底。
- [x] 仅 `admin/` 改动；`go build ./...` 全绿。

## Concerns

- 旧 `AdapterScript`/`ErrorMapScript` 字段保留是计划内的过渡态，T2 切 agent 后删除。期间若 admin 收到老前端上传的 `adapter/codec.lua`，会被上传循环里的 `name == "codec.lua"` 守卫显式忽略（warn 日志），不写入新字段也不报错——这是「admin 不再接收旧字段」的预期行为，前端需 T3 同步切到多文件上传。
- `handleGetTaskConfig` 对 `adapter/codec.lua`/`adapter/error.lua` 返回 404：若有 agent（T2 前）仍按旧路径拉取，会拉不到——但旧字段本就不再被 admin 写入，agent 改造（T2）会同步切路径，无静默回退风险。
