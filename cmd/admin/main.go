package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"

	"stressbot/admin"
	"stressbot/logview"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

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
	flag.Parse()

	cfg, err := admin.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 初始化日志
	logConf := &stresslog.Config{
		PrintConsole: true,
		LogLevel:     cfg.Log.Level,
		MaxSize:      cfg.Log.MaxSizeMB,
		MaxBackups:   cfg.Log.MaxBackups,
		MaxAge:       30,
		Compress:     true,
	}
	stresslog.InitLog(cfg.Log.Path, "admin", logConf, "")
	newLogger := logview.AttachRingBuffer(stresslog.GetLogger(), 5000, zap.String("SR", "admin"))
	stresslog.ReplaceLogger(newLogger)

	srv, err := admin.NewAdminServer(*cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化 Admin 服务器失败: %v\n", err)
		os.Exit(1)
	}

	if err := srv.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Admin 服务器退出: %v\n", err)
		os.Exit(1)
	}
}
