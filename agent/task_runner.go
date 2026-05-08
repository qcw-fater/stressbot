package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/monitor"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/robot"
	"stressbot/script"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

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

// Run 执行任务。阻塞直到任务完成或 ctx 被取消。
func (r *TaskRunner) Run(ctx context.Context) (TaskResult, string) {
	taskID := r.assignment.TaskID

	// 0. 任务级临时切换日志等级（来自前端 RobotConfig.logLevel）
	//    结束时（包括异常路径）恢复原等级，避免影响后续任务。
	if r.assignment.LogLevel != "" {
		prev := stresslog.GetLogLevel()
		next := stresslog.StrToLevel(r.assignment.LogLevel)
		if next != prev {
			stresslog.SetLogLevel(next)
			stresslog.Info("[TASK] 临时切换日志等级",
				zap.String("taskID", taskID),
				zap.String("from", prev.String()),
				zap.String("to", next.String()))
			defer func() {
				stresslog.SetLogLevel(prev)
				stresslog.Info("[TASK] 已恢复日志等级",
					zap.String("taskID", taskID),
					zap.String("level", prev.String()))
			}()
		}
	}

	// 1. 创建临时目录
	r.workDir = filepath.Join(r.cfg.TaskWorkDir, "stressbot-task-"+taskID)
	confDir := filepath.Join(r.workDir, "conf")
	protoDir := filepath.Join(confDir, "proto")
	scriptsDir := filepath.Join(confDir, "scripts")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		return TaskFailed, fmt.Sprintf("创建临时目录失败: %v", err)
	}
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		return TaskFailed, fmt.Sprintf("创建脚本目录失败: %v", err)
	}

	stresslog.Info("[TASK] 临时目录已创建", zap.String("dir", r.workDir))

	// 2. 从 Admin 下载配置文件
	if r.assignment.ConfigURL != "" && len(r.assignment.ConfigFiles) > 0 {
		configURL := strings.TrimRight(r.assignment.ConfigURL, "/")
		for _, relPath := range r.assignment.ConfigFiles {
			url := configURL + "/" + relPath
			targetPath := filepath.Join(confDir, relPath)
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return TaskFailed, fmt.Sprintf("创建目录 %s 失败: %v", filepath.Dir(targetPath), err)
			}
			if err := r.downloadFile(ctx, url, targetPath); err != nil {
				return TaskFailed, fmt.Sprintf("下载 %s 失败: %v", relPath, err)
			}
			stresslog.Info("[TASK] 配置文件已下载", zap.String("path", relPath))
		}
		stresslog.Info("[TASK] 所有配置文件已下载", zap.Int("count", len(r.assignment.ConfigFiles)))
	} else {
		return TaskFailed, "无配置文件可下载（configUrl 或 configFiles 为空）"
	}

	// 3. 加载协议适配器（使用 Agent 本地的 codec.lua）
	poolSize := runtime.NumCPU()
	adp, err := adapter.NewLuaAdapter(poolSize, r.cfg.AdapterScript)
	if err != nil {
		return TaskFailed, fmt.Sprintf("加载适配器失败: %v", err)
	}
	defer adp.Close()

	// 4. 加载 .proto 文件
	loader := protox.NewLoader([]string{protoDir}, nil)
	files, err := loader.Load()
	if err != nil {
		return TaskFailed, fmt.Sprintf("加载 proto 文件失败: %v", err)
	}

	registry := protox.NewRegistry(files)
	factory := protox.NewFactory(registry)

	// 5. 加载流程配置
	flow, err := loadTaskFlow(filepath.Join(confDir, "flow.json"))
	if err != nil {
		return TaskFailed, fmt.Sprintf("加载流程配置失败: %v", err)
	}

	// 回填 ActionDef.Name
	for name, action := range flow.Actions {
		action.Name = name
	}

	stresslog.Info("[TASK] 流程配置已加载",
		zap.String("taskID", taskID),
		zap.Int("nodes", len(flow.Nodes)),
		zap.Int("actions", len(flow.Actions)))

	// 6. 重置监控
	//
	// **不能**调用 monitor.Init —— 那会新建一个 collector 替换 global，
	// 而 a.collector / StressReporter.src 持有的还是旧引用，导致
	// Robot 业务通过 monitor.Global() 写入新 collector，reporter 上报永远是空。
	// 任务级仅需 Reset + SetApdexT 即可，全局单例保持不变。
	r.collector.Reset()
	r.collector.SetApdexT(r.assignment.ApdexT)

	// 7. 解析超时参数
	hbInterval := utils.ParseDurationDefault(r.assignment.HeartbeatInterval, 5*time.Second)
	tcpTimeout := utils.ParseDurationDefault(r.assignment.TCPTimeout, 60*time.Second)
	httpTimeout := utils.ParseDurationDefault(r.assignment.HTTPTimeout, 10*time.Second)

	// 8. 启动 gnet 网络引擎
	dialer := network.NewDialer(adp, hbInterval)
	if err := dialer.Start(); err != nil {
		return TaskFailed, fmt.Sprintf("启动网络引擎失败: %v", err)
	}
	defer dialer.Stop()

	// 9. 初始化 Lua 运行时池
	luaPool := script.NewRuntimePool(scriptsDir)
	if err := luaPool.PrecompileScripts([]string{scriptsDir}); err != nil {
		stresslog.Warn("[TASK] Lua 脚本预编译失败（非致命）", zap.Error(err))
	}

	// 10. 创建 Robot Manager
	accountPrefix := r.assignment.AccountPrefix
	if accountPrefix == "" {
		accountPrefix = "bot_"
	}
	mainService := r.assignment.MainService
	if mainService == "" {
		mainService = "logic"
	}

	mgrCfg := robot.ManagerConfig{
		AccountPrefix:  accountPrefix,
		StartNumber:    r.assignment.StartNumber,
		Count:          r.assignment.TotalBots,
		ConcurrentNum:  r.assignment.ConcurrentNum,
		AuthBaseURL:    r.assignment.AuthAddress,
		AuthExtra:      r.assignment.AuthExtra,
		Adapter:        adp,
		RequestTimeout: tcpTimeout,
		MainService:    mainService,
		HTTPTimeout:    httpTimeout,
	}
	mgr := robot.NewManager(mgrCfg, flow, factory, dialer, luaPool)

	// 11. 启动机器人
	if err := mgr.StartAll(); err != nil {
		return TaskFailed, fmt.Sprintf("启动机器人失败: %v", err)
	}

	stresslog.Info("[TASK] 任务执行中",
		zap.String("taskID", taskID),
		zap.Int("totalBots", r.assignment.TotalBots),
		zap.Int("startNumber", r.assignment.StartNumber))

	// 12. 等待 ctx 取消
	<-ctx.Done()

	// 13. 停止所有机器人
	mgr.StopAll()

	if ctx.Err() == context.Canceled {
		return TaskStopped, ""
	}
	return TaskCompleted, ""
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

	_, err = f.ReadFrom(resp.Body)
	return err
}

func loadTaskFlow(path string) (*engine.TaskFlow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取流程配置失败: %w", err)
	}

	flow := &engine.TaskFlow{}
	if err := json.Unmarshal(data, flow); err != nil {
		return nil, fmt.Errorf("解析流程配置失败: %w", err)
	}

	return flow, nil
}
