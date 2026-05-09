package admin

import (
	"net/http"
	"strconv"

	"stressbot/logview"
)

func stringOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func intOr(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// secsOr int 秒数 → Go duration 字符串（"5s"）。
func secsOr(v, fallback int) string {
	if v <= 0 {
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
