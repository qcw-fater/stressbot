// Package standalone 实现 stressbot 单机压测应用。
package standalone

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stressbot/internal/stresslog"
	"stressbot/monitor"
	"stressbot/robot"
	"stressbot/runner"
	"stressbot/state/shared"

	"go.uber.org/zap"
)

// Run 加载单机资源、启动机器人，并阻塞到运行结束或 ctx 被取消。
func Run(ctx context.Context, cfg *Config, paths Paths) error {
	if cfg == nil || cfg.Standalone == nil {
		return fmt.Errorf("单机配置不能为空")
	}
	standaloneConfig := cfg.Standalone

	stresslog.Info("[MAIN] 单机模式启动",
		zap.Int("botCount", standaloneConfig.Bot.TotalBots),
		zap.Int("concurrent", standaloneConfig.Bot.Concurrency),
		zap.String("flow", paths.Flow),
		zap.String("proto", paths.Proto),
		zap.String("scripts", paths.Scripts),
		zap.String("adapter", paths.Adapter))

	resources, err := runner.LoadResources(runner.ResourcePaths{
		Flow: paths.Flow, Proto: paths.Proto, Scripts: paths.Scripts, Adapter: paths.Adapter,
	})
	if err != nil {
		return fmt.Errorf("加载运行资源失败: %w", err)
	}
	defer resources.Close()
	if !resources.HasErrorsFile {
		stresslog.Warn("[MAIN] 未找到 errors.json 错误码表，错误码将不显示中文描述", zap.String("dir", paths.Adapter))
	}
	stresslog.Info("[MAIN] 运行资源已加载",
		zap.Int("connections", len(resources.CodecMap)),
		zap.Int("nodes", len(resources.Flow.Nodes)),
		zap.Int("actions", len(resources.Flow.Actions)),
		zap.Int("listens", len(resources.Flow.Listens)))

	if cfg.Monitor != nil {
		monitor.Init(*cfg.Monitor)
		stresslog.Info("[MAIN] 监控已启用",
			zap.Bool("httpEnabled", cfg.Monitor.HTTP != nil),
			zap.Int("apdexThresholdMs", cfg.Monitor.ApdexThresholdMs))
	}

	luaPool, err := runner.NewRuntimePool(paths.Scripts)
	if err != nil {
		stresslog.Warn("[MAIN] Lua 脚本预编译失败（非致命错误）", zap.Error(err))
	} else {
		stresslog.Info("[MAIN] Lua 脚本已预编译", zap.Int("count", len(luaPool.ListScripts())))
	}

	duration, err := parseDuration(standaloneConfig.Duration)
	if err != nil {
		return err
	}

	sharedStore, err := openSharedStore(cfg.Redis, paths.Scripts)
	if err != nil {
		return err
	}
	if sharedStore != nil {
		defer closeSharedStore(sharedStore)
	}

	managerConfig := robot.ManagerConfig{
		AccountPrefix:  standaloneConfig.Bot.AccountPrefix,
		StartNumber:    standaloneConfig.Bot.StartNumber,
		Count:          standaloneConfig.Bot.TotalBots,
		ConcurrentNum:  standaloneConfig.Bot.Concurrency,
		StateExtra:     standaloneConfig.StateExtra,
		CodecResolver:  resources.Resolver,
		RequestTimeout: 60 * time.Second,
		MainService:    standaloneConfig.Bot.MainService,
		HTTPTimeout:    10 * time.Second,
		RampUp:         standaloneConfig.RampUp,
		Duration:       duration,
		Shared:         sharedStore,
	}

	dialer, err := runner.StartDialer(resources, 5*time.Second)
	if err != nil {
		return fmt.Errorf("启动网络引擎失败: %w", err)
	}
	defer runner.StopDialer(dialer)

	manager := robot.NewManager(ctx, managerConfig, resources.Flow, resources.Factory, dialer, luaPool)
	if standaloneConfig.RampUp != nil && len(standaloneConfig.RampUp.Stages) > 0 {
		stresslog.Info("[MAIN] 渐进加压模式启动", zap.Int("stages", len(standaloneConfig.RampUp.Stages)))
		if err := manager.StartWithRampUp(); err != nil {
			manager.StopAll()
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				return nil
			}
			return fmt.Errorf("渐进加压启动机器人失败: %w", err)
		}
	} else if err := manager.StartAll(); err != nil {
		manager.StopAll()
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return nil
		}
		return fmt.Errorf("启动机器人失败: %w", err)
	}

	stopMonitoring := startMonitoring(cfg.Monitor)

	select {
	case <-ctx.Done():
		stresslog.Info("[MAIN] 收到退出请求，正在关闭...", zap.Error(ctx.Err()))
	case <-manager.Done():
		stresslog.Info("[MAIN] 运行时长已到，正在关闭...")
	}

	stopMonitoring()
	manager.StopAll()
	exportMetrics(cfg.Monitor)
	stresslog.Info("[MAIN] 已退出")
	return nil
}

