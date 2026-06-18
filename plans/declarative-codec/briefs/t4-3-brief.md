# T4.3 Brief — Admin 多 codec 分发（上传/下载/baseline/configFiles/TaskConfig）

> 你是 implementer。先读本 brief。参考 `plans/declarative-codec/04-track-config-distribution.md` §2/§3.4（设计要点 + Admin 清单，含 handlers.go 行号参考）、`plans/declarative-codec/00-master.md` §2/决策 #8（每连接一份）、T1.6 产物 `conf/adapter/{tcp_logic,tcp_battle,udp_battle}_codec.json`+`errors.json`。
> 工作目录：worktree 根。**不要 git commit**。

## 目标

把 admin 的 codec 资源分发从「单 `codec.lua` + `error.lua`」改成「**每连接多份** `*_codec.json` + 共享 `errors.json`」。涵盖：multipart 上传、configFiles 清单、baseline 读写、下载、TaskConfig 类型。

## 关键范围决策（务必遵守）

- **仅 admin/ 改动**：不动 agent/、cmd/、robot/、codec/、adapter/。agent 下载/加载 + runtime 接线归 **T2**。
- **TaskConfig 字段策略（保持编译绿 + 全链路由 T2 收口）**：
  - **新增** `Codecs map[string][]byte`（key = 文件名如 `tcp_logic_codec.json`，value = 文件字节）+ `ErrorMap []byte`（共享 errors.json）。
  - **暂保留**旧 `AdapterScript`/`ErrorMapScript []byte` 字段**不删**（agent/task_runner 仍读它们，删了会破坏 agent 编译）。本任务 admin 一律读写**新字段**（Codecs/ErrorMap），不再写旧字段。
  - 这是有意的过渡态：新字段在 admin 全链路一致；旧字段待 T2 切 agent 后删除。**不要**写「旧→新自动迁移」代码（违反禁止兜底）——admin 直接用新字段。
- `go build ./...` 必须保持绿（旧字段保留即满足；agent 不动）。

## 改动点（先读 admin/ 确认当前实现与行号）

1. **`admin/types.go`**：`TaskConfig` 加 `Codecs map[string][]byte json:"codecs,omitempty"` + `ErrorMap []byte json:"errorMap,omitempty"`（旧 `AdapterScript`/`ErrorMapScript` 保留）。
2. **multipart 上传**（`admin/handlers.go` 约 476-494）：接收 `adapter/` 下**多个** `*_codec.json`（按上传文件名收集到 `Codecs` map）+ `adapter/errors.json`（→ `ErrorMap`）。不再接收 `codec.lua`/`error.lua`。
3. **configFiles 清单**（约 810-815）：列出 `adapter/<每个 *_codec.json>` + `adapter/errors.json`（不再列 `codec.lua`/`error.lua`）。
4. **baseline 落盘**（`writeBaselineFiles` 约 1505-1515）：遍历 `Codecs` map 落盘各 `*_codec.json` + `errors.json`。
5. **baseline HTTP 读**（约 96-97/1559-1564）：`/sbot/baseline/adapter/*_codec.json` 与 `/errors.json`（按 `{path...}` 透传，多文件均可读）。
6. **下载端点**（约 622-697）：路径透传 `{path...}`，通常无需改；核实多文件均可下载。
7. **任务创建/保存**：把上传的 codec 字节填进 `TaskConfig.Codecs`/`ErrorMap`；从 `Codecs`/`ErrorMap` 分发。

> 行号是计划文档时的参考，**以当前代码为准**；先 grep `AdapterScript`/`ErrorMapScript`/`codec.lua`/`error.lua` 在 admin/ 的全部引用，逐处改到新字段。

## 关键约束

- **多文件模型**：codec 是 `Codecs` map（文件名→字节），不是单脚本。errors 是单份 `ErrorMap`。
- **无兜底**：admin 不写「缺 codec 就用默认」逻辑；缺映射/缺文件按现有 admin 错误风格报错。
- **仅 admin/**；保留旧 TaskConfig 字段以保编译；不删旧字段（T2 删）；不写旧→新迁移。
- admin 任务保存**不执行** codec 加载（仍是存/发文件）；深度校验由 codec preview 端点（T4.2）+ agent/standalone runner 负责。
- **不要 git commit。**

## 工作方式（TDD）

1. RED：`admin/` 测试（按现有 admin 测试模式）：
   - multipart 上传多份 `*_codec.json`+`errors.json` → TaskConfig.Codecs 含各文件、ErrorMap 非空。
   - configFiles 清单含各 `adapter/*_codec.json`+`errors.json`，不含 `codec.lua`。
   - baseline 落盘后磁盘有各 `*_codec.json`+`errors.json`；baseline HTTP 可读各文件。
   - 下载端点能取到多文件。
   - 用 T1.6 的 conf/adapter/*.json 作测试输入字节。
2. GREEN：实现各改动点。
3. `go build ./...`、`go vet ./admin/...`、`go test ./admin/... -count=1` 全绿、输出干净。**确认 `go build ./...` 全仓绿**（旧字段保留 → agent 不受影响）。
4. **不要 git commit。**

## 验收（self-review）

- TaskConfig 新增 Codecs/ErrorMap；旧字段保留（agent 编译不破）。
- multipart/configFiles/baseline/upload/download 全走多文件；admin 内无 `codec.lua`/`error.lua` 残留引用。
- 无旧→新迁移代码；无默认 codec 兜底。
- 仅 admin/ 改动；`go build ./...` 全绿。

## 报告

写完整报告到 `plans/declarative-codec/reports/t4-3-report.md`：改动点逐处（含实际行号）、TaskConfig 字段策略、各测试、`go build ./...` 全绿证据、self-review、concerns。
返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
