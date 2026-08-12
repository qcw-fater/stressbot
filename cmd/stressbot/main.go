// Package main 是 stressbot 单机压测模式入口。
//
// 单机模式（standalone）在本进程内完成全部工作：加载配置 → codec → proto →
// 流程 → gnet 网络引擎 → Lua 运行时池 → Robot Manager → 批量启动机器人。
// 与 Agent 模式（cmd/agent）完全独立，各有专属配置文件（stressbot.toml / agent.toml）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/monitor"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/robot"
	configschema "stressbot/schema"
	"stressbot/script"
	"stressbot/sharedstate"
	"stressbot/utils"
	json "stressbot/utils/jsonx"
	stresslog "stressbot/utils/log"

	_ "net/http/pprof"

	"go.uber.org/zap"
)

// Version 编译时注入：-ldflags "-X main.Version=v1.0.0"
var Version = "dev"

// BotConfig 机器人批量参数。
type BotConfig struct {
	AccountPrefix string `toml:"accountPrefix"` // 账号名前缀（默认 bot_）
	StartNumber   int    `toml:"startNumber"`   // 起始编号（默认 1）
	TotalBots     int    `toml:"totalBots"`     // 机器人总数（默认 1）
	Concurrency   int    `toml:"concurrency"`   // 并发启动数（0=不限）
	MainService   string `toml:"mainService"`   // 主服务名（必填，TCP 连接标识）
}

// StandaloneConfig 单机模式专用配置（对应 stressbot.toml [standalone] 段）。
type StandaloneConfig struct {
	Bot        BotConfig            `toml:"bot"`        // 机器人批量参数
	StateExtra map[string]string    `toml:"stateExtra"` // 初始状态注入（键值对）
	Duration   string               `toml:"duration"`   // 运行时长（"10m"/"1h"），"0"或空=一直运行
	RampUp     *robot.RampUpConfig  `toml:"rampUp"`     // 渐进加压（可选，配置则分阶段创建）
}

// NetworkConfig gnet 网络引擎调优参数（对应 [network] 段，全部可选）。
type NetworkConfig struct {
	ReadBufferCap      int `toml:"readBufferCap"`      // gnet 读缓冲区容量（字节，默认 32768）
	WriteBufferCap     int `toml:"writeBufferCap"`     // gnet 写缓冲区容量（字节，默认 32768）
	NumEventLoop       int `toml:"numEventLoop"`       // 事件循环数（0=CPU 核数）
	MaxConcurrentDials int `toml:"maxConcurrentDials"` // 同时阻塞的拨号数上限（默认 512）
	MaxBodyLen         int `toml:"maxBodyLen"`         // 单包最大 body 字节数（默认 16MB）
}

// Config 单机模式配置结构（对应 stressbot.toml）。
type Config struct {
	Log        *stresslog.Config         `toml:"log"`        // 日志
	Monitor    *monitor.CollectorConfig  `toml:"monitor"`    // 监控（nil=不启用）
	Pprof      *utils.PprofConfig        `toml:"pprof"`      // pprof 调试（nil=不启用）
	Standalone *StandaloneConfig         `toml:"standalone"` // 单机压测参数
	Redis      *sharedstate.RedisConfig  `toml:"redis"`      // Redis 共享状态（nil=未配置）
	Network    NetworkConfig             `toml:"network"`    // gnet 网络引擎调优
	Daemon     bool                      `toml:"daemon"`     // 守护进程模式（仅 Linux）
}

// Defaults 返回填充了默认值的单机模式配置。
func Defaults() Config {
	return Config{
		Standalone: &StandaloneConfig{
			Bot: BotConfig{
				AccountPrefix: "bot_",
				StartNumber:   1,
				TotalBots:     1,
			},
		},
	}
}

