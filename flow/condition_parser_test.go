package flow

import (
	"testing"

	"stressbot/state"
)

func newCondStore(data map[string]any) *state.Store {
	s := state.NewStore()
	for k, v := range data {
		s.Set(k, v)
	}
	return s
}

func evalCompiledCondition(expression string, store *state.Store) bool {
	condition, err := compileCondition(PrefixState + expression)
	if err != nil {
		return false
	}
	return condition.EvalState(store)
}

func evalFullCompiledCondition(expression string, store *state.Store) bool {
	condition, err := compileCondition(expression)
	if err != nil {
		return false
	}
	return condition.EvalState(store)
}

func TestParseExpr_SingleKey(t *testing.T) {
	s := newCondStore(map[string]any{"alive": true})
	if !evalCompiledCondition("alive", s) {
		t.Error("alive=true should be true")
	}

	s2 := newCondStore(map[string]any{"alive": false})
	if evalCompiledCondition("alive", s2) {
		t.Error("alive=false should be false")
	}
}

func TestParseExpr_SingleComparison(t *testing.T) {
	s := newCondStore(map[string]any{"hp": int64(50)})
	if !evalCompiledCondition("hp > 0", s) {
		t.Error("hp=50 > 0 should be true")
	}
	if evalCompiledCondition("hp > 100", s) {
		t.Error("hp=50 > 100 should be false")
	}
	if !evalCompiledCondition("hp >= 50", s) {
		t.Error("hp=50 >= 50 should be true")
	}
}

func TestParseExpr_SingleKey_Nil(t *testing.T) {
	// missing key → 错误（warn）+ false
	s := newCondStore(map[string]any{})
	if evalCompiledCondition("missing", s) {
		t.Error("missing key should be false")
	}
}

// 严格类型：裸数值不再隐式 truthy。必须显式比较。
func TestParseExpr_SingleKey_IntRequiresExplicitCompare(t *testing.T) {
	// count=3 裸用 → 非布尔上下文 → false
	s := newCondStore(map[string]any{"count": int64(3)})
	if evalCompiledCondition("count", s) {
		t.Error("count=3 bare should be false (非布尔上下文)")
	}
	// 显式比较才合法
	if !evalCompiledCondition("count != 0", s) {
		t.Error("count=3 != 0 should be true")
	}

	// count=0
	s2 := newCondStore(map[string]any{"count": int64(0)})
	if !evalCompiledCondition("count == 0", s2) {
		t.Error("count=0 == 0 should be true")
	}
	if evalCompiledCondition("count != 0", s2) {
		t.Error("count=0 != 0 should be false")
	}
}

func TestParseExpr_And(t *testing.T) {
	s := newCondStore(map[string]any{"a": true, "b": true})
	if !evalCompiledCondition("a && b", s) {
		t.Error("a && b (both true) should be true")
	}

	s2 := newCondStore(map[string]any{"a": true, "b": false})
	if evalCompiledCondition("a && b", s2) {
		t.Error("a && b (b=false) should be false")
	}
}

func TestParseExpr_Or(t *testing.T) {
	s := newCondStore(map[string]any{"a": false, "b": true})
	if !evalCompiledCondition("a || b", s) {
		t.Error("a || b (b=true) should be true")
	}

	s2 := newCondStore(map[string]any{"a": false, "b": false})
	if evalCompiledCondition("a || b", s2) {
		t.Error("a || b (both false) should be false")
	}
}

func TestParseExpr_AndOr(t *testing.T) {
	s := newCondStore(map[string]any{"a": true, "b": true, "c": false})
	if !evalCompiledCondition("a && b || c", s) {
		t.Error("a && b || c should be true")
	}

	s2 := newCondStore(map[string]any{"a": false, "b": true, "c": false})
	if evalCompiledCondition("a && b || c", s2) {
		t.Error("a && b || c (a=false,c=false) should be false")
	}
}

func TestParseExpr_Parens(t *testing.T) {
	s := newCondStore(map[string]any{"a": true, "b": false, "c": false})
	if !evalCompiledCondition("a || (b && c)", s) {
		t.Error("a || (b && c) should be true")
	}

	s2 := newCondStore(map[string]any{"a": false, "b": true, "c": true})
	if !evalCompiledCondition("(a || b) && c", s2) {
		t.Error("(a || b) && c should be true")
	}
}

func TestParseExpr_Not(t *testing.T) {
	s := newCondStore(map[string]any{"dead": true})
	if evalCompiledCondition("!dead", s) {
		t.Error("!dead (dead=true) should be false")
	}

	s2 := newCondStore(map[string]any{"dead": false})
	if !evalCompiledCondition("!dead", s2) {
		t.Error("!dead (dead=false) should be true")
	}
}

