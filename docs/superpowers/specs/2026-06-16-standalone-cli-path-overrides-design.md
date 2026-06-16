# 单机模式资源路径 CLI 覆盖

## 背景

`cmd/agent/main.go` 的单机模式 `runStandalone(cfg, confDir)` 中，所有资源路径都从 `confDir`（`-config` 文件所在目录）硬编码派生：

- flow：`confDir/flow/flow.json`（`loadFlow`）
- proto：`confDir/proto`（`protox.NewLoader`）
- scripts：`confDir/scripts`（Lua 运行时池 + 预编译 + `detectShareUsage`）
- adapter：`confDir/adapter/codec.lua` + 可选 `error.lua`（`adapter.NewLuaAdapter`）

`confDir` 在该函数内**仅**用于派生这 4 个资源路径。要切换压测场景（不同 flow/proto/scripts）必须挪动文件或改 `config.json` 位置，不便。

## 目标

允许像 `-config` 一样通过 CLI flag 覆盖这 4 个资源路径。仅在单机模式生效（Agent 模式的资源来自 Admin 下发的任务包，不使用本地 conf）。

## 方案

新增 4 个可选 flag（空值回退到 `confDir` 相对默认路径，完全向后兼容）：

| Flag | 类型 | 空值默认 | 用途 |
|------|------|---------|------|
| `-flow` | 文件 | `<conf>/flow/flow.json` | `loadFlow` |
| `-proto` | 目录 | `<conf>/proto` | `protox.NewLoader` |
| `-scripts` | 目录 | `<conf>/scripts` | Lua 池 + 预编译 + `detectShareUsage` |
| `-adapter` | 目录 | `<conf>/adapter` | `codec.lua` + 可选 `error.lua` |

**覆盖语义**：flag 为空 → `confDir` 相对默认（当前行为）；flag 非空 → 用该值并 `filepath.Abs` 解析为绝对路径。`-adapter` 是**目录**（与 proto/scripts 一致），`codec.lua` 与可选 `error.lua` 从中派生，保留现有 `os.Stat` 可选检测逻辑。

被否决的方案：在 `StandaloneConfig`（config.json）中加字段。理由——用户明确要求 CLI 传参；且新增 JSON 字段需按项目「新字段全链路一致」规则穿透 Agent 任务包下发路径，范围过大、超出本需求。

## 代码结构

`cmd/agent/main.go`：

1. 新增纯函数 + 可测试辅助：

```go
type standalonePaths struct {
    Flow, Proto, Scripts, Adapter string
}

// flag 为空 → 回退 confDir 下默认相对路径；否则解析为绝对路径。
func resolveStandalonePaths(confDir, flow, proto, scripts, adapter string) standalonePaths
```

2. `main()`：解析 4 个 flag（空默认值，因为 `confDir` 在 `flag.Parse` 之后才确定），调用 `resolveStandalonePaths` 得到 `paths`，传给 `runStandalone(cfg, paths)`。

3. `runStandalone` 签名由 `(cfg, confDir)` 改为 `(cfg, paths)`；函数体内不再需要 `confDir`，4 处 `filepath.Join(confDir, ...)` 改为 `paths.*`。

4. 启动时打印 4 个解析后的路径，便于确认覆盖是否生效。

5. Agent 模式：flag 全局定义但在 `runAgentMode` 中不使用（资源来自 Admin 任务包），不报错、无行为变化。

## 测试

`cmd/agent/main_test.go`（`package main`）覆盖 `resolveStandalonePaths`：
- 全空 → 回退 confDir 默认
- 每个 flag 单独覆盖 → 使用该值
- 相对路径 → 绝对路径

flag 解析与 `runStandalone` 接线为 main 包顶层逻辑，不做单元测试。

## 验证

1. `go build ./...`
2. `go test ./cmd/agent/...`
3. 运行 `go run ./cmd/agent -config conf/config.json -flow conf/flow/rank.json`，确认日志显示覆盖后的 flow 路径且流程加载成功（完整 2~5 分钟运行需真实游戏服务器）。
