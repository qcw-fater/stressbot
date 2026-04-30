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

	// 启动前先处理残留的升级标记（覆盖上次启动残留）
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

		// 等待子进程退出或外部信号
		waitDone := make(chan error, 1)
		go func() { waitDone <- cmd.Wait() }()

		var waitErr error
		select {
		case waitErr = <-waitDone:
		case sig := <-sigCh:
			log("received %v, killing agent", sig)
			_ = cmd.Process.Kill()
			waitErr = <-waitDone
			log("launcher exit by signal")
			return
		}

		exitCode := exitCodeOf(waitErr)
		log("agent exited code=%d", exitCode)

		// 处理升级结果（仅当 .bak 存在时，说明刚做过替换）
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

// applyPendingUpgrade 检查 .upgrade.pending 标记，存在则替换 agent 二进制。
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
		_ = atomicReplace(bakPath, agentPath)
		_ = os.Remove(pending)
		return
	}

	_ = os.Remove(pending)
	log("upgrade applied, .bak preserved for rollback verification")
}

// watchUpgradeOutcome 监视升级结果：等待 .upgrade.success 或 60s 超时后回滚。
func watchUpgradeOutcome(agentPath, dir string) {
	bakPath := agentPath + suffixBak
	if _, err := os.Stat(bakPath); errors.Is(err, os.ErrNotExist) {
		return
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

// atomicReplace 原子替换 dst 为 src。
// Windows 需先删除目标（因 os.Rename 不能覆盖已存在文件），
// Linux os.Rename 自动覆盖。
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

// copyFile 复制 src 到 dst（同设备内完整拷贝），权限 0o755。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// exitCodeOf 从 cmd.Wait() 返回的 error 中提取子进程退出码。
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// log 输出到 stderr，带 [launcher] 前缀。
func log(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[launcher] "+format+"\n", args...)
}

// die 输出到 stderr 后退出。
func die(format string, args ...any) {
	log(format, args...)
	os.Exit(1)
}
