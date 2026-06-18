# T4.1 报告 — CodecResolver + 多 codec loader

> 状态：**DONE**。新增 `adapter/codec_resolver.go`(+test)，全 repo `go build`/`go vet ./adapter/...`/`go test ./adapter/... -count=1` 绿。未 git commit。

## 1. 实现内容

### 1.1 `adapter/codec_resolver.go`

- **`CodecResolver` 接口**：单方法 `Resolve(server string) Adapter`，按 `<proto>:<service>` server 串返回 Adapter。
- **`codecResolver` impl**：`byServer map[string]Adapter` 纯显式映射，无 fallback。
- **`Resolve`**：命中返回 Adapter，未声明返回 nil（调用方负责 fail loud，不在 resolver 内 panic）。
- **`NewCodecResolver(byServer map[string]Adapter) CodecResolver`**：直接构造。防御性拷贝入参 map，保证 resolver 构造后只读、上层后续修改不影响内部状态。
- **编译期断言**：`var _ CodecResolver = (*codecResolver)(nil)`。
- **`LoadCodecResolver(codecDir string, codecs map[string]string, errorsFile string) (CodecResolver, error)`**：loader。
- 辅助 `resolvePath(codecDir, name)`：name 绝对则直接用，否则 `filepath.Join(codecDir, name)`。

### 1.2 import 约束

仅 `fmt`、`path/filepath`、`sort`、`stressbot/codec`——**零 gopher-lua**，符合 brief。

## 2. LoadCodecResolver 流程

1. `len(codecs)==0`（含 nil）→ 中文 error「未声明任何连接的 codec 映射」。
2. `errorsFile != ""` → `resolvePath` + `codec.LoadErrorMap`；失败 wrap 中文上下文 fail loud。`errorsFile==""` 跳过，errorMap 保持 nil。
3. 维护 `fileCache map[string]Adapter` 做 dedup；`byServer` 装结果。
4. **按 server 串 `sort.Strings` 排序后遍历**（错误信息稳定可读，非功能必需）。
5. 每条 `server→file`：
   - file 为空 → 中文 error（带 server 串）。
   - fileCache 命中 → 直接复用实例填 byServer（dedup）。
   - 否则 `codec.LoadSchema(resolvePath(codecDir, file))` → `NewSchemaAdapter(schema, errorMap)`；任一失败 wrap「连接 %q 文件 %q：%w」fail loud。
   - 成功则填 fileCache + byServer。
6. 返回 `NewCodecResolver(byServer)`。

## 3. 关键用例（TDD RED → GREEN）

测试 `adapter/codec_resolver_test.go` 用 T1.6 真实产物 `conf/adapter/{tcp_logic,tcp_battle,udp_battle}_codec.json + errors.json`，非 mock：

| 用例 | 期望 | 结果 |
|---|---|---|
| 三 codec 加载 + 三个 server Resolve 非 nil | 成功 | PASS |
| 未知 server（`tcp:nonexistent`/`""`）Resolve | nil | PASS |
| dedup（`tcp:logic` 与 `tcp:logic2` 同指 `tcp_logic_codec.json`） | 指针相等 | PASS |
| 映射含不存在文件 | 中文 error 含 server+文件名 | PASS |
| 空映射 | error | PASS |
| nil 映射 | error | PASS |
| errorsFile=`""` | 可加载，DescribeError 返回 `""` | PASS |
| errorsFile 非空但缺文件 | 中文 error fail loud | PASS |
| errors.json 加载后 DescribeError(0) 不 panic | OK | PASS |
| codecDir 绝对路径 | 成功 | PASS |
| NewCodecResolver 直接构造（含未知 server→nil） | 行为正确 | PASS |
| NewCodecResolver 空 map 不报错（与 loader 不同） | OK | PASS |

RED 阶段先写测试运行确认编译期失败（无 codec_resolver.go）→ GREEN 实现 → 全 PASS。

## 4. 改动文件

- 新增 `adapter/codec_resolver.go`（121 行）
- 新增 `adapter/codec_resolver_test.go`（13 测试）

**未触碰**：`robot/`、`network/`、`cmd/`、`agent/`、`admin/`、`codec/` 任何文件，未 git commit。

## 5. Self-review（对照 brief 验收清单）

- [x] CodecResolver 接口 + impl + NewCodecResolver + LoadCodecResolver 齐全。
- [x] `var _ CodecResolver = (*codecResolver)(nil)` 断言。
- [x] 同文件 dedup 同一实例（指针相等）；缺映射/缺文件/空映射 fail loud（中文）；errorsFile 可选。
- [x] 仅 adapter/ 新增；无 gopher-lua；不改 runtime/admin。
- [x] 测试用真实 `conf/adapter` 产物。
- [x] `go build ./...` + `go vet ./adapter/...` + `go test ./adapter/... -count=1` 全绿。

## 6. 给 T2 的接线契约（reminder）

- runtime（main.go/task_runner）职责：解析 config（`standalone.adapter.codecs` map）→ 调 `LoadCodecResolver(codecDir, codecs, errorsFile)` → resolver 传 Manager → Robot/Dialer 在建连接时按 `<proto>:<service>` 调 `Resolve`，nil 视为致命错误。
- 未显式声明的连接按 `<proto>_<service>_codec.json` 推断（T2 实现），缺文件 fail loud。

## 7. Concerns

无。设计逐字对齐 brief；签名与 T1 冻结契约一致；唯一一处自由度是 `NewCodecResolver` 做了防御性 map 拷贝（brief 未禁止，且利于只读语义），不影响外部行为。
