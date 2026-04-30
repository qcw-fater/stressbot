# Launcher 实施方案

> **角色定位**：Launcher 是一个独立的小型守护程序，负责拉起 Agent 业务进程（`agent.exe`）并在升级时执行二进制替换。
> **本文档目标读者**：负责 `cmd/launcher` 模块的开发者。
> **前置阅读**：`docs/design-distributed-master.md` §15 「热更新与滚动升级」。

---

## 0. 文档约定

- 项目名/Go module：`stressbot`
- 业务进程二进制：`agent.exe`（Linux：`agent`），来自 `cmd/agent`
- 守护进程二进制：`agent-launcher.exe`（Linux：`agent-launcher`），来自 `cmd/launcher`
- 两个二进制部署在**同一目录**下，Launcher 通过相对路径定位 Agent 进程
- 所有文件 IPC 标记位于二进制所在目录（与可执行文件同级）

## 1. 模块职责

Launcher 是一个**单文件、零业务依赖、独立可执行**的守护进程，它：

1. spawn 子进程 `agent.exe`，把 Launcher 自身收到的命令行参数原样透传
2. 阻塞等待子进程退出，根据 exit code 决定下一步动作
3. 当 Agent 进程通过 exit code `99` + `.upgrade.pending` 标记请求升级时，执行二进制替换（备份 → rename → 启动新版本）
4. 启动新版本后等待 `.upgrade.success` 标记，超时未收到则自动回滚到备份
5. 子进程崩溃（exit code 非 0 / 非 99）时，冷却 2 秒后自动重启
6. 接收到 SIGINT / SIGTERM 时，优雅地把信号转发给子进程，并在子进程退出后自身退出

**不做的事情**：
- 不与 Admin 通信，不监听任何端口
- 不解析 `config.json`，不知道任何业务配置
- 不依赖 stressbot 的任何业务包（`admin/`、`agent/`、`engine/`、`network/`、`monitor/` 等）
- 不做日志框架封装，直接 `fmt.Fprintln(os.Stderr, ...)`

## 2. 包结构

```
cmd/launcher/
  main.go               — 全部代码入口（包名 main）
  fileops.go            — 文件操作工具（copy / rename / windows 处理）
  fileops_test.go       — 测试
```

**只允许**引用以下标准库：

| 包 | 用途 |
|---|---|
| `os` | 文件操作、退出码、信号 |
| `os/exec` | spawn 子进程 |
| `os/signal` | 接收 SIGINT/SIGTERM |
| `syscall` | 信号转发（Windows 行为有差异） |
| `path/filepath` | 路径拼接 |
| `time` | 超时与冷却 |
| `errors`, `fmt`, `io` | 标准 |
| `runtime` | 平台分支 |

> 不引入第三方依赖。Launcher 编译产物预期 < 2MB。

## 3. 状态机

```
                      ┌──────────────────────────────────┐
                      │  initial: 启动 Launcher          │
                      └──────────────┬───────────────────┘
                                     ▼
                      ┌──────────────────────────────────┐
                      │  applyPendingUpgrade()           │
                      │  （检查 .upgrade.pending，       │
                      │   有则执行替换，无则跳过）       │
                      └──────────────┬───────────────────┘
                                     ▼
                      ┌──────────────────────────────────┐
                      │  spawn agent.exe                 │
                      └──────────────┬───────────────────┘
                                     ▼
                      ┌──────────────────────────────────┐
                      │  cmd.Wait() 阻塞                 │
                      │  ├ 收到 SIGINT/TERM ─→ 转发      │
                      │  └ 子进程退出 ─→ 取 exitCode     │
                      └──────────────┬───────────────────┘
                                     ▼
                ┌──────────────────────────────────┐
                │  watchUpgradeOutcome()           │
                │  （仅当上一步刚做过升级替换：    │
                │   等 .upgrade.success / 60s 超时 │
                │   超时则回滚 .bak）              │
                └──────────────┬───────────────────┘
                               ▼
            ┌─────────────────────────────────────────┐
            │  exitCode 分支                          │
            │  ├ 0  → 正常退出，Launcher 也退出        │
            │  ├ 99 → 升级请求，回到 applyPendingUpgrade │
            │  └ 其他 → 崩溃，sleep 2s 后回到 spawn   │
            └─────────────────────────────────────────┘
```