func main() {
	var closeLog func() error
	exitProcess := func(code int) {
		if closeLog != nil {
			_ = closeLog()
		}
		os.Exit(code)
	}

	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(os.Stderr, "[STRESSBOT] 顶层 panic: %v\n%s\n", rec, debug.Stack())
			if stresslog.GetLogger() != nil {
				stresslog.Error("[STRESSBOT] 顶层 panic",
					zap.Any("panic", rec),
					zap.String("stack", string(debug.Stack())))
			}
			if closeLog != nil {
				_ = closeLog()
			}
			os.Exit(2)
		}
		if closeLog != nil {
			if err := closeLog(); err != nil {
				fmt.Fprintf(os.Stderr, "关闭日志失败: %v\n", err)
			}
		}
	}()

	configPath := flag.String("config", "conf/stressbot.toml", "配置文件路径")
	flowPath := flag.String("flow", "", "流程配置文件路径（默认 <conf>/flow/flow.json）")
	protoDir := flag.String("proto", "", "proto 目录路径（默认 <conf>/proto）")
	scriptsDir := flag.String("scripts", "", "Lua 脚本目录路径（默认 <conf>/scripts）")
	adapterDir := flag.String("adapter", "", "适配器目录路径（默认 <conf>/adapter，含 *_codec.json 与 errors.json）")
	daemonFlag := flag.Bool("d", false, "以守护进程模式运行")
	flag.Parse()

	if *daemonFlag {
		utils.Daemon("-d")
		return
	}

	// 推导 conf 根目录：config 文件所在目录的绝对路径
	configAbs, err := filepath.Abs(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析配置路径失败: %v\n", err)
		exitProcess(1)
	}
	confDir := filepath.Dir(configAbs)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		exitProcess(1)
	}

	if cfg.Daemon && os.Getppid() != 1 {
		utils.Daemon()
		return
	}

	closeLog = stresslog.InitLog("log/stressbot.log", "stressbot", cfg.Log, "")

	botCount := 0
	conc := 0
	if cfg.Standalone != nil {
		botCount = cfg.Standalone.Bot.TotalBots
		conc = cfg.Standalone.Bot.Concurrency
	}
	stresslog.Info("[MAIN] 单机模式启动",
		zap.Int("botCount", botCount),
		zap.Int("concurrent", conc))
	paths := resolveStandalonePaths(confDir, *flowPath, *protoDir, *scriptsDir, *adapterDir)
	runStandalone(cfg, paths)
}

func loadConfig(path string) (*Config, error) {
	cfg, err := utils.LoadTOML(path, Defaults())
	if err != nil {
		return nil, err
	}

	if cfg.Standalone == nil {
		cfg.Standalone = &StandaloneConfig{}
	}
	s := cfg.Standalone
	if s.Bot.StartNumber == 0 {
		s.Bot.StartNumber = 1
	}
	if s.Bot.TotalBots == 0 {
		s.Bot.TotalBots = 1
	}
	if s.Bot.AccountPrefix == "" {
		s.Bot.AccountPrefix = "bot_"
	}
	if s.Bot.MainService == "" {
		return nil, fmt.Errorf("standalone.bot.mainService is required")
	}

	// 校验渐进加压：各阶段 count 之和应等于 totalBots
	if s.RampUp != nil && len(s.RampUp.Stages) > 0 {
		sum := 0
		for _, stage := range s.RampUp.Stages {
			sum += stage.Count
		}
		if sum != s.Bot.TotalBots {
			return nil, fmt.Errorf("standalone.rampUp.stages 各阶段 count 之和 (%d) 不等于 standalone.bot.totalBots (%d)", sum, s.Bot.TotalBots)
		}
	}

	return cfg, nil
}

// ── 单机模式运行 ──────────────────────────────────────────