func TestParseExpr_NotWithComparison(t *testing.T) {
	s := newCondStore(map[string]any{"hp": int64(0)})
	if !evalCompiledCondition("!(hp > 0)", s) {
		t.Error("!(hp > 0) with hp=0 should be true")
	}
}

func TestParseExpr_Complex(t *testing.T) {
	s := newCondStore(map[string]any{
		"hp":    int64(80),
		"alive": true,
		"admin": false,
		"level": int64(10),
	})
	if !evalCompiledCondition("hp > 0 && (alive || admin)", s) {
		t.Error("complex expr 1 should be true")
	}
	if !evalCompiledCondition("level >= 10 || (alive && admin)", s) {
		t.Error("complex expr 2 should be true")
	}
	if !evalCompiledCondition("!admin && hp > 50", s) {
		t.Error("complex expr 3 should be true")
	}
}

func TestParseExpr_Empty(t *testing.T) {
	s := newCondStore(map[string]any{})
	if !evalCompiledCondition("", s) {
		t.Error("empty expr should be true")
	}
	if !evalCompiledCondition("  ", s) {
		t.Error("whitespace-only expr should be true")
	}
}

func TestParseExpr_MultipleAnd(t *testing.T) {
	s := newCondStore(map[string]any{"a": true, "b": true, "c": true})
	if !evalCompiledCondition("a && b && c", s) {
		t.Error("a && b && c (all true) should be true")
	}

	s2 := newCondStore(map[string]any{"a": true, "b": true, "c": false})
	if evalCompiledCondition("a && b && c", s2) {
		t.Error("a && b && c (c=false) should be false")
	}
}

func TestParseExpr_MultipleOr(t *testing.T) {
	s := newCondStore(map[string]any{"a": false, "b": false, "c": true})
	if !evalCompiledCondition("a || b || c", s) {
		t.Error("a || b || c (c=true) should be true")
	}
}

// ─── 算术 ────────────────────────────────────────────────

func TestParseExpr_ArithmeticPrecedence(t *testing.T) {
	s := newCondStore(map[string]any{"n": int64(5)})
	if !evalCompiledCondition("n + 2 * 3 == 11", s) { // 5 + 6
		t.Error("n + 2*3 == 11 should be true")
	}
	if !evalCompiledCondition("(n + 2) * 3 == 21", s) { // 7 * 3
		t.Error("(n+2)*3 == 21 should be true")
	}
}

func TestParseExpr_Modulo(t *testing.T) {
	even := newCondStore(map[string]any{"index": int64(4)})
	odd := newCondStore(map[string]any{"index": int64(5)})
	if !evalCompiledCondition("index % 2 == 0", even) {
		t.Error("4 % 2 == 0 should be true")
	}
	if evalCompiledCondition("index % 2 == 0", odd) {
		t.Error("5 % 2 == 0 should be false")
	}
}

// 内置 id/index 经 state.Set 注入为原生 Go int（非 int64）——数值层必须识别，
// 否则 index % 2、index > 0 等会误判为非数值。
func TestParseExpr_BuiltinIntIndex(t *testing.T) {
	even := newCondStore(map[string]any{"index": 4}) // 原生 int
	odd := newCondStore(map[string]any{"index": 5})
	if !evalCompiledCondition("index % 2 == 0", even) {
		t.Error("index(原生 int) % 2 == 0 应为 true")
	}
	if evalCompiledCondition("index % 2 == 0", odd) {
		t.Error("index(原生 int)=5 % 2 == 0 应为 false")
	}
	s := newCondStore(map[string]any{"index": 7})
	if !evalCompiledCondition("index > 0", s) {
		t.Error("index(原生 int) > 0 应为 true")
	}
	if !evalCompiledCondition("index == 7", s) {
		t.Error("index(原生 int) == 7 应为 true")
	}
	if !evalCompiledCondition("-index < 0", s) {
		t.Error("-index(原生 int) < 0 应为 true")
	}
}

func TestParseExpr_Division_IntVsFloat(t *testing.T) {
	// 字面量整除
	if !evalCompiledCondition("7 / 2 == 3", newCondStore(nil)) {
		t.Error("7 / 2 == 3 (整除) should be true")
	}
	if evalCompiledCondition("7 / 2 == 3.5", newCondStore(nil)) {
		t.Error("7 / 2 == 3.5 should be false (整除得 3)")
	}
	// 任一浮点 → 浮点除
	if !evalCompiledCondition("7.0 / 2 == 3.5", newCondStore(nil)) {
		t.Error("7.0 / 2 == 3.5 (浮点除) should be true")
	}
}