## 4. IPC 文件协议（与 Agent 进程的契约）

所有标记文件位于 `dir(agent.exe)`，即 Launcher 自身所在目录。

| 文件 | 写入方 | 读取方 | 写入时机 | 含义 / 内容 |
|---|---|---|---|---|
| `agent.exe.new` | Agent 进程 | Launcher | Agent 下载并校验通过后 | 新版本二进制，准备就绪 |
| `.upgrade.pending` | Agent 进程 | Launcher | Agent `os.Exit(99)` 前 | 文本：版本号字符串（如 `v1.2.0`），用于日志 |
| `agent.exe.bak` | Launcher | Launcher | Launcher 准备替换时 | 旧版本备份，由 Launcher 维护 |
| `.upgrade.success` | 新版本 Agent 进程 | Launcher | 新 Agent 注册 Admin 成功后 | 空文件，仅作信号 |

**Agent 进程退出码协议**：

| Exit Code | Launcher 行为 |
|---|---|
| `0` | Agent 主动退出（用户停止），Launcher 自身随之退出 |
| `99` | 升级请求：检测 `.upgrade.pending` → 替换 → 重新 spawn |
| 非 0 非 99 | 视为崩溃：冷却 2s 后重新 spawn |

> **重要**：exit code `99` 是 Agent 与 Launcher 之间的私有约定。Agent 必须保证只在确实想升级且写入了 `.upgrade.pending` + `agent.exe.new` 之后才使用此退出码。

## 5. 跨平台细节

### 5.1 退出码读取

```go
func exitCodeOf(err error) int {
    if err == nil {
        return 0
    }
    if exitErr, ok := err.(*exec.ExitError); ok {
        return exitErr.ExitCode() // Go 1.12+：跨平台
    }
    return -1 // 启动失败、非 ExitError
}
```

`ExitCode()` 在 Windows / Linux / macOS 上行为一致。

### 5.2 信号转发

| 平台 | 实现 |
|---|---|
| Linux / macOS | `cmd.Process.Signal(os.Interrupt)` 转发 SIGINT，`syscall.SIGTERM` 转发 SIGTERM |
| Windows | `cmd.Process.Signal(os.Interrupt)` 不可靠；改为关闭 `cmd.Process.Kill()`；或借助 `taskkill` |

**简化策略**：Launcher 收到信号后直接 `cmd.Process.Kill()`，让 Agent 自身的 graceful 逻辑由 Admin 通过 `/agent/v1/stop` 触发，Launcher 不承担优雅退出语义。

### 5.3 文件覆盖

- **Windows**：`os.Rename` 在目标存在时直接失败 → 必须先 `os.Remove(target)`，但运行中的 exe 不能 Remove 也不能 Rename。Launcher 操作 `agent.exe` 时它已经退出，所以 OK。
- **Linux**：`os.Rename` 即使目标存在也会原子覆盖，无需先 Remove。

统一的工具函数：

```go
// 原子替换 dst 为 src（src 必须存在；若 dst 存在，先删后改名）
func atomicReplace(src, dst string) error {
    if runtime.GOOS == "windows" {
        // Windows 必须先删除目标
        if _, err := os.Stat(dst); err == nil {
            if err := os.Remove(dst); err != nil {
                return fmt.Errorf("remove old: %w", err)
            }
        }
    }
    return os.Rename(src, dst)
}
```

### 5.4 文件复制（备份）

`os.Rename` 不能跨设备，但同目录内 OK。备份用 copy 而非 rename，避免破坏原文件：

```go
func copyFile(src, dst string) error {
    in, err := os.Open(src)
    if err != nil { return err }
    defer in.Close()

    out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
    if err != nil { return err }
    defer out.Close()

    if _, err := io.Copy(out, in); err != nil { return err }
    return out.Sync()
}
```

## 6. 完整代码骨架

