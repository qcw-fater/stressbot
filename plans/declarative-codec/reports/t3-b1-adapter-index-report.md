# T3 Batch-1 前置任务报告 — Admin 适配器基线索引端点

> Worktree：`D:\Gitee\stressbot\.claude\worktrees\declarative-codec`（分支 `worktree-declarative-codec`）。
> 需求来源：`plans/declarative-codec/briefs/t3-b1-adapter-index-brief.md`。

## 1. 实现要点

镜像现有 `handleBaselineProtoIndex`，在 admin 包内纯增量补齐 adapter 基线索引端点
（前端 T3 §3.1/§3.5 多 codec 基线同步枚举用）。

### 改动 1：`admin/handlers.go` — 新增 handler

紧邻 `handleBaselineCodecFile` 新增 `handleBaselineCodecIndex`（godoc 中文）：

```go
// handleBaselineCodecIndex 列出 adapter 基线目录下的 codec/errors 文件名（T3 前端基线同步枚举用）。
// 目录契约：conf/adapter 下只有 *_codec.json 与 errors.json（写入侧 buildConfigFiles 已拒绝其它）。
// handler 只按 .json 后缀如实列目录，不二次过滤文件名（前端按 errors.json/其余分类）。
func (s *AdminServer) handleBaselineCodecIndex(w http.ResponseWriter, r *http.Request) {
	files, err := listDirFiles("conf/adapter", ".json")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}
```

### 改动 2：`admin/handlers.go` — 注册路由（1 行）

在 `handleBaselineCodecFile` 通配路由之前加 `index.json` 字面路由（与 proto `index.json`
vs `{name}` 完全同款，Go 1.22 ServeMux 字面优先匹配）：

```go
// T4.3：adapter 基线改为按文件名透传（支持多 *_codec.json + errors.json）。
mux.HandleFunc("GET /sbot/baseline/adapter/index.json", s.handleBaselineCodecIndex)
mux.HandleFunc("GET /sbot/baseline/adapter/{name}", s.handleBaselineCodecFile)
```

### 后缀选择 `.json`（不变更）
conf/adapter 现有 4 个文件全 `.json`（`tcp_logic_codec.json`/`tcp_battle_codec.json`/
`udp_battle_codec.json`/`errors.json`），旧 `codec.lua`/`error.lua` 被 `.json` 过滤自然排除。
未做 `_codec.json`/`errors.json` 二次过滤——handler 如实列目录，前端按文件名分类。

## 2. 测试用例

新增 `admin/baseline_codec_index_test.go`：`TestBaselineCodecIndex_ListsJsonFiles`。

- 复用 `codec_distribution_test.go` 的 `setupCodecDistServer`（临时目录 + nop logger + chdir）。
- 落盘 4 份目标 `.json`（用 T1.6 产物字节）+ 2 份干扰 `.lua`（`codec.lua`/`error.lua`）。
- `GET /sbot/baseline/adapter/index.json`：
  - 断言 `200`、`Content-Type` 前缀 `application/json`；
  - 响应数组排序后**集合相等** `[errors.json, tcp_battle_codec.json, tcp_logic_codec.json, udp_battle_codec.json]`；
  - 明确排除 `codec.lua`/`error.lua`。

### 关于 Content-Type 断言
Go `net/http` 的 JSON helper 写出的 Content-Type 是 `application/json; charset=utf-8`，
故断言改为前缀匹配（`strings.HasPrefix(ct, "application/json")`）而非全等。这是端口契约的
真实形态，proto/scripts 索引端点同为 `; charset=utf-8`，前缀匹配更鲁棒且符合实际。

## 3. TDD 流程

1. 先写测试 → `go test` 红：`srv.handleBaselineCodecIndex undefined`（handler 未实现）。
2. 实现 handler + 路由 → 绿。
3. 中间一次因 Content-Type 全等断言过严失败（`application/json; charset=utf-8`），改为前缀匹配。
4. gofmt canonical 校验发现测试文件 map 第一行少一空格对齐，补齐后 CLEAN。

## 4. 验证命令与输出

### `go build ./...`
```
$ go build ./...
（无输出）
BUILD_EXIT=0
```

### `go vet ./admin`
```
$ go vet ./admin
（无输出）
VET_EXIT=0
```

### `go test ./admin -run 'BaselineCodecIndex|CodecDist' -count=1 -v`（关键输出）
```
=== RUN   TestBaselineCodecIndex_ListsJsonFiles
--- PASS: TestBaselineCodecIndex_ListsJsonFiles (0.01s)
=== RUN   TestCodecDist_UploadPopulatesMultiCodec
--- PASS: TestCodecDist_UploadPopulatesMultiCodec (0.04s)
=== RUN   TestCodecDist_ConfigFilesListsMultiCodec
--- PASS: TestCodecDist_ConfigFilesListsMultiCodec (0.01s)
=== RUN   TestCodecDist_BaselineWriteAndReadRoundTrip
--- PASS: TestCodecDist_BaselineWriteAndReadRoundTrip (0.17s)
=== RUN   TestCodecDist_DownloadServesMultiFiles
--- PASS: TestCodecDist_DownloadServesMultiFiles (0.01s)
PASS
ok  	stressbot/admin	1.501s
```

### `go test ./admin -count=1`（整包不破坏）
```
$ go test ./admin -count=1
ok  	stressbot/admin	1.688s
TEST_EXIT=0
```

### gofmt canonical 校验（Windows CRLF strip）
```
$ sed 's/\r$//' admin/handlers.go > /tmp/x_handlers.go && gofmt -l /tmp/x_handlers.go
gofmt_handlers_OK   (空输出)
$ sed 's/\r$//' admin/baseline_codec_index_test.go > /tmp/x_test.go && gofmt -l /tmp/x_test.go
gofmt_test_OK       (空输出)
```

## 5. 自审发现

- **scope 合规**：仅动 `admin/handlers.go`（+1 handler +1 路由）与新增测试文件；未碰
  `listDirFiles`/`serveBaselineFile`/`writeBaselineFiles`/`buildConfigFiles`/codec 包/runtime/前端。
- **无兼容性兜底**：纯增量端点，无任何旧→新 fallback。
- **后缀 `.json`**：handler 如实列目录，未做 `_codec.json` 二次过滤（brief §3.3 明确要求）。
- **路由优先级**：`index.json` 字面路径在 `{name}` 通配前注册，Go 1.22 ServeMux 字面优先匹配，
  与 proto `index.json` vs `{name}` 同款已验证可行。
- **Content-Type 真实形态**：Go JSON helper 带默认 charset，断言用前缀匹配更贴合实际；
  proto/scripts 索引端点同为 `; charset=utf-8`，行为一致。
- **godoc/日志中文**：handler godoc 中文，错误经 `writeError`→`listDirFiles`（中文错误）传递。

## 6. 改动文件清单

- `admin/handlers.go`（+1 handler `handleBaselineCodecIndex` +1 路由）
- `admin/baseline_codec_index_test.go`（新增，1 测试）

未 git commit（项目规则：implementer 不自动提交，交 controller 处理）。
