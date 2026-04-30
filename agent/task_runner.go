package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	stresslog "stressbot/utils/log"

	"stressbot/monitor"

	"go.uber.org/zap"
)

// TaskRunner 管理单次压测任务的执行：拉配置、写目录、起 Manager、等完成。
type TaskRunner struct {
	assignment *TaskAssignment
	cfg        *ResolvedConfig
	cli        *AdminClient
	collector  *monitor.MetricsCollector
	httpCli    *http.Client
	workDir    string
}

// NewTaskRunner 创建任务执行器。
func NewTaskRunner(assignment *TaskAssignment, cfg *ResolvedConfig, cli *AdminClient, collector *monitor.MetricsCollector) *TaskRunner {
	return &TaskRunner{
		assignment: assignment,
		cfg:        cfg,
		cli:        cli,
		collector:  collector,
		httpCli:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// Run 执行任务。返回任务结果和错误信息。
// 阻塞直到任务完成或 ctx 被取消。
func (r *TaskRunner) Run(ctx context.Context) (TaskResult, string) {
	taskID := r.assignment.TaskID

	// 1. 创建临时目录
	r.workDir = filepath.Join(r.cfg.TaskWorkDir, "stressbot-task-"+taskID)
	confDir := filepath.Join(r.workDir, "conf")
	if err := os.MkdirAll(filepath.Join(confDir, "proto"), 0o755); err != nil {
		return TaskFailed, fmt.Sprintf("创建临时目录失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(confDir, "scripts"), 0o755); err != nil {
		return TaskFailed, fmt.Sprintf("创建脚本目录失败: %v", err)
	}

	stresslog.Info("[TASK] 临时目录已创建", zap.String("dir", r.workDir))

	// 2. 拉取配置文件
	if err := r.pullConfigFiles(ctx, confDir); err != nil {
		return TaskFailed, fmt.Sprintf("拉取配置文件失败: %v", err)
	}

	// 3. 初始化监控（重置 collector）
	monitor.Init(monitor.CollectorConfig{
		Enabled: true,
		ApdexT:  r.assignment.RobotConfig.ApdexT,
	})

	// 4. TODO: 加载 adapter / proto / flow / scripts，创建 Manager
	// 当前阶段先模拟执行，等待 Phase 10/11 完成（monitor 扩展 + Manager RunWithContext）
	// 完整实现需要：
	//   - adapter.NewLuaAdapter(...) 用 confDir/codec.lua（如果有的话）
	//   - protox.NewLoader(confDir+"/proto") 加载 proto
	//   - loadFlow(confDir + "/flow.json")
	//   - script.NewRuntimePool(confDir + "/scripts")
	//   - network.NewDialer(...)
	//   - robot.NewManager(managerCfg, flow, factory, dialer, luaPool)
	//   - mgr.RunWithContext(ctx, runCfg)

	stresslog.Info("[TASK] 任务执行中（占位实现）",
		zap.String("taskID", taskID),
		zap.Int("totalBots", r.assignment.TotalBots),
		zap.Int("startNumber", r.assignment.StartNumber))

	// 等待 ctx 取消或超时
	select {
	case <-ctx.Done():
		return TaskStopped, ""
	case <-time.After(10 * time.Minute):
		// 防止无限执行（占位逻辑）
		return TaskCompleted, ""
	}
}

// Cleanup 清理临时目录。
func (r *TaskRunner) Cleanup() {
	if r.workDir == "" {
		return
	}
	if err := os.RemoveAll(r.workDir); err != nil {
		stresslog.Warn("[TASK] 清理临时目录失败", zap.String("dir", r.workDir), zap.Error(err))
	} else {
		stresslog.Info("[TASK] 临时目录已清理", zap.String("dir", r.workDir))
	}
}

// pullConfigFiles 从 Admin 拉取所有配置文件并写入本地。
func (r *TaskRunner) pullConfigFiles(ctx context.Context, confDir string) error {
	for _, cf := range r.assignment.ConfigFiles {
		targetPath := filepath.Join(confDir, cf.Path)

		// 确保父目录存在
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", filepath.Dir(targetPath), err)
		}

		stresslog.Info("[TASK] 拉取配置文件",
			zap.String("path", cf.Path),
			zap.String("url", cf.URL))

		if err := r.downloadFile(ctx, cf.URL, targetPath); err != nil {
			return fmt.Errorf("下载 %s 失败: %w", cf.Path, err)
		}

		// SHA256 校验
		if cf.SHA256 != "" {
			if err := r.verifySHA256(targetPath, cf.SHA256); err != nil {
				os.Remove(targetPath)
				return fmt.Errorf("校验 %s 失败: %w", cf.Path, err)
			}
		}

		stresslog.Info("[TASK] 配置文件已保存", zap.String("path", cf.Path))
	}
	return nil
}

func (r *TaskRunner) downloadFile(ctx context.Context, url, dstPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := r.httpCli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

func (r *TaskRunner) verifySHA256(path, expectedHex string) error {
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
