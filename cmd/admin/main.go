package main

import (
	"flag"
	"fmt"
	"os"

	"stressbot/admin"
	"stressbot/logview"
	stresslog "stressbot/utils/log"
)

func main() {
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
	newLogger := logview.AttachRingBuffer(stresslog.GetLogger(), 5000)
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
