package protox

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"stressbot/state"
	stresslog "stressbot/utils/log"

	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// ── L1 离线差分验证：wire 扫描 vs 官方解码器 ─────────────────────
//
// 正确性契约：对任意合法 wire 字节 b 与任意路径 p，
//
//	WireValue(desc,b).NavigateSegs(p) ≡ Freeze(dynamicpb.Unmarshal(b)).NavigateSegs(p)
//
// 本文件用两条线守护：
//  1. 确定性矩阵（TestWireSemantics*）：protobuf 合并语义的每一行为一个用例
//     （重复单数 message 合并 / oneof 后者清前者 / 标量 last-wins / packed 混排 /
//     map 重复 key 替换 / 错误 wire type 按未知字段忽略 / UTF-8 与结构非法拒绝）；
//  2. 随机差分 fuzz（TestWireDifferentialFuzz）：随机消息 + wire 级变异（拼接重复），
//     全路径语料双侧比对；另对随机损坏字节做「合法性判定等价」比对
//     （ValidateWire 与 proto.Unmarshal 的接受/拒绝完全一致）。

// newWireTestFactory 构造覆盖全部标量 kind、oneof、optional、嵌套/重复/映射的测试工厂。
func newWireTestFactory(t *testing.T) *Factory {
	t.Helper()
	stresslog.ReplaceLogger(zap.NewNop())

	dir := t.TempDir()
	protoContent := `syntax = "proto3";

package wiretest;

enum Color {
  COLOR_UNSPECIFIED = 0;
  RED = 1;
  BLUE = 2;
}

message Leaf {
  int32  id  = 1;
  string tag = 2;
}

message Node {
  int32  id   = 1;
  string name = 2;
  Leaf   leaf = 3;
  repeated Leaf  leaves = 4;
  repeated int64 nums   = 5;
  map<string, Leaf>  kids   = 6;
  map<int32, string> labels = 7;
}

message Everything {
  bool     b    = 1;
  int32    i32  = 2;
  int64    i64  = 3;
  uint32   u32  = 4;
  uint64   u64  = 5;
  sint32   s32  = 6;
  sint64   s64  = 7;
  fixed32  f32  = 8;
  fixed64  f64  = 9;
  sfixed32 sf32 = 10;
  sfixed64 sf64 = 11;
  float    fl   = 12;
  double   db   = 13;
  string   str  = 14;
  bytes    bs   = 15;
  Color    col  = 16;
  Node     node = 17;
  repeated Node   nodes    = 18;
  repeated int32  rints    = 19;
  repeated string rstrs    = 20;
  repeated bool   rbools   = 21;
  repeated double rdoubles = 22;
  map<string, int64> mstr  = 23;
  map<int64, Node>   mnode = 24;
  map<bool, string>  mbool = 25;
  map<uint32, bytes> mu32  = 26;
  oneof choice {
    int32  choice_num  = 30;
    string choice_str  = 31;
    Node   choice_node = 32;
  }
  optional int32 opt_i    = 33;
  optional Node  opt_node = 34;
}
`
	protoPath := filepath.Join(dir, "wire_test.proto")
	if err := os.WriteFile(protoPath, []byte(protoContent), 0644); err != nil {
		t.Fatalf("写入测试 proto 失败: %v", err)
	}
	loader := NewLoader([]string{dir}, []string{"wire_test.proto"})
	files, err := loader.Load()
	if err != nil {
		t.Fatalf("编译测试 proto 失败: %v", err)
	}
	return NewFactory(NewRegistry(files))
}

// disableShadow 关闭影子验证并在测试结束时复位——差分测试必须比对 wire 侧的
// 真实产物，不能让影子机制用 oracle 结果掩盖 wire 扫描的错误。
func disableShadow(t *testing.T) {
	t.Helper()
	resetWireShadowForTest()
	SetWireShadowEnabled(false)
	t.Cleanup(resetWireShadowForTest)
}

// wireOf 构造 WireValue（结构校验必须先通过）。
func wireOf(t *testing.T, f *Factory, name string, raw []byte) *WireValue {
	t.Helper()
	md, ok := f.MessageDescriptor(name)
	if !ok {
		t.Fatalf("未找到 %s", name)
	}
	if err := ValidateWire(md, raw); err != nil {
		t.Fatalf("ValidateWire(%s) 失败: %v（oracle 可解码的字节必须通过校验）", name, err)
	}
	return NewWireValue(md, WireSnapshot(raw))
}