func runStandalone(cfg *Config, paths standalonePaths) {
	s := cfg.Standalone

	stresslog.Info("[MAIN] 单机模式资源路径",
		zap.String("flow", paths.Flow),
		zap.String("proto", paths.Proto),
		zap.String("scripts", paths.Scripts),
		zap.String("adapter", paths.Adapter))

	// 构造生产 CodecResolver
	codecMap, err := adapter.InferCodecMap(paths.Adapter)
	if err != nil {
		stresslog.Fatal("推断 codec 映射失败", zap.String("dir", paths.Adapter), zap.Error(err))
	}
	errorsFile := "errors.json"
	if _, statErr := os.Stat(filepath.Join(paths.Adapter, errorsFile)); statErr != nil {
		stresslog.Warn("[MAIN] 未找到 errors.json 错误码表，跳过加载，错误码将不显示中文描述",
			zap.String("dir", paths.Adapter))
		errorsFile = ""
	}
	resolver, err := adapter.LoadCodecResolver(paths.Adapter, codecMap, errorsFile)
	if err != nil {
		stresslog.Fatal("加载 CodecResolver 失败", zap.String("dir", paths.Adapter), zap.Error(err))
	}
	stresslog.Info("[MAIN] CodecResolver 已加载",
		zap.Int("connections", len(codecMap)),
		zap.Int("headerSize", adapter.PickMetaAdapter(resolver, codecMap).HeaderSize()))

	// 加载 .proto 文件
	loader := protox.NewLoader([]string{paths.Proto}, nil)
	files, err := loader.Load()
	if err != nil {
		stresslog.Fatal("加载 proto 文件失败", zap.Error(err))
	}

	registry := protox.NewRegistry(files)
	factory := protox.NewFactory(registry)

	// 加载流程配置
	flow, err := loadFlow(paths.Flow)
	if err != nil {
		stresslog.Fatal("加载流程配置失败", zap.Error(err))
	}

	stresslog.Info("[MAIN] 流程配置已加载",
		zap.Int("nodes", len(flow.Nodes)),
		zap.Int("actions", len(flow.Actions)),
		zap.Int("listens", len(flow.Listens)))

	for name, action := range flow.Actions {
		action.Name = name
	}

	// 初始化监控（nil = 不启用）
	if cfg.Monitor != nil {
		monitor.Init(*cfg.Monitor)
		httpEnabled := cfg.Monitor.HTTP != nil
		stresslog.Info("[MAIN] 监控已启用",
			zap.Bool("httpEnabled", httpEnabled),
			zap.Int("apdexThresholdMs", cfg.Monitor.ApdexThresholdMs))
	}

	// 初始化 Lua 运行时池
	luaPool := script.NewRuntimePool(paths.Scripts)
	if err := luaPool.PrecompileScripts([]string{paths.Scripts}); err != nil {
		stresslog.Warn("[MAIN] Lua 脚本预编译失败（非致命错误）", zap.Error(err))
	} else {
		stresslog.Info("[MAIN] Lua 脚本已预编译", zap.Int("count", len(luaPool.ListScripts())))
	}

	var duration time.Duration
	if s.Duration != "" && s.Duration != "0" {
		d, err := time.ParseDuration(s.Duration)
		if err != nil {
			stresslog.Fatal("解析 duration 失败", zap.String("duration", s.Duration), zap.Error(err))
		}
		duration = d
	}

	// 自动检测共享状态
	var sharedStore sharedstate.Store
	if detectShareUsage(paths.Scripts) {
		if cfg.Redis == nil || !cfg.Redis.Enabled() {
			stresslog.Fatal("流程脚本使用了共享状态(share)，但未配置 Redis（redis.host 为空），无法启动")
		}
		resolved, rerr := cfg.Redis.Resolve()
		if rerr != nil {
			stresslog.Fatal("共享状态配置无效", zap.Error(rerr))
		}
		runID := fmt.Sprintf("standalone-%d-%d", time.Now().UnixMilli(), os.Getpid())
		store, serr := sharedstate.NewRedisStore(resolved, runID)
		if serr != nil {
			stresslog.Fatal("连接共享状态(Redis)失败", zap.Error(serr))
		}
		sharedStore = store
		stresslog.Info("[MAIN] Redis 共享状态已启用",
			zap.String("addr", fmt.Sprintf("%s:%d", resolved.Host, resolved.Port)), zap.String("runId", runID))
	} else {
		stresslog.Info("[MAIN] Redis 共享状态未启用（脚本未使用 share）")
	}

	mgrCfg := robot.ManagerConfig{
		AccountPrefix:  s.Bot.AccountPrefix,
		StartNumber:    s.Bot.StartNumber,
		Count:          s.Bot.TotalBots,
		ConcurrentNum:  s.Bot.Concurrency,
		StateExtra:     s.StateExtra,
		CodecResolver:  resolver,
		RequestTimeout: 60 * time.Second,
		MainService:    s.Bot.MainService,
		HTTPTimeout:    10 * time.Second,
		RampUp:         s.RampUp,
		Duration:       duration,
		Shared:         sharedStore,
	}

	dialer := network.NewDialer(adapter.PickMetaAdapter(resolver, codecMap), 5*time.Second)
	if err := dialer.Start(); err != nil {
		stresslog.Fatal("启动网络引擎失败", zap.Error(err))
	}
	defer dialer.Stop()

	mgr := robot.NewManager(context.Background(), mgrCfg, flow, factory, dialer, luaPool)

	// 渐进加压或一次性启动
	if s.RampUp != nil && len(s.RampUp.Stages) > 0 {
		stresslog.Info("[MAIN] 渐进加压模式启动", zap.Int("stages", len(s.RampUp.Stages)))
		if err := mgr.StartWithRampUp(); err != nil {
			stresslog.Fatal("渐进加压启动机器人失败", zap.Error(err))
		}
	} else {
		if err := mgr.StartAll(); err != nil {
			stresslog.Fatal("启动机器人失败", zap.Error(err))
		}
	}

	// 启动监控 Reporter 和 HTTP
	var reporter *monitor.Reporter
	var stopMonitorHTTP func()
	if cfg.Monitor != nil {
		reportInterval := 5 * time.Second
		if cfg.Monitor.ReportInterval != "" {
			if d, err := time.ParseDuration(cfg.Monitor.ReportInterval); err == nil && d > 0 {
				reportInterval = d
			}
		}
		reporter = monitor.NewReporter(monitor.Global(), reportInterval)
		reporter.Start()

		if cfg.Monitor.HTTP != nil {
			monitor.RegisterHandlers(monitor.Global())
			stop, err := monitor.StartHTTPServer(cfg.Monitor.HTTP.Port)
			if err != nil {
				stresslog.Error("[MONITOR] 启动 HTTP 指标服务失败", zap.Error(err))
			} else {
				stopMonitorHTTP = stop
			}
		}
	}

	// pprof 调试服务
	var stopPprof func()
	if cfg.Pprof != nil {
		pprofPort := cfg.Pprof.Port
		if pprofPort <= 0 {
			pprofPort = 6060
		}
		stopPprof = utils.StartPprofServer(pprofPort)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		stresslog.Info("[MAIN] 收到退出信号，正在关闭...")
	case <-mgr.Done():
		stresslog.Info("[MAIN] 运行时长已到，正在关闭...")
	}

	if reporter != nil {
		reporter.Stop()
	}
	if stopMonitorHTTP != nil {
		stopMonitorHTTP()
	}

	mgr.StopAll()

	if sharedStore != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := sharedStore.Cleanup(cleanupCtx); err != nil {
			stresslog.Error("[MAIN] 共享状态清理失败", zap.Error(err))
		} else {
			stresslog.Info("[MAIN] 共享状态已清理")
		}
		cancel()
		_ = sharedStore.Close()
	}

	if cfg.Monitor != nil {
		os.MkdirAll("metrics", 0o755)
		csvPath := fmt.Sprintf("metrics/metrics_%s.csv", time.Now().Format("2006_01_02_15_04_05"))
		if err := monitor.ExportCSV(monitor.Global(), csvPath); err != nil {
			stresslog.Error("[MONITOR] CSV 导出失败", zap.Error(err))
		} else {
			stresslog.Info("[MONITOR] CSV 已导出", zap.String("path", csvPath))
		}
	}

	if stopPprof != nil {
		stopPprof()
	}
	utils.GetWorkPool().Shutdown()
	stresslog.Info("[MAIN] 已退出")
}

