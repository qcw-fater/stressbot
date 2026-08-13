package shared

import (
	"regexp"
	"strings"
)

// requireSharePattern 匹配 Lua 脚本中对 share 模块的引用：require("share") / require('share')。
var requireSharePattern = regexp.MustCompile(`require\s*\(?\s*['"]share['"]`)

// UsesShare 判断单段 Lua 脚本内容是否使用了共享状态模块。
//
// 检测前先剥离 Lua 注释（行注释 -- 与块注释 --[[ ]]，含长括号 --[=[ ]=]），
// 避免「被注释掉的 require("share")」造成误判。字符串内容保留（require("share")
// 本身就含字符串字面量，剥离会破坏检测）；字符串中手写 require("share") 这种极罕见
// 场景的误判可接受（仅会多创建一次未用到的连接，行为安全）。
func UsesShare(content string) bool {
	return requireSharePattern.MatchString(stripLuaComments(content))
}

// AnyUsesShare 判断一组脚本内容中是否有任意一段使用共享状态模块。
func AnyUsesShare(contents map[string]string) bool {
	for _, c := range contents {
		if UsesShare(c) {
			return true
		}
	}
	return false
}

// stripLuaComments 移除 Lua 源码中的注释，保留代码与字符串字面量。
// 正确处理：短字符串 "..."/'...'、长字符串 [[...]]/[=[...]=]、
// 行注释 --...、块注释 --[[...]]/--[=[...]=]。
func stripLuaComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	i, n := 0, len(src)
	for i < n {
		c := src[i]
		switch {
		case c == '"' || c == '\'':
			// 短字符串：原样拷贝（含转义）
			j := i + 1
			for j < n {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == c {
					j++
					break
				}
				j++
			}
			if j > n {
				j = n
			}
			b.WriteString(src[i:j])
			i = j
		case c == '[' && isLongBracketOpen(src, i):
			// 长字符串：原样拷贝
			level := longBracketLevel(src, i)
			end := skipLongBracket(src, i, level)
			b.WriteString(src[i:end])
			i = end
		case c == '-' && i+1 < n && src[i+1] == '-':
			// 注释
			j := i + 2
			if j < n && src[j] == '[' && isLongBracketOpen(src, j) {
				// 块注释
				level := longBracketLevel(src, j)
				i = skipLongBracket(src, j, level)
			} else {
				// 行注释：跳到行尾（保留换行符）
				for j < n && src[j] != '\n' {
					j++
				}
				i = j
			}
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// isLongBracketOpen 判断 src[pos] 起是否为长括号开头 `[` `=`* `[`。
func isLongBracketOpen(src string, pos int) bool {
	i := pos + 1
	for i < len(src) && src[i] == '=' {
		i++
	}
	return i < len(src) && src[i] == '['
}

// longBracketLevel 返回长括号的等号级别（pos 处须是长括号开头）。
func longBracketLevel(src string, pos int) int {
	i := pos + 1
	level := 0
	for i < len(src) && src[i] == '=' {
		level++
		i++
	}
	return level
}

// skipLongBracket 跳过整个长括号块，返回闭合括号之后的索引；未闭合则返回 len(src)。
func skipLongBracket(src string, pos, level int) int {
	start := pos + level + 2 // 跳过 `[` `=`*level `[`
	closing := "]" + strings.Repeat("=", level) + "]"
	idx := strings.Index(src[start:], closing)
	if idx < 0 {
		return len(src)
	}
	return start + idx + len(closing)
}
