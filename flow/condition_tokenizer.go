// Package flow 提供流程执行引擎。
// cond_tokenizer.go 把条件表达式（已剥离 state: 前缀）切成 token 序列。
//
// 字面量只有两类：数字（123、1.5）和带引号字符串（"member"）。裸标识符恒为
// state 路径。无 true/false/nil 等关键字——存在性由"按类型的非零比较"表达。
package flow

import "fmt"

// tokKind token 类型。
type tokKind int

const (
	tokEOF    tokKind = iota
	tokNumber         // 数字字面量（整数或小数）
	tokString         // 字符串字面量（带引号，lit 为去引号内容）
	tokPath           // state 路径（标识符 + 可选 .seg / [N] 续段）
	tokOp             // 运算符：|| && == != >= <= > < + - * / % !
	tokLParen         // (
	tokRParen         // )
)

// token 单个词法单元。
type token struct {
	kind tokKind
	lit  string // number/path：原始文本；string：去引号内容；op：运算符文本
	pos  int    // 在输入中的字节偏移，用于错误定位
}

// tokenize 将表达式切分为 token 序列，末尾追加 EOF 哨兵。
// 遇到非法输入（单字符 =/|/&、未闭合字符串、空/非数字数组下标、未知字符等）返回错误。
func tokenize(input string) ([]token, error) {
	var toks []token
	i := 0
	n := len(input)
	for i < n {
		c := input[i]

		// 跳过空白
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}

		start := i
		switch {
		case isDigit(c):
			// NUMBER：数字开头，允许一个小数点（点后必须跟数字）
			i++
			for i < n && isDigit(input[i]) {
				i++
			}
			if i < n && input[i] == '.' && i+1 < n && isDigit(input[i+1]) {
				i++ // 消费 '.'
				for i < n && isDigit(input[i]) {
					i++
				}
			}
			toks = append(toks, token{kind: tokNumber, lit: input[start:i], pos: start})

		case c == '"':
			// STRING：无转义，读到下一个 "
			i++
			for i < n && input[i] != '"' {
				i++
			}
			if i >= n {
				return nil, fmt.Errorf("未闭合的字符串字面量（位置 %d）", start)
			}
			lit := input[start+1 : i] // 去掉首尾引号
			i++                       // 消费闭合 "
			toks = append(toks, token{kind: tokString, lit: lit, pos: start})

		case isIdentStart(c):
			// PATH：标识符 + 可选 .seg / [N] 续段
			i++
			for i < n && isIdentCont(input[i]) {
				i++
			}
		pathLoop:
			for i < n {
				switch {
				case input[i] == '.' && i+1 < n && isIdentStart(input[i+1]):
					i++ // 消费 '.'
					for i < n && isIdentCont(input[i]) {
						i++
					}
				case input[i] == '[':
					// 数组下标 [N]，仅数字
					j := i + 1
					for j < n && isDigit(input[j]) {
						j++
					}
					if j == i+1 {
						return nil, fmt.Errorf("数组下标必须为数字（位置 %d）", i)
					}
					if j >= n || input[j] != ']' {
						return nil, fmt.Errorf("数组下标缺少右括号 ]（位置 %d）", i)
					}
					i = j + 1 // 消费到 ] 之后
				default:
					break pathLoop
				}
			}
			toks = append(toks, token{kind: tokPath, lit: input[start:i], pos: start})

		case c == '(':
			toks = append(toks, token{kind: tokLParen, lit: "(", pos: start})
			i++
		case c == ')':
			toks = append(toks, token{kind: tokRParen, lit: ")", pos: start})
			i++

		case c == '|' || c == '&' || c == '=':
			// 双字符运算符 ||、&&、==；单字符 =/|/& 视为笔误报错
			if i+1 < n && input[i+1] == c {
				toks = append(toks, token{kind: tokOp, lit: input[i : i+2], pos: start})
				i += 2
			} else {
				return nil, fmt.Errorf("非法字符 %q（位置 %d，是否想用 %c%c）", c, start, c, c)
			}
		case c == '!':
			if i+1 < n && input[i+1] == '=' {
				toks = append(toks, token{kind: tokOp, lit: "!=", pos: start})
				i += 2
			} else {
				toks = append(toks, token{kind: tokOp, lit: "!", pos: start})
				i++
			}
		case c == '>':
			if i+1 < n && input[i+1] == '=' {
				toks = append(toks, token{kind: tokOp, lit: ">=", pos: start})
				i += 2
			} else {
				toks = append(toks, token{kind: tokOp, lit: ">", pos: start})
				i++
			}
		case c == '<':
			if i+1 < n && input[i+1] == '=' {
				toks = append(toks, token{kind: tokOp, lit: "<=", pos: start})
				i += 2
			} else {
				toks = append(toks, token{kind: tokOp, lit: "<", pos: start})
				i++
			}
		case c == '+' || c == '-' || c == '*' || c == '/' || c == '%':
			toks = append(toks, token{kind: tokOp, lit: string(c), pos: start})
			i++

		default:
			return nil, fmt.Errorf("非法字符 %q（位置 %d）", c, start)
		}
	}
	toks = append(toks, token{kind: tokEOF, pos: n})
	return toks, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}
func isIdentCont(c byte) bool { return isIdentStart(c) || isDigit(c) }
