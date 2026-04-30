package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"

	"stressbot/adapter"
	"stressbot/agent"
	"stressbot/engine"
	"stressbot/monitor"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/script"
)

// Version 编译时注入：-ldflags "-X main.Version=v1.0.0"
var Version = "dev"

// Config 全局配置结构
type Config struct {
	Bot struct {
		AccountPrefix string `json:"accountPrefix"`
		StartNumber   int    `json:"startNumber"`
		Count         int    `json:"count"`
		ConcurrentNum int    `json:"concurrentNum"`
		MainService   string `json:"mainService"`
	} `json:"bot"`

	Auth struct {
		Address string            `json:"address"`
		Extra   map[string]string `json:"extra"`
	} `json:"auth"`

	Adapter struct {
		Script   string `json:"script"`
		PoolSize int    `json:"poolSize"`
	} `json:"adapter"`

	Network struct {
		HeartbeatInterval string `json:"heartbeatInterval"`
		TCPTimeout        string `json:"tcpTimeout"`
		HTTPTimeout       string `json:"httpTimeout"`
	} `json:"network"`

	Proto struct {
		Dirs  []string `json:"dirs"`
		Files []string `json:"files"`
	} `json:"proto"`

	Flow string `json:"flow"`

	Script struct {
		Dirs []string `json:"dirs"`
	} `json:"script"`

	Log struct {
		Path         string `json:"path"`
		Level        string `json:"level"`
		PrintConsole bool   `json:"printConsole"`
		MaxSize      int    `json:"maxSize"`
		MaxBackups   int    `json:"maxBackups"`
		MaxAge       int    `json:"maxAge"`
		Compress     bool   `json:"compress"`
	} `json:"log"`

	Monitor monitor.CollectorConfig `json:"monitor"`

	Agent agent.AgentConfig `json:"agent"`
}

func main() {
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

	if cfg.Agent.Enabled {
		stresslog.Info("[MAIN] Agent 模式启动", zap.String("adminAddr", cfg.Agent.AdminAddr))
		runAgentMode(cfg)
	} else {
		stresslog.Info("[MAIN] 单机模式启动",
			zap.Int("botCount", cfg.Bot.Count),
			zap.Int("concurrent", cfg.Bot.ConcurrentNum))
		runStandalone(cfg)
	}
}

func runAgentMode(cfg *Config) {
	resolved, err := cfg.Agent.Resolve()
	if err != nil {
		stresslog.Fatal("Agent 配置校验失败", zap.Error(err))
	}

	// 初始化监控
	monitor.Init(monitor.CollectorConfig{
		Enabled:        true,
		ApdexT:         cfg.Monitor.ApdexT,
		ReportInterval: cfg.Monitor.ReportInterval,
	})

	a, err := agent.New(resolved, monitor.Global())
	if err != nil {
		stresslog.Fatal("创建 Agent 失败", zap.Error(err))
	}

	// 检查是否是升级后的首次启动
	a.MarkSuccess()

	if err := a.Run(); err != nil {
		stresslog.Fatal("Agent 运行失败", zap.Error(err))
	}
}