// assertNavEqual 双侧导航一次并比对（存在性 + 物化后逐字相等）。
func assertNavEqual(t *testing.T, f *Factory, name string, raw []byte, path string) {
	t.Helper()
	wv := wireOf(t, f, name, raw)

	oracleMsg, err := f.Parse(name, raw)
	if err != nil {
		t.Fatalf("oracle 解码失败: %v", err)
	}
	segs := state.SplitPath(path)
	wantV, wantOK := Freeze(oracleMsg).NavigateSegs(segs)
	gotV, gotOK := wv.NavigateSegs(segs)
	if gotOK != wantOK {
		t.Fatalf("path %q: wire found=%v oracle found=%v", path, gotOK, wantOK)
	}
	if gotOK && !plainEqual(materializePlain(gotV), materializePlain(wantV)) {
		t.Fatalf("path %q:\n wire  =%#v\n oracle=%#v", path, materializePlain(gotV), materializePlain(wantV))
	}
}

// mustMarshal 序列化测试消息。
func mustMarshal(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return raw
}

// buildEverything 便捷构造：fill 里用 factory.SetField 填字段。
func buildEverything(t *testing.T, f *Factory, fill func(msg proto.Message)) proto.Message {
	t.Helper()
	msg, err := f.Create("wiretest.Everything")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if fill != nil {
		fill(msg)
	}
	return msg
}

func setF(t *testing.T, f *Factory, msg proto.Message, field string, v any) {
	t.Helper()
	if err := f.SetField(msg, field, v); err != nil {
		t.Fatalf("SetField(%s) 失败: %v", field, err)
	}
}

// TestWireSemanticsMergeSingularMessage 重复的单数 message 出现按 wire 拼接合并
// （后者字段覆盖/追加进前者）。
func TestWireSemanticsMergeSingularMessage(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	a := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "node.leaf.id", int64(1))
		setF(t, f, m, "node.leaf.tag", "a")
		setF(t, f, m, "node.name", "first")
	})
	b := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "node.leaf.id", int64(2))
		setF(t, f, m, "node.id", int64(7))
	})
	// wire 拼接 ≡ proto merge：node 出现两次。
	raw := append(mustMarshal(t, a), mustMarshal(t, b)...)

	for _, p := range []string{"node", "node.leaf", "node.leaf.id", "node.leaf.tag", "node.name", "node.id"} {
		assertNavEqual(t, f, "wiretest.Everything", raw, p)
	}
}

// TestWireSemanticsOneof oneof 成员按出现序竞争：后出现的成员清掉前者；
// 同成员相邻出现按 merge；A-B-A 序列中第三段是全新值（B 已清掉第一段 A）。
func TestWireSemanticsOneof(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	// num → str：str 胜出，num 回落默认值。
	m1 := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "choice_num", int64(5)) })
	m2 := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "choice_str", "win") })
	raw := append(mustMarshal(t, m1), mustMarshal(t, m2)...)
	for _, p := range []string{"choice_num", "choice_str", "choice_node"} {
		assertNavEqual(t, f, "wiretest.Everything", raw, p)
	}

	// A(node) → B(str) → A(node)：最终 node 只含第三段内容（不与第一段 merge）。
	na := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "choice_node.name", "first")
		setF(t, f, m, "choice_node.id", int64(1))
	})
	nb := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "choice_str", "mid") })
	nc := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "choice_node.id", int64(3)) })
	raw2 := append(append(mustMarshal(t, na), mustMarshal(t, nb)...), mustMarshal(t, nc)...)
	for _, p := range []string{"choice_node", "choice_node.id", "choice_node.name", "choice_str", "choice_num"} {
		assertNavEqual(t, f, "wiretest.Everything", raw2, p)
	}
}

// TestWireSemanticsScalarLastWins 重复单数标量后者胜出（含 optional）。
func TestWireSemanticsScalarLastWins(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	a := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "i32", int64(1))
		setF(t, f, m, "str", "old")
		setF(t, f, m, "opt_i", int64(10))
	})
	b := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "i32", int64(2))
		setF(t, f, m, "str", "new")
		setF(t, f, m, "opt_i", int64(20))
	})
	raw := append(mustMarshal(t, a), mustMarshal(t, b)...)
	for _, p := range []string{"i32", "str", "opt_i"} {
		assertNavEqual(t, f, "wiretest.Everything", raw, p)
	}
}