func TestParseExpr_UnaryMinus(t *testing.T) {
	s := newCondStore(map[string]any{"hp": int64(5)})
	if !evalCompiledCondition("-hp < 0", s) {
		t.Error("-hp < 0 (hp=5) should be true")
	}
	if !evalCompiledCondition("-5 == -5", newCondStore(nil)) {
		t.Error("-5 == -5 should be true")
	}
}

func TestParseExpr_NegativeDivMod(t *testing.T) {
	// Go 语义：整除向零截断、取模取被除数符号
	if !evalCompiledCondition("-7 / 2 == -3", newCondStore(nil)) {
		t.Error("-7 / 2 == -3 (向零截断) should be true")
	}
	if !evalCompiledCondition("-5 % 3 == -2", newCondStore(nil)) {
		t.Error("-5 % 3 == -2 should be true")
	}
}

// ─── 字符串字面量 ────────────────────────────────────────

func TestParseExpr_StringLiteral(t *testing.T) {
	s := newCondStore(map[string]any{"role": "member", "empty": ""})
	if !evalCompiledCondition("role == \"member\"", s) {
		t.Error("role == \"member\" should be true")
	}
	if !evalCompiledCondition("role != \"guest\"", s) {
		t.Error("role != \"guest\" should be true")
	}
	if !evalCompiledCondition("empty == \"\"", s) {
		t.Error("empty == \"\" should be true")
	}
}

// ─── 类型不匹配（均应 false + warn）─────────────────────

func TestParseExpr_TypeMismatch(t *testing.T) {
	cases := []struct {
		name string
		expr string
		data map[string]any
	}{
		{"int vs string", "hp == \"x\"", map[string]any{"hp": int64(5)}},
		{"bool vs int", "alive == 1", map[string]any{"alive": true}},
		{"number in bool context", "hp && alive", map[string]any{"hp": int64(5), "alive": true}},
		{"bool in arithmetic", "alive + 1 == 1", map[string]any{"alive": true}},
		{"string concat", "\"a\" + \"b\" == \"ab\"", nil},
	}
	for _, c := range cases {
		s := newCondStore(c.data)
		if evalCompiledCondition(c.expr, s) {
			t.Errorf("%s: %q should be false (类型不匹配)", c.name, c.expr)
		}
	}
}

// ─── missing key（均应 false + warn）────────────────────

func TestParseExpr_MissingKey(t *testing.T) {
	s := newCondStore(map[string]any{})
	if evalCompiledCondition("missing == 0", s) {
		t.Error("missing == 0 should be false (key 不存在)")
	}
	if evalCompiledCondition("missing != \"\"", s) {
		t.Error("missing != \"\" should be false (key 不存在)")
	}
}

// ─── 除零 / 取模零 / 浮点取模 ────────────────────────────

func TestParseExpr_ArithmeticErrors(t *testing.T) {
	s := newCondStore(map[string]any{"hp": int64(5)})
	if evalCompiledCondition("hp / 0 == 0", s) {
		t.Error("hp / 0 (除零) should be false")
	}
	if evalCompiledCondition("hp % 0 == 0", s) {
		t.Error("hp % 0 (取模零) should be false")
	}
	if evalCompiledCondition("1.5 % 2 == 0", newCondStore(nil)) {
		t.Error("1.5 % 2 (浮点取模) should be false")
	}
}

// ─── uint64 精确比较（防 2^53 失真）────────────────────

func TestParseExpr_Uint64Exactness(t *testing.T) {
	// 2^53+1 作为 float64 会 round 到 2^53；必须按整数精确比较。
	pid := uint64(9007199254740993) // 2^53 + 1
	s := newCondStore(map[string]any{"pid": pid})
	if !evalCompiledCondition("pid == 9007199254740993", s) {
		t.Error("uint64 精确比较失败（浮点会失真）")
	}
	if evalCompiledCondition("pid == 9007199254740992", s) {
		t.Error("pid 不应等于 2^53")
	}
}

// ─── 括号算术分组 ────────────────────────────────────────

func TestParseExpr_ParensArithmetic(t *testing.T) {
	x3 := newCondStore(map[string]any{"x": int64(3)})
	x4 := newCondStore(map[string]any{"x": int64(4)})
	if !evalCompiledCondition("(x + 1) % 2 == 0", x3) { // 4 % 2
		t.Error("(3+1)%2 == 0 should be true")
	}
	if evalCompiledCondition("(x + 1) % 2 == 0", x4) { // 5 % 2
		t.Error("(4+1)%2 == 0 should be false")
	}
}

