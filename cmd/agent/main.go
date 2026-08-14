package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"stressbot/agent"
	"stressbot/config"
	"stressbot/internal/daemon"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"

	"go.uber.org/zap"
)

// Version 编译时注入：-ldflags "-X main.Version=v1.0.0"
var Version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

type options struct {
	configPath string
	daemon     bool
}

func parseOptions(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.configPath, "config", "conf/agent.toml", "Agent 配置文件路径")
	flags.BoolVar(&opts.daemon, "d", false, "以守护进程模式运行")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	return opts, nil
}

func run(args []string) (exitCode int) {
	var closeLog func() error
	defer func() {
		if rec := recover(); rec != nil {
			stack := debug.Stack()
			fmt.Fprintf(os.Stderr, "[AGENT] 顶层 panic: %v\n%s\n", rec, stack)
			if stresslog.GetLogger() != nil {
				stresslog.Error("[AGENT] 顶层 panic", zap.Any("panic", rec), zap.String("stack", string(stack)))
			}
			exitCode = 2
		}
		if closeLog != nil {
			if err := closeLog(); err != nil {
				fmt.Fprintf(os.Stderr, "关闭 Agent 日志失败: %v\n", err)
			}
		}
	}()

	opts, err := parseOptions(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析启动参数失败: %v\n", err)
		return 1
	}

	if opts.daemon {
		daemon.Daemon("-d")
		return 0
	}

	cfg, err := agent.LoadConfig(opts.configPath, Version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 1
	}
	if cfg.Daemon && os.Getppid() != 1 {
		daemon.Daemon()
		return 0
	}

	logPath := "log/agent.log"
	if cfg.Log != nil && cfg.Log.Path != "" {
		logPath = cfg.Log.Path
	}
	closeLog = stresslog.InitLog(logPath, "agent", cfg.Log, "")
	stresslog.Info("[MAIN] Agent 模式启动", zap.String("version", Version), zap.String("adminAddress", cfg.Agent.AdminAddress))

	node, err := agent.NewFromConfig(cfg)
	if err != nil {
		stresslog.Error("[MAIN] 初始化 Agent 失败", zap.Error(err))
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	defer workpool.Default().Shutdown()
	if cfg.Pprof != nil {
		port := cfg.Pprof.Port
		if port <= 0 {
			port = 6060
		}
		stopPprof := config.StartPprofServer(ctx, port)
		defer stopPprof()
	}

	if err := node.Run(ctx); err != nil {
		stresslog.Error("[MAIN] Agent 运行失败", zap.Error(err))
		return 1
	}
	return 0
}
