package protox

import (
	"math/rand"
	"testing"

	"stressbot/state"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

// TestNavProgramParity 预解析 fd 链与逐层 ByName 的导航结果逐字一致
// （随机消息 + 全路径语料，含 [0..2]/[99] 下标段——同时覆盖列表下标早退与
// 全量收集两条分支）。三方对拍：程序化导航 ≡ 无程序导航 ≡ Frozen oracle。
func TestNavProgramParity(t *testing.T) {
	f := newWireTestFactory(t)
	defer f.Close()
	disableShadow(t)
	md, _ := f.MessageDescriptor("wiretest.Everything")

	rnd := rand.New(rand.NewSource(20260730))
	for it := range 50 {
		msg := dynamicpb.NewMessage(md)
		randFill(rnd, msg.ProtoReflect(), 3)
		raw, err := proto.Marshal(msg)
		if err != nil {
			t.Fatalf("iter %d: 序列化失败: %v", it, err)
		}
		oracle := dynamicpb.NewMessage(md)
		if err := proto.Unmarshal(raw, oracle); err != nil {
			t.Fatalf("iter %d: oracle 解码失败: %v", it, err)
		}
		fzOracle := Freeze(oracle)
		tree := messageToMap(oracle.ProtoReflect())

		for _, p := range collectPathCorpus(md, tree) {
			segs := state.SplitPath(p)
			prog := compileNavFds(md, segs)

			progV, progOK := wireNavigate(md, raw, segs, prog)
			bareV, bareOK := wireNavigate(md, raw, segs, nil)
			wantV, wantOK := fzOracle.NavigateSegs(segs)

			if progOK != bareOK || progOK != wantOK {
				t.Fatalf("iter %d path %q: found 不一致 prog=%v bare=%v oracle=%v",
					it, p, progOK, bareOK, wantOK)
			}
			if !progOK {
				continue
			}
			pm, bm, wm := materializePlain(progV), materializePlain(bareV), materializePlain(wantV)
			if !plainEqual(pm, bm) || !plainEqual(pm, wm) {
				t.Fatalf("iter %d path %q: 值不一致 prog=%#v bare=%#v oracle=%#v",
					it, p, pm, bm, wm)
			}
		}
	}
}

// TestNavInfoDescIdentityReplace proto 重载（同名 schema、不同描述符身份）后
// 驻留条目被替换：fd 链重编译、首 K 全查计数重新开始。
func TestNavInfoDescIdentityReplace(t *testing.T) {
	f1 := newWireTestFactory(t)
	defer f1.Close()
	f2 := newWireTestFactory(t)
	defer f2.Close()
	resetWireShadowForTest()
	t.Cleanup(resetWireShadowForTest)

	md1, _ := f1.MessageDescriptor("wiretest.Everything")
	md2, _ := f2.MessageDescriptor("wiretest.Everything")
	if md1 == md2 {
		t.Fatal("两个工厂应产出不同身份的描述符")
	}
	segs := []string{"i32"}

	e1 := navInfoFor(md1, segs)
	if e1 == nil || e1.desc != md1 {
		t.Fatalf("首次驻留失败: %+v", e1)
	}
	e1.seen.Store(shadowFirstK) // 模拟已完成首 K 全查

	e2 := navInfoFor(md2, segs)
	if e2 == nil || e2.desc != md2 {
		t.Fatalf("重载后应替换为新描述符条目: %+v", e2)
	}
	if e2 == e1 || e2.seen.Load() != 0 {
		t.Fatalf("替换条目应重新开始首 K 计数: seen=%d", e2.seen.Load())
	}
	// 同条目重复查找应命中缓存（指针相等）。
	if e2b := navInfoFor(md2, segs); e2b != e2 {
		t.Fatal("重复查找应命中同一条目")
	}
}

// TestNavResolveShadowCadence 影子采样节奏与旧 shadowShouldVerify 一致：
// 每 (schema, 路径) 首 shadowFirstK 次必查，之后进入 per-schema 周期采样。
func TestNavResolveShadowCadence(t *testing.T) {
	f := newWireTestFactory(t)
	defer f.Close()
	resetWireShadowForTest()
	t.Cleanup(resetWireShadowForTest)

	md, _ := f.MessageDescriptor("wiretest.Everything")
	segs := []string{"str"}

	for i := range shadowFirstK {
		if _, verify := navResolve(md, segs); !verify {
			t.Fatalf("第 %d 次应触发首 K 全查", i+1)
		}
	}
	// 稳态：远小于采样周期的窗口内不应连续命中。
	hits := 0
	for range 64 {
		if _, verify := navResolve(md, segs); verify {
			hits++
		}
	}
	if hits > 1 {
		t.Fatalf("稳态 64 次内命中 %d 次，采样周期 %d 下应至多 1 次", hits, shadowSampleEvery)
	}

	// fd 链应已驻留且与 ByName 一致。
	fds, _ := navResolve(md, segs)
	if len(fds) != 1 || fds[0] == nil || fds[0].Name() != "str" {
		t.Fatalf("驻留 fd 链异常: %v", fds)
	}
}

// TestWireCollectListLimit 早退收集的前缀与全量收集逐元素一致，且 limit
// 覆盖 packed 整块解出可能略多于 limit 的情形。
func TestWireCollectListLimit(t *testing.T) {
	f := newWireTestFactory(t)
	defer f.Close()
	disableShadow(t)
	md, _ := f.MessageDescriptor("wiretest.Everything")

	rnd := rand.New(rand.NewSource(42))
	msg := dynamicpb.NewMessage(md)
	randFill(rnd, msg.ProtoReflect(), 3)
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}

	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if !fd.IsList() {
			continue
		}
		full, ok := wireCollectList(raw, fd, 0)
		if !ok {
			t.Fatalf("字段 %s 全量收集失败", fd.Name())
		}
		for _, limit := range []int{1, 2, len(full), len(full) + 5} {
			part, ok := wireCollectList(raw, fd, limit)
			if !ok {
				t.Fatalf("字段 %s limit=%d 收集失败", fd.Name(), limit)
			}
			want := len(full)
			if want > limit {
				// 至少 limit 个（packed 整块可能略多），且不超过全量。
				if len(part) < limit || len(part) > want {
					t.Fatalf("字段 %s limit=%d: 收集 %d 个，期望 [%d,%d]",
						fd.Name(), limit, len(part), limit, want)
				}
			} else if len(part) != want {
				t.Fatalf("字段 %s limit=%d: 收集 %d 个，期望全量 %d",
					fd.Name(), limit, len(part), want)
			}
			for j := range part {
				if !plainEqual(materializePlain(part[j].terminalValue(fd)),
					materializePlain(full[j].terminalValue(fd))) {
					t.Fatalf("字段 %s limit=%d: 第 %d 个元素与全量不一致", fd.Name(), limit, j)
				}
			}
		}
	}
}
