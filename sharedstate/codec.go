package sharedstate

import (
	"fmt"
	"strings"

	json "stressbot/utils/jsonx"
)

// maxSafeInt Lua double 可精确表示的最大整数（2^53）。
// 与 script/api_robot.go 的 maxSafeInt 保持一致：超过该范围的大整数转字符串，
// 避免在 Lua/JSON 之间丢精度。
const maxSafeInt = int64(1) << 53

// encodeValue 把 Go 值（来自 Lua 的 any）编码为 JSON 字符串存入 Redis。
// 支持：nil / bool / number / string / []any / map[string]any。
// 不支持：function / channel / 复杂结构（来自 Lua 的值不会出现这些）。
func encodeValue(v any) (string, error) {
	if v == nil {
		return "null", nil
	}
	switch v.(type) {
	case bool, float64, float32, int, int32, int64, uint, uint32, uint64, string, []byte, []any, map[string]any:
		// 允许的类型
	default:
		return "", fmt.Errorf("sharedstate: 不支持的值类型 %T", v)
	}
	s, err := json.MarshalToString(v)
	if err != nil {
		return "", fmt.Errorf("sharedstate: JSON 编码失败: %w", err)
	}
	return s, nil
}

// decodeValue 把 Redis 中的 JSON 字符串还原为 Go 值（供转回 Lua）。
//
// 使用 json.Decoder + UseNumber，避免数字过早变 float64：
//   - 可安全用 Lua number 表示的整数（|n| <= 2^53）→ int64 或 float64。
//   - 超出安全范围的大整数 → string（与 script 层 goValueToLua 的策略一致）。
//   - 含小数/指数的数字 → float64。
//
// strings.NewReader 直接读取字符串，省去 []byte(s) 的额外拷贝。
func decodeValue(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("sharedstate: JSON 解码失败: %w", err)
	}
	return normalizeNumbers(raw), nil
}

// normalizeNumbers 递归把 json.Number 转成 int64 / float64 / string（大整数）。
func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		return normalizeNumber(x)
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeNumbers(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = normalizeNumbers(e)
		}
		return x
	default:
		return v
	}
}

func normalizeNumber(n json.Number) any {
	s := n.String()
	// 整数：尝试 int64；超过安全范围保留字符串
	if i, err := n.Int64(); err == nil {
		if i > maxSafeInt || i < -maxSafeInt {
			return s
		}
		return i
	}
	// 浮点
	if f, err := n.Float64(); err == nil {
		return f
	}
	// 解析不了就原样返回字符串（极端大整数）
	return s
}
