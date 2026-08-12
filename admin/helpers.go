package admin

import (
	"strconv"

	"stressbot/sharedstate"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

// taskUsesShare 扫描任务的 Lua 脚本，判断是否使用了共享状态模块（require("share")）。
func taskUsesShare(task *Task) bool {
	if task == nil {
		return false
	}
	for _, content := range task.Config.LuaScripts {
		if sharedstate.UsesShare(string(content)) {
			return true
		}
	}
	return false
}

func stringOr(v, fallback string, label ...string) string {
	if v == "" {
		tag := "unknown"
		if len(label) > 0 {
			tag = label[0]
		}
		stresslog.Warn("[CONFIG] 配置未填写，使用默认值",
			zap.String("key", tag),
			zap.String("default", fallback))
		return fallback
	}
	return v
}

func intOr(v, fallback int, label ...string) int {
	if v <= 0 {
		tag := "unknown"
		if len(label) > 0 {
			tag = label[0]
		}
		stresslog.Warn("[CONFIG] 配置未填写，使用默认值",
			zap.String("key", tag),
			zap.Int("default", fallback))
		return fallback
	}
	return v
}

// secsOr int 秒数 → Go duration 字符串（"5s"）。
func secsOr(v, fallback int, label ...string) string {
	if v <= 0 {
		tag := "unknown"
		if len(label) > 0 {
			tag = label[0]
		}
		stresslog.Warn("[CONFIG] 配置未填写，使用默认值",
			zap.String("key", tag),
			zap.Int("default", fallback))
		v = fallback
	}
	return strconv.Itoa(v) + "s"
}