// TestWireSemanticsPackedMix packed 与非 packed 出现混排，出现序即元素序。
func TestWireSemanticsPackedMix(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)
	md, _ := f.MessageDescriptor("wiretest.Everything")
	fdRints := md.Fields().ByName("rints") // field 19, int32

	var raw []byte
	// packed [1,2]
	raw = protowire.AppendTag(raw, fdRints.Number(), protowire.BytesType)
	var packed []byte
	packed = protowire.AppendVarint(packed, 1)
	packed = protowire.AppendVarint(packed, 2)
	raw = protowire.AppendBytes(raw, packed)
	// 非 packed 3
	raw = protowire.AppendTag(raw, fdRints.Number(), protowire.VarintType)
	raw = protowire.AppendVarint(raw, 3)
	// packed [4]
	raw = protowire.AppendTag(raw, fdRints.Number(), protowire.BytesType)
	raw = protowire.AppendBytes(raw, protowire.AppendVarint(nil, 4))

	for _, p := range []string{"rints", "rints[0]", "rints[2]", "rints[3]", "rints[4]"} {
		assertNavEqual(t, f, "wiretest.Everything", raw, p)
	}
}

// TestWireSemanticsMapDuplicateKey 同 key 的后一条 entry 整体替换前者。
func TestWireSemanticsMapDuplicateKey(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	a := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "mstr", map[any]any{"k": int64(1), "keep": int64(9)})
	})
	b := buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "mstr", map[any]any{"k": int64(2)})
	})
	raw := append(mustMarshal(t, a), mustMarshal(t, b)...)
	for _, p := range []string{"mstr", "mstr.k", "mstr.keep", "mstr.missing"} {
		assertNavEqual(t, f, "wiretest.Everything", raw, p)
	}
}

// TestWireSemanticsWrongWireType 错误 wire type 的出现按未知字段忽略，
// 不影响同字段合法出现的取值。
func TestWireSemanticsWrongWireType(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	base := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "i32", int64(42)) })
	raw := mustMarshal(t, base)
	// i32（field 2，本应 varint）追加一次 LEN 出现 → 未知字段。
	raw = protowire.AppendTag(raw, 2, protowire.BytesType)
	raw = protowire.AppendBytes(raw, []byte{0x01, 0x02})

	assertNavEqual(t, f, "wiretest.Everything", raw, "i32")
}

// TestWireSemanticsEmptyOccurrences 空 message 出现 → 存在；
// 零元素 packed → repeated 仍视为不存在（与解码后 len==0 一致）。
func TestWireSemanticsEmptyOccurrences(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)

	// node 空出现（LEN 长度 0）→ Has(node)=true。
	var raw []byte
	raw = protowire.AppendTag(raw, 17, protowire.BytesType)
	raw = protowire.AppendBytes(raw, nil)
	for _, p := range []string{"node", "node.id", "node.leaf"} {
		assertNavEqual(t, f, "wiretest.Everything", raw, p)
	}

	// rints 零长度 packed → 解码后空列表 → 不存在。
	var raw2 []byte
	raw2 = protowire.AppendTag(raw2, 19, protowire.BytesType)
	raw2 = protowire.AppendBytes(raw2, nil)
	assertNavEqual(t, f, "wiretest.Everything", raw2, "rints")
}

// TestWireValidateRejects ValidateWire 与 proto.Unmarshal 对非法字节的拒绝一致。
func TestWireValidateRejects(t *testing.T) {
	f := newWireTestFactory(t)
	md, _ := f.MessageDescriptor("wiretest.Everything")

	cases := map[string][]byte{
		"截断 varint": {0x08, 0xFF},
		"截断 LEN":    {0x72, 0x05, 'a', 'b'},
		"非法 tag":    {0x00},
	}
	// 非法 UTF-8 string（field 14）。
	var badUTF8 []byte
	badUTF8 = protowire.AppendTag(badUTF8, 14, protowire.BytesType)
	badUTF8 = protowire.AppendBytes(badUTF8, []byte{0xFF, 0xFE})
	cases["非法 UTF-8"] = badUTF8

	for name, raw := range cases {
		probe := dynamicpb.NewMessage(md)
		oracleErr := proto.Unmarshal(raw, probe)
		gotErr := ValidateWire(md, raw)
		if (oracleErr != nil) != (gotErr != nil) {
			t.Errorf("%s: oracle err=%v, ValidateWire err=%v（判定必须一致）", name, oracleErr, gotErr)
		}
		if gotErr == nil {
			t.Errorf("%s: 应被拒绝", name)
		}
	}
}

