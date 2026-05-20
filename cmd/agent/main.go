package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
		AccountPrefix string `json:"accountPrefix"` // 账号名前缀（默认 bot_）
		StartNumber   int    `json:"startNumber"`   // 起始编号（默认 1）
		Count         int    `json:"count"`         // 机器人总数（默认 1）
		ConcurrentNum int    `json:"concurrentNum"` // 并发启动数（0=不限）
		MainService   string `json:"mainService"`   // 主服务名（必填，TCP 连接标识）
	} `json:"bot"`

	// StateExtra 初始状态额外键值对，注入每个 Robot 的 state。
	StateExtra map[string]string `json:"stateExtra"`
}

// Config 全局配置结构。
type Config struct {
	Log        *stresslog.Config       `json:"log"`
	Monitor    monitor.CollectorConfig `json:"monitor"`
	Standalone *StandaloneConfig       `json:"standalone"`
	Agent      agent.Config            `json:"agent"`
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
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
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
		Enabled: true,
		ApdexT:  cfg.Monitor.ApdexT,
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
	poolSize := runtime.NumCPU()
	errorMapPath := "conf/adapter/error.lua"
	if _, err := os.Stat(errorMapPath); err != nil {
		errorMapPath = ""
	}
	adp, err := adapter.NewLuaAdapter(poolSize, "conf/adapter/codec.lua", errorMapPath)
	if err != nil {
		stresslog.Fatal("加载适配器失败", zap.Error(err))
	}
	stresslog.Info("[MAIN] 适配器已初始化", zap.Int("headerSize", adp.HeaderSize()))

	// 加载 .proto 文件
	loader := protox.NewLoader([]string{"conf/proto"}, nil)
	files, err := loader.Load()
	if err != nil {
		stresslog.Fatal("加载 proto 文件失败", zap.Error(err))
	}

	registry := protox.NewRegistry(files)
	factory := protox.NewFactory(registry)

	// 加载流程配置
	flow, err := loadFlow("conf/flow/flow.json")
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
			zap.Bool("httpEnabled", cfg.Monitor.HTTPEnabled),
			zap.Int("apdexT", cfg.Monitor.ApdexT))
	}

	// 初始化 Lua 运行时池
	luaPool := script.NewRuntimePool("conf/scripts")
	if err := luaPool.PrecompileScripts([]string{"conf/scripts"}); err != nil {
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
		RequestTimeout: 60 * time.Second,
		MainService:    s.Bot.MainService,
		HTTPTimeout:    10 * time.Second,
	}

	dialer := network.NewDialer(adp, 5*time.Second)
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

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	stresslog.Info("[MAIN] 收到退出信号，正在关闭...")

	if reporter != nil {
		reporter.Stop()
	}

	mgr.StopAll()

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
