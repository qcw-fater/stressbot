// Package jsonx 提供基于 bytedance/sonic 的高性能 JSON 实现，
// 接口与 encoding/json 完全兼容，作为 drop-in 替换使用：
//
//	import json "stressbot/utils/jsonx"
//
// 仅需改动 import 行，调用点 json.Marshal / Unmarshal / NewEncoder / NewDecoder /
// MarshalIndent / RawMessage 等均无需修改。
//
// 默认采用 sonic.ConfigStd（HTML 转义 + map key 排序），与 encoding/json 行为一致，
// 保证序列化输出稳定可复现（配置文件、历史归档、RPC 等控制面对一致性敏感）。
package jsonx

import (
	"encoding/json"
	"io"

	"github.com/bytedance/sonic"
)

// std 与 encoding/json 行为兼容的 sonic 实例。
var std = sonic.ConfigStd

// RawMessage 复用标准库类型定义（底层即 []byte），sonic 完全兼容其编解码。
type RawMessage = json.RawMessage

// Number 复用标准库类型定义，sonic 完全兼容。
type Number = json.Number

// Marshal 等价于 encoding/json.Marshal。
func Marshal(v any) ([]byte, error) {
	return std.Marshal(v)
}

// Unmarshal 等价于 encoding/json.Unmarshal。
func Unmarshal(data []byte, v any) error {
	return std.Unmarshal(data, v)
}

// MarshalIndent 等价于 encoding/json.MarshalIndent。
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	return std.MarshalIndent(v, prefix, indent)
}

// MarshalToString 返回 JSON 编码字符串，省去 []byte→string 的额外拷贝。
func MarshalToString(v any) (string, error) {
	return std.MarshalToString(v)
}

// NewEncoder 等价于 encoding/json.NewEncoder。
func NewEncoder(w io.Writer) sonic.Encoder {
	return std.NewEncoder(w)
}

// NewDecoder 等价于 encoding/json.NewDecoder。
func NewDecoder(r io.Reader) sonic.Decoder {
	return std.NewDecoder(r)
}

// Valid 等价于 encoding/json.Valid。
func Valid(data []byte) bool {
	return std.Valid(data)
}