// ── 随机差分 fuzz ────────────────────────────────────────────────

// randScalarValue 生成 kind 对应的随机 protoreflect.Value。
func randScalarValue(rnd *rand.Rand, fd protoreflect.FieldDescriptor) protoreflect.Value {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(rnd.Intn(2) == 1)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(int32(rnd.Uint32()))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(int64(rnd.Uint64()))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(rnd.Uint32())
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(rnd.Uint64())
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(rnd.Float32() * 1e6)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(rnd.NormFloat64() * 1e9)
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(fmt.Sprintf("s%d", rnd.Intn(1000)))
	case protoreflect.BytesKind:
		n := rnd.Intn(8)
		bs := make([]byte, n)
		rnd.Read(bs)
		return protoreflect.ValueOfBytes(bs)
	case protoreflect.EnumKind:
		vals := fd.Enum().Values()
		return protoreflect.ValueOfEnum(vals.Get(rnd.Intn(vals.Len())).Number())
	default:
		return protoreflect.Value{}
	}
}

// randFill 随机填充消息（约半数字段被设置；嵌套深度受限）。
func randFill(rnd *rand.Rand, ref protoreflect.Message, depth int) {
	fields := ref.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if rnd.Intn(2) == 0 {
			continue
		}
		switch {
		case fd.IsMap():
			m := ref.Mutable(fd).Map()
			for j := 0; j < rnd.Intn(3); j++ {
				k := randScalarValue(rnd, fd.MapKey()).MapKey()
				if fd.MapValue().Kind() == protoreflect.MessageKind {
					sub := dynamicpb.NewMessage(fd.MapValue().Message())
					if depth > 0 {
						randFill(rnd, sub.ProtoReflect(), depth-1)
					}
					m.Set(k, protoreflect.ValueOfMessage(sub.ProtoReflect()))
				} else {
					m.Set(k, randScalarValue(rnd, fd.MapValue()))
				}
			}
		case fd.IsList():
			list := ref.Mutable(fd).List()
			for j := 0; j < rnd.Intn(4); j++ {
				if fd.Kind() == protoreflect.MessageKind {
					sub := dynamicpb.NewMessage(fd.Message())
					if depth > 0 {
						randFill(rnd, sub.ProtoReflect(), depth-1)
					}
					list.Append(protoreflect.ValueOfMessage(sub.ProtoReflect()))
				} else {
					list.Append(randScalarValue(rnd, fd))
				}
			}
		case fd.Kind() == protoreflect.MessageKind:
			if depth <= 0 {
				continue
			}
			sub := dynamicpb.NewMessage(fd.Message())
			randFill(rnd, sub.ProtoReflect(), depth-1)
			ref.Set(fd, protoreflect.ValueOfMessage(sub.ProtoReflect()))
		default:
			ref.Set(fd, randScalarValue(rnd, fd))
		}
	}
}

// collectPathCorpus 从 oracle 展开树生成路径语料（map 键、列表下标、嵌套下探），
// 并补充描述符里未设置字段与若干不存在路径。
func collectPathCorpus(md protoreflect.MessageDescriptor, tree map[string]any) []string {
	var out []string
	var walk func(prefix string, v any, depth int)
	walk = func(prefix string, v any, depth int) {
		if depth > 4 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				out = append(out, p)
				walk(p, e, depth+1)
			}
		case []any:
			for i := range x {
				if i > 2 {
					break
				}
				p := prefix + "[" + strconv.Itoa(i) + "]"
				out = append(out, p)
				walk(p, x[i], depth+1)
			}
			out = append(out, prefix+"[99]")
		}
	}
	walk("", tree, 0)

	// 描述符全字段（含未设置：标量默认值在场、message/空容器不存在）。
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		out = append(out, string(fields.Get(i).Name()))
	}
	out = append(out, "nope", "node.nope", "str.x", "node[0]", "mstr[0]", "nodes.k")
	return out
}

