# T4.2 Brief — Admin codec 端点（preview / algorithms）

> 你是 implementer。先读本 brief。参考 `plans/declarative-codec/04-track-config-distribution.md` §3.4（preview/algorithms 端点）、`plans/declarative-codec/reports/t1-freeze-handoff.md`（codec.Preview / codec.Algorithms 契约）、现有 `admin/` 路由与 handler 模式。
> 工作目录：worktree 根。**不要 git commit**。

## 目标

在 admin 新增两个**纯计算**只读端点，供前端 codec 编辑器调用（实时预览 + 算法下拉）：

1. `POST /sbot/codec/preview` —— 包 `codec.Preview`：入参含 codec schema + mode/route/bodyHex/keyHex/frameHex，跑一次 encode 或 decode，返回帧/字段解释。
2. `GET /sbot/codec/algorithms` —— 包 `codec.Algorithms()`：返回算法元数据清单。

两者**不入库、不下发任务、无副作用**（纯内存计算）。

## T1 已冻结的契约（直接调用）

```go
// codec.Preview：schema 编译 + 单次 encode/decode。失败填 Error 不 panic。
//   schema 来自请求体的 codec JSON（需先 json.Unmarshal 成 *codec.CodecSchema）。
func codec.Preview(schema *codec.CodecSchema, mode, transport string, route map[string]any, bodyHex, keyHex, frameHex string) codec.PreviewResult

type codec.PreviewResult struct {
	Mode      string
	FrameHex  string   // encode 出参
	BodyHex   string   // decode 出参
	RouteKey  string
	HeaderErr uint64
	Fields    []PreviewField
	Error     string   // schema 编译/运行错误（中文）
}

// codec.Algorithms()：返回 []codec.AlgoMeta（cipher/compressor/checksum/hash，含 name/op/params/description）。
```

## 端点设计

### POST /sbot/codec/preview

请求体 JSON（前端构造）：
```json
{
  "schema":   { /* 完整 codec.json 内容，对象形式 */ },
  "mode":     "encode",           // "encode" | "decode"
  "transport": "tcp",             // "tcp" | "udp"（当前 codec.Preview 单 codec 下 transport 主要为语义清晰，不强制影响计算）
  "route":    { "cmd": 1, "act": 2 },
  "bodyHex":  "010203",
  "keyHex":   "...",
  "frameHex": "..."               // decode 模式用
}
```

处理：
1. 解析请求体；把 `schema`（json.RawMessage 或 map）`json.Unmarshal` 成 `*codec.CodecSchema`。
2. 调 `codec.Preview(schema, mode, transport, route, bodyHex, keyHex, frameHex)`。
3. 把 `PreviewResult` 以 JSON 返回（HTTP 200；**即使 PreviewResult.Error 非空也返回 200 + Error 字段**——这是「编辑器预览」语义，前端据 Error 显示提示，不是 HTTP 错误）。
4. 仅当请求 JSON 本身非法（unmarshal 请求体失败）才返回 400。

### GET /sbot/codec/algorithms

无入参（或忽略 query）。直接 `codec.Algorithms()` → JSON 200 返回。

## 实现要求

- 先读 `admin/` 摸清现有路由注册方式（找 `/sbot/...` 路由表/handler 注册点）与 handler 写法（JSON 编解码、错误响应风格）。**遵循现有模式**（handler 签名、response helper、中文错误信息风格）。
- 端点注册到与其它 `/sbot/...` 同一前缀/路由表。
- handler 内只做「解析请求 → 调 codec → 返回 JSON」，无业务/存储副作用。
- **不引入**新的持久化、不改任务/agent/baseline 流程。
- 不 import gopher-lua（admin 已可能不依赖；新增代码只 import codec + stdlib + admin 既有依赖）。

## 关键约束

- **纯计算**：preview/algorithms 不写文件、不入 DB、不下发、不依赖任务状态。
- **仅 admin/**：新增 handler（建议 `admin/codec_handlers.go` 或并入合适现有文件）+ 路由注册。**不改** codec/、adapter/、robot/、engine/、cmd/、agent/。
- preview 的 schema 来自请求体（用户正在编辑的 codec 配置），**不读 conf/adapter/**。
- **不要 git commit。**

## 工作方式（TDD）

1. RED：`admin/codec_handlers_test.go`（或扩展现有 admin 测试）：
   - preview encode：用合法 schema（可用 conf/adapter/tcp_logic_codec.json 内容当 schema）+ route/body/key → 200，FrameHex 非空、Fields 含 cmd/act 等。
   - preview decode：用 encode 得到的帧 hex → 200，BodyHex/RouteKey 还原。
   - preview 畸形 schema（如缺 length 字段）→ 200，`Error` 非空（中文）。
   - preview 请求体非法 JSON → 400。
   - algorithms：200，返回清单含 `xor_carry_rol`/`gzip`/`xor8` 等现协议算法，op 分组。
   - 用 net/http/httptest 走 handler（按现有 admin 测试模式）。
2. GREEN：实现 handler + 注册路由。
3. `go build ./...`、`go vet ./admin/...`、`go test ./admin/... -count=1` 全绿、输出干净。
4. **不要 git commit。**

## 验收（self-review）

- 两个端点注册到 `/sbot/` 路由表；遵循现有 admin handler 模式。
- preview 纯计算：encode/decode 往返、畸形 schema 填 Error 不 panic、非法请求体 400。
- algorithms 返回 codec.Algorithms() 全量。
- 仅 admin/ 改动；无 gopher-lua；无副作用。

## 报告

写完整报告到 `plans/declarative-codec/reports/t4-2-report.md`：端点设计、路由注册位置、preview/algorithms 各用例、TDD RED/GREEN、改动文件、self-review、concerns。
返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
