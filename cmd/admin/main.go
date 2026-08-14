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

	"stressbot/admin"
	"stressbot/config"
	"stressbot/internal/daemon"
	"stressbot/internal/stresslog"

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
	flags := flag.NewFlagSet("admin", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.configPath, "config", "conf/admin.toml", "Admin 配置文件路径")
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
			fmt.Fprintf(os.Stderr, "[ADMIN] 顶层 panic: %v\n%s\n", rec, stack)
			if stresslog.GetLogger() != nil {
				stresslog.Error("[ADMIN] 顶层 panic", zap.Any("panic", rec), zap.String("stack", string(stack)))
			}
			exitCode = 2
		}
		if closeLog != nil {
			if err := closeLog(); err != nil {
				fmt.Fprintf(os.Stderr, "关闭 Admin 日志失败: %v\n", err)
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

	cfg, err := admin.LoadConfig(opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 1
	}
	if cfg.Daemon && os.Getppid() != 1 {
		daemon.Daemon()
		return 0
	}

	closeLog = stresslog.InitLog(cfg.Log.Path, "admin", &cfg.Log, "")
	stresslog.Info("[MAIN] Admin 服务器启动", zap.String("version", Version))

	server, err := admin.NewServer(*cfg)
	if err != nil {
		stresslog.Error("[MAIN] 初始化 Admin 服务器失败", zap.Error(err))
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var stopPprof func()
	if cfg.Pprof != nil {
		port := cfg.Pprof.Port
		if port <= 0 {
			port = 6060
		}
		stopPprof = config.StartPprofServer(ctx, port)
		defer stopPprof()
	}

	if err := server.Run(ctx); err != nil {
		stresslog.Error("[MAIN] Admin 服务器退出", zap.Error(err))
		return 1
	}
	return 0
}
