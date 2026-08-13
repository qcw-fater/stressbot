// Package config 提供三种运行模式共用的配置加载与基础配置类型。
//
// 本文件定义配置加载的共享基础设施：
//   - LoadTOML 泛型加载函数（Defaults 填充 + 严格未知字段检查）
//   - ExpandConfigStrings 环境变量展开（${VAR} / ${VAR:-default}，未定义报错）
package config

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/drone/envsubst"
	"github.com/drone/envsubst/parse"
)

// defaultOps 是 drone/envsubst 中提供 default/fallback 语义的操作符集合。
// 引用未定义变量时，这些操作符会用 default 值代替（不报错）。
// 完整列表参照 drone/envsubst funcs.go 的 lookupFunc 中映射到 toDefault 的操作符。
var defaultOps = map[string]bool{
	":-": true, ":=": true, "-": true, ":": true,
	"=?": true, ":+": true, "+": true,
}

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
	dec := toml.NewDecoder(bytes.NewReader(data))
	meta, err := dec.Decode(&cfg)
	if err != nil {
		return nil, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}
	// 严格模式：检查未映射到任何 struct 字段的 key（拼写错误立即暴露）
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("配置文件 %s 包含未知字段: %s", path, strings.Join(keys, ", "))
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
//
// 两阶段实现「无 default 的 ${VAR} 在未定义时报错」语义：
//  1. 用 parse.Parse 解析 AST，遍历所有 FuncNode，收集「无 default 操作符 且 环境变量未定义」
//     的变量名；若存在则直接报错（fail-loud，避免密码静默为空）。
//  2. 用 envsubst.Eval 执行展开。mapping 用 os.Getenv——此时未定义引用要么已被
//     阶段 1 拦截（无 default），要么带 default 操作符（os.Getenv 返回空串触发 default 分支）。
func expandString(s string) (string, error) {
	// 快速路径：不含 $ 直接返回（绝大多数配置值不含环境变量引用）
	if !strings.Contains(s, "$") {
		return s, nil
	}

	tree, err := parse.Parse(s)
	if err != nil {
		return s, fmt.Errorf("解析环境变量表达式 %q: %w", s, err)
	}

	// 阶段 1：检查无 default 操作符的未定义变量引用
	undefinedVars := collectUndefinedNoDefault(tree.Root)
	if len(undefinedVars) > 0 {
		return s, fmt.Errorf("配置值 %q 引用了未定义的环境变量: %s",
			s, strings.Join(undefinedVars, ", "))
	}

	// 阶段 2：执行展开
	result, err := envsubst.Eval(s, os.Getenv)
	if err != nil {
		return s, fmt.Errorf("展开环境变量表达式 %q: %w", s, err)
	}

	return result, nil
}

// collectUndefinedNoDefault 递归遍历 AST，收集「无 default 操作符 且 环境变量未定义」的变量名。
// 去重保序返回。
func collectUndefinedNoDefault(node parse.Node) []string {
	var result []string
	seen := make(map[string]bool)

	var walk func(parse.Node)
	walk = func(n parse.Node) {
		switch v := n.(type) {
		case *parse.FuncNode:
			// 无 default 操作符（Name 为空或不在 defaultOps 中）且环境变量未定义
			if !defaultOps[v.Name] {
				if _, ok := os.LookupEnv(v.Param); !ok {
					if !seen[v.Param] {
						seen[v.Param] = true
						result = append(result, v.Param)
					}
				}
			}
			// 递归处理参数节点（default 值本身可能也含 ${VAR}）
			for _, arg := range v.Args {
				walk(arg)
			}
		case *parse.ListNode:
			for _, child := range v.Nodes {
				walk(child)
			}
		}
	}

	walk(node)
	return result
}
