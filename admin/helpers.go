package admin

import (
	"net/http"
	"strconv"

	"stressbot/logview"

	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
)

func stringOr(v, fallback string, label ...string) string {
	if v == "" {
		tag := "unknown"
		if len(label) > 0 {
			tag = label[0]
		}
		stresslog.Warn("[CONFIG] 配置为空，使用默认值",
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
		stresslog.Warn("[CONFIG] 配置非法，使用默认值",
			zap.String("key", tag),
			zap.Int("value", v),
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
		stresslog.Warn("[CONFIG] 配置非法，使用默认值",
			zap.String("key", tag),
			zap.Int("value", v),
			zap.Int("default", fallback))
		v = fallback
	}
	return strconv.Itoa(v) + "s"
}

func parseLogQueryParams(r *http.Request) logview.QueryParams {
	q := r.URL.Query()
	limit := parseIntOrDefault(q.Get("limit"), 200)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	return logview.QueryParams{
		AfterSeq: logview.ParseUint64OrDefault(q.Get("afterSeq"), 0),
		Limit:    limit,
	}
}

// parseUint64OrDefault 解析 uint64 参数。
func parseUint64OrDefault(s string, def uint64) uint64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return def
	}
	return v
}
