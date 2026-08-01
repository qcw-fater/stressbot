package script

import (
	"strconv"
	"testing"

	"stressbot/state"

	lua "github.com/yuin/gopher-lua"
)

// Lua→Go API 调用开销基准。
//
// 动机：10000 机器人单机压测的 CPU 剖面里 callGFunction 占 17.43%，其中大头是每次
// API 调用的固定前缀开销（GetContext 的 registry 字符串查表）与埋点分配，而不是
// API 自身的业务逻辑。这些基准把「每次调用的固定开销」单独量出来，避免凭剖面比例
// 拍脑袋优化。
//
// 分工：
//   - GetID   只有 GetContext + push，隔离出纯前缀开销；
//   - Get     叠加 statekeys 埋点 + Store 读 + goValueToLua；
//   - GetPath 再叠加路径切分与嵌套导航。
//
// 三者之差即各段成本。ns/call 由 ReportMetric 给出（每次脚本执行内含 benchAPICalls
// 次调用，直接读 ns/op 会被脚本启停开销稀释）。

const benchAPICalls = 1000

// newAPIBenchState 构造只含一个 bench 脚本的运行时池，脚本体为 body 的定长循环。
func newAPIBenchState(b *testing.B, body string) (*RuntimePool, *lua.LState) {
	b.Helper()

	src := `local robot = require("robot")
function execute(r)
  for i = 1, ` + strconv.Itoa(benchAPICalls) + ` do
    ` + body + `
  end
  return nil
end`

	compiler := lua.NewState()
	fn, err := compiler.LoadString(src)
	compiler.Close()
	if err != nil {
		b.Fatalf("编译 bench 脚本失败: %v", err)
	}

	rp := NewRuntimePool("")
	rp.registerPrecompiledScript("bench.lua", fn.Proto)

	st := state.NewStore()
	st.Set("scalar", 42)
	st.Set("nested", map[string]any{"a": map[string]any{"b": 7}})

	L := rp.Acquire()
	SetContext(L, &Context{RobotID: 1, Index: 0, Account: "bench", Store: st})
	return rp, L
}

func runAPIBench(b *testing.B, body string) {
	rp, L := newAPIBenchState(b, body)
	defer rp.Release(L)

	// 预热一次：首次执行会加载 chunk、建立入口函数缓存，不应计入稳态开销。
	if _, _, _, err := rp.RunActionScript(L, "bench.lua"); err != nil {
		b.Fatalf("预热执行失败: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := rp.RunActionScript(L, "bench.lua"); err != nil {
			b.Fatalf("执行失败: %v", err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*benchAPICalls), "ns/call")
}

// BenchmarkLuaAPI_GetID 纯调用前缀开销（GetContext + push），不碰 Store、不走埋点。
func BenchmarkLuaAPI_GetID(b *testing.B) {
	runAPIBench(b, `robot.get_id()`)
}

// BenchmarkGetContext 单独量 GetContext：它是每一个 Lua→Go API 函数的第一行，
// 成本摊在全部 API 调用上。走 Lua VM 的基准噪声太大盖不住它的差异，故在 Go 层直接打。
func BenchmarkGetContext(b *testing.B) {
	rp := NewRuntimePool("")
	L := rp.Acquire()
	defer rp.Release(L)
	SetContext(L, &Context{RobotID: 1, Account: "bench"})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if GetContext(L) == nil {
			b.Fatal("GetContext 返回 nil")
		}
	}
}

// BenchmarkLuaAPI_Get 顶层标量整读：前缀 + statekeys 埋点 + Store.Get + 值转换。
func BenchmarkLuaAPI_Get(b *testing.B) {
	runAPIBench(b, `robot.get("scalar")`)
}

// BenchmarkLuaAPI_GetPath 嵌套路径读：再叠加 splitPath 与逐段导航。
func BenchmarkLuaAPI_GetPath(b *testing.B) {
	runAPIBench(b, `robot.get_path("nested.a.b")`)
}
