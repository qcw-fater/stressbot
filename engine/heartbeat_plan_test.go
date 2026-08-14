package engine

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"stressbot/state"
)

// 对拍原则：BuildHeartbeatBody（逐 tick 打包）是 oracle，CompileHeartbeatPlan+Build
// 是被验证的快路径。oracle 保持原样不复用新代码（putLE），否则对拍失去意义。

//go:fix inline
func i64p(v int64) *int64 { return &v }

func f64p(v float64) *float64 { return &v }

// newHBStore 造一份带初值的 store（两份独立实例用于 plan/oracle 各自跑，
// 避免 stateCounter 自增的副作用互相污染）。
func newHBStore() *state.Store {
	st := state.NewStore()
	st.Set("battleId", int64(0x1122334455667788))
	st.Set("fighterIndex", 7)
	st.Set("battleAck", int32(-3))
	st.Set("rttFloat", 12.5)
	st.Set("pkgIdx", int64(41))
	return st
}

// deterministicFields 覆盖全部「结果可复现」的 type × source 组合
// （timestamp/randomInt 单独测，见 TestHeartbeatPlanNonDeterministicSources）。
func deterministicFields() []HeartbeatField {
	return []HeartbeatField{
		{Type: "u8", Source: HeartbeatSourceFixed, Value: i64p(0xAB)},
		{Type: "i8", Source: HeartbeatSourceFixed, Value: i64p(-2)},
		{Type: "u16", Source: HeartbeatSourceFixed, Value: i64p(0x1234)},
		{Type: "i16", Source: HeartbeatSourceState, Key: "battleAck"},
		{Type: "u32", Source: HeartbeatSourceStateCounter, Key: "pkgIdx"},
		{Type: "i32", Source: HeartbeatSourceState, Key: "fighterIndex"},
		{Type: "u64", Source: HeartbeatSourceState, Key: "battleId"},
		{Type: "i64", Source: HeartbeatSourceCounter, Start: i64p(1), Step: i64p(3)},
		{Type: "f32", Source: HeartbeatSourceState, Key: "rttFloat"},
		{Type: "f64", Source: HeartbeatSourceFixed, FloatValue: f64p(-0.25)},
		{Type: "u8", Source: HeartbeatSourceCounter}, // Start/Step 缺省 0/1
	}
}

// TestHeartbeatPlanParity 编译布局与逐 tick 打包在多 tick 上逐字节一致
// （含 stateCounter 自增与私有计数器推进的跨 tick 演进）。
func TestHeartbeatPlanParity(t *testing.T) {
	fields := deterministicFields()

	plan, err := CompileHeartbeatPlan(fields)
	if err != nil {
		t.Fatalf("编译心跳布局失败: %v", err)
	}

	stPlan, stOracle := newHBStore(), newHBStore()
	cntPlan := initCounters(fields)
	cntOracle := initCounters(fields)
	stepsPlan := CompileHeartbeatCounters(fields)

	for tick := range 5 {
		gotBody, gotSkip, gotErr := plan.Build(stPlan, cntPlan, false)
		wantBody, wantSkip, wantErr := BuildHeartbeatBody(fields, stOracle, cntOracle, false)
		if gotErr != nil || wantErr != nil {
			t.Fatalf("tick=%d 构建报错: plan=%v oracle=%v", tick, gotErr, wantErr)
		}
		if gotSkip != wantSkip {
			t.Fatalf("tick=%d skip 不一致: plan=%v oracle=%v", tick, gotSkip, wantSkip)
		}
		if !bytes.Equal(gotBody, wantBody) {
			t.Fatalf("tick=%d body 不一致:\nplan  =% x\noracle=% x", tick, gotBody, wantBody)
		}
		// 推进私有计数器：plan 侧走编译表，oracle 侧走原逐字段扫描。
		AdvanceHeartbeatCounters(stepsPlan, cntPlan)
		advanceCountersLegacy(fields, cntOracle)
		if len(cntPlan) != len(cntOracle) {
			t.Fatalf("tick=%d 私有计数器条目数不一致: plan=%d oracle=%d", tick, len(cntPlan), len(cntOracle))
		}
		for k, v := range cntOracle {
			if cntPlan[k] != v {
				t.Fatalf("tick=%d 私有计数器[%d] 不一致: plan=%d oracle=%d", tick, k, cntPlan[k], v)
			}
		}
	}
}

