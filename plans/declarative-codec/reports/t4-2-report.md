# T4.2 Report — Admin codec 端点（preview / algorithms）

> 状态：**DONE**。worktree `worktree-declarative-codec`。**未 commit**（按 brief 约束）。
> 全 repo `go build ./...` / `go vet ./...` / `go test ./admin/... -count=1` 全绿。

## 1. 端点设计

| 端点 | 方法 | 入参 | 出参 | 副作用 |
|---|---|---|---|---|
| `/sbot/codec/preview` | POST | JSON body（schema/mode/transport/route/bodyHex/keyHex/frameHex） | `codec.PreviewResult`（HTTP 200，即使 Error 非空） | 无 |
| `/sbot/codec/algorithms` | GET | 无（query 忽略） | `[]codec.AlgoMeta`（HTTP 200） | 无 |

请求体（preview）：
```json
{
  "schema":    { /* 完整 codec.json 内容，对象形式（json.RawMessage 承载） */ },
  "mode":      "encode",            // "encode" | "decode"
  "transport": "tcp",               // "tcp" | "udp"（当前不影响单 codec 计算）
  "route":     { "cmd": 100, "act": 7 },  // encode 入参
  "bodyHex":   "...",               // encode 入参
  "keyHex":    "...",               // encode/decode 共用
  "frameHex":  "..."                // decode 入参
}
```

**关键语义决策（来自 brief）**：
- preview 即使 `PreviewResult.Error` 非空也返回 **HTTP 200**——这是「编辑器预览」语义，前端据 `Error` 字段展示提示。
- 仅当请求 JSON 本身非法，或 `schema` 字段反序列化为 `*codec.CodecSchema` 失败时返回 **400**。
- schema 来自**请求体**（用户正在编辑的 codec 配置），不读 `conf/adapter/`。

## 2. 路由注册位置

`admin/handlers.go` 的 `registerRoutes()` 方法，紧随 `GET /sbot/capabilities` 之后新增一块：

```go
// ── Codec 预览/算法元数据（T4.2，纯计算，供前端 codec 编辑器调用）──
mux.HandleFunc("POST /sbot/codec/preview", s.handleCodecPreview)
mux.HandleFunc("GET /sbot/codec/algorithms", s.handleCodecAlgorithms)
```

与其它 `/sbot/...` 路由同一前缀、同一 mux、同一 `recoverMiddleware` 包裹。

## 3. 实现要点

### `admin/codec_handlers.go`（新增）

- **handler 签名遵循现有模式**：`func (s *AdminServer) handleCodecXxx(w http.ResponseWriter, r *http.Request)`。是 `*AdminServer` 方法仅为遵循 admin handler 注册约定；**handler 内不访问任何 `s.*` 字段**（pure compute）。
- **两步反序列化 schema**：先用 `json.RawMessage` 承载 `schema` 字段，解出整张请求体后再 `json.Unmarshal(req.Schema, &schema)`。这样 schema 解析失败能单独返回 400，与请求体整体非法 JSON 区分。
- **响应**：`writeJSON(w, http.StatusOK, res)` / `writeError(w, ErrInvalidArgument.WithMessage("..."))`——直接复用 `admin/errors.go` 现有 helper，无新 response 风格。
- **错误信息风格**：沿用现有 admin handler 的英文 API error body（如 `"invalid json: ..."`）。注：CLAUDE.md 的「错误信息使用中文」针对日志（`recoverMiddleware` 的 panic 日志为中文/英文混排，与本次改动无关）；`codec.Preview` 自身返回的中文 Error 字段经 JSON 原样透传给前端，已是中文。

### `admin/codec_handlers_test.go`（新增）

测试策略：codec 端点为纯计算，用**零值 `*AdminServer{}`** + `httptest.NewRecorder` 直接驱动 handler，避免 `NewAdminServer` 的重依赖（TaskStore 落盘 `data/`、Redis 连通性校验）。路由注册测试额外调 `s.registerRoutes()` 走完整 mux 验证。

## 4. TDD 用例（RED → GREEN）

RED：先写测试，`go vet ./admin/...` 报 `handleCodecPreview undefined`（handler 未实现）。
GREEN：实现 handler + 注册路由，全部通过。

