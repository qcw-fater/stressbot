package shared

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

// testRedisConfig 从 STRESSBOT_TEST_REDIS 环境变量读取 host:port 并构造 RedisConfig。
// 环境变量未设置时跳过当前测试。
func testRedisConfig(t *testing.T) RedisConfig {
	t.Helper()
	addr := os.Getenv("STRESSBOT_TEST_REDIS")
	if addr == "" {
		t.Skip("未设置 STRESSBOT_TEST_REDIS，跳过 Redis 集成测试")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("解析 STRESSBOT_TEST_REDIS 失败 (期望 host:port): %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("解析 STRESSBOT_TEST_REDIS 端口失败: %v", err)
	}
	return RedisConfig{Host: host, Port: port, KeyPrefix: "sbtest"}
}

// newTestStore 连接测试 Redis；未配置 STRESSBOT_TEST_REDIS 时跳过整组集成测试。
// 用法：STRESSBOT_TEST_REDIS=127.0.0.1:6379 go test ./state/shared/ -run Integration
func newTestStore(t *testing.T) *RedisStore {
	t.Helper()
	cfg := testRedisConfig(t)
	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	runID := "test-" + time.Now().Format("150405.000000")
	store, err := NewRedisStore(resolved, runID)
	if err != nil {
		t.Fatalf("connect redis: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Cleanup(context.Background())
		_ = store.Close()
	})
	return store
}

func TestIntegrationKV(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, ok, _ := mustGet(t, store, "missing"); ok {
		t.Fatal("missing key should not exist")
	}

	if err := store.Set(ctx, "name", "alice", 0); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, ok, err := store.Get(ctx, "name")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if v != "alice" {
		t.Fatalf("got %v want alice", v)
	}

	ok, err = store.Exists(ctx, "name")
	if err != nil || !ok {
		t.Fatalf("exists: ok=%v err=%v", ok, err)
	}
	if err := store.Delete(ctx, "name"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := store.Get(ctx, "name"); ok {
		t.Fatal("deleted key still present")
	}
}

func TestIntegrationCounter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	n, err := store.Incr(ctx, "c", 1, 0)
	if err != nil || n != 1 {
		t.Fatalf("incr1: n=%d err=%v", n, err)
	}
	n, err = store.Incr(ctx, "c", 5, 0)
	if err != nil || n != 6 {
		t.Fatalf("incr5: n=%d err=%v", n, err)
	}
	// delta=0 不应被改写成 +1，而是读取当前值
	n, err = store.Incr(ctx, "c", 0, 0)
	if err != nil || n != 6 {
		t.Fatalf("incr0 should read current value: n=%d err=%v", n, err)
	}
}

// TestIntegrationDeleteSpansTypes 验证 del/exists 跨数据类型命名空间。
func TestIntegrationDeleteSpansTypes(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_, _ = store.Incr(ctx, "x", 3, 0)
	_ = store.QueuePush(ctx, "x", "a", 0)

	if ok, _ := store.Exists(ctx, "x"); !ok {
		t.Fatal("exists should see counter/queue under key x")
	}
	if err := store.Delete(ctx, "x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ok, _ := store.Exists(ctx, "x"); ok {
		t.Fatal("delete should remove all data types under key x")
	}
	if n, _ := store.QueueLen(ctx, "x"); n != 0 {
		t.Fatalf("queue not deleted: len=%d", n)
	}
}

func TestIntegrationClaim(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ok, err := store.Claim(ctx, "lock", "a", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim a: ok=%v err=%v", ok, err)
	}
	ok, err = store.Claim(ctx, "lock", "b", 30*time.Second)
	if err != nil || ok {
		t.Fatalf("claim b should fail: ok=%v err=%v", ok, err)
	}
	owner, ok, err := store.Owner(ctx, "lock")
	if err != nil || !ok || owner != "a" {
		t.Fatalf("owner: %q ok=%v err=%v", owner, ok, err)
	}
	released, err := store.Release(ctx, "lock", "b")
	if err != nil || released {
		t.Fatalf("release by wrong owner should fail: released=%v err=%v", released, err)
	}
	released, err = store.Release(ctx, "lock", "a")
	if err != nil || !released {
		t.Fatalf("release by owner: released=%v err=%v", released, err)
	}
}

func TestIntegrationQueue(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_ = store.QueuePush(ctx, "q", "x", 0)
	_ = store.QueuePush(ctx, "q", "y", 0)
	n, _ := store.QueueLen(ctx, "q")
	if n != 2 {
		t.Fatalf("len=%d want 2", n)
	}
	v, ok, _ := store.QueuePop(ctx, "q")
	if !ok || v != "x" {
		t.Fatalf("pop1=%v ok=%v want x", v, ok)
	}
	v, ok, _ = store.QueuePop(ctx, "q")
	if !ok || v != "y" {
		t.Fatalf("pop2=%v ok=%v want y", v, ok)
	}
	if _, ok, _ := store.QueuePop(ctx, "q"); ok {
		t.Fatal("empty queue pop should return ok=false")
	}
}

func TestIntegrationHash(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	_ = store.HashSet(ctx, "h", "a", float64(1), 0)
	_ = store.HashSet(ctx, "h", "b", "two", 0)
	n, err := store.HashIncr(ctx, "h", "a", 4, 0)
	if err != nil || n != 5 {
		t.Fatalf("hash_incr: n=%d err=%v", n, err)
	}
	all, ok, err := store.HashGetAll(ctx, "h")
	if err != nil || !ok {
		t.Fatalf("hgetall: ok=%v err=%v", ok, err)
	}
	if all["a"] != int64(5) || all["b"] != "two" {
		t.Fatalf("hgetall mismatch: %#v", all)
	}
}

func TestIntegrationCleanup(t *testing.T) {
	cfg := testRedisConfig(t)
	resolved, _ := cfg.Resolve()
	store, err := NewRedisStore(resolved, "cleanup-"+time.Now().Format("150405.000000"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	_ = store.Set(ctx, "k1", "v", 0)
	_, _ = store.Incr(ctx, "c1", 1, 0)
	_ = store.QueuePush(ctx, "q1", "x", 0)

	if err := store.Cleanup(ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, ok, _ := store.Get(ctx, "k1"); ok {
		t.Fatal("key survived cleanup")
	}
}

func mustGet(t *testing.T, store *RedisStore, key string) (any, bool, error) {
	t.Helper()
	v, ok, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return v, ok, nil
}
