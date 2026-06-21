# T3 Batch-1 前置任务 — Admin 适配器基线索引端点

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 依赖：T4.3 已把 adapter 基线改为按文件名透传（`GET /sbot/baseline/adapter/{name}`），但**没有索引端点**。
> 本任务：补齐多 codec 基线同步契约的最后一块（前端 T3 §3.1/§3.5 需要枚举服务器上有哪些 `*_codec.json`/`errors.json`）。

## 1. 任务定位

T3（前端 codec 编辑器）Batch-1 的目标之一是「多文件 codec.json 端到端可编辑/校验/下发」。前端
`syncResourcesFromBaseline`（`resourcesStore.ts`）做基线三方同步时，对 proto/scripts 用
`GET /sbot/baseline/proto/index.json` 与 `/scripts/index.json` 枚举服务器文件清单。adapter 现在
只有按文件名取单文件的 `GET /sbot/baseline/adapter/{name}`（T4.3），**没有索引**，前端无法枚举
要同步哪些 codec 文件 → 必须补一个 `adapter/index.json` 端点，与 proto/scripts 完全对称。

本任务是**纯后端、admin 包内**的小改动，镜像现有 proto 索引实现。

## 2. 现状（已读码，照搬即可）

`admin/handlers.go`：

```go
// 路由注册（约 92-98 行）：
mux.HandleFunc("GET /sbot/baseline/proto/index.json", s.handleBaselineProtoIndex)
mux.HandleFunc("GET /sbot/baseline/proto/{name}", s.handleBaselineProtoFile)
mux.HandleFunc("GET /sbot/baseline/scripts/index.json", s.handleBaselineScriptIndex)
mux.HandleFunc("GET /sbot/baseline/scripts/{name}", s.handleBaselineScriptFile)
// T4.3：adapter 基线改为按文件名透传（支持多 *_codec.json + errors.json）。
mux.HandleFunc("GET /sbot/baseline/adapter/{name}", s.handleBaselineCodecFile)

// 现有 proto 索引 handler（1570-1577）—— 逐字镜像：
func (s *AdminServer) handleBaselineProtoIndex(w http.ResponseWriter, r *http.Request) {
	files, err := listDirFiles("conf/proto", ".proto")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

// adapter 单文件 handler（1596-1600）：
func (s *AdminServer) handleBaselineCodecFile(w http.ResponseWriter, r *http.Request) {
	serveBaselineFile(w, r, "conf/adapter", "name")
}

// listDirFiles（1615-1628）：返回 dir 中后缀 == ext 的文件名（不含路径，不排序）。
```

**路由匹配注意**：Go 1.22 `net/http` ServeMux 中，字面路径 `GET /sbot/baseline/adapter/index.json`
比通配 `GET /sbot/baseline/adapter/{name}` 更具体，会正确优先匹配（与 proto 的 `index.json` vs
`{name}` 完全同款，已验证可行）。

## 3. 实现规格（逐项，照搬 proto 索引）

1. **新增 handler** `handleBaselineCodecIndex`：与 `handleBaselineProtoIndex` 逐字同构，仅目录与后缀不同：
   ```go
   // handleBaselineCodecIndex 列出 adapter 基线目录下的 codec/errors 文件名（T3 前端基线同步枚举用）。
   // 目录契约：conf/adapter 下只有 *_codec.json 与 errors.json（写入侧 buildConfigFiles 已拒绝其它）。
   func (s *AdminServer) handleBaselineCodecIndex(w http.ResponseWriter, r *http.Request) {
   	files, err := listDirFiles("conf/adapter", ".json")
   	if err != nil {
   		writeError(w, err)
   		return
   	}
   	writeJSON(w, http.StatusOK, files)
   }
   ```
   放在 `handleBaselineCodecFile` 紧邻处（1596-1600 附近）。

