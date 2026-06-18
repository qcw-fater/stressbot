package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/monitor"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/robot"
	"stressbot/script"
	"stressbot/sharedstate"
	"stressbot/utils"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// taskCleanupTimeout 任务清理（停止 gnet 引擎 + 关闭适配器）的超时时间。
// 部分机器人卡死时 gnet.Client.Stop() 可能长时间阻塞，需要超时保护。
const taskCleanupTimeout = 30 * time.Second

// RunResult 表示 TaskRunner 一次执行的最终结果。
type RunResult struct {
	Result        TaskResult
	ErrorMsg      string
	CleanupStatus robot.CleanupStatus
}

func runFailed(msg string) RunResult {
	return RunResult{Result: TaskFailed, ErrorMsg: msg, CleanupStatus: robot.UnknownCleanupStatus(robot.CleanupReasonStopWaitTimeout, "任务启动失败，未进入机器人清理阶段")}
}

// TaskRunner 管理单次压测任务的执行：拉配置、写目录、起 Manager、等完成。
type TaskRunner struct {
	assignment *TaskAssignment
	cfg        *ResolvedConfig
	cli        *AdminClient
	collector  *monitor.MetricsCollector
	httpCli    *http.Client
	workDir    string

	// OnStageReset reset 边界阶段段落上报回调，由调用方（Agent.executeTask）注入。
	// 在 resetBots() 完成后调用，用于上报刚结束段落的累计指标并重置采集器。
	// 参数为即将进入的配置阶段下标（0-based，>=1）。
	OnStageReset func(nextStageIdx int)
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
func (r *TaskRunner) Run(ctx context.Context) RunResult {
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
		return runFailed(fmt.Sprintf("创建临时目录失败: %v", err))
	}
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		return runFailed(fmt.Sprintf("创建脚本目录失败: %v", err))
	}

	stresslog.Info("[TASK] 临时目录已创建", zap.String("dir", r.workDir))

	// 2. 从 Admin 下载配置文件
	if r.assignment.ConfigURL != "" && len(r.assignment.ConfigFiles) > 0 {
		configURL := strings.TrimRight(r.assignment.ConfigURL, "/")
		for _, relPath := range r.assignment.ConfigFiles {
			url := configURL + "/" + relPath
			targetPath := filepath.Join(confDir, relPath)
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return runFailed(fmt.Sprintf("创建目录 %s 失败: %v", filepath.Dir(targetPath), err))
			}
			if err := r.downloadFile(ctx, url, targetPath); err != nil {
				return runFailed(fmt.Sprintf("下载 %s 失败: %v", relPath, err))
			}
			stresslog.Info("[TASK] 配置文件已下载", zap.String("path", relPath))
		}
		stresslog.Info("[TASK] 所有配置文件已下载", zap.Int("count", len(r.assignment.ConfigFiles)))
	} else {
		return runFailed("无配置文件可下载（configUrl 或 configFiles 为空）")
	}

	// 3. 加载协议适配器（优先使用任务下发的 codec.lua，回退到 Agent 本地配置）
	adapterScript := filepath.Join(confDir, "adapter", "codec.lua")
	if _, err := os.Stat(adapterScript); err != nil {
		adapterScript = r.cfg.AdapterScript
	}
	// 可选：加载错误码映射
	errorMapScript := filepath.Join(confDir, "adapter", "error.lua")
	if _, err := os.Stat(errorMapScript); err != nil {
		errorMapScript = ""
	}
	poolSize := adapter.SuggestedPoolSize()
	adp, err := adapter.NewLuaAdapter(poolSize, adapterScript, errorMapScript)
	if err != nil {
		stresslog.Error("[TASK] 加载适配器失败", zap.String("taskID", taskID), zap.Error(err))
		return runFailed(fmt.Sprintf("加载适配器失败: %v", err))
	}
	defer adp.Close()

	// T2-C1：构造 CodecResolver（dial/decode 侧 Go SchemaAdapter）。
	// 任务下发的 adapter 目录 confDir/adapter 含 *_codec.json + errors.json（T4.3 分发）。
	// 与上方 LuaAdapter 形成**双 codec 过渡态**：decode/dial → resolver；encode/Lua → adp。
	codecAdapterDir := filepath.Join(confDir, "adapter")
	codecMap, err := adapter.InferCodecMap(codecAdapterDir)
	if err != nil {
		stresslog.Error("[TASK] 推断 codec 映射失败", zap.String("taskID", taskID), zap.String("dir", codecAdapterDir), zap.Error(err))
		return runFailed(fmt.Sprintf("推断 codec 映射失败: %v", err))
	}
	resolver, err := adapter.LoadCodecResolver(codecAdapterDir, codecMap, "errors.json")
	if err != nil {
		stresslog.Error("[TASK] 加载 CodecResolver 失败", zap.String("taskID", taskID), zap.String("dir", codecAdapterDir), zap.Error(err))
		return runFailed(fmt.Sprintf("加载 CodecResolver 失败: %v", err))
	}
	stresslog.Info("[TASK] CodecResolver 已加载",
		zap.String("taskID", taskID),
		zap.Int("connections", len(codecMap)))

	// 4. 加载 .proto 文件
	loader := protox.NewLoader([]string{protoDir}, nil)
	files, err := loader.Load()
	if err != nil {
		stresslog.Error("[TASK] 加载 proto 文件失败", zap.String("taskID", taskID), zap.Error(err))
		return runFailed(fmt.Sprintf("加载 proto 文件失败: %v", err))
	}

	registry := protox.NewRegistry(files)
	factory := protox.NewFactory(registry)

	// 5. 加载流程配置
	flow, err := loadTaskFlow(filepath.Join(confDir, "flow", "flow.json"))
	if err != nil {
		stresslog.Error("[TASK] 加载流程配置失败", zap.String("taskID", taskID), zap.Error(err))
		return runFailed(fmt.Sprintf("加载流程配置失败: %v", err))
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
	hbInterval := utils.ParseDurationDefault(r.assignment.HeartbeatInterval, 5*time.Second, "heartbeatInterval")
	tcpTimeout := utils.ParseDurationDefault(r.assignment.TCPTimeout, 60*time.Second, "tcpTimeout")
	httpTimeout := utils.ParseDurationDefault(r.assignment.HTTPTimeout, 10*time.Second, "httpTimeout")

	// 8. 启动 gnet 网络引擎
	// Dialer 元信息源：T2-C1 起用 resolver 任一 Go SchemaAdapter（HeaderSize 全局一致，T1.6 同源）。
	dialer := network.NewDialer(adapter.PickMetaAdapter(resolver, codecMap), hbInterval)
	if err := dialer.Start(); err != nil {
		stresslog.Error("[TASK] 启动网络引擎失败", zap.String("taskID", taskID), zap.Error(err))
		return runFailed(fmt.Sprintf("启动网络引擎失败: %v", err))
	}
	defer stopDialerWithTimeout(dialer)

	// 9. 初始化 Lua 运行时池
	luaPool := script.NewRuntimePool(scriptsDir)
	if err := luaPool.PrecompileScripts([]string{scriptsDir}); err != nil {
		stresslog.Warn("[TASK] Lua 脚本预编译失败（非致命）", zap.Error(err))
	}

	// 9.5 创建共享状态后端（Admin 已检测脚本使用 share 且下发了 Redis 配置）。
	//     Agent 只负责 Close（断开连接）；统一 Cleanup 由 Admin 在任务终态时触发，
	//     避免多 Agent 共享同一 runId 时某个 Agent 先结束就删掉别人还在用的 key。
	var sharedStore sharedstate.Store
	if r.assignment.Shared != nil && r.assignment.Shared.Redis.Enabled() {
		resolved, rerr := r.assignment.Shared.Redis.Resolve()
		if rerr != nil {
			return runFailed(fmt.Sprintf("共享状态配置无效: %v", rerr))
		}
		store, serr := sharedstate.NewRedisStore(resolved, r.assignment.Shared.RunID)
		if serr != nil {
			return runFailed(fmt.Sprintf("连接共享状态(Redis)失败: %v", serr))
		}
		sharedStore = store
		defer func() { _ = sharedStore.Close() }()
		stresslog.Info("[TASK] 共享状态已启用",
			zap.String("taskID", taskID),
			zap.String("addr", resolved.Addr),
			zap.Int("db", resolved.DB),
			zap.String("runId", r.assignment.Shared.RunID))
	} else {
		stresslog.Info("[TASK] 共享状态未启用",
			zap.String("taskID", taskID))
	}

	// 10. 创建 Robot Manager
	accountPrefix := r.assignment.AccountPrefix
	if accountPrefix == "" {
		accountPrefix = "bot_"
	}
	mainService := r.assignment.MainService

	mgrCfg := robot.ManagerConfig{
		AccountPrefix:  accountPrefix,
		StartNumber:    r.assignment.StartNumber,
		Count:          r.assignment.TotalBots,
		ConcurrentNum:  r.assignment.ConcurrentNum,
		StateExtra:     r.assignment.StateExtra,
		Adapter:        adp,
		CodecResolver:  resolver,
		RequestTimeout: tcpTimeout,
		MainService:    mainService,
		HTTPTimeout:    httpTimeout,
		Shared:         sharedStore,
	}
	if r.assignment.RampUp != nil && len(r.assignment.RampUp.Stages) > 0 {
		mgrCfg.RampUp = &robot.RampUpConfig{}
		for _, s := range r.assignment.RampUp.Stages {
			mgrCfg.RampUp.Stages = append(mgrCfg.RampUp.Stages, robot.RampUpStage{
				Count:       s.Count,
				Concurrency: s.Concurrency,
				HoldSec:     s.HoldSec,
				Reset:       s.Reset,
			})
		}
	}
	mgr := robot.NewManager(mgrCfg, flow, factory, dialer, luaPool)
	mgr.OnStageReset = r.OnStageReset
	mgr.OnStageChange = func(current, total int) {
		r.collector.SetRampUpStage(current, total)
	}

	// 11. 启动机器人
	var startErr error
	if mgrCfg.RampUp != nil {
		startErr = mgr.StartWithRampUp()
	} else {
		startErr = mgr.StartAll()
	}
	if startErr != nil {
		// 渐进式加压在 ctx cancel 后会从 StartWithRampUp 返回 context.Canceled，
		// 这是"用户主动停止"而非"失败"，按 TaskStopped 上报，避免历史归档误判为失败。
		if ctx.Err() == context.Canceled || strings.Contains(startErr.Error(), context.Canceled.Error()) {
			stresslog.Info("[TASK] 启动阶段被取消", zap.String("taskID", taskID), zap.Error(startErr))
			cleanup := mgr.StopAll()
			return RunResult{Result: TaskStopped, CleanupStatus: cleanup}
		}
		return runFailed(fmt.Sprintf("启动机器人失败: %v", startErr))
	}
	stresslog.Info("[TASK] 任务执行中",
		zap.String("taskID", taskID),
		zap.Int("totalBots", r.assignment.TotalBots),
		zap.Int("startNumber", r.assignment.StartNumber))

	// 12. 等待 ctx 取消或运行时长到期
	select {
	case <-ctx.Done():
	case <-mgr.Done():
	}

	// 13. 停止所有机器人
	cleanup := mgr.StopAll()

	if ctx.Err() == context.Canceled {
		return RunResult{Result: TaskStopped, CleanupStatus: cleanup}
	}
	stresslog.Info("[TASK] 任务已完成", zap.String("taskID", taskID), zap.Int("totalBots", r.assignment.TotalBots))
	return RunResult{Result: TaskCompleted, CleanupStatus: cleanup}
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

// stopDialerWithTimeout 带超时地停止 gnet 引擎。
// 机器人卡死时 gnet.Client.Stop() 可能长时间阻塞（等待事件循环排空），
// 超时后强制返回，避免 Agent 因清理阻塞而无法接受新任务。
func stopDialerWithTimeout(d *network.Dialer) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := d.Stop(); err != nil {
			stresslog.Warn("[TASK] 停止网络引擎失败", zap.Error(err))
		}
	}()
	select {
	case <-done:
	case <-time.After(taskCleanupTimeout):
		stresslog.Warn("[TASK] 停止网络引擎超时，强制返回（资源由 OS 回收）",
			zap.Duration("timeout", taskCleanupTimeout))
	}
}
