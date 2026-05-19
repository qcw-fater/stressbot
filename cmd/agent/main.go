package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
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
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// Version 编译时注入：-ldflags "-X main.Version=v1.0.0"
var Version = "dev"

// StandaloneConfig 单机模式专用配置。
type StandaloneConfig struct {
	// Bot 机器人批量参数。
	Bot struct {
		// AccountPrefix 账号名前缀。
		AccountPrefix string `json:"accountPrefix"`
		// StartNumber 起始编号。
		StartNumber int `json:"startNumber"`
		// Count 机器人总数。
		Count int `json:"count"`
		// ConcurrentNum 并发启动数。
		ConcurrentNum int `json:"concurrentNum"`
		// MainService 主服务名（TCP 连接标识）。
		MainService string `json:"mainService"`
	} `json:"bot"`

	// StateExtra 初始状态额外键值对，注入每个 Robot 的 state。
	StateExtra map[string]string `json:"stateExtra"`

	// Adapter 协议适配器配置。
	Adapter struct {
		// Script codec.lua 脚本路径。
		Script string `json:"script"`
		// PoolSize Lua 协程池大小，0 表示自动。
		PoolSize int `json:"poolSize"`
	} `json:"adapter"`

	// Network 网络超时配置。
	Network struct {
		// HeartbeatInterval 心跳发送间隔（duration 字符串）。
		HeartbeatInterval string `json:"heartbeatInterval"`
		// TCPTimeout TCP 请求超时（duration 字符串）。
		TCPTimeout string `json:"tcpTimeout"`
		// HTTPTimeout HTTP 请求超时（duration 字符串）。
		HTTPTimeout string `json:"httpTimeout"`
	} `json:"network"`

	// Proto protobuf 文件加载配置。
	Proto struct {
		// Dirs proto 文件搜索目录。
		Dirs []string `json:"dirs"`
		// Files 额外 proto 文件路径。
		Files []string `json:"files"`
	} `json:"proto"`

	// Flow 流程配置文件路径。
	Flow string `json:"flow"`

	// Script Lua 脚本配置。
	Script struct {
		// Dirs Lua 脚本搜索目录。
		Dirs []string `json:"dirs"`
	} `json:"script"`
}

// Config 全局配置结构。
// Log 和 Monitor 两种模式共享；Standalone 仅单机模式；Agent 仅 Agent 模式。
type Config struct {
	// Log 日志配置。
	Log struct {
		// Path 日志文件路径。
		Path string `json:"path"`
		// Level 日志等级（debug/info/warn/error）。
		Level string `json:"level"`
		// PrintConsole 是否同时输出到控制台。
		PrintConsole bool `json:"printConsole"`
		// MaxSize 单个日志文件最大 MB。
		MaxSize int `json:"maxSize"`
		// MaxBackups 保留的旧日志文件数。
		MaxBackups int `json:"maxBackups"`
		// MaxAge 日志文件最大保留天数。
		MaxAge int `json:"maxAge"`
		// Compress 是否压缩旧日志文件。
		Compress bool `json:"compress"`
	} `json:"log"`

	// Monitor 指标采集配置。
	Monitor monitor.CollectorConfig `json:"monitor"`

	// Standalone 单机模式配置，Agent 模式下为 nil。
	Standalone *StandaloneConfig `json:"standalone"`

	// Agent Agent 模式配置。
	Agent agent.AgentConfig `json:"agent"`
}

func main() {
	// 进程级顶层 recover：防止任何未捕获的 panic 直接让进程崩溃，
	// 同时尽量把 stack trace 写入日志而不是仅 stderr。
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
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 初始化日志
	logPath := cfg.Log.Path
	if logPath == "" {
		logPath = "log/stressbot.log"
	}
	logConf := &stresslog.Config{
		PrintConsole: cfg.Log.PrintConsole,
		LogLevel:     cfg.Log.Level,
		MaxSize:      cfg.Log.MaxSize,
		MaxBackups:   cfg.Log.MaxBackups,
		MaxAge:       cfg.Log.MaxAge,
		Compress:     cfg.Log.Compress,
	}
	stresslog.InitLog(logPath, "stressbot", logConf, "")
	newLogger := logview.AttachRingBuffer(stresslog.GetLogger(), 50000, zap.String("SR", "stressbot"))
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
		runStandalone(cfg)
	}
}

