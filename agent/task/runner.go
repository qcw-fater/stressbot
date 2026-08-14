package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stressbot/config"
	"stressbot/internal/stresslog"
	"stressbot/monitor"
	"stressbot/robot"
	"stressbot/runner"
	"stressbot/state/shared"

	"go.uber.org/zap"
)

// taskCleanupTimeout 任务清理（停止 gnet 引擎 + 关闭适配器）的超时时间。
// 部分机器人卡死时 gnet.Client.Stop() 可能长时间阻塞，需要超时保护。
// RunResult 表示 TaskRunner 一次执行的最终结果。
type RunResult struct {
	Result        TaskResult
	ErrorMsg      string
	CleanupStatus robot.CleanupStatus
}

func runFailed(msg string) RunResult {
	return RunResult{Result: TaskFailed, ErrorMsg: msg, CleanupStatus: robot.UnknownCleanupStatus(robot.CleanupReasonStopWaitTimeout, "任务启动失败，未进入机器人清理阶段")}
}

type robotStopper interface {
	StopAll() robot.CleanupStatus
}

func finishManagerStartFailure(ctx context.Context, stopper robotStopper, startErr error) RunResult {
	cleanup := stopper.StopAll()
	if ctx.Err() == context.Canceled || strings.Contains(startErr.Error(), context.Canceled.Error()) {
		return RunResult{Result: TaskStopped, CleanupStatus: cleanup}
	}
	return RunResult{
		Result:        TaskFailed,
		ErrorMsg:      fmt.Sprintf("启动机器人失败: %v", startErr),
		CleanupStatus: cleanup,
	}
}

// TaskRunner 管理单次压测任务的执行：拉配置、写目录、起 Manager、等完成。
type TaskRunner struct {
	assignment *TaskAssignment
	collector  *monitor.MetricsCollector
	workDir    string

	// OnStageReset reset 边界阶段段落上报回调，由调用方（Agent.executeTask）注入。
	// 在 resetBots() 完成后调用，用于上报刚结束段落的累计指标并重置采集器。
	// 参数为即将进入的配置阶段下标（0-based，>=1）。
	OnStageReset func(nextStageIdx int)
}

// NewTaskRunner 创建任务执行器。
func NewTaskRunner(assignment *TaskAssignment, collector *monitor.MetricsCollector) *TaskRunner {
	return &TaskRunner{
		assignment: assignment,
		collector:  collector,
	}
}

