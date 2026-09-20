// Package config 提供三种运行模式共用的配置加载与基础配置类型。
//
// 本文件定义配置加载的共享基础设施：
//   - LoadTOML 泛型加载函数（Defaults 填充 + 严格未知字段检查）
//   - ExpandConfigStrings 环境变量展开（${VAR} / ${VAR:-default}，未定义报错）
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/fluxcd/pkg/envsubst"
	"github.com/pelletier/go-toml/v2"
)

// LoadTOML 从 TOML 文件加载配置。
//
// 流程：读取文件 → 用 defaults 作为基底 → TOML 解析 → 严格未知字段检查
// （未知字段报错，拼写错误立即暴露）→ 环境变量展开 → 返回。
//
// 参数：
//   - path: TOML 文件路径
//   - defaults: 预填默认值的配置结构体（零值字段会被文件覆盖）
//
// 泛型约束：T 必须是可寻址的 struct（反射展开需要写回字段）。
func LoadTOML[T any](path string, defaults T) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件: %w", err)
	}

	cfg := defaults
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		if strictErr, ok := errors.AsType[*toml.StrictMissingError](err); ok {
			keys := make([]string, 0, len(strictErr.Errors))
			for i := range strictErr.Errors {
				keys = append(keys, strings.Join(strictErr.Errors[i].Key(), "."))
			}
			return nil, fmt.Errorf("配置文件 %s 包含未知字段: %s: %w",
				path, strings.Join(keys, ", "), err)
		}
		return nil, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}

	if err := ExpandConfigStrings(&cfg); err != nil {
		return nil, fmt.Errorf("展开环境变量: %w", err)
	}

	return &cfg, nil
}

// ExpandConfigStrings 反射遍历 v 的所有字符串字段（含 map[string]string 的 value），
// 执行 ${VAR} / ${VAR:-default} 环境变量展开。
//
// 展开规则：
//   - ${VAR}          — 展开，未定义则返回 error（fail-loud，避免密码静默为空）
//   - ${VAR:-default} — 展开，未定义则用 default
//   - $$              — 转义为字面 $
//   - 不含 $ 的字符串  — 原样返回（零开销）
//
// 支持的类型：string、*string、map[string]string、struct、slice/array（均递归）。
// 其他标量类型（int / bool 等）跳过。
func ExpandConfigStrings(v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr {
		return fmt.Errorf("ExpandConfigStrings: 参数必须是指针，得到 %T", v)
	}
	return expandValue(rv.Elem())
}

// expandValue 递归展开一个 reflect.Value。
func expandValue(rv reflect.Value) error {
	switch rv.Kind() {
	case reflect.String:
		expanded, err := expandString(rv.String())
		if err != nil {
			return err
		}
		if expanded != rv.String() {
			rv.SetString(expanded)
		}
		return nil

	case reflect.Ptr:
		if rv.IsNil() {
			return nil
		}
		return expandValue(rv.Elem())

	case reflect.Struct:
		for i := range rv.NumField() {
			field := rv.Field(i)
			// 跳过不可导出字段
			if !field.CanInterface() {
				continue
			}
			if err := expandValue(field); err != nil {
				return err
			}
		}
		return nil

	case reflect.Map:
		// 仅处理 map[string]string
		if rv.Type().Key().Kind() != reflect.String || rv.Type().Elem().Kind() != reflect.String {
			return nil
		}
		if rv.IsNil() {
			return nil
		}
		for _, key := range rv.MapKeys() {
			val := rv.MapIndex(key)
			expanded, err := expandString(val.String())
			if err != nil {
				return err
			}
			if expanded != val.String() {
				rv.SetMapIndex(key, reflect.ValueOf(expanded))
			}
		}
		return nil

	case reflect.Slice, reflect.Array:
		// 展开必须覆盖切片元素：地址列表、目录列表这类 []string 配置项与标量字段
		// 一样会写 ${VAR}，漏掉它们等于让这些字段静默保留未展开的字面量。
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		// 元素类型不可能含字符串（[]byte / []int 之类）时整段跳过，避免逐元素空转。
		if !mayContainString(rv.Type().Elem()) {
			return nil
		}
		for i := range rv.Len() {
			if err := expandValue(rv.Index(i)); err != nil {
				return err
			}
		}
		return nil

	default:
		// int / bool / interface 等不展开
		return nil
	}
}

// mayContainString 判断该类型自身或其内部是否可能藏着待展开的字符串。
func mayContainString(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String, reflect.Struct, reflect.Map, reflect.Interface:
		return true
	case reflect.Ptr, reflect.Slice, reflect.Array:
		return mayContainString(t.Elem())
	default:
		return false
	}
}

// expandString 对单个字符串执行环境变量展开。
func expandString(s string) (string, error) {
	// 快速路径：不含 $ 直接返回（绝大多数配置值不含环境变量引用）
	if !strings.Contains(s, "$") {
		return s, nil
	}

	result, err := envsubst.EvalEnv(s, true)
	if err != nil {
		return s, fmt.Errorf("展开环境变量表达式 %q: %w", s, err)
	}

	return result, nil
}
