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
	} `json:"bot"`

	Auth struct {
		Address string            `json:"address"`
		Extra   map[string]string `json:"extra"`
	} `json:"auth"`

	Network struct {
		TCPTimeout        string   `json:"tcpTimeout"`
		HeartbeatInterval string   `json:"heartbeatInterval"`
		UDPServices       []string `json:"udpServices"`
		MainService       string   `json:"mainService"`
		AdapterPoolSize   int      `json:"adapterPoolSize"`
	} `json:"network"`

	Proto struct {
		Dirs  []string `json:"dirs"`
		Files []string `json:"files"`
	} `json:"proto"`

	AdapterScript string `json:"adapterScript"`
	Flow          string `json:"flow"`
	Script        struct {
		Dirs []string `json:"dirs"`
	} `json:"script"`
}

func main() {
	configPath := flag.String("config", "conf/config.json", "配置文件路径")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	stresslog.InitLog("log/stressbot.log", "stressbot", nil, "")
	stresslog.Info("[MAIN] 配置已加载", zap.Int("botCount", cfg.Bot.Count), zap.Int("concurrent", cfg.Bot.ConcurrentNum))

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
		if d, err := time.ParseDuration(cfg.Network.HeartbeatInterval); err == nil {
			heartbeatInterval = d
		}
	}

	// 解析 TCP 超时
	tcpTimeout := 60 * time.Second
	if cfg.Network.TCPTimeout != "" {
		if d, err := time.ParseDuration(cfg.Network.TCPTimeout); err == nil {
			tcpTimeout = d
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
		UDPServices:    cfg.Network.UDPServices,
		MainService:    cfg.Network.MainService,
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
	if cfg.AdapterScript == "" {
		cfg.AdapterScript = "conf/adapter/codec.lua"
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
	if len(cfg.Network.UDPServices) == 0 {
		cfg.Network.UDPServices = []string{"udp"}
	}
	if cfg.Network.MainService == "" {
		cfg.Network.MainService = "logic"
	}

	return cfg, nil
}

func loadAdapter(cfg *Config) (*adapter.LuaAdapter, error) {
	scriptPath := cfg.AdapterScript
	poolSize := cfg.Network.AdapterPoolSize
	if poolSize <= 0 {
		poolSize = runtime.NumCPU()
	}
	return adapter.NewLuaAdapter(poolSize, scriptPath)
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