// Run 执行任务。阻塞直到任务完成或 ctx 被取消。
func (r *TaskRunner) Run(ctx context.Context) RunResult {
	taskID := r.assignment.TaskID
	if err := r.assignment.Validate(); err != nil {
		return runFailed(err.Error())
	}

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

	// 1. 资源包已经由 gRPC 命令执行器下载、校验并原子发布。
	confDir := r.assignment.BundleDir
	if confDir == "" {
		return runFailed("任务资源包目录为空")
	}
	protoDir := filepath.Join(confDir, "proto")
	scriptsDir := filepath.Join(confDir, "scripts")
	stresslog.Info("[TASK] 使用已校验资源包", zap.String("dir", confDir))

	codecAdapterDir := filepath.Join(confDir, "adapter")
	resources, err := runner.LoadResources(runner.ResourcePaths{
		Flow:    filepath.Join(confDir, "flow", "flow.json"),
		Proto:   protoDir,
		Scripts: scriptsDir,
		Adapter: codecAdapterDir,
	})
	if err != nil {
		stresslog.Error("[TASK] 加载运行资源失败", zap.String("taskID", taskID), zap.Error(err))
		return runFailed(err.Error())
	}
	// 任务级 Factory 必须在任务结束时释放。此 defer 早于 dialer 停止的 defer，
	// LIFO 保证网络 pump 停止后才清空缓存和调试端点注册。
	defer resources.Close()
	if !resources.HasErrorsFile {
		stresslog.Warn("[TASK] 未下发 errors.json 错误码表，跳过加载，错误码将不显示中文描述",
			zap.String("taskID", taskID), zap.String("dir", codecAdapterDir))
	}
	stresslog.Info("[TASK] CodecResolver 已加载",
		zap.String("taskID", taskID),
		zap.Int("connections", len(resources.CodecMap)))

	stresslog.Info("[TASK] 流程配置已加载",
		zap.String("taskID", taskID),
		zap.Int("nodes", len(resources.Flow.Nodes)),
		zap.Int("actions", len(resources.Flow.Actions)))

	// 6. 重置监控
	//
	// **不能**调用 monitor.Init —— 那会新建一个 collector 替换 global，
	// 而 a.collector / StressReporter.src 持有的还是旧引用，导致
	// Robot 业务通过 monitor.Global() 写入新 collector，reporter 上报永远是空。
	// 任务级仅需 Reset + SetApdexT 即可，全局单例保持不变。
	r.collector.Reset()
	r.collector.SetApdexT(r.assignment.ApdexT)

	// 7. 解析超时参数
	hbInterval := config.ParseDurationDefault(r.assignment.HeartbeatInterval, 5*time.Second, "heartbeatInterval")
	tcpTimeout := config.ParseDurationDefault(r.assignment.TCPTimeout, 60*time.Second, "tcpTimeout")
	httpTimeout := config.ParseDurationDefault(r.assignment.HTTPTimeout, 10*time.Second, "httpTimeout")

	// 8. 启动 gnet 网络引擎
	// Dialer 元信息源：resolver 任一 Go SchemaAdapter（HeaderSize 全局一致，T1.6 同源）。
	dialer, err := runner.StartDialer(resources, hbInterval)
	if err != nil {
		stresslog.Error("[TASK] 启动网络引擎失败", zap.String("taskID", taskID), zap.Error(err))
		return runFailed(fmt.Sprintf("启动网络引擎失败: %v", err))
	}
	defer runner.StopDialer(dialer)

	// 9. 初始化 Lua 运行时池
	luaPool, err := runner.NewRuntimePool(scriptsDir)
	if err != nil {
		stresslog.Warn("[TASK] Lua 脚本预编译失败（非致命）", zap.Error(err))
	}

	// 9.5 创建共享状态后端（Admin 已检测脚本使用 share 且下发了 Redis 配置）。
	//     Agent 只负责 Close（断开连接）；统一 Cleanup 由 Admin 在任务终态时触发，
	//     避免多 Agent 共享同一 runId 时某个 Agent 先结束就删掉别人还在用的 key。
	var sharedStore shared.Store
	if r.assignment.Shared == nil {
		stresslog.Info("[TASK] 共享状态未启用：任务脚本未使用 share",
			zap.String("taskID", taskID))
	} else if !r.assignment.Shared.Redis.Enabled() {
		stresslog.Warn("[TASK] 共享状态未启用：任务分配缺少有效 Redis 配置",
			zap.String("taskID", taskID))
	} else {
		resolved, rerr := r.assignment.Shared.Redis.Resolve()
		if rerr != nil {
			return runFailed(fmt.Sprintf("Redis 共享状态配置无效: %v", rerr))
		}
		store, serr := shared.NewRedisStore(resolved, r.assignment.Shared.RunID)
		if serr != nil {
			return runFailed(fmt.Sprintf("连接 Redis 共享状态失败: %v", serr))
		}
		sharedStore = store
		defer func() { _ = sharedStore.Close() }()
		stresslog.Info("[TASK] Redis 共享状态已启用",
			zap.String("taskID", taskID),
			zap.String("addr", fmt.Sprintf("%s:%d", resolved.Host, resolved.Port)),
			zap.String("runId", r.assignment.Shared.RunID))
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
		StartIndex:     *r.assignment.StartIndex,
		Count:          r.assignment.TotalBots,
		ConcurrentNum:  r.assignment.ConcurrentNum,
		StateExtra:     r.assignment.StateExtra,
		CodecResolver:  resources.Resolver,
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
	// 传入任务级 ctx：Manager 及其 Robot 的生命周期上下文由它派生，任务取消（含 ramp-up 创建阶段
	// 进行中）沿 task → manager → robot 链立即传播，StartWithRampUp 能尽快返回 context.Canceled。
	mgr := robot.NewManager(ctx, mgrCfg, resources.Flow, resources.Factory, dialer, luaPool)
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
		result := finishManagerStartFailure(ctx, mgr, startErr)
		if result.Result == TaskStopped {
			// 渐进式加压在 ctx cancel 后会从 StartWithRampUp 返回 context.Canceled，
			// 这是"用户主动停止"而非"失败"，按 TaskStopped 上报，避免历史归档误判为失败。
			stresslog.Info("[TASK] 启动阶段被取消", zap.String("taskID", taskID), zap.Error(startErr))
		} else {
			stresslog.Error("[TASK] 启动机器人失败，已停止已创建机器人",
				zap.String("taskID", taskID),
				zap.String("cleanup", string(result.CleanupStatus.Status)),
				zap.Error(startErr))
		}
		return result
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