// ── Agent 模式 ──────────────────────────────────────────

func runAgentMode(cfg *Config) {
	resolved, err := cfg.Agent.Resolve()
	if err != nil {
		stresslog.Fatal("Agent 配置校验失败", zap.Error(err))
	}

	monitor.Init(monitor.CollectorConfig{
		Enabled:        true,
		ApdexT:         cfg.Monitor.ApdexT,
		ReportInterval: cfg.Monitor.ReportInterval,
	})

	a, err := agent.New(resolved, monitor.Global())
	if err != nil {
		stresslog.Fatal("创建 Agent 失败", zap.Error(err))
	}

	if err := a.Run(); err != nil {
		stresslog.Fatal("Agent 运行失败", zap.Error(err))
	}
}

// ── 单机模式 ──────────────────────────────────────────────

func runStandalone(cfg *Config) {
	s := cfg.Standalone

	// 加载协议适配器
	adp, err := loadAdapter(s)
	if err != nil {
		stresslog.Fatal("加载适配器失败", zap.Error(err))
	}
	stresslog.Info("[MAIN] 适配器已初始化", zap.Int("headerSize", adp.HeaderSize()))

	// 加载 .proto 文件
	loader := protox.NewLoader(s.Proto.Dirs, s.Proto.Files)
	files, err := loader.Load()
	if err != nil {
		stresslog.Fatal("加载 proto 文件失败", zap.Error(err))
	}

	registry := protox.NewRegistry(files)
	factory := protox.NewFactory(registry)

	// 加载流程配置
	flow, err := loadFlow(s.Flow)
	if err != nil {
		stresslog.Fatal("加载流程配置失败", zap.Error(err))
	}

	stresslog.Info("[MAIN] 流程配置已加载",
		zap.Int("nodes", len(flow.Nodes)),
		zap.Int("actions", len(flow.Actions)),
		zap.Int("callbacks", len(flow.Callbacks)))

	// 回填 ActionDef.Name
	for name, action := range flow.Actions {
		action.Name = name
		flow.Actions[name] = action
	}

	// 初始化监控
	monitor.Init(cfg.Monitor)
	if cfg.Monitor.Enabled {
		stresslog.Info("[MAIN] 监控已启用",
			zap.String("reportInterval", cfg.Monitor.ReportInterval),
			zap.Bool("httpEnabled", cfg.Monitor.HTTPEnabled),
			zap.Int("apdexT", cfg.Monitor.ApdexT))
	}

	// 解析超时
	heartbeatInterval := utils.ParseDurationDefault(s.Network.HeartbeatInterval, 5*time.Second, "network.heartbeatInterval")
	tcpTimeout := utils.ParseDurationDefault(s.Network.TCPTimeout, 60*time.Second, "network.tcpTimeout")
	httpTimeout := utils.ParseDurationDefault(s.Network.HTTPTimeout, 10*time.Second, "network.httpTimeout")

	// 启动 gnet 网络引擎
	dialer := network.NewDialer(adp, heartbeatInterval)
	if err := dialer.Start(); err != nil {
		stresslog.Fatal("启动网络引擎失败", zap.Error(err))
	}
	defer dialer.Stop()

	// 初始化 Lua 运行时池
	scriptDir := "conf/scripts"
	if len(s.Script.Dirs) > 0 {
		scriptDir = s.Script.Dirs[0]
	}
	luaPool := script.NewRuntimePool(scriptDir)
	if err := luaPool.PrecompileScripts(s.Script.Dirs); err != nil {
		stresslog.Warn("[MAIN] Lua 脚本预编译失败（非致命错误）", zap.Error(err))
	} else {
		stresslog.Info("[MAIN] Lua 脚本已预编译", zap.Int("count", len(luaPool.ListScripts())))
	}

	mgrCfg := robot.ManagerConfig{
		AccountPrefix:  s.Bot.AccountPrefix,
		StartNumber:    s.Bot.StartNumber,
		Count:          s.Bot.Count,
		ConcurrentNum:  s.Bot.ConcurrentNum,
		StateExtra:     s.StateExtra,
		Adapter:        adp,
		RequestTimeout: tcpTimeout,
		MainService:    s.Bot.MainService,
		HTTPTimeout:    httpTimeout,
	}

	mgr := robot.NewManager(mgrCfg, flow, factory, dialer, luaPool)

	if err := mgr.StartAll(); err != nil {
		stresslog.Fatal("启动机器人失败", zap.Error(err))
	}

	// 启动监控 Reporter 和 HTTP
	var reporter *monitor.Reporter
	if cfg.Monitor.Enabled {
		interval := 5 * time.Second
		if cfg.Monitor.ReportInterval != "" {
			if d, err := time.ParseDuration(cfg.Monitor.ReportInterval); err == nil && d > 0 {
				interval = d
			}
		}
		reporter = monitor.NewReporter(monitor.Global(), interval)
		reporter.Start()

		if cfg.Monitor.HTTPEnabled {
			monitor.RegisterHandlers(monitor.Global())
			monitor.StartHTTPServer(cfg.Monitor.HTTPPort)
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	stresslog.Info("[MAIN] 收到退出信号，正在关闭...")

	if reporter != nil {
		reporter.Stop()
	}

	mgr.StopAll()

	if cfg.Monitor.Enabled {
		csvPath := cfg.Monitor.CsvPath
		if csvPath == "" {
			csvPath = "log/metrics.csv"
			stresslog.Warn("[CONFIG] monitor.csvPath 为空，使用默认值", zap.String("default", csvPath))
		}
		if err := monitor.ExportCSV(monitor.Global(), csvPath); err != nil {
			stresslog.Error("[MONITOR] CSV 导出失败", zap.Error(err))
		} else {
			stresslog.Info("[MONITOR] CSV 已导出", zap.String("path", csvPath))
		}
	}

	adp.Close()
	utils.GetWorkPool().Shutdown()
	stresslog.Info("[MAIN] 已退出")
}

// ── 通用工具 ──────────────────────────────────────────────

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// Agent 模式公共默认值
	if cfg.Agent.AppVersion == "" {
		cfg.Agent.AppVersion = Version
	}

	if cfg.Agent.Enabled {
		// Agent 模式：仅校验 Agent 段（adminAddr 在 Resolve() 中校验）
	} else {
		// 单机模式：校验并填充 Standalone 段
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
		if s.Adapter.Script == "" {
			s.Adapter.Script = "conf/adapter/codec.lua"
		}
		if s.Flow == "" {
			s.Flow = "conf/flow/flow.json"
		}
		if len(s.Proto.Dirs) == 0 {
			s.Proto.Dirs = []string{"conf/proto"}
		}
		if len(s.Script.Dirs) == 0 {
			s.Script.Dirs = []string{"conf/scripts"}
		}
	}

	return cfg, nil
}

func loadAdapter(s *StandaloneConfig) (*adapter.LuaAdapter, error) {
	poolSize := s.Adapter.PoolSize
	if poolSize <= 0 {
		poolSize = runtime.NumCPU()
		stresslog.Warn("[CONFIG] standalone.adapter.poolSize 非法，使用默认值", zap.Int("default", poolSize))
	}
	// 可选：加载错误码映射
	errorMapPath := filepath.Join(filepath.Dir(s.Adapter.Script), "error.lua")
	if _, err := os.Stat(errorMapPath); err != nil {
		errorMapPath = ""
	}
	return adapter.NewLuaAdapter(poolSize, s.Adapter.Script, errorMapPath)
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
