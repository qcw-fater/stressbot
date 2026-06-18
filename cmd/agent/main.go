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
	"stressbot/agent"
	"stressbot/engine"
	"stressbot/logview"
	"stressbot/monitor"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/robot"
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

// StandaloneConfig 单机模式专用配置。
type StandaloneConfig struct {
	// Bot 机器人批量参数。
	Bot struct {
		AccountPrefix string `json:"accountPrefix"` // 账号名前缀（默认 bot_）
		StartNumber   int    `json:"startNumber"`   // 起始编号（默认 1）
		Count         int    `json:"count"`         // 机器人总数（默认 1）
		ConcurrentNum int    `json:"concurrentNum"` // 并发启动数（0=不限）
		MainService   string `json:"mainService"`   // 主服务名（必填，TCP 连接标识）
	} `json:"bot"`

	// StateExtra 初始状态额外键值对，注入每个 Robot 的 state。
	StateExtra map[string]string `json:"stateExtra"`

	// Duration 运行时长（如 "10m"、"1h"），0 = 一直运行直到手动停止。
	Duration string `json:"duration"`
}

// PprofConfig pprof 调试服务配置（standalone / agent / admin 共用）。
type PprofConfig struct {
	Enabled bool `json:"enabled"` // 是否启用 pprof（默认 false）
	Port    int  `json:"port"`    // pprof 监听端口（默认 6060）
}

// Config 全局配置结构。
type Config struct {
	Log        *stresslog.Config       `json:"log"`
	Monitor    monitor.CollectorConfig `json:"monitor"`
	Pprof      PprofConfig             `json:"pprof"`
	Standalone *StandaloneConfig       `json:"standalone"`
	Agent      agent.Config            `json:"agent"`
	Shared     *sharedstate.Config     `json:"shared"` // 共享状态（Redis）配置，单机/Agent 共用
	Daemon     bool                    `json:"daemon"` // 以守护进程模式运行（仅 Linux）
}

func main() {
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(os.Stderr, "[AGENT] 顶层 panic: %v\n%s\n", rec, debug.Stack())
			stresslog.Error("[AGENT] 顶层 panic",
				zap.Any("panic", rec),
				zap.String("stack", string(debug.Stack())))
			os.Exit(2)
		}
	}()

	configPath := flag.String("config", "conf/config.json", "配置文件路径")
	// 单机模式资源路径覆盖（空值回退到 <conf> 下默认相对路径；Agent 模式不使用）
	flowPath := flag.String("flow", "", "单机模式流程配置文件路径（默认 <conf>/flow/flow.json）")
	protoDir := flag.String("proto", "", "单机模式 proto 目录路径（默认 <conf>/proto）")
	scriptsDir := flag.String("scripts", "", "单机模式 Lua 脚本目录路径（默认 <conf>/scripts）")
	adapterDir := flag.String("adapter", "", "单机模式协议适配器目录路径（默认 <conf>/adapter，含 codec.lua 与可选 error.lua）")
	daemonFlag := flag.Bool("d", false, "以守护进程模式运行")
	flag.Parse()

	// -d 模式：fork 子进程后父进程退出
	if *daemonFlag {
		utils.Daemon("-d")
		return
	}

	// 推导 conf 根目录：config 文件所在目录的绝对路径
	configAbs, err := filepath.Abs(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析配置路径失败: %v\n", err)
		os.Exit(1)
	}
	confDir := filepath.Dir(configAbs)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 配置中启用守护进程且当前不是守护进程子进程
	if cfg.Daemon && os.Getppid() != 1 {
		utils.Daemon()
		return
	}

	// 初始化日志（路径按模式自动选择）
	logPath := "log/stressbot.log"
	logTag := "stressbot"
	if cfg.Agent.Enabled {
		logPath = "log/agent.log"
		logTag = "agent"
	}
	stresslog.InitLog(logPath, logTag, cfg.Log, "")
	newLogger := logview.AttachRingBuffer(stresslog.GetLogger(), 50000, zap.String("SR", logTag))
	stresslog.ReplaceLogger(newLogger)

	if cfg.Agent.Enabled {
		stresslog.Info("[MAIN] Agent 模式启动", zap.String("adminAddr", cfg.Agent.AdminAddr))
		runAgentMode(cfg)
	} else {
		botCount := 0
		conc := 0
		if cfg.Standalone != nil {
			botCount = cfg.Standalone.Bot.Count
			conc = cfg.Standalone.Bot.ConcurrentNum
		}
		stresslog.Info("[MAIN] 单机模式启动",
			zap.Int("botCount", botCount),
			zap.Int("concurrent", conc))
		paths := resolveStandalonePaths(confDir, *flowPath, *protoDir, *scriptsDir, *adapterDir)
		runStandalone(cfg, paths)
	}
}

// ── Agent 模式 ──────────────────────────────────────────

