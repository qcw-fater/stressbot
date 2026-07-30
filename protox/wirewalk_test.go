package protox

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// ── D1 直转器（WalkWire）定向用例 ─────────────────────────────────
//
// 随机差分由 TestWireDifferentialFuzz 覆盖（MaterializeValue 已切直转器，
// 其整树比对分支即 walker 的 L1 fuzz）。本文件补确定性语义行与降级回退。

// assertTreeEqual 整树直转 vs oracle 解码树逐字比对。
func assertTreeEqual(t *testing.T, f *Factory, name string, raw []byte) {
	t.Helper()
	wv := wireOf(t, f, name, raw)

	sink := newMapTreeSink()
	if err := wv.Walk(sink); err != nil {
		t.Fatalf("Walk 失败: %v", err)
	}
	oracleMsg, err := f.Parse(name, raw)
	if err != nil {
		t.Fatalf("oracle 解码失败: %v", err)
	}
	oracle := messageToMap(oracleMsg.ProtoReflect())
	if !plainEqual(sink.m, oracle) {
		t.Fatalf("整树不一致\n wire  =%#v\n oracle=%#v", sink.m, oracle)
	}
}

// TestWalkWireSemanticsMatrix 合并语义矩阵的整树版：
// 单数 message 段拼接、oneof 交替清除、标量 last-wins、map 重复 key 替换、
// 空 message 出现、错误 wire type 忽略。
func TestWalkWireSemanticsMatrix(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	// 单数 message 多次出现合并 + 标量 last-wins。
	a := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "node.leaf.id", int64(1))
		setF(t, f, m, "node.name", "first")
		setF(t, f, m, "i32", int64(1))
	})
	b := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "node.leaf.tag", "x")
		setF(t, f, m, "node.id", int64(7))
		setF(t, f, m, "i32", int64(2))
	})
	assertTreeEqual(t, f, "wiretest.Everything", append(mustMarshal(t, a), mustMarshal(t, b)...))

	// oneof 交替：A(node) → B(str) → A(node)，最终 node 只含第三段。
	na := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "choice_node.name", "first")
		setF(t, f, m, "choice_node.id", int64(1))
	})
	nb := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "choice_str", "mid") })
	nc := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "choice_node.id", int64(3)) })
	assertTreeEqual(t, f, "wiretest.Everything",
		append(append(mustMarshal(t, na), mustMarshal(t, nb)...), mustMarshal(t, nc)...))

	// map 重复 key：后一条 entry 整体替换。
	ma := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "mstr", map[any]any{"k": int64(1), "keep": int64(9)})
	})
	mb := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "mstr", map[any]any{"k": int64(2)})
	})
	assertTreeEqual(t, f, "wiretest.Everything", append(mustMarshal(t, ma), mustMarshal(t, mb)...))

	// 空消息出现：node 出现但内容为空 → 在场空表。
	empty := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "str", "with-empty-node")
	})
	raw := mustMarshal(t, empty)
	md, _ := f.MessageDescriptor("wiretest.Everything")
	nodeFd := md.Fields().ByName("node")
	raw = protowire.AppendTag(raw, nodeFd.Number(), protowire.BytesType)
	raw = protowire.AppendBytes(raw, nil)
	assertTreeEqual(t, f, "wiretest.Everything", raw)
}

// TestWalkWireAccsPoolNoContamination 池化 scratch 的复用隔离（P2 契约）：
// 满负载消息（激活全部字段形态）走一遍后，紧跟的空消息/另一形态消息产物
// 必须与解码 oracle 逐字一致——若归还前 reset 有漏（残留 spans/elems/entries），
// 这里会以字段串扰形式暴露。串行循环多轮保证命中池内复用而非新分配。
func TestWalkWireAccsPoolNoContamination(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	full := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "str", "full")
		setF(t, f, m, "rints", []any{int64(1), int64(2), int64(3)})
		setF(t, f, m, "node.leaf.id", int64(5))
		setF(t, f, m, "mstr", map[any]any{"k": int64(1)})
		setF(t, f, m, "choice_str", "picked")
	})
	empty := buildEverything(t, f, nil)
	sparse := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "i32", int64(9)) })

	for i := 0; i < 8; i++ {
		assertTreeEqual(t, f, "wiretest.Everything", mustMarshal(t, full))
		assertTreeEqual(t, f, "wiretest.Everything", mustMarshal(t, empty))
		assertTreeEqual(t, f, "wiretest.Everything", mustMarshal(t, sparse))
	}
}

// TestMaterializeDegradedFallsBack 降级 schema 的 MaterializeValue 回落解码路径且产物不变。
func TestMaterializeDegradedFallsBack(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	msg := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "str", "fallback")
		setF(t, f, m, "node.id", int64(5))
	})
	raw := mustMarshal(t, msg)
	wv := wireOf(t, f, "wiretest.Everything", raw)

	direct := wv.MaterializeValue()

	// 人工降级该 schema。
	recordWireMismatch(wv, shadowWholeTreeSegs, "测试注入")
	if wv.MaterializeAllowed() {
		t.Fatal("降级后 MaterializeAllowed 应为 false")
	}
	fallback := wv.MaterializeValue()
	if !plainEqual(direct, fallback) {
		t.Fatalf("直转与回落产物不一致\n direct  =%#v\n fallback=%#v", direct, fallback)
	}
}

// TestMaterializeShadowSamplingVerifies 影子开启时首 K 次物化触发校验计数。
func TestMaterializeShadowSamplingVerifies(t *testing.T) {
	f := newWireTestFactory(t)
	resetWireShadowForTest()
	t.Cleanup(resetWireShadowForTest)

	msg := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "i32", int64(9)) })
	wv := wireOf(t, f, "wiretest.Everything", mustMarshal(t, msg))

	before := SnapshotWireShadowStats()
	for i := 0; i < shadowFirstK+2; i++ {
		if _, ok := wv.MaterializeValue().(map[string]any); !ok {
			t.Fatal("MaterializeValue 应产出 map")
		}
	}
	after := SnapshotWireShadowStats()
	if after.Checks-before.Checks != shadowFirstK {
		t.Fatalf("首 K 次物化应各触发一次校验: got %d want %d", after.Checks-before.Checks, shadowFirstK)
	}
	if after.Mismatches != before.Mismatches {
		t.Fatalf("正确直转不应产生失配: %d → %d", before.Mismatches, after.Mismatches)
	}
}