// detectShareUsage 递归扫描 scripts 目录下的所有 .lua 文件，判断是否有脚本使用了 share 模块。
func detectShareUsage(scriptsDir string) bool {
	found := false
	_ = filepath.WalkDir(scriptsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".lua") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if sharedstate.UsesShare(string(data)) {
			found = true
		}
		return nil
	})
	return found
}

// ── 资源路径解析 ──────────────────────────────────────────

// standalonePaths 单机模式解析后的资源路径。
type standalonePaths struct {
	Flow    string
	Proto   string
	Scripts string
	Adapter string
}

func resolveStandalonePaths(confDir, flow, proto, scripts, adapter string) standalonePaths {
	return standalonePaths{
		Flow:    resolvePath(flow, filepath.Join(confDir, "flow", "flow.json")),
		Proto:   resolvePath(proto, filepath.Join(confDir, "proto")),
		Scripts: resolvePath(scripts, filepath.Join(confDir, "scripts")),
		Adapter: resolvePath(adapter, filepath.Join(confDir, "adapter")),
	}
}

func resolvePath(flagVal, defaultPath string) string {
	if flagVal == "" {
		return defaultPath
	}
	abs, err := filepath.Abs(flagVal)
	if err != nil {
		return flagVal
	}
	return abs
}

func loadFlow(path string) (*engine.TaskFlow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取流程配置失败: %w", err)
	}
	if err := configschema.ValidateFlow(data); err != nil {
		return nil, fmt.Errorf("校验流程配置结构失败: %w", err)
	}

	flow := &engine.TaskFlow{}
	if err := json.Unmarshal(data, flow); err != nil {
		return nil, fmt.Errorf("解析流程配置失败: %w", err)
	}

	if err := engine.PrepareTaskFlow(flow); err != nil {
		return nil, fmt.Errorf("校验流程配置失败: %w", err)
	}

	return flow, nil
}