| 用例 | 期望 | 结果 |
|---|---|---|
| `TestCodecPreview_Encode_OK` | 合法 schema + route + body + key → 200，FrameHex 非空、Fields 含 cmd=100/act=7（7 字段） | PASS |
| `TestCodecPreview_EncodeDecode_Roundtrip` | encode 帧再 decode → 200，BodyHex 往还原、RouteKey=`100:7`、HeaderErr=0 | PASS |
| `TestCodecPreview_BadSchema_OK_WithError` | 缺 length 字段的 schema → **200** + 非空 Error（中文，含汉字） | PASS |
| `TestCodecPreview_UnknownMode_OK_WithError` | mode=`frobnicate` → **200** + 非空 Error | PASS |
| `TestCodecPreview_InvalidRequestBody_400` | body=`{not json` → 400 | PASS |
| `TestCodecPreview_SchemaNotObject_400` | schema=`"not-an-object"`（非对象） → 400 | PASS |
| `TestCodecAlgorithms_OK` | 200，返回清单含 `xor_carry_rol`/`gzip`/`xor8`，op 分组顺序 cipher→compress→checksum→hash | PASS |
| `TestCodecAlgorithms_RouteRegistered` | `/sbot/codec/algorithms` 经 mux 命中后 200（非 404 fallback） | PASS |
| `TestCodecPreview_RouteRegistered` | `/sbot/codec/preview` 经 mux 命中后 非 404 | PASS |

```
ok  stressbot/admin  1.357s   (9/9 PASS)
```

## 5. 改动文件

| 文件 | 类型 | 说明 |
|---|---|---|
| `admin/codec_handlers.go` | 新增 | 2 个 handler（`handleCodecPreview`/`handleCodecAlgorithms`）+ `codecPreviewRequest` 类型 |
| `admin/codec_handlers_test.go` | 新增 | 9 个用例（encode/decode 往返、畸形 schema、400 边界、algorithms、路由注册） |
| `admin/handlers.go` | 修改 | `registerRoutes()` 新增 2 行路由注册（`POST /sbot/codec/preview` + `GET /sbot/codec/algorithms`） |

**未改动**：`codec/`、`adapter/`、`robot/`、`engine/`、`cmd/`、`agent/`、`conf/`、`go.mod`。

## 6. Self-review（对照 brief 验收）

- [x] 两个端点注册到 `/sbot/` 路由表；遵循现有 admin handler 模式（方法签名、json 解码、writeJSON/writeError）。
- [x] preview 纯计算：encode/decode 往返、畸形 schema 填 Error 不 panic（中文）、非法请求体 400、schema 非对象 400。
- [x] algorithms 返回 `codec.Algorithms()` 全量（含 `xor_carry_rol`/`gzip`/`xor8`），op 分组正确。
- [x] 仅 `admin/` 改动；无 gopher-lua；无副作用（不入库、不下发、不读 conf/adapter）。
- [x] preview 的 schema 来自请求体，不读 `conf/adapter/`。
- [x] preview 即使 Error 非空也返回 200。
- [x] `go build ./...` / `go vet ./...` / `go test ./admin/... -count=1` 全绿。
- [x] **未 git commit。**

## 7. Concerns

1. **测试用零值 `*AdminServer{}` 而非 `NewAdminServer`**：codec handler 是纯计算不访问 `s.*`，零值 Server 足够覆盖。路由注册测试调 `s.registerRoutes()` 也工作正常（`StaticDir=""` 的 `http.FileServer(http.Dir(""))` 对未命中路径返回 404，正好用作「未注册」的判别）。此为 admin 包首个 `_test.go`，建立了纯计算端点的轻量测试范式，可供 T4.3（多 codec 分发）参考。
2. **API 错误 body 用英文**：与现有 admin handler 一致（如 `"invalid json: ..."`）。CLAUDE.md 的「错误信息使用中文」主要约束日志；`codec.Preview` 自身返回的中文 Error 经 JSON 透传已是中文，未受影响。若需统一中文 API error body，应作为独立的全 admin 重构任务，不在本 brief 范围。
3. **transport 入参当前不影响计算**：`codec.Preview` 文档明确单 codec 下 offset 已固化在 schema，transport 仅为语义清晰保留。T4.3 多 codec 分发若需按 transport 选 codec，那是 T4.3 的事，本端点继续单 codec 语义。