// TestHeartbeatPlanStateOrderingPreserved 同一 key 既被 state 读又被 stateCounter 自增时，
// 编译布局必须保持「按字段顺序求值」的交错语义（读到的是自增前/后的正确值）。
func TestHeartbeatPlanStateOrderingPreserved(t *testing.T) {
	fields := []HeartbeatField{
		{Type: "u32", Source: HeartbeatSourceState, Key: "pkgIdx"},        // 自增前
		{Type: "u32", Source: HeartbeatSourceStateCounter, Key: "pkgIdx"}, // 自增
		{Type: "u32", Source: HeartbeatSourceState, Key: "pkgIdx"},        // 自增后
	}
	plan, err := CompileHeartbeatPlan(fields)
	if err != nil {
		t.Fatalf("编译心跳布局失败: %v", err)
	}

	gotBody, _, err := plan.Build(newHBStore(), map[int]int64{}, false)
	if err != nil {
		t.Fatalf("plan 构建失败: %v", err)
	}
	got := append([]byte(nil), gotBody...)
	wantBody, _, err := BuildHeartbeatBody(fields, newHBStore(), map[int]int64{}, false)
	if err != nil {
		t.Fatalf("oracle 构建失败: %v", err)
	}
	if !bytes.Equal(got, wantBody) {
		t.Fatalf("交错语义不一致:\nplan  =% x\noracle=% x", got, wantBody)
	}
	// 显式钉住语义：41 → 自增得 42 → 再读得 42。
	if v := binary.LittleEndian.Uint32(got[0:4]); v != 41 {
		t.Fatalf("自增前读值应为 41，实际 %d", v)
	}
	if v := binary.LittleEndian.Uint32(got[4:8]); v != 42 {
		t.Fatalf("自增返回值应为 42，实际 %d", v)
	}
	if v := binary.LittleEndian.Uint32(got[8:12]); v != 42 {
		t.Fatalf("自增后读值应为 42，实际 %d", v)
	}
}

// TestHeartbeatPlanNonDeterministicSources timestamp/randomInt 无法逐字节对拍，
// 验证「宽度/偏移正确 + 取值落在合法区间 + 相邻确定槽位不被写坏」。
func TestHeartbeatPlanNonDeterministicSources(t *testing.T) {
	fields := []HeartbeatField{
		{Type: "u32", Source: HeartbeatSourceFixed, Value: i64p(0xDEADBEEF)},
		{Type: "u64", Source: HeartbeatSourceTimestamp},            // 缺省 ms
		{Type: "u64", Source: HeartbeatSourceTimestamp, Unit: "s"}, // 秒
		{Type: "u16", Source: HeartbeatSourceRandomInt, Min: i64p(10), Max: i64p(40)},
		{Type: "u32", Source: HeartbeatSourceFixed, Value: i64p(0x01020304)},
	}
	plan, err := CompileHeartbeatPlan(fields)
	if err != nil {
		t.Fatalf("编译心跳布局失败: %v", err)
	}
	if plan.Size() != 4+8+8+2+4 {
		t.Fatalf("布局总长应为 26，实际 %d", plan.Size())
	}

	before := time.Now()
	body, skip, err := plan.Build(newHBStore(), map[int]int64{}, false)
	after := time.Now()
	if err != nil || skip {
		t.Fatalf("构建失败: err=%v skip=%v", err, skip)
	}
	if v := binary.LittleEndian.Uint32(body[0:4]); v != 0xDEADBEEF {
		t.Fatalf("首个 fixed 槽位被写坏: %#x", v)
	}
	if v := binary.LittleEndian.Uint32(body[22:26]); v != 0x01020304 {
		t.Fatalf("末尾 fixed 槽位被写坏: %#x", v)
	}
	ms := int64(binary.LittleEndian.Uint64(body[4:12]))
	if ms < before.UnixMilli() || ms > after.UnixMilli() {
		t.Fatalf("ms 时间戳越界: %d 不在 [%d,%d]", ms, before.UnixMilli(), after.UnixMilli())
	}
	sec := int64(binary.LittleEndian.Uint64(body[12:20]))
	if sec < before.Unix() || sec > after.Unix() {
		t.Fatalf("s 时间戳越界: %d 不在 [%d,%d]", sec, before.Unix(), after.Unix())
	}
	if r := binary.LittleEndian.Uint16(body[20:22]); r < 10 || r > 40 {
		t.Fatalf("randomInt 越界: %d 不在 [10,40]", r)
	}
}

