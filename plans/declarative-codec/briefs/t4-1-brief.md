# T4.1 Brief — CodecResolver + 多 codec loader

> 你是 implementer。先读本 brief。参考 `plans/declarative-codec/04-track-config-distribution.md`（§2 设计要点、§3.0 加载顺序与映射契约）、`plans/declarative-codec/02-track-backend-integration.md` §2-C（CodecResolver 形态）、总纲 §2/决策 #8（每连接一份）、`plans/declarative-codec/reports/t1-freeze-handoff.md`（T1 冻结契约）。
> 工作目录：worktree 根。**不要 git commit**。

## 目标

新增 `adapter/codec_resolver.go`：`CodecResolver` 接口 + 实现 + 构造函数 + **多 codec loader**（从「连接 → codec 文件」映射 + 目录构建 resolver）。这是 T4 的基础设施，供 T2 在 runtime 接线（main.go/task_runner 调 loader → resolver 传给 Manager）。

## 背景（T1 已冻结，可直接用）

- `codec.LoadSchema(path) (*codec.CodecSchema, error)`
- `codec.LoadErrorMap(path) (map[uint64]string, error)`
- `adapter.NewSchemaAdapter(schema *codec.CodecSchema, errorMap map[uint64]string) (Adapter, error)` —— 返回的 Adapter 并发安全、无状态。
- 生产迁移产物已在 `conf/adapter/`：`tcp_logic_codec.json`、`tcp_battle_codec.json`、`udp_battle_codec.json`、`errors.json`。

## 新增文件

- `adapter/codec_resolver.go`：接口 + impl + `NewCodecResolver` + `LoadCodecResolver`。
- `adapter/codec_resolver_test.go`：TDD。

## CodecResolver（逐字基线）

```go
package adapter

// CodecResolver：server 串（"<proto>:<service>"，如 "tcp:logic"/"tcp:battle"/"udp:battle"）→ Adapter。
// 缺映射返回 nil，由调用方 fail loud（不在 resolver 内 panic）。
type CodecResolver interface {
	Resolve(server string) Adapter
}

// 纯显式映射，无 fallback（遵循「禁止兼容性兜底」）。
type codecResolver struct {
	byServer map[string]Adapter
}

func (r *codecResolver) Resolve(server string) Adapter { return r.byServer[server] }

// NewCodecResolver：显式 map 构造（每个声明的 server → 其 codec）。
func NewCodecResolver(byServer map[string]Adapter) CodecResolver {
	return &codecResolver{byServer: byServer}
}
```

加编译期断言：`var _ CodecResolver = (*codecResolver)(nil)`。

## LoadCodecResolver（loader）

```go
// LoadCodecResolver：按「server 串 → codec 文件名」映射，从 codecDir 逐份加载并构建 resolver。
//   codecs     : server串 → 文件名（如 "tcp:logic" → "tcp_logic_codec.json"）。
//   codecDir   : 存放 *_codec.json 的目录（如 "conf/adapter"）。
//   errorsFile : 共享 errors.json 的路径（相对 codecDir 或绝对；为空则不加载错误码 map）。
// 同一文件被多个 server 引用时 dedup——编译一次、共享同一无状态 Adapter 实例。
// 任一文件缺失/解析失败/校验失败/未知算法 → 返回 error（中文，带 server 串 + 文件名 + 原因）。
func LoadCodecResolver(codecDir string, codecs map[string]string, errorsFile string) (CodecResolver, error)
```

实现要点：
- 先（可选）`codec.LoadErrorMap(errorsFile)` 得共享 errorMap（一次）。
- 维护 `fileCache map[string]Adapter`：同一文件名只 `codec.LoadSchema + adapter.NewSchemaAdapter` 一次，复用实例（dedup）。
- 遍历 `codecs` 的每个 `server→file`：拼 `filepath.Join(codecDir, file)`；`codec.LoadSchema` → `adapter.NewSchemaAdapter(schema, errorMap)`；填 `byServer[server]`。
- 顺序：为错误信息稳定，按 server 串排序后遍历（非必须，但利于可读错误）。
- 失败 fail loud：文件不存在 / JSON 非法 / Validate 失败 / 编译失败 → `fmt.Errorf("codec 加载失败：连接 %q 文件 %q：%w", server, file, err)`（中文上下文）。
- `codecs` 为空 → 返回 error（"未声明任何连接的 codec 映射"），不返回空 resolver（避免静默）。
- 并发安全：resolver 构造后 `byServer` 只读（构造期填好后不再变）。

## 关键约束

- **无 fallback**：缺文件/缺映射直接报错，绝不回退到「某个默认 codec」。
- **仅 adapter/ 包**：只新增 `adapter/codec_resolver.go`(+test)。**不改** Manager/Robot/Dialer/Connection/cmd/agent/admin（runtime 接线是 T2，admin 分发是 T4.2/T4.3）。
- 不 import gopher-lua。
- loader 只做「加载 + 编译 + 组装 resolver」，不读运行 config（config 解析 + 调 loader 是 T2 的 main.go/task_runner 职责）。
- **不要 git commit。**

## 工作方式（TDD）

1. RED：`adapter/codec_resolver_test.go`：
   - 用 T1.6 生产产物 `conf/adapter/{tcp_logic,tcp_battle,udp_battle}_codec.json` + `errors.json`，传 3 条映射，`LoadCodecResolver` 成功；Resolve 三个 server 串各返回**非 nil** Adapter；Resolve 未知 server（如 `"tcp:xxx"`）返回 nil。
   - dedup：两条 server 指向同一文件（如 `"tcp:logic"` 与 `"tcp:logic2"` 都指 `tcp_logic_codec.json`）→ Resolve 返回**同一实例**（指针相等）。
   - 缺文件：映射指向不存在文件 → 返回含 server+文件名的中文 error。
   - 空映射 → error。
   - errors.json 可选：errorsFile 传空字符串也能加载（errorMap 为空/nil，adapter 仍可用，DescribeError 返回 ""）。
   - NewCodecResolver 直接构造：Resolve 行为同上。
2. GREEN：实现 codec_resolver.go。
3. `go build ./...`、`go vet ./adapter/...`、`go test ./adapter/... -count=1` 全绿、输出干净。
4. **不要 git commit。**

## 验收（self-review）

- CodecResolver 接口 + impl + NewCodecResolver + LoadCodecResolver 齐全；`var _ CodecResolver` 断言。
- 同文件 dedup 为同一实例；缺映射/缺文件/空映射 fail loud（中文）；errors.json 可选。
- 仅 adapter/ 新增；无 gopher-lua；不改 runtime/admin。
- 测试用真实 conf/adapter 产物，非 mock。

## 报告

写完整报告到 `plans/declarative-codec/reports/t4-1-report.md`：实现内容、loader 流程、dedup/fail-loud/errors-optional 各用例、TDD RED/GREEN、改动文件、self-review、concerns。
返回（<15 行）：Status、改动文件、一行测试摘要、concerns、报告路径。
