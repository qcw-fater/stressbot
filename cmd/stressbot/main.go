package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"

	"stressbot/adapter"
	"stressbot/engine"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/robot"
	"stressbot/script"
)

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

	Flow   string `json:"flow"`
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
	Pprof struct {
		Enabled bool `json:"enabled"`
		Port    int  `json:"port"`
	} `json:"pprof"`
}

func main() {
	configPath := flag.String("config", "conf/config.json", "配置文件路径")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

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
	stresslog.Info("[MAIN] 配置已加载", zap.Int("botCount", cfg.Bot.Count), zap.Int("concurrent", cfg.Bot.ConcurrentNum))

	// 启动 pprof
	if cfg.Pprof.Enabled {
		pprofPort := cfg.Pprof.Port
		if pprofPort == 0 {
			pprofPort = 6060
		}
		addr := fmt.Sprintf(":%d", pprofPort)
		go func() {
			if err := http.ListenAndServe(addr, nil); err != nil {
				stresslog.Warn("[MAIN] pprof 服务启动失败", zap.String("addr", addr), zap.Error(err))
			}
		}()
		stresslog.Info("[MAIN] pprof 已启动", zap.String("addr", addr))
	}

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
	factory := protox.NewFactory(registry)

	// 加载流程配置
	flow, err := loadFlow(cfg.Flow)
	if err != nil {
		stresslog.Fatal("加载流程配置失败", zap.Error(err))
	}

	stresslog.Info("[MAIN] 流程配置已加载",
		zap.Int("nodes", len(flow.Nodes)), zap.Int("actions", len(flow.Actions)), zap.Int("callbacks", len(flow.Callbacks)))

	// 解析心跳间隔
	heartbeatInterval := 5 * time.Second
	if cfg.Network.HeartbeatInterval != "" {
		if d, err := time.ParseDuration(cfg.Network.HeartbeatInterval); err != nil {
			stresslog.Fatal("解析心跳间隔失败", zap.Error(err))
		} else {
			heartbeatInterval = d
		}
	}

	// 解析 TCP 超时
	tcpTimeout := 60 * time.Second
	if cfg.Network.TCPTimeout != "" {
		if d, err := time.ParseDuration(cfg.Network.TCPTimeout); err != nil {
			stresslog.Fatal("解析 TCP 超时失败", zap.Error(err))
		} else {
			tcpTimeout = d
		}
	}

	// 解析 HTTP 超时
	httpTimeout := 10 * time.Second
	if cfg.Network.HTTPTimeout != "" {
		if d, err := time.ParseDuration(cfg.Network.HTTPTimeout); err != nil {
			stresslog.Fatal("解析 HTTP 超时失败", zap.Error(err))
		} else {
			httpTimeout = d
		}
	}

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

	mgrCfg := robot.ManagerConfig{
		AccountPrefix:  cfg.Bot.AccountPrefix,
		StartNumber:    cfg.Bot.StartNumber,
		Count:          cfg.Bot.Count,
		ConcurrentNum:  cfg.Bot.ConcurrentNum,
		AuthBaseURL:    cfg.Auth.Address,
		AuthExtra:      cfg.Auth.Extra,
		Adapter:        adp,
		RequestTimeout: tcpTimeout,
		MainService:    cfg.Bot.MainService,
		HTTPTimeout:    httpTimeout,
	}

	mgr := robot.NewManager(mgrCfg, flow, factory, dialer, luaPool)

	if err := mgr.StartAll(); err != nil {
		stresslog.Fatal("启动机器人失败", zap.Error(err))
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	stresslog.Info("[MAIN] 收到退出信号，正在关闭...")
	mgr.StopAll()
	adp.Close()
	stresslog.Info("[MAIN] 已退出")
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