// TestHeartbeatPlanSkipWhenMissing state 缺失时的 skip 语义与 oracle 一致。
func TestHeartbeatPlanSkipWhenMissing(t *testing.T) {
	fields := []HeartbeatField{
		{Type: "u32", Source: HeartbeatSourceFixed, Value: i64p(1)},
		{Type: "u32", Source: HeartbeatSourceState, Key: "notExist"},
		{Type: "f32", Source: HeartbeatSourceState, Key: "notExistFloat"},
	}
	plan, err := CompileHeartbeatPlan(fields)
	if err != nil {
		t.Fatalf("编译心跳布局失败: %v", err)
	}

	// skipWhenMissing=true → skip，不报错。
	_, skip, err := plan.Build(newHBStore(), map[int]int64{}, true)
	if err != nil || !skip {
		t.Fatalf("缺失 key 应 skip: skip=%v err=%v", skip, err)
	}
	_, oracleSkip, oracleErr := BuildHeartbeatBody(fields, newHBStore(), map[int]int64{}, true)
	if oracleSkip != skip || (oracleErr == nil) != (err == nil) {
		t.Fatalf("skip 语义与 oracle 不一致: plan(skip=%v err=%v) oracle(skip=%v err=%v)",
			skip, err, oracleSkip, oracleErr)
	}

	// skipWhenMissing=false → 报错（不静默兜底）。
	if _, _, err := plan.Build(newHBStore(), map[int]int64{}, false); err == nil {
		t.Fatal("缺失 key 且 skipWhenMissing=false 应报错")
	}
}

// TestHeartbeatPlanCompileRejectsBadConfig 编译期拒绝的配置，逐 tick 形态也必然报错——
// 证明编译门槛不比运行期更严（不会把本来能跑的配置挡在注册阶段）。
func TestHeartbeatPlanCompileRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name  string
		field HeartbeatField
	}{
		{"未知类型", HeartbeatField{Type: "u24", Source: HeartbeatSourceFixed, Value: i64p(1)}},
		{"未知来源", HeartbeatField{Type: "u32", Source: "nope"}},
		{"fixed 缺 value", HeartbeatField{Type: "u32", Source: HeartbeatSourceFixed}},
		{"fixed 浮点缺 floatValue", HeartbeatField{Type: "f32", Source: HeartbeatSourceFixed}},
		{"state 缺 key", HeartbeatField{Type: "u32", Source: HeartbeatSourceState}},
		{"state 浮点缺 key", HeartbeatField{Type: "f64", Source: HeartbeatSourceState}},
		{"stateCounter 缺 key", HeartbeatField{Type: "u32", Source: HeartbeatSourceStateCounter}},
		{"randomInt 缺 min/max", HeartbeatField{Type: "u32", Source: HeartbeatSourceRandomInt}},
		{"randomInt min>max", HeartbeatField{Type: "u32", Source: HeartbeatSourceRandomInt, Min: i64p(9), Max: i64p(2)}},
		{"浮点不支持的 source", HeartbeatField{Type: "f32", Source: HeartbeatSourceTimestamp}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := []HeartbeatField{tc.field}
			if _, err := CompileHeartbeatPlan(fields); err == nil {
				t.Fatal("编译应报错")
			}
			if _, _, err := BuildHeartbeatBody(fields, newHBStore(), map[int]int64{}, false); err == nil {
				t.Fatal("逐 tick 形态也应报错（编译门槛不得比运行期更严）")
			}
		})
	}
}