```go
// cmd/launcher/main.go
package main

import (
    "errors"
    "fmt"
    "io"
    "os"
    "os/exec"
    "os/signal"
    "path/filepath"
    "runtime"
    "syscall"
    "time"
)

const (
    exitUpgrade = 99

    flagPending = ".upgrade.pending"
    flagSuccess = ".upgrade.success"

    suffixNew = ".new"
    suffixBak = ".bak"

    crashCooldown   = 2 * time.Second
    upgradeWatchTTL = 60 * time.Second
)

func agentBinaryName() string {
    if runtime.GOOS == "windows" {
        return "agent.exe"
    }
    return "agent"
}

func main() {
    selfPath, err := os.Executable()
    if err != nil {
        die("os.Executable: %v", err)
    }
    dir := filepath.Dir(selfPath)
    agentPath := filepath.Join(dir, agentBinaryName())

    if _, err := os.Stat(agentPath); err != nil {
        die("agent binary not found at %s: %v", agentPath, err)
    }

    sigCh := make(chan os.Signal, 2)
    signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
    defer signal.Stop(sigCh)

    // 1. 启动前先尝试 apply pending upgrade（覆盖上一次启动残留）
    applyPendingUpgrade(agentPath, dir)

    for {
        cmd := exec.Command(agentPath, os.Args[1:]...)
        cmd.Stdout = os.Stdout
        cmd.Stderr = os.Stderr
        cmd.Stdin = os.Stdin

        if err := cmd.Start(); err != nil {
            log("spawn agent failed: %v, retry in %s", err, crashCooldown)
            time.Sleep(crashCooldown)
            continue
        }
        log("spawned agent pid=%d", cmd.Process.Pid)

        // 等待子进程或外部信号
        waitDone := make(chan error, 1)
        go func() { waitDone <- cmd.Wait() }()

        var waitErr error
        select {
        case waitErr = <-waitDone:
        case sig := <-sigCh:
            log("received %v, killing agent", sig)
            _ = cmd.Process.Kill()
            waitErr = <-waitDone
            // 信号触发的退出，Launcher 也直接退出
            log("launcher exit by signal")
            return
        }

        exitCode := exitCodeOf(waitErr)
        log("agent exited code=%d", exitCode)

        // 处理升级结果（仅当上一次启动是从 .upgrade.pending 过来的）
        watchUpgradeOutcome(agentPath, dir)

        switch exitCode {
        case 0:
            log("normal exit, launcher quits")
            return
        case exitUpgrade:
            applyPendingUpgrade(agentPath, dir)
            // 立即 spawn 新版本
        default:
            log("crash detected, sleep %s before restart", crashCooldown)
            time.Sleep(crashCooldown)
        }
    }
}

// 检查 .upgrade.pending 标记，存在则替换 agent 二进制
func applyPendingUpgrade(agentPath, dir string) {
    pending := filepath.Join(dir, flagPending)
    if _, err := os.Stat(pending); errors.Is(err, os.ErrNotExist) {
        return
    } else if err != nil {
        log("stat %s: %v", pending, err)
        return
    }

    newPath := agentPath + suffixNew
    bakPath := agentPath + suffixBak

    if _, err := os.Stat(newPath); err != nil {
        log("upgrade pending but %s missing: %v", newPath, err)
        _ = os.Remove(pending)
        return
    }

    log("applying upgrade...")

    if err := copyFile(agentPath, bakPath); err != nil {
        log("backup failed: %v, abort upgrade", err)
        _ = os.Remove(pending)
        return
    }

    if err := atomicReplace(newPath, agentPath); err != nil {
        log("rename %s → %s failed: %v, rolling back", newPath, agentPath, err)
        _ = atomicReplace(bakPath, agentPath) // 恢复
        _ = os.Remove(pending)
        return
    }

    _ = os.Remove(pending)
    log("upgrade applied, .bak preserved for rollback verification")
}

// 监视升级结果：等待 .upgrade.success 或 60s 超时
// 仅在 .bak 存在时执行（说明刚做过替换）
func watchUpgradeOutcome(agentPath, dir string) {
    bakPath := agentPath + suffixBak
    if _, err := os.Stat(bakPath); errors.Is(err, os.ErrNotExist) {
        return // 没做过升级，nothing to watch
    }

    success := filepath.Join(dir, flagSuccess)
    deadline := time.Now().Add(upgradeWatchTTL)

    for time.Now().Before(deadline) {
        if _, err := os.Stat(success); err == nil {
            _ = os.Remove(success)
            _ = os.Remove(bakPath)
            log("upgrade success, .bak cleaned")
            return
        }
        time.Sleep(1 * time.Second)
    }

    // 超时：回滚
    log("upgrade success timeout (%s), rolling back to .bak", upgradeWatchTTL)
    if err := atomicReplace(bakPath, agentPath); err != nil {
        log("rollback failed: %v, manual intervention required", err)
    }
}

// 工具函数
func atomicReplace(src, dst string) error {
    if runtime.GOOS == "windows" {
        if _, err := os.Stat(dst); err == nil {
            if err := os.Remove(dst); err != nil {
                return fmt.Errorf("remove %s: %w", dst, err)
            }
        }
    }
    return os.Rename(src, dst)
}

func copyFile(src, dst string) error {
    in, err := os.Open(src)
    if err != nil { return err }
    defer in.Close()

    out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
    if err != nil { return err }
    defer out.Close()

    if _, err := io.Copy(out, in); err != nil { return err }
    return out.Sync()
}

func exitCodeOf(err error) int {
    if err == nil { return 0 }
    var exitErr *exec.ExitError
    if errors.As(err, &exitErr) {
        return exitErr.ExitCode()
    }
    return -1
}

func log(format string, args ...any) {
    fmt.Fprintf(os.Stderr, "[launcher] "+format+"\n", args...)
}

func die(format string, args ...any) {
    log(format, args...)
    os.Exit(1)
}
```