func parseDuration(value string) (time.Duration, error) {
	if value == "" || value == "0" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("解析 standalone.duration 失败: %w", err)
	}
	return duration, nil
}

func openSharedStore(cfg *shared.RedisConfig, scriptsDir string) (shared.Store, error) {
	usesShare, err := detectShareUsage(scriptsDir)
	if err != nil {
		return nil, fmt.Errorf("检查脚本共享状态使用失败: %w", err)
	}
	if !usesShare {
		stresslog.Info("[MAIN] Redis 共享状态未启用（脚本未使用 share）")
		return nil, nil
	}
	if cfg == nil || !cfg.Enabled() {
		return nil, fmt.Errorf("流程脚本使用了共享状态 share，但未配置 Redis")
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		return nil, fmt.Errorf("共享状态配置无效: %w", err)
	}
	runID := fmt.Sprintf("standalone-%d-%d", time.Now().UnixMilli(), os.Getpid())
	store, err := shared.NewRedisStore(resolved, runID)
	if err != nil {
		return nil, fmt.Errorf("连接共享状态 Redis 失败: %w", err)
	}
	stresslog.Info("[MAIN] Redis 共享状态已启用",
		zap.String("addr", fmt.Sprintf("%s:%d", resolved.Host, resolved.Port)),
		zap.String("runId", runID))
	return store, nil
}

func closeSharedStore(store shared.Store) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.Cleanup(cleanupCtx); err != nil {
		stresslog.Error("[MAIN] 共享状态清理失败", zap.Error(err))
	} else {
		stresslog.Info("[MAIN] 共享状态已清理")
	}
	if err := store.Close(); err != nil {
		stresslog.Error("[MAIN] 关闭共享状态失败", zap.Error(err))
	}
}

func startMonitoring(cfg *monitor.CollectorConfig) func() {
	if cfg == nil {
		return func() {}
	}
	reportInterval := 5 * time.Second
	if cfg.ReportInterval != "" {
		if parsed, err := time.ParseDuration(cfg.ReportInterval); err == nil && parsed > 0 {
			reportInterval = parsed
		}
	}
	reporter := monitor.NewReporter(monitor.Global(), reportInterval)
	reporter.Start()

	var stopHTTP func()
	if cfg.HTTP != nil {
		monitor.RegisterHandlers(monitor.Global())
		var err error
		stopHTTP, err = monitor.StartHTTPServer(cfg.HTTP.Port)
		if err != nil {
			stresslog.Error("[MONITOR] 启动 HTTP 指标服务失败", zap.Error(err))
		}
	}
	return func() {
		reporter.Stop()
		if stopHTTP != nil {
			stopHTTP()
		}
	}
}

func exportMetrics(cfg *monitor.CollectorConfig) {
	if cfg == nil {
		return
	}
	if err := os.MkdirAll("metrics", 0o755); err != nil {
		stresslog.Error("[MONITOR] 创建指标目录失败", zap.Error(err))
		return
	}
	csvPath := fmt.Sprintf("metrics/metrics_%s.csv", time.Now().Format("2006_01_02_15_04_05"))
	if err := monitor.ExportCSV(monitor.Global(), csvPath); err != nil {
		stresslog.Error("[MONITOR] CSV 导出失败", zap.Error(err))
	} else {
		stresslog.Info("[MONITOR] CSV 已导出", zap.String("path", csvPath))
	}
}

func detectShareUsage(scriptsDir string) (bool, error) {
	found := false
	err := filepath.WalkDir(scriptsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if found {
			return fs.SkipAll
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lua") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if shared.UsesShare(string(data)) {
			found = true
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}