// ─── PATH 数组下标 ───────────────────────────────────────

func TestParseExpr_PathArrayIndex(t *testing.T) {
	s := newCondStore(map[string]any{
		"items": []any{
			map[string]any{"count": int64(10)},
		},
	})
	if !evalCompiledCondition("items[0].count > 5", s) {
		t.Error("items[0].count > 5 should be true")
	}
}

// ─── 畸形表达式（均应 false + warn）────────────────────

func TestParseExpr_Malformed(t *testing.T) {
	cases := []string{
		"hp >> 0",   // 多余 >
		"hp = 0",    // 单 =
		"(hp > 0",   // 缺 )
		"hp > 0)",   // 多余 )
		"1.guildId", // 数字后跟 .
		".5",        // 前导 .
		"hp ? 1",    // 非法字符
		"items[]",   // 空下标
	}
	for _, e := range cases {
		s := newCondStore(map[string]any{"hp": int64(5)})
		if evalCompiledCondition(e, s) {
			t.Errorf("malformed %q should be false", e)
		}
	}
}

// ─── 无空格健壮性 ────────────────────────────────────────

func TestParseExpr_NoWhitespace(t *testing.T) {
	s := newCondStore(map[string]any{"hp": int64(50), "alive": true, "admin": false})
	if !evalCompiledCondition("hp>0&&(alive||admin)", s) {
		t.Error("无空格表达式应与带空格等价")
	}
}

// ! 在 unary 层（高于比较）：!a == b 解析为 !(a == b)，对 bool 等价于 a != b。
func TestParseExpr_NotPrecedence(t *testing.T) {
	// a=true, b=false: !(a==b) = !(true==false) = !false = true
	s := newCondStore(map[string]any{"a": true, "b": false})
	if !evalCompiledCondition("!a == b", s) {
		t.Error("!a == b (a=true,b=false) 应为 true（等价 !(a==b)）")
	}
	// a=true, b=true: !(true==true) = !true = false
	s2 := newCondStore(map[string]any{"a": true, "b": true})
	if evalCompiledCondition("!a == b", s2) {
		t.Error("!a == b (a=true,b=true) 应为 false")
	}
}

// 链式比较不合法（多余 token）。
func TestParseExpr_ChainedComparisonRejected(t *testing.T) {
	if evalCompiledCondition("1 < 2 < 3", newCondStore(nil)) {
		t.Error("1 < 2 < 3 应被拒绝（多余 token）")
	}
}

// 裸算术在顶层（非布尔结果）→ false。
func TestParseExpr_BareArithmeticTopLevel(t *testing.T) {
	s := newCondStore(map[string]any{"x": int64(3)})
	if evalCompiledCondition("x + 1", s) {
		t.Error("x + 1 顶层裸算术应为 false（结果非布尔）")
	}
}

// 非标量操作数（list / []byte）→ false。
func TestParseExpr_NonScalarOperand(t *testing.T) {
	list := newCondStore(map[string]any{"items": []any{int64(1)}})
	if evalCompiledCondition("items == 0", list) {
		t.Error("list == 0 应为 false（非标量）")
	}
	bytes := newCondStore(map[string]any{"data": []byte("x")})
	if evalCompiledCondition("data == \"x\"", bytes) {
		t.Error("[]byte == \"x\" 应为 false（非标量，不隐式转字符串）")
	}
}

// local-false 语义：missing || fallback，fallback 为真 → 结果 true（同时 warn missing）。
func TestParseExpr_LocalFalseOrFallback(t *testing.T) {
	s := newCondStore(map[string]any{"fallback": true})
	if !evalCompiledCondition("missing || fallback", s) {
		t.Error("missing || fallback (fallback=true) 应为 true（local-false 语义）")
	}
}

// ─── 完整条件文本编译入口（含 state: 前缀）────────────────

func TestCompileCondition_StatePrefix(t *testing.T) {
	s := newCondStore(map[string]any{"hp": int64(80), "index": int64(4)})
	if !evalFullCompiledCondition("state:hp > 0", s) {
		t.Error("state:hp > 0 should be true")
	}
	if !evalFullCompiledCondition("state:index % 2 == 0", s) {
		t.Error("state:index % 2 == 0 should be true")
	}
	if evalFullCompiledCondition("hp > 0", s) {
		t.Error("缺少 state: 前缀应返回 false")
	}
	if !evalFullCompiledCondition("  ", s) {
		t.Error("空表达式应返回 true")
	}
}