// TestHeartbeatPlanBufferReuseZeroAlloc 每 tick 复用同一缓冲：切片身份不变且零分配。
func TestHeartbeatPlanBufferReuseZeroAlloc(t *testing.T) {
	fields := deterministicFields()
	plan, err := CompileHeartbeatPlan(fields)
	if err != nil {
		t.Fatalf("编译心跳布局失败: %v", err)
	}
	st := newHBStore()
	cnt := initCounters(fields)

	b1, _, err := plan.Build(st, cnt, false)
	if err != nil {
		t.Fatalf("首次构建失败: %v", err)
	}
	b2, _, err := plan.Build(st, cnt, false)
	if err != nil {
		t.Fatalf("二次构建失败: %v", err)
	}
	if &b1[0] != &b2[0] || len(b1) != len(b2) {
		t.Fatal("body 缓冲未复用（跨 tick 底层数组应相同）")
	}

	allocs := testing.AllocsPerRun(200, func() {
		if _, _, err := plan.Build(st, cnt, false); err != nil {
			t.Fatalf("构建失败: %v", err)
		}
	})
	// state 读回的 any 值可能因装箱产生分配（int64→any 由 store 侧持有），
	// 这里只要求打包本身不再分配：放宽到 <1 次/轮以容忍偶发的运行时噪声。
	if allocs >= 1 {
		t.Fatalf("每 tick 打包应零分配，实际 %.2f 次", allocs)
	}
}

// TestHeartbeatPlanFloatBitPattern f32/f64 的 IEEE754 位模式与 oracle 一致。
func TestHeartbeatPlanFloatBitPattern(t *testing.T) {
	fields := []HeartbeatField{
		{Type: "f32", Source: HeartbeatSourceFixed, FloatValue: f64p(1.5)},
		{Type: "f64", Source: HeartbeatSourceState, Key: "rttFloat"},
	}
	plan, err := CompileHeartbeatPlan(fields)
	if err != nil {
		t.Fatalf("编译心跳布局失败: %v", err)
	}
	body, _, err := plan.Build(newHBStore(), map[int]int64{}, false)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if got := math.Float32frombits(binary.LittleEndian.Uint32(body[0:4])); got != 1.5 {
		t.Fatalf("f32 fixed 值错误: %v", got)
	}
	if got := math.Float64frombits(binary.LittleEndian.Uint64(body[4:12])); got != 12.5 {
		t.Fatalf("f64 state 值错误: %v", got)
	}
	want, _, err := BuildHeartbeatBody(fields, newHBStore(), map[int]int64{}, false)
	if err != nil {
		t.Fatalf("oracle 构建失败: %v", err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("浮点位模式与 oracle 不一致:\nplan  =% x\noracle=% x", body, want)
	}
}

// TestHeartbeatPlanEmptyFields 空布局与 oracle 一致（空 body，不报错）。
func TestHeartbeatPlanEmptyFields(t *testing.T) {
	plan, err := CompileHeartbeatPlan(nil)
	if err != nil {
		t.Fatalf("空布局编译应成功: %v", err)
	}
	body, skip, err := plan.Build(newHBStore(), map[int]int64{}, false)
	if err != nil || skip || len(body) != 0 {
		t.Fatalf("空布局应产出空 body: body=%v skip=%v err=%v", body, skip, err)
	}
}

// TestCompileHeartbeatCountersParity 编译出的推进表与原逐字段扫描等价。
func TestCompileHeartbeatCountersParity(t *testing.T) {
	fields := deterministicFields()
	steps := CompileHeartbeatCounters(fields)
	if len(steps) != 2 {
		t.Fatalf("应摘出 2 个 counter 源，实际 %d", len(steps))
	}
	got, want := initCounters(fields), initCounters(fields)
	for range 3 {
		AdvanceHeartbeatCounters(steps, got)
		advanceCountersLegacy(fields, want)
	}
	if len(got) != len(want) {
		t.Fatalf("条目数不一致: got=%d want=%d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("计数器[%d] 不一致: got=%d want=%d", k, got[k], v)
		}
	}
}

// initCounters 复刻 netSenderAdapter.installHeartbeat 的私有计数器初值逻辑。
func initCounters(fields []HeartbeatField) map[int]int64 {
	m := make(map[int]int64, len(fields))
	for i, f := range fields {
		if f.Source == HeartbeatSourceCounter && f.Start != nil {
			m[i] = *f.Start
		}
	}
	return m
}

// advanceCountersLegacy 复刻改造前 goBuilder 尾部的逐字段扫描推进（对拍基准）。
func advanceCountersLegacy(fields []HeartbeatField, privateCounters map[int]int64) {
	for i := range fields {
		f := &fields[i]
		if f.Source == HeartbeatSourceCounter {
			step := int64(1)
			if f.Step != nil {
				step = *f.Step
			}
			privateCounters[i] += step
		}
	}
}
