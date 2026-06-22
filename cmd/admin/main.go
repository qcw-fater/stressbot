package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"stressbot/admin"
	"stressbot/logview"
	"stressbot/utils"
	stresslog "stressbot/utils/log"

	// pprof 通过 utils.StartPprofServer 间接引用
	_ "net/http/pprof"

	"go.uber.org/zap"
)

// Version 编译时注入：-ldflags "-X main.Version=v1.0.0"
var Version = "dev"

func main() {
	// 进程级顶层 recover：防止任何未捕获的 panic 直接让进程崩溃，
	// 同时尽量把 stack trace 写入日志而不是仅 stderr。
	defer func() {
		if rec := recover(); rec != nil {
			// 日志系统可能还没初始化，两路都写一份
			fmt.Fprintf(os.Stderr, "[ADMIN] 顶层 panic: %v\n%s\n", rec, debug.Stack())
			stresslog.Error("[ADMIN] 顶层 panic",
				zap.Any("panic", rec),
				zap.String("stack", string(debug.Stack())))
			os.Exit(2)
		}
	}()

	configPath := flag.String("config", "conf/admin-config.json", "Admin 配置文件路径")
	daemonFlag := flag.Bool("d", false, "以守护进程模式运行")
	flag.Parse()

	// -d 模式：fork 子进程后父进程退出
	if *daemonFlag {
		utils.Daemon("-d")
		return
	}

	cfg, err := admin.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 配置中启用守护进程且当前不是守护进程子进程
	if cfg.Daemon && os.Getppid() != 1 {
		utils.Daemon()
		return
	}

	// 初始化日志
	logConf := &stresslog.Config{
		PrintConsole: true,
		LogLevel:     cfg.Log.LogLevel,
		MaxSize:      cfg.Log.MaxSize,
		MaxBackups:   cfg.Log.MaxBackups,
		MaxAge:       30,
		Compress:     true,
	}
	stresslog.InitLog(cfg.Log.Path, "admin", logConf, "")
	newLogger := logview.AttachRingBuffer(stresslog.GetLogger(), 5000, zap.String("SR", "admin"))
	stresslog.ReplaceLogger(newLogger)

	stresslog.Info("[MAIN] Admin 服务器启动", zap.String("version", Version))

	// pprof 调试服务（独立端口，不依赖 monitor）
	var stopPprof func()
	if cfg.Pprof != nil {
		pprofPort := cfg.Pprof.Port
		if pprofPort <= 0 {
			pprofPort = 6060
		}
		stopPprof = utils.StartPprofServer(pprofPort)
	}

	srv, err := admin.NewAdminServer(*cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化 Admin 服务器失败: %v\n", err)
		os.Exit(1)
	}

	if err := srv.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Admin 服务器退出: %v\n", err)
		os.Exit(1)
	}
	if stopPprof != nil {
		stopPprof()
	}
}