> **代码量预估**：~250 行，含工具函数与日志。完全够用，无需拆包。

## 7. 错误处理策略

| 场景 | 处理 |
|---|---|
| `agent.exe` 不存在（首次启动） | Launcher 立即退出，错误信息打印到 stderr，提示部署不完整 |
| spawn 失败（权限/磁盘问题） | 视为崩溃路径，冷却 2s 后重试，永不放弃 |
| 升级时备份失败 | 跳过本次升级，删除 `.upgrade.pending` 让流程继续；老版本继续运行 |
| 升级时 rename 失败 | 立即用 `.bak` 回滚，删除 `.upgrade.pending` |
| 升级后 60s 内未收到 `.upgrade.success` | 自动回滚 `.bak`，不再尝试新版本 |
| 回滚失败 | 打印 stderr，不再尝试，留待运维介入；但 Launcher 仍继续 spawn 当前 `agent.exe`（即新版的）以避免完全瘫痪 |
| 子进程持续崩溃（10 次/分钟） | 不做特殊处理；Admin 心跳超时会标记节点 unhealthy/offline，由运维感知 |

## 8. 部署建议

### 8.1 同目录部署

```
/opt/stressbot-agent/
  agent-launcher          ← Linux 启动入口（chmod +x）
  agent                   ← 业务进程，Launcher 拉起
  conf/
    config.json
    flow.json
    proto/
    scripts/
  log/
```

启动方式（systemd / Windows Service / 直接）：

```bash
# Linux
./agent-launcher -config conf/config.json

# Windows
.\agent-launcher.exe -config conf\config.json
```

### 8.2 systemd 单元（可选）

虽然 Launcher 自身已经守护 Agent，但生产环境建议再用 systemd 守护 Launcher（双层守护防止 Launcher 自身崩溃）：

