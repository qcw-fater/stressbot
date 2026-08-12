// Package main 是 stressbot Agent 节点入口（分布式模式）。
//
// Agent 主动建立到 Admin 的 gRPC 长连接 → Hello/心跳与租约 → 接收可靠命令 →
// 下载内容寻址资源包 → TaskRunner 执行 → 流式上报压力/系统指标 → 最终报告确认。
// 与单机模式（cmd/stressbot）完全独立，各有专属配置文件（agent.toml / stressbot.toml）。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"

	"stressbot/agent"
	"stressbot/monitor"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	_ "net/http/pprof"

	"go.uber.org/zap"
)

// Version 编译时注入：-ldflags "-X main.Version=v1.0.0"
var Version = "dev"

// Config Agent 模式配置结构（对应 agent.toml）。
type Config struct {
	Log     *stresslog.Config        `toml:"log"`     // 日志
	Monitor *monitor.CollectorConfig `toml:"monitor"` // 监控（nil=不启用）
	Pprof   *utils.PprofConfig       `toml:"pprof"`   // pprof 调试（nil=不启用）
	Agent   agent.Config             `toml:"agent"`   // Agent 节点参数
	Daemon  bool                     `toml:"daemon"`  // 守护进程模式（仅 Linux）
}

// Defaults 返回填充了默认值的 Agent 模式配置。
func Defaults() Config {
	return Config{
		Agent: agent.Defaults(),
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
			fmt.Fprintf(os.Stderr, "[AGENT] 顶层 panic: %v\n%s\n", rec, debug.Stack())
			if stresslog.GetLogger() != nil {
				stresslog.Error("[AGENT] 顶层 panic",
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
				fmt.Fprintf(os.Stderr, "关闭 Agent 日志失败: %v\n", err)
			}
		}
	}()

	configPath := flag.String("config", "conf/agent.toml", "配置文件路径")
	daemonFlag := flag.Bool("d", false, "以守护进程模式运行")
	flag.Parse()

	if *daemonFlag {
		utils.Daemon("-d")
		return
	}

	// 推导 conf 根目录（供日志等相对路径解析）
	configAbs, err := filepath.Abs(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析配置路径失败: %v\n", err)
		exitProcess(1)
	}
	_ = filepath.Dir(configAbs) // confDir 当前无直接用途，保留推导以备扩展

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		exitProcess(1)
	}

	if cfg.Daemon && os.Getppid() != 1 {
		utils.Daemon()
		return
	}

	closeLog = stresslog.InitLog("log/agent.log", "agent", cfg.Log, "")

	stresslog.Info("[MAIN] Agent 模式启动", zap.String("adminAddress", cfg.Agent.AdminAddress))
	runAgentMode(cfg)
}

func loadConfig(path string) (*Config, error) {
	cfg, err := utils.LoadTOML(path, Defaults())
	if err != nil {
		return nil, err
	}
	cfg.Agent.AppVersion = Version
	return cfg, nil
}

// ── Agent 模式 ──────────────────────────────────────────

func runAgentMode(cfg *Config) {
	// pprof 调试服务（独立端口，不依赖 monitor）
	var stopPprof func()
	if cfg.Pprof != nil {
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

	// Agent 模式默认启用监控（配置可空）。配置非空时沿用其 ApdexThresholdMs/TimingDetail。
	monCfg := monitor.CollectorConfig{}
	if cfg.Monitor != nil {
		monCfg = *cfg.Monitor
	}
	monitor.Init(monCfg)
	// /metrics 与 /metrics/summary 挂 DefaultServeMux，与 pprof 同端口对外。
	// Agent 模式同样注册：指标虽然会随心跳上报给 master，但排查施压机自身问题
	// （decode 排队、分发唤醒延迟等每动作细分）时需要在机器上直接取当前快照，
	// 没有这个口就只能为了看一个数重跑一轮压测。
	monitor.RegisterHandlers(monitor.Global())

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
