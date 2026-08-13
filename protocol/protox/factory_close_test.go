package protox

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func statsListLens() (wire, frozen int) {
	statsMu.Lock()
	defer statsMu.Unlock()
	return len(statsCaches), len(statsFrozenCaches)
}

// TestFactoryCloseReleasesCaches 验证 Close 清空两个去重缓存并从统计列表反注册：
// 任务级 Factory 不释放会把缓存条目跨任务钉住（8000 人实测每轮泄漏 ~268MB）。
func TestFactoryCloseReleasesCaches(t *testing.T) {
	wireBefore, frozenBefore := statsListLens()

	f := newWireTestFactory(t)

	wireAfterNew, frozenAfterNew := statsListLens()
	if wireAfterNew != wireBefore+1 || frozenAfterNew != frozenBefore+1 {
		t.Fatalf("NewFactory 应各注册一个缓存: wire %d→%d frozen %d→%d",
			wireBefore, wireAfterNew, frozenBefore, frozenAfterNew)
	}

	// 各插入一条内容，确认 purge 前有钉住量。
	raw := mustMarshal(t, buildEverything(t, f, func(m proto.Message) {
		setF(t, f, m, "str", "close-test")
		setF(t, f, m, "i32", int64(42))
	}))
	if _, err := f.WireShared("wiretest.Everything", raw); err != nil {
		t.Fatalf("WireShared 失败: %v", err)
	}
	if _, err := f.ParseFrozenShared("wiretest.Everything", raw); err != nil {
		t.Fatalf("ParseFrozenShared 失败: %v", err)
	}
	if s := f.wireCache.Stats(); s.Entries == 0 || s.RawBytes == 0 {
		t.Fatalf("WireCache 插入后应有条目: %+v", s)
	}
	if s := f.frozenCache.Stats(); s.Entries == 0 || s.CostBytes == 0 {
		t.Fatalf("FrozenCache 插入后应有条目: %+v", s)
	}

	f.Close()
	f.Close() // 幂等

	if s := f.wireCache.Stats(); s.Entries != 0 || s.RawBytes != 0 {
		t.Fatalf("Close 后 WireCache 应清空: %+v", s)
	}
	if s := f.frozenCache.Stats(); s.Entries != 0 || s.CostBytes != 0 {
		t.Fatalf("Close 后 FrozenCache 应清空: %+v", s)
	}
	wireAfterClose, frozenAfterClose := statsListLens()
	if wireAfterClose != wireBefore || frozenAfterClose != frozenBefore {
		t.Fatalf("Close 后统计列表应回落: wire %d→%d(期望 %d) frozen %d→%d(期望 %d)",
			wireAfterNew, wireAfterClose, wireBefore, frozenAfterNew, frozenAfterClose, frozenBefore)
	}
}