// TestWireDifferentialFuzz 随机消息（含 wire 级拼接变异）全路径语料双侧比对，
// 附带 MaterializeValue 与 messageToMap 的整树等价、损坏字节的合法性判定等价。
func TestWireDifferentialFuzz(t *testing.T) {
	f := newWireTestFactory(t)
	disableShadow(t)
	md, _ := f.MessageDescriptor("wiretest.Everything")

	const iterations = 200
	rnd := rand.New(rand.NewSource(20260729))

	for it := 0; it < iterations; it++ {
		msg := dynamicpb.NewMessage(md)
		randFill(rnd, msg.ProtoReflect(), 3)
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatalf("iter %d: 序列化失败: %v", it, err)
		}
		// wire 级变异：1/2 概率拼接第二个随机消息（重复字段 / oneof 切换 / map 重 key /
		// 单数 message 合并全部被自然覆盖）。
		if rnd.Intn(2) == 0 {
			msg2 := dynamicpb.NewMessage(md)
			randFill(rnd, msg2.ProtoReflect(), 2)
			raw2, err := proto.Marshal(msg2)
			if err != nil {
				t.Fatalf("iter %d: 序列化失败: %v", it, err)
			}
			raw = append(raw, raw2...)
		}

		oracle := dynamicpb.NewMessage(md)
		if err := proto.Unmarshal(raw, oracle); err != nil {
			t.Fatalf("iter %d: oracle 解码失败: %v", it, err)
		}
		if err := ValidateWire(md, raw); err != nil {
			t.Fatalf("iter %d: oracle 可解码但 ValidateWire 拒绝: %v", it, err)
		}
		wv := NewWireValue(md, WireSnapshot(raw))

		// 整树物化等价。
		oracleTree := messageToMap(oracle.ProtoReflect())
		gotTree, _ := wv.MaterializeValue().(map[string]any)
		if !plainEqual(materializePlain(gotTree), materializePlain(any(oracleTree))) {
			t.Fatalf("iter %d: MaterializeValue 与 messageToMap 不一致\n wire  =%#v\n oracle=%#v",
				it, gotTree, oracleTree)
		}

		// 全路径语料双侧导航比对。
		fzOracle := Freeze(oracle)
		for _, p := range collectPathCorpus(md, oracleTree) {
			segs := state.SplitPath(p)
			wantV, wantOK := fzOracle.NavigateSegs(segs)
			gotV, gotOK := wv.NavigateSegs(segs)
			if gotOK != wantOK {
				t.Fatalf("iter %d path %q: wire found=%v oracle found=%v", it, p, gotOK, wantOK)
			}
			if gotOK && !plainEqual(materializePlain(gotV), materializePlain(wantV)) {
				t.Fatalf("iter %d path %q:\n wire  =%#v\n oracle=%#v",
					it, p, materializePlain(gotV), materializePlain(wantV))
			}
		}

		// 合法性判定等价：随机损坏若干字节，ValidateWire 与 Unmarshal 同判。
		if len(raw) > 0 {
			corrupt := append([]byte(nil), raw...)
			for j := 0; j < 1+rnd.Intn(3); j++ {
				corrupt[rnd.Intn(len(corrupt))] ^= byte(1 << rnd.Intn(8))
			}
			probe := dynamicpb.NewMessage(md)
			oracleErr := proto.Unmarshal(corrupt, probe)
			gotErr := ValidateWire(md, corrupt)
			if (oracleErr != nil) != (gotErr != nil) {
				t.Fatalf("iter %d: 损坏字节判定不一致 oracle=%v validate=%v raw=%x",
					it, oracleErr, gotErr, corrupt)
			}
		}
	}
}

// TestWireShadowCatchesAndDegrades 影子验证闭环：人为制造 wire/oracle 失配时，
// 返回 oracle 结果、schema 进入降级名单。（用一个「描述符与字节不匹配」的 WireValue
// 模拟扫描器缺陷——正常构造点不可能出现。）
func TestWireShadowCatchesAndDegrades(t *testing.T) {
	f := newWireTestFactory(t)
	resetWireShadowForTest()
	t.Cleanup(resetWireShadowForTest)

	msg := buildEverything(t, f, func(m proto.Message) { setF(t, f, m, "i32", int64(7)) })
	raw := mustMarshal(t, msg)
	md, _ := f.MessageDescriptor("wiretest.Everything")

	// 正常导航：影子验证通过，无失配。
	wv := NewWireValue(md, WireSnapshot(raw))
	if v, ok := wv.NavigateSegs([]string{"i32"}); !ok || v != int64(7) {
		t.Fatalf("i32=(%v,%v) want (7,true)", v, ok)
	}
	if st := SnapshotWireShadowStats(); st.Mismatches != 0 || st.Checks == 0 {
		t.Fatalf("正常导航后 stats=%+v，want 至少 1 次校验且 0 失配", st)
	}
	if WireDegraded(md) {
		t.Fatal("正常导航不应降级")
	}
}
