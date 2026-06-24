package script

import (
	"context"
	"testing"
	"time"
)

// fakeKVStore 是 sharedstate.Store 的最小内存实现，仅 KV 走真实存储，其余返回零值。
// 用于验证 share.* 的协作式 awaitIO 路径（yield WaitIO → 后台作业 → renderer 产出 Lua 值）。
type fakeKVStore struct{ kv map[string]any }

func newFakeKVStore() *fakeKVStore { return &fakeKVStore{kv: map[string]any{}} }

func (s *fakeKVStore) Set(_ context.Context, key string, value any, _ time.Duration) error {
	s.kv[key] = value
	return nil
}
func (s *fakeKVStore) Get(_ context.Context, key string) (any, bool, error) {
	v, ok := s.kv[key]
	return v, ok, nil
}
func (s *fakeKVStore) Delete(_ context.Context, key string) error { delete(s.kv, key); return nil }
func (s *fakeKVStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := s.kv[key]
	return ok, nil
}
func (s *fakeKVStore) Expire(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *fakeKVStore) Incr(context.Context, string, int64, time.Duration) (int64, error) {
	return 0, nil
}
func (s *fakeKVStore) Claim(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *fakeKVStore) Release(context.Context, string, string) (bool, error) { return true, nil }
func (s *fakeKVStore) Owner(context.Context, string) (string, bool, error)   { return "", false, nil }
func (s *fakeKVStore) Renew(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *fakeKVStore) QueuePush(context.Context, string, any, time.Duration) error { return nil }
func (s *fakeKVStore) QueuePop(context.Context, string) (any, bool, error)         { return nil, false, nil }
func (s *fakeKVStore) QueueLen(context.Context, string) (int64, error)             { return 0, nil }
func (s *fakeKVStore) QueueExpire(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *fakeKVStore) HashSet(context.Context, string, string, any, time.Duration) error {
	return nil
}
func (s *fakeKVStore) HashGet(context.Context, string, string) (any, bool, error) {
	return nil, false, nil
}
func (s *fakeKVStore) HashGetAll(context.Context, string) (map[string]any, bool, error) {
	return nil, false, nil
}
func (s *fakeKVStore) HashDelete(context.Context, string, string) error { return nil }
func (s *fakeKVStore) HashIncr(context.Context, string, string, int64, time.Duration) (int64, error) {
	return 0, nil
}
func (s *fakeKVStore) HashExpire(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *fakeKVStore) DefaultClaimTTL() time.Duration { return 0 }
func (s *fakeKVStore) OpTimeout() time.Duration       { return time.Second }
func (s *fakeKVStore) Cleanup(context.Context) error  { return nil }
func (s *fakeKVStore) Close() error                   { return nil }

// TestShareIO_YieldsWaitIOAndResumes 验证 share.* 走协作式 awaitIO：
// 脚本 yield 一次 WaitIO（交给 Waiter 跑后台作业），renderer 在执行器 goroutine 上产出 Lua
// 返回值，脚本据此续跑——set 后能 get 回原值。
func TestShareIO_YieldsWaitIOAndResumes(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"share.lua": `function execute(r)
  local share = require("share")
  local ok, err = share.set("k", "hello")
  if not ok then error("set failed: " .. tostring(err)) end
  local v, found = share.get("k")
  if not found then error("get not found") end
  if v ~= "hello" then error("get mismatch: " .. tostring(v)) end
  return nil
end`,
	})

	w := &recordingWaiter{}
	store := newFakeKVStore()
	err := runScript(t, rp, &Context{Waiter: w, Shared: store, Ctx: context.Background()}, "share.lua")
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	// set + get 各 yield 一次 WaitIO。
	if len(w.specs) != 2 {
		t.Fatalf("应 yield 两次 WaitIO（set + get），实际 %d 次", len(w.specs))
	}
	for i, sp := range w.specs {
		if sp.Kind != WaitIO {
			t.Fatalf("第 %d 次 yield Kind=%d，want WaitIO(%d)", i, sp.Kind, WaitIO)
		}
	}
	if store.kv["k"] != "hello" {
		t.Fatalf("share.set 未写入底层存储，实际 %+v", store.kv["k"])
	}
}

// TestShareIO_NotEnabled 未启用共享状态（Shared 为 nil）时 share.* 同步返回错误，不 yield。
func TestShareIO_NotEnabled(t *testing.T) {
	rp := newTestPool(t, map[string]string{
		"noshare.lua": `function execute(r)
  local share = require("share")
  local ok, err = share.set("k", "v")
  if ok then error("expected not-enabled error") end
  return nil
end`,
	})

	w := &recordingWaiter{}
	err := runScript(t, rp, &Context{Waiter: w}, "noshare.lua")
	if err != nil {
		t.Fatalf("不期望 error: %v", err)
	}
	if len(w.specs) != 0 {
		t.Fatalf("未启用共享状态不应 yield，实际 %d 次", len(w.specs))
	}
}