func runStandalone(cfg *Config) {
	// 加载协议适配器
	adp, err := loadAdapter(cfg)
	if err != nil {
		stresslog.Fatal("加载适配器失败", zap.Error(err))
	}
	stresslog.Info("[MAIN] 适配器已初始化", zap.Int("headerSize", adp.HeaderSize()))

	// 加载 .proto 文件
	loader := protox.NewLoader(cfg.Proto.Dirs, cfg.Proto.Files)
	files, err := loader.Load()
	if err != nil {
		stresslog.Fatal("加载 proto 文件失败", zap.Error(err))
	}

	registry := protox.NewRegistry(files)
	_ = protox.NewFactory(registry) // TODO: standalone 模式完整集成时使用

	// 加载流程配置
	flow, err := loadFlow(cfg.Flow)
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
	heartbeatInterval := parseDurationDefault(cfg.Network.HeartbeatInterval, 5*time.Second)
	tcpTimeout := parseDurationDefault(cfg.Network.TCPTimeout, 60*time.Second)
	httpTimeout := parseDurationDefault(cfg.Network.HTTPTimeout, 10*time.Second)

	// 启动 gnet 网络引擎
	dialer := network.NewDialer(adp, heartbeatInterval)
	if err := dialer.Start(); err != nil {
		stresslog.Fatal("启动网络引擎失败", zap.Error(err))
	}
	defer dialer.Stop()

	// 初始化 Lua 运行时池
	scriptDir := "conf/scripts"
	if len(cfg.Script.Dirs) > 0 {
		scriptDir = cfg.Script.Dirs[0]
	}
	luaPool := script.NewRuntimePool(scriptDir)
	if err := luaPool.PrecompileScripts(cfg.Script.Dirs); err != nil {
		stresslog.Warn("[MAIN] Lua 脚本预编译失败（非致命错误）", zap.Error(err))
	} else {
		stresslog.Info("[MAIN] Lua 脚本已预编译", zap.Int("count", len(luaPool.ListScripts())))
	}

	mgrCfg := robotManagerConfig(cfg, tcpTimeout, httpTimeout)

	// 创建 Manager（注意：这里用 robot 包，需要 import）
	// 为了避免循环引用，standalone 模式保持使用现有的 robot.Manager
	_ = mgrCfg // TODO: 复用 robot/manager.go

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

	if cfg.Monitor.Enabled {
		csvPath := cfg.Monitor.CsvPath
		if csvPath == "" {
			csvPath = "log/metrics.csv"
		}
		if err := monitor.ExportCSV(monitor.Global(), csvPath); err != nil {
			stresslog.Error("[MONITOR] CSV 导出失败", zap.Error(err))
		} else {
			stresslog.Info("[MONITOR] CSV 已导出", zap.String("path", csvPath))
		}
	}

	adp.Close()
	stresslog.Info("[MAIN] 已退出")
}

func robotManagerConfig(cfg *Config, tcpTimeout, httpTimeout time.Duration) struct {
	AccountPrefix  string
	StartNumber    int
	Count          int
	ConcurrentNum  int
	AuthBaseURL    string
	AuthExtra      map[string]string
	RequestTimeout time.Duration
	MainService    string
	HTTPTimeout    time.Duration
} {
	return struct {
		AccountPrefix  string
		StartNumber    int
		Count          int
		ConcurrentNum  int
		AuthBaseURL    string
		AuthExtra      map[string]string
		RequestTimeout time.Duration
		MainService    string
		HTTPTimeout    time.Duration
	}{
		AccountPrefix:  cfg.Bot.AccountPrefix,
		StartNumber:    cfg.Bot.StartNumber,
		Count:          cfg.Bot.Count,
		ConcurrentNum:  cfg.Bot.ConcurrentNum,
		AuthBaseURL:    cfg.Auth.Address,
		AuthExtra:      cfg.Auth.Extra,
		RequestTimeout: tcpTimeout,
		MainService:    cfg.Bot.MainService,
		HTTPTimeout:    httpTimeout,
	}
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

	if cfg.Bot.StartNumber == 0 {
		cfg.Bot.StartNumber = 1
	}
	if cfg.Bot.Count == 0 {
		cfg.Bot.Count = 1
	}
	if cfg.Bot.AccountPrefix == "" {
		cfg.Bot.AccountPrefix = "bot_"
	}
	if cfg.Bot.MainService == "" {
		cfg.Bot.MainService = "logic"
	}
	if cfg.Adapter.Script == "" {
		cfg.Adapter.Script = "conf/adapter/codec.lua"
	}
	if cfg.Flow == "" {
		cfg.Flow = "conf/flow.json"
	}
	if len(cfg.Proto.Dirs) == 0 {
		cfg.Proto.Dirs = []string{"conf/proto"}
	}
	if len(cfg.Script.Dirs) == 0 {
		cfg.Script.Dirs = []string{"conf/scripts"}
	}
	if cfg.Agent.AppVersion == "" {
		cfg.Agent.AppVersion = Version
	}

	return cfg, nil
}

func loadAdapter(cfg *Config) (*adapter.LuaAdapter, error) {
	poolSize := cfg.Adapter.PoolSize
	if poolSize <= 0 {
		poolSize = runtime.NumCPU()
	}
	return adapter.NewLuaAdapter(poolSize, cfg.Adapter.Script)
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

func parseDurationDefault(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
