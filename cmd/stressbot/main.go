// stressbot 通用游戏压测工具入口程序。
// 加载配置 → 初始化引擎 → 创建并启动机器人。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	stresslog "stressbot/utils/log"

	"stressbot/engine"
	"stressbot/network"
	"stressbot/protox"
	"stressbot/robot"
	"stressbot/script"
)

// Config 全局配置结构（从 config.json 加载）
type Config struct {
	Bot struct {
		AccountPrefix string `json:"accountPrefix"`
		StartNumber   int    `json:"startNumber"`
		Count         int    `json:"count"`
		ConcurrentNum int    `json:"concurrentNum"`
	} `json:"bot"`

	Auth struct {
		Address  string `json:"address"`
		Version  string `json:"version"`
		Channel  string `json:"channel"`
		Platform string `json:"platform"`
	} `json:"auth"`

	Network struct {
		TCPTimeout        string `json:"tcpTimeout"`
		HeartbeatInterval string `json:"heartbeatInterval"`
	} `json:"network"`

	Proto struct {
		Dirs  []string `json:"dirs"`
		Files []string `json:"files"`
	} `json:"proto"`

	Header string `json:"header"` // header.json 路径
	Flow   string `json:"flow"`   // flow.json 路径
	Script struct {
		Dirs []string `json:"dirs"` // Lua 脚本目录列表
	} `json:"script"`
	Middleware struct {
		Standard []string `json:"standard"` // 框架标准中间件（"gzip" 等）
		Scripts  []string `json:"scripts"`  // Lua 中间件脚本目录
		PoolSize int      `json:"poolSize"` // Lua 中间件 LState 池大小
	} `json:"middleware"`
}

func main() {
	configPath := flag.String("config", "conf/config.json", "配置文件路径")
	flag.Parse()

	// 加载全局配置
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 初始化日志
	stresslog.InitLog("log/stressbot.log", "stressbot", nil, "")

	stresslog.Info("[MAIN] 配置已加载", zap.Int("botCount", cfg.Bot.Count), zap.Int("concurrent", cfg.Bot.ConcurrentNum))

	// 注册中间件（在加载协议之前）
	if err := initMiddleware(cfg); err != nil {
		stresslog.Fatal("初始化中间件失败", zap.Error(err))
	}

	// 加载消息头协议配置
	protocol, err := loadProtocol(cfg.Header)
	if err != nil {
		stresslog.Fatal("加载消息头协议失败", zap.Error(err))
	}

	stresslog.Info("[MAIN] 消息头协议已加载", zap.String("protocol", protocol.String()))

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
		zap.String("startNode", flow.StartNode), zap.Int("actions", len(flow.Actions)), zap.Int("callbacks", len(flow.Callbacks)))

	// 解析心跳间隔
	heartbeatInterval := 5 * time.Second
	if cfg.Network.HeartbeatInterval != "" {
		if d, err := time.ParseDuration(cfg.Network.HeartbeatInterval); err == nil {
			heartbeatInterval = d
		}
	}

	// 启动 gnet 网络引擎
	dialer := network.NewDialer(protocol, heartbeatInterval)
	if err := dialer.Start(); err != nil {
		stresslog.Fatal("启动网络引擎失败", zap.Error(err))
	}
	defer dialer.Stop()

	// 创建机器人管理器并启动
	mgrCfg := robot.ManagerConfig{
		AccountPrefix: cfg.Bot.AccountPrefix,
		StartNumber:   cfg.Bot.StartNumber,
		Count:         cfg.Bot.Count,
		ConcurrentNum: cfg.Bot.ConcurrentNum,
		AuthBaseURL:   cfg.Auth.Address,
		Version:       cfg.Auth.Version,
		Channel:       cfg.Auth.Channel,
		Platform:      cfg.Auth.Platform,
	}

	// 初始化 Lua 运行时池并预编译脚本
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

	mgr := robot.NewManager(mgrCfg, flow, factory, protocol, dialer, luaPool)

	if err := mgr.StartAll(); err != nil {
		stresslog.Fatal("启动机器人失败", zap.Error(err))
	}

	// 等待退出信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	<-sigCh
	stresslog.Info("[MAIN] 收到退出信号，正在关闭...")
	mgr.StopAll()
	stresslog.Info("[MAIN] 已退出")
}

// loadConfig 加载全局配置
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 设置默认值
	if cfg.Bot.StartNumber == 0 {
		cfg.Bot.StartNumber = 1
	}
	if cfg.Bot.Count == 0 {
		cfg.Bot.Count = 1
	}
	if cfg.Bot.AccountPrefix == "" {
		cfg.Bot.AccountPrefix = "bot_"
	}
	if cfg.Header == "" {
		cfg.Header = "conf/header.json"
	}
	if cfg.Flow == "" {
		cfg.Flow = "conf/flow.json"
	}
	if len(cfg.Proto.Dirs) == 0 {
		cfg.Proto.Dirs = []string{"conf/protos"}
	}
	if len(cfg.Script.Dirs) == 0 {
		cfg.Script.Dirs = []string{"conf/scripts"}
	}

	return cfg, nil
}

// loadProtocol 加载消息头协议配置
func loadProtocol(path string) (*network.Protocol, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取消息头配置失败: %w", err)
	}

	var cfg network.ProtocolConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析消息头配置失败: %w", err)
	}

	return network.NewProtocol(cfg), nil
}

// loadFlow 加载流程配置
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

// initMiddleware 从 Lua 脚本目录加载并注册中间件。
// 必须在 loadProtocol 之前调用。
func initMiddleware(cfg *Config) error {
	// 注册框架标准中间件
	for _, name := range cfg.Middleware.Standard {
		if !network.RegisterStandard(name) {
			stresslog.Warn("[MAIN] 未知的标准中间件", zap.String("name", name))
		}
	}

	// 加载 Lua 中间件脚本
	if len(cfg.Middleware.Scripts) == 0 {
		return nil
	}

	pool := network.NewLuaMiddlewarePool(cfg.Middleware.PoolSize)
	if err := pool.LoadScripts(cfg.Middleware.Scripts); err != nil {
		return fmt.Errorf("加载 Lua 中间件脚本失败: %w", err)
	}

	for _, name := range pool.ScriptNames() {
		factory := network.CreateLuaMiddlewareFactory(pool, name)
		network.RegisterMiddleware(name, factory)
		stresslog.Info("[MAIN] 已注册 Lua 中间件", zap.String("name", name))
	}

	return nil
}
