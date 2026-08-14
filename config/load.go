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
		var strictErr *toml.StrictMissingError
		if errors.As(err, &strictErr) {
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
// 支持的类型：string、*string、map[string]string、struct（递归）。
// 其他类型（int / bool / slice 等）跳过。
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
		for i := 0; i < rv.NumField(); i++ {
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

	default:
		// int / bool / slice / array / interface 等不展开
		return nil
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