2. **注册路由**：在 `mux.HandleFunc("GET /sbot/baseline/adapter/{name}", ...)` **之前**加一行
   （位置与 proto `index.json` 在 `{name}` 之前的排布一致）：
   ```go
   mux.HandleFunc("GET /sbot/baseline/adapter/index.json", s.handleBaselineCodecIndex)
   mux.HandleFunc("GET /sbot/baseline/adapter/{name}", s.handleBaselineCodecFile)
   ```

3. **后缀选择 `.json`**（不要更窄/更宽）：conf/adapter 现有 4 个文件全 `.json`
   （`tcp_logic_codec.json` / `tcp_battle_codec.json` / `udp_battle_codec.json` / `errors.json`），
   旧 `codec.lua`/`error.lua` 是 `.lua` 会被自然排除。与 proto 用 `.proto`、scripts 用 `.lua` 的
   「目录内单一类型」契约一致。**不要**在 handler 里再按 `_codec.json`/`errors.json` 二次过滤——
   交给前端按文件名分类（前端知道 `errors.json` 是错误表，其余是 codec），handler 只负责如实列目录。

## 4. 测试（TDD）

在 `admin/codec_distribution_test.go` 风格或新建 `admin/baseline_codec_index_test.go` 中加测试：
`TestBaselineCodecIndex_ListsJsonFiles`——用 T1.6 产物（`conf/adapter/*.json`）准备临时 conf/adapter
目录，`GET /sbot/baseline/adapter/index.json`，断言：
- 状态 200、`Content-Type: application/json`；
- 返回数组**恰好含** `tcp_logic_codec.json` / `tcp_battle_codec.json` / `udp_battle_codec.json` /
  `errors.json` 四个（集合相等，顺序不强制——前端排序）；
- **不含** `codec.lua` / `error.lua`。

复用 `codec_distribution_test.go` 里已有的临时目录 + `serveBaselineFile` 测试搭建方式（看其
`TestCodecDist_BaselineWriteAndReadRoundTrip` 怎么构造目录与发请求）。若该测试不便复用，按
proto/script 索引测试的搭建方式（如有）镜像。

## 5. 验证

- `go build ./...` 退出 0；`go vet ./admin` 退出 0。
- `go test ./admin -run 'BaselineCodecIndex|CodecDist' -count=1` 全过。
- `go test ./admin -count=1` 整包绿（不破坏既有 51 端点测试）。

## 6. 全局约束（bind）

- **禁止兼容性兜底**：不写任何旧→新 fallback。本任务纯增量端点，不碰旧逻辑。
- **日志/错误中文**；godoc 中文。
- **admin 包内**：只动 `admin/handlers.go`（+1 handler +1 路由）与测试文件。不动 runtime、不动
  codec 包、不动前端。
- **gofmt（Windows 环境注）**：本 worktree `core.autocrlf=true`，工作树 `.go` 检出为 CRLF，
  `gofmt -l` 会把全部文件标脏——这是环境现象，**不要**对单文件 `gofmt -w` 去「修」CRLF。
  校验内容 canonical：`sed 's/\r$//' admin/handlers.go > /tmp/x.go && gofmt -l /tmp/x.go`（空即 canonical）。
- **不 commit**：implementer 不要 git commit，完成后报告改动文件与测试结果。

## 7. 报告

写到 `plans/declarative-codec/reports/t3-b1-adapter-index-report.md`：实现要点、测试用例与命令输出、
`go build`/`vet`/`test` 结果、自审发现。返回时只给：状态（DONE/DONE_WITH_CONCERNS/BLOCKED）、
改动文件清单、一行测试摘要、concerns（若有）。

## 8. 不做（scope 外）

- 不动前端任何文件（前端接线在 §3.1/§3.5）。
- 不改 `listDirFiles` / `serveBaselineFile` / `writeBaselineFiles` / `buildConfigFiles`。
- 不加排序、不加分页、不加鉴权（沿用现有基线端点的鉴权态势——当前基线端点无额外鉴权）。