```ini
# /etc/systemd/system/stressbot-agent.service
[Unit]
Description=stressbot agent (launcher)
After=network.target

[Service]
ExecStart=/opt/stressbot-agent/agent-launcher -config /opt/stressbot-agent/conf/config.json
WorkingDirectory=/opt/stressbot-agent
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## 9. 测试用例

### 9.1 自动化测试（cmd/launcher/main_test.go）

由于 Launcher 主流程依赖文件系统 + 子进程，建议测试方式：
- 用 `go build` 构建一个 mock agent 二进制（cmd/launcher/testdata/mock_agent/）作为子进程
- mock_agent 接受 `--exit-code N` 参数并立即退出，便于测试不同退出码
- 测试函数生成临时目录，复制 launcher + mock_agent 进去，启动 launcher 子进程并断言行为

最少覆盖以下用例：

| 用例 | mock_agent 行为 | 预期 Launcher 行为 |
|---|---|---|
| `TestNormalExit` | exit 0 | Launcher 也退出 |
| `TestCrashRestart` | exit 1 | Launcher 重启子进程 |
| `TestUpgradeApply` | 写 .new + .pending → exit 99 | Launcher 替换 agent.exe，新进程被 spawn |
| `TestUpgradeRollbackOnTimeout` | 同上但新版本启动后不写 .success | 60s 后 Launcher 回滚 .bak |
| `TestUpgradeRollbackOnNewCrash` | 同上但新版本立刻 exit 1 | Launcher 检测到无 .success，回滚 |
| `TestSignalForward` | sleep 长时间 | 发送 SIGTERM 给 launcher，子进程被 kill，launcher 退出 |
| `TestMissingNewBinary` | 写 .pending 但不写 .new → exit 99 | Launcher 跳过升级，spawn 老 agent |

### 9.2 手工验证（compatible with Phase 6 验收）

```bash
# 1. 编译 launcher + 一个 always-fail 的 mock agent
go build -o /tmp/lt/agent-launcher ./cmd/launcher

# 2. 写一个最小 mock agent
cat > /tmp/mock_agent.go <<'EOF'
package main
import (
    "os"
    "os/signal"
    "time"
    "syscall"
)
func main() {
    sig := make(chan os.Signal, 1)
    signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
    select {
    case <-sig:
    case <-time.After(30 * time.Second):
    }
    os.Exit(0)
}
EOF
go build -o /tmp/lt/agent /tmp/mock_agent.go

# 3. 启动 launcher，在另一个终端发送 SIGTERM 验证转发
cd /tmp/lt && ./agent-launcher
```

## 10. 实施分阶段计划

| 阶段 | 内容 | 工时估算 |
|---|---|---|
| Phase 1 | 基础骨架：`main.go` + `agentBinaryName` + spawn/wait 循环 | 0.5 天 |
| Phase 2 | 信号处理 + 崩溃冷却 + exitCode 判断 | 0.25 天 |
| Phase 3 | `applyPendingUpgrade` + 文件操作工具 + 跨平台原子替换 | 0.5 天 |
| Phase 4 | `watchUpgradeOutcome` + 自动回滚 | 0.25 天 |
| Phase 5 | 单元测试（含 mock agent） | 0.5 天 |
| Phase 6 | Windows / Linux 双平台手工验证 | 0.25 天 |

**总计：约 2.25 天**。

## 11. 验收标准

完成时必须满足以下全部条件：

- [ ] `go build ./cmd/launcher` 在 Windows / Linux 均能编译，无第三方依赖
- [ ] 编译产物 < 2MB
- [ ] 单元测试全部通过（含上述 7 个用例）
- [ ] 手工验证：构造 `.upgrade.pending` + 一个会立即写 `.upgrade.success` 的 mock agent，能完整跑通替换 + 清理 `.bak` 流程
- [ ] 手工验证：构造无 `.upgrade.success` 的场景，60s 后自动回滚
- [ ] 手工验证：Windows 下成功替换 `agent.exe`（验证 `os.Remove` + `os.Rename` 流程）
- [ ] Launcher 收到 SIGTERM 时，子进程被 kill，Launcher 在 5s 内退出
- [ ] 所有日志带 `[launcher]` 前缀输出到 stderr，便于和 Agent stdout 区分

## 12. 与其他模块的契约（必读）

### 12.1 Agent 进程必须遵守

1. 在写入 `agent.exe.new` 后才能写 `.upgrade.pending`
2. 在写入 `.upgrade.pending` 后才能 `os.Exit(99)`
3. 新版本 Agent 启动后**第一次成功注册到 Admin** 时必须写空文件 `.upgrade.success`
4. 不允许使用退出码 `99` 作其他用途
5. **不能直接读写 `.bak` 文件**（这是 Launcher 的私有备份）

### 12.2 Launcher 不假设的事

- 不知道 Admin 地址
- 不知道当前版本号（`.upgrade.pending` 内容只用于日志，不参与决策）
- 不解析 `config.json`
- 不做 SHA256 校验（由 Agent 进程下载时完成）

> 所有跨进程契约都在 §4 IPC 文件协议表格中。任何违反即为 Bug。
