package codec

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// LoadErrorMap 读取 errors.json：扁平 {"code":"中文描述"}，code 字符串解析为 uint64。
// 缺失文件、JSON 非法、key 非数字均报错。
func LoadErrorMap(path string) (map[uint64]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取错误码文件失败 %q: %w", path, err)
	}
	var in map[string]string
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("解析错误码文件失败 %q: %w", path, err)
	}
	out := make(map[uint64]string, len(in))
	for k, v := range in {
		code, parseErr := strconv.ParseUint(k, 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("错误码 key %q 不是合法数字: %w", k, parseErr)
		}
		out[code] = v
	}
	return out, nil
}

// DescribeError 返回错误码对应中文描述；未命中返回空串 ""（v1 冻结默认）。
func DescribeError(m map[uint64]string, code uint64) string {
	if m == nil {
		return ""
	}
	return m[code]
}
