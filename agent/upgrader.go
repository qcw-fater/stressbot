package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// handleUpgradeAsync 异步处理升级请求。
func (a *Agent) handleUpgradeAsync(req UpgradeRequest) {
	selfPath, err := os.Executable()
	if err != nil {
		stresslog.Error("[AGENT] 获取自身路径失败", zap.Error(err))
		a.mu.Lock()
		a.status = StatusIdle
		a.mu.Unlock()
		return
	}

	newPath := selfPath + ".new"

	// 1. 下载新版本
	stresslog.Info("[AGENT] 下载新版本", zap.String("url", req.URL))
	if err := a.downloadUpgrade(req.URL, newPath); err != nil {
		stresslog.Error("[AGENT] 下载失败", zap.Error(err))
		a.mu.Lock()
		a.status = StatusIdle
		a.mu.Unlock()
		return
	}

	// 2. SHA256 校验
	if req.SHA256 != "" {
		if err := verifyFileSHA256(newPath, req.SHA256); err != nil {
			stresslog.Error("[AGENT] SHA256 校验失败", zap.Error(err))
			os.Remove(newPath)
			a.mu.Lock()
			a.status = StatusIdle
			a.mu.Unlock()
			return
		}
		stresslog.Info("[AGENT] SHA256 校验通过")
	}

	// 3. drain 当前任务
	a.mu.Lock()
	cancel := a.taskCancel
	a.mu.Unlock()
	if cancel != nil {
		stresslog.Info("[AGENT] 正在 drain 当前任务...")
		cancel()
		// 等待任务 goroutine 退出
		a.wg.Wait() // TODO: 只等任务相关的 goroutine，不是全部
	}

	// 4. 写入 .upgrade.pending
	pendingPath := filepath.Join(filepath.Dir(selfPath), ".upgrade.pending")
	if err := os.WriteFile(pendingPath, []byte(req.Version), 0o644); err != nil {
		stresslog.Error("[AGENT] 写入 .upgrade.pending 失败", zap.Error(err))
		os.Remove(newPath)
		a.mu.Lock()
		a.status = StatusIdle
		a.mu.Unlock()
		return
	}

	stresslog.Info("[AGENT] 升级准备完成，即将退出",
		zap.String("version", req.Version),
		zap.Int("exitCode", 99))

	// 5. 退出，由 Launcher 接管
	os.Exit(99)
}

// MarkSuccess 新版本注册成功后调用，写入 .upgrade.success 标记。
func (a *Agent) MarkSuccess() {
	selfPath, err := os.Executable()
	if err != nil {
		return
	}
	bakPath := selfPath + ".bak"
	if _, err := os.Stat(bakPath); err != nil {
		return // 没有 bak 文件，不是升级场景
	}

	successPath := filepath.Join(filepath.Dir(selfPath), ".upgrade.success")
	if err := os.WriteFile(successPath, nil, 0o644); err != nil {
		stresslog.Warn("[AGENT] 写入 .upgrade.success 失败", zap.Error(err))
	} else {
		stresslog.Info("[AGENT] 升级成功标记已写入")
	}
}

func (a *Agent) downloadUpgrade(url, dst string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()

	return a.httpCli.DownloadFile(ctx, url, f)
}

func verifyFileSHA256(path, expectedHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expectedHex {
		return fmt.Errorf("SHA256 不匹配: expected=%s actual=%s", expectedHex, actual)
	}
	return nil
}
