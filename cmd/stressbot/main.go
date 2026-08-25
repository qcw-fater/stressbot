// Package main 是单机压测模式（standalone）的进程入口：解析启动参数
// （-config 及 flow/proto/scripts/adapter 资源路径覆盖）与守护进程化后，
// 加载配置、初始化日志与可选 pprof，并在单进程内加载资源、启动机器人加压。
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

	"stressbot/config"
	"stressbot/internal/daemon"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"
	"stressbot/standalone"

	"go.uber.org/zap"
)

// Version 编译时注入：-ldflags "-X main.Version=v1.0.0"
var Version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

type options struct {
	configPath string
	flowPath   string
	protoDir   string
	scriptsDir string
	adapterDir string
	daemon     bool
}

func parseOptions(args []string) (options, error) {
	var opts options
	flags := flag.NewFlagSet("stressbot", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.configPath, "config", "conf/stressbot.toml", "配置文件路径")
	flags.StringVar(&opts.flowPath, "flow", "", "流程配置文件路径（默认 <conf>/flow/flow.json）")
	flags.StringVar(&opts.protoDir, "proto", "", "proto 目录路径（默认 <conf>/proto）")
	flags.StringVar(&opts.scriptsDir, "scripts", "", "Lua 脚本目录路径（默认 <conf>/scripts）")
	flags.StringVar(&opts.adapterDir, "adapter", "", "适配器目录路径（默认 <conf>/adapter）")
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
			fmt.Fprintf(os.Stderr, "[STRESSBOT] 顶层 panic: %v\n%s\n", rec, stack)
			if stresslog.GetLogger() != nil {
				stresslog.Error("[STRESSBOT] 顶层 panic", zap.Any("panic", rec), zap.String("stack", string(stack)))
			}
			exitCode = 2
		}
		if closeLog != nil {
			if err := closeLog(); err != nil {
				fmt.Fprintf(os.Stderr, "关闭 stressbot 日志失败: %v\n", err)
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

	cfg, err := standalone.LoadConfig(opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 1
	}
	if cfg.Daemon && os.Getppid() != 1 {
		daemon.Daemon()
		return 0
	}
	paths, err := standalone.ResolvePaths(opts.configPath, opts.flowPath, opts.protoDir, opts.scriptsDir, opts.adapterDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	logPath := "log/stressbot.log"
	if cfg.Log != nil && cfg.Log.Path != "" {
		logPath = cfg.Log.Path
	}
	closeLog = stresslog.InitLog(logPath, "stressbot", cfg.Log, "")
	stresslog.Info("[MAIN] stressbot 启动", zap.String("version", Version))

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

	if err := standalone.Run(ctx, cfg, paths); err != nil {
		stresslog.Error("[MAIN] 单机模式运行失败", zap.Error(err))
		return 1
	}
	return 0
}