func runAgentMode(cfg *Config) {
	// pprof 调试服务（独立端口，不依赖 monitor）
	var stopPprof func()
	if cfg.Pprof.Enabled {
		pprofPort := cfg.Pprof.Port
		if pprofPort <= 0 {
			pprofPort = 6060
		}
		stopPprof = utils.StartPprofServer(pprofPort)
	}

	resolved, err := cfg.Agent.Resolve()
	if err != nil {
		stresslog.Fatal("Agent 配置校验失败", zap.Error(err))
	}

	monitor.Init(monitor.CollectorConfig{
		Enabled:      true,
		ApdexT:       cfg.Monitor.ApdexT,
		TimingDetail: cfg.Monitor.TimingDetail,
	})

	a, err := agent.New(resolved, monitor.Global())
	if err != nil {
		stresslog.Fatal("创建 Agent 失败", zap.Error(err))
	}

	if err := a.Run(); err != nil {
		stresslog.Fatal("Agent 运行失败", zap.Error(err))
	}
	if stopPprof != nil {
		stopPprof()
	}
	utils.GetWorkPool().Shutdown()
}

// ── 单机模式 ──────────────────────────────────────────────

func runStandalone(cfg *Config, paths standalonePaths) {
	s := cfg.Standalone

	stresslog.Info("[MAIN] 单机模式资源路径",
		zap.String("flow", paths.Flow),
		zap.String("proto", paths.Proto),
		zap.String("scripts", paths.Scripts),
		zap.String("adapter", paths.Adapter))

	// 加载协议适配器
	poolSize := adapter.SuggestedPoolSize()
	errorMapPath := filepath.Join(paths.Adapter, "error.lua")
	if _, err := os.Stat(errorMapPath); err != nil {
		errorMapPath = ""
	}
	adp, err := adapter.NewLuaAdapter(poolSize, filepath.Join(paths.Adapter, "codec.lua"), errorMapPath)
	if err != nil {
		stresslog.Fatal("加载适配器失败", zap.Error(err))
	}
	stresslog.Info("[MAIN] 适配器已初始化", zap.Int("headerSize", adp.HeaderSize()))

	// T2-C1：构造 CodecResolver（dial/decode 侧 Go SchemaAdapter）。
	// 扫 paths.Adapter 下 *_codec.json 推断「server 串 → 文件名」映射，再 LoadCodecResolver 编译。
	// 与上方 LuaAdapter 形成**双 codec 过渡态**：decode/dial → resolver（Go，无 luaMu）；
	// encode/心跳/listen/Lua → adp（Lua RobotAdapter，→ 2-C2/2-C3 切换并删除）。
	codecMap, err := adapter.InferCodecMap(paths.Adapter)
	if err != nil {
		stresslog.Fatal("推断 codec 映射失败", zap.String("dir", paths.Adapter), zap.Error(err))
	}
	resolver, err := adapter.LoadCodecResolver(paths.Adapter, codecMap, "errors.json")
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

	// 回填 ActionDef.Name
	for name, action := range flow.Actions {
		action.Name = name
	}

	// 初始化监控
	monitor.Init(cfg.Monitor)
	if cfg.Monitor.Enabled {
		stresslog.Info("[MAIN] 监控已启用",
			zap.Bool("httpEnabled", cfg.Monitor.HTTPEnabled),
			zap.Int("apdexT", cfg.Monitor.ApdexT))
	}

	// 初始化 Lua 运行时池
	luaPool := script.NewRuntimePool(paths.Scripts)
	if err := luaPool.PrecompileScripts([]string{paths.Scripts}); err != nil {
		stresslog.Warn("[MAIN] Lua 脚本预编译失败（非致命错误）", zap.Error(err))
	} else {
		stresslog.Info("[MAIN] Lua 脚本已预编译", zap.Int("count", len(luaPool.ListScripts())))
	}

	var duration time.Duration
	if s.Duration != "" {
		d, err := time.ParseDuration(s.Duration)
		if err != nil {
			stresslog.Fatal("解析 duration 失败", zap.String("duration", s.Duration), zap.Error(err))
		}
		duration = d
	}

	// 自动检测共享状态：脚本是否使用 require("share")。
	// 使用但未配置 Redis → 启动前直接失败（与 Admin 行为一致），避免运行后 share.* 全部报错。
	var sharedStore sharedstate.Store
	if detectShareUsage(paths.Scripts) {
		if cfg.Shared == nil || !cfg.Shared.Redis.Enabled() {
			stresslog.Fatal("流程脚本使用了共享状态(share)，但未配置 Redis（shared.redis.addr 为空），无法启动")
		}
		resolved, rerr := cfg.Shared.Redis.Resolve()
		if rerr != nil {
			stresslog.Fatal("共享状态配置无效", zap.Error(rerr))
		}
		runID := fmt.Sprintf("standalone-%d-%d", time.Now().UnixMilli(), os.Getpid())
		store, serr := sharedstate.NewRedisStore(resolved, runID)
		if serr != nil {
			stresslog.Fatal("连接共享状态(Redis)失败", zap.Error(serr))
		}
		sharedStore = store
		stresslog.Info("[MAIN] 共享状态已启用",
			zap.String("addr", resolved.Addr), zap.Int("db", resolved.DB), zap.String("runId", runID))
	} else {
		stresslog.Info("[MAIN] 共享状态未启用（脚本未使用 share）")
	}

	mgrCfg := robot.ManagerConfig{
		AccountPrefix:  s.Bot.AccountPrefix,
		StartNumber:    s.Bot.StartNumber,
		Count:          s.Bot.Count,
		ConcurrentNum:  s.Bot.ConcurrentNum,
		StateExtra:     s.StateExtra,
		Adapter:        adp,
		CodecResolver:  resolver,
		RequestTimeout: 60 * time.Second,
		MainService:    s.Bot.MainService,
		HTTPTimeout:    10 * time.Second,
		Duration:       duration,
		Shared:         sharedStore,
	}

	// Dialer 元信息源：T2-C1 起改用 resolver 任一 adapter（Go SchemaAdapter）。
	// 当前协议 HeaderSize/BodyLength 全局一致（3 份 codec.json 同 frame spec，T1.6 同源生成），
	// 故取任一即可；per-connection HeaderSize 下沉留到 2-C3 connectionPump。
	dialer := network.NewDialer(adapter.PickMetaAdapter(resolver, codecMap), 5*time.Second)
	if err := dialer.Start(); err != nil {
		stresslog.Fatal("启动网络引擎失败", zap.Error(err))
	}
	defer dialer.Stop()

	mgr := robot.NewManager(mgrCfg, flow, factory, dialer, luaPool)

	if err := mgr.StartAll(); err != nil {
		stresslog.Fatal("启动机器人失败", zap.Error(err))
	}

	// 启动监控 Reporter 和 HTTP
	var reporter *monitor.Reporter
	if cfg.Monitor.Enabled {
		reporter = monitor.NewReporter(monitor.Global(), 5*time.Second)
		reporter.Start()

		if cfg.Monitor.HTTPEnabled {
			monitor.RegisterHandlers(monitor.Global())
			monitor.StartHTTPServer(cfg.Monitor.HTTPPort)
		}
	}

	// pprof 调试服务（独立端口，不依赖 monitor）
	var stopPprof func()
	if cfg.Pprof.Enabled {
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

	mgr.StopAll()

	// 单机模式：本进程独占 runId，统一清理共享状态后再关闭连接。
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

	if cfg.Monitor.Enabled {
		os.MkdirAll("metrics", 0o755)
		csvPath := fmt.Sprintf("metrics/metrics_%s.csv", time.Now().Format("2006_01_02_15_04_05"))
		if err := monitor.ExportCSV(monitor.Global(), csvPath); err != nil {
			stresslog.Error("[MONITOR] CSV 导出失败", zap.Error(err))
		} else {
			stresslog.Info("[MONITOR] CSV 已导出", zap.String("path", csvPath))
		}
	}

	adp.Close()
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

// ── 通用工具 ──────────────────────────────────────────────

// standalonePaths 单机模式解析后的资源路径（CLI flag 覆盖或 <conf> 下默认相对路径）。
type standalonePaths struct {
	Flow    string // 流程配置文件
	Proto   string // proto 目录
	Scripts string // Lua 脚本目录
	Adapter string // 适配器目录（含 codec.lua 与可选 error.lua）
}

// resolveStandalonePaths 解析单机模式资源路径：对应 flag 为空时回退到 confDir 下的默认相对路径，
// 非空时通过 resolvePath 解析为绝对路径。
func resolveStandalonePaths(confDir, flow, proto, scripts, adapter string) standalonePaths {
	return standalonePaths{
		Flow:    resolvePath(flow, filepath.Join(confDir, "flow", "flow.json")),
		Proto:   resolvePath(proto, filepath.Join(confDir, "proto")),
		Scripts: resolvePath(scripts, filepath.Join(confDir, "scripts")),
		Adapter: resolvePath(adapter, filepath.Join(confDir, "adapter")),
	}
}

// resolvePath flagVal 为空返回 defaultPath，否则解析为绝对路径（解析失败则原样返回）。
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

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	cfg.Agent.AppVersion = Version

	if !cfg.Agent.Enabled {
		if cfg.Standalone == nil {
			cfg.Standalone = &StandaloneConfig{}
		}
		s := cfg.Standalone

		if s.Bot.StartNumber == 0 {
			s.Bot.StartNumber = 1
		}
		if s.Bot.Count == 0 {
			s.Bot.Count = 1
		}
		if s.Bot.AccountPrefix == "" {
			s.Bot.AccountPrefix = "bot_"
		}
		if s.Bot.MainService == "" {
			return nil, fmt.Errorf("standalone.bot.mainService is required")
		}
	}

	return cfg, nil
}

func loadFlow(path string) (*engine.TaskFlow, error) {
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