// TestParseExpr_MalformedNoPanic 覆盖 R14：畸形/截断表达式绝不能 panic（配置 typo 曾让
// 递归下降解析器在 EOF 越界，跑同一 flow 的所有 robot 集体崩溃）。本测试不约束返回值。
func TestParseExpr_MalformedNoPanic(t *testing.T) {
	s := newCondStore(map[string]any{"hp": int64(80)})
	cases := []string{
		"(",       // 触发原越界 panic 的最小输入
		"((",      // 多层未闭合
		"hp >",    // 比较缺右操作数
		"hp > (",  // 右操作数为未闭合括号
		"!",       // 一元缺操作数
		"&&",      // 二元缺两侧
		"hp &&",   // 缺右操作数
		"(hp > 0", // 缺右括号
		"hp > 0)", // 多余右括号（多余 token）
		"- ",      // 负号缺操作数
		") (",     // 顺序错乱
		"1 +",     // 算术缺右操作数
	}
	// 核心保证是「不 panic」。返回值遵循既有 local-false 语义（错误操作数吸收为 false），
	// 故个别输入如 "!" 会得到 !false==true——这属既有语义，不在本测断言范围内。
	for _, expr := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("evalCompiledCondition(%q) 发生 panic：%v", expr, r)
				}
			}()
			_ = evalCompiledCondition(expr, s)
		}()
	}
}

// TestCompileConditionSyntax 覆盖加载期 fail-closed：语法错误的条件表达式必须在加载时
// 报错，而非运行时被 local-false 语义静默吞掉（畸形 "!" 曾被吞成 true/false 掩盖配置错误）。
func TestCompileConditionSyntax(t *testing.T) {
	// 结构合法：应通过（不访问 store，缺失 state 路径不算语法错误）。
	valid := []string{
		"",                        // 空表达式跳过
		"  ",                      // 纯空白跳过
		"hp > 0",                  // 基本比较
		"hp > 0 && lvl >= 10",     // 逻辑与
		"!(hp > 0) || alive",      // 一元 + 括号 + 逻辑或
		"index % 2 == 0",          // 取模
		"(a > 1) && (b < 2 || c)", // 嵌套括号
		"missingKey > 0",          // 缺失 state 路径仍是合法语法
	}
	for _, expr := range valid {
		if _, err := compileCondition(PrefixState + expr); err != nil {
			t.Errorf("compileCondition(%q) 期望通过，却报错：%v", expr, err)
		}
	}

	// 结构非法：应报错（fail-closed）。
	invalid := []string{
		"(",       // 未闭合括号
		"((",      // 多层未闭合
		"hp >",    // 比较缺右操作数
		"hp > (",  // 右操作数为未闭合括号
		"!",       // 一元缺操作数
		"&&",      // 二元缺两侧
		"hp &&",   // 缺右操作数
		"(hp > 0", // 缺右括号
		"hp > 0)", // 多余右括号
		"1 +",     // 算术缺右操作数
	}
	for _, expr := range invalid {
		if _, err := compileCondition(PrefixState + expr); err == nil {
			t.Errorf("compileCondition(%q) 期望报错（fail-closed），却通过", expr)
		}
	}
}

// TestPrepareTaskFlow_ConditionSyntax 覆盖 flow 加载期对节点/绑定条件的编译：
// 带 state: 前缀的畸形条件必须让 PrepareTaskFlow 返回错误。
func TestPrepareTaskFlow_ConditionSyntax(t *testing.T) {
	// 节点 condition 语法错误 → 报错。
	badNode := &TaskFlow{
		Nodes: map[string]*Node{
			"n1": {Condition: "state:hp >"},
		},
	}
	if err := PrepareTaskFlow(badNode); err == nil {
		t.Error("节点 condition 语法错误应被 PrepareTaskFlow 拒绝")
	}

	// 合法 condition → 通过。
	goodNode := &TaskFlow{
		Nodes: map[string]*Node{
			"n1": {Condition: "state:hp > 0 && lvl >= 1"},
		},
	}
	if err := PrepareTaskFlow(goodNode); err != nil {
		t.Errorf("合法节点 condition 不应报错：%v", err)
	}

	// 非 state: 前缀（如 lua:）不做语法校验 → 通过。
	luaNode := &TaskFlow{
		Nodes: map[string]*Node{
			"n1": {Condition: "lua:check.lua"},
		},
	}
	if err := PrepareTaskFlow(luaNode); err != nil {
		t.Errorf("lua: 前缀条件不应被语法校验拒绝：%v", err)
	}
}
