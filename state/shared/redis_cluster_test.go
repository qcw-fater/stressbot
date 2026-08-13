package shared

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// extractHashtag 模拟 Redis Cluster 的 hashtag 提取：取第一个 {...} 内的子串。
// 空串表示无 hashtag（slot 由整个 key 决定）。hashtag 相同 → CRC16 相同 → 同 slot。
func extractHashtag(key string) string {
	i := strings.IndexByte(key, '{')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(key[i+1:], '}')
	if j < 0 {
		return ""
	}
	return key[i+1 : i+1+j]
}

// TestKeyAndIndexKeyShareHashtag 验证数据 key 与索引集合 key 共用 {runID} hashtag，
// 从而在 Redis Cluster 下落同一 slot——这是修复 CROSSSLOT 的核心契约。
// 生产改动会让本测试失败的点：redis_store.go 的 key()/indexKey() 不再带 {runID} hashtag。
func TestKeyAndIndexKeyShareHashtag(t *testing.T) {
	s := &RedisStore{cfg: ResolvedRedisConfig{KeyPrefix: "stressbot"}, runID: "run-abc"}

	cases := []struct{ typ, userKey string }{
		{typeCounter, "ranked:v2:seq"},
		{typeQueue, "ranked:v2:waiting"},
		{typeHash, "ranked:v2:team:t1:members"},
	}
	for _, c := range cases {
		dataKey := s.key(c.typ, c.userKey)
		idxKey := s.indexKey()
		dh := extractHashtag(dataKey)
		if dh != "run-abc" {
			t.Fatalf("数据 key %q 的 hashtag 应为 run-abc，实际 %q", dataKey, dh)
		}
		if extractHashtag(idxKey) != dh {
			t.Fatalf("索引 key %q 与数据 key %q 的 hashtag 不同（Cluster 下跨 slot → CROSSSLOT）", idxKey, dataKey)
		}
	}
}

// TestIsClusterInfoReply 验证 CLUSTER INFO 响应判读：集群节点返回 cluster_enabled:1，
// 单机节点对该命令返回 ERR（不识别 cluster 子命令）。
// 生产改动会让本测试失败的点：isClusterInfoReply 误判集群/单机，导致选错客户端。
func TestIsClusterInfoReply(t *testing.T) {
	errStandalone := errors.New("ERR This instance has cluster support disabled")
	cases := []struct {
		name string
		res  string
		err  error
		want bool
	}{
		{"cluster_ok", "cluster_state:ok\r\ncluster_enabled:1\r\ncluster_slots_assigned:16384\r\n", nil, true},
		{"cluster_state_fail_but_enabled", "cluster_state:fail\r\ncluster_enabled:1\r\n", nil, true},
		{"standalone_disabled_flag", "cluster_enabled:0\r\n", nil, false},
		{"standalone_err", "", errStandalone, false},
		{"empty_no_flag", "", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isClusterInfoReply(c.res, c.err); got != c.want {
				t.Fatalf("isClusterInfoReply(%q, %v)=%v, want %v", c.res, c.err, got, c.want)
			}
		})
	}
}

// TestNewRedisStoreRejectsUnsafeRunID 验证含 { 或 } 的 runID 在拨号前被拒，
// 防止截断 {runID} hashtag（截断后数据 key 与索引 key 可能不同 slot）。
// 用文案断言而非「err != nil」，避免 Redis 未连通时探测失败造成假通过。
// 生产改动会让本测试失败的点：NewRedisStore 未在拨号前校验 runID 是否含花括号。
func TestNewRedisStoreRejectsUnsafeRunID(t *testing.T) {
	cfg := ResolvedRedisConfig{Host: "127.0.0.1", Port: 6379}
	for _, bad := range []string{"run{x", "run}x", "run{}x"} {
		_, err := NewRedisStore(cfg, bad)
		if err == nil || !strings.Contains(err.Error(), "runID") {
			t.Fatalf("runID %q 应在拨号前被拒（错误需含 runID），实际 err=%v", bad, err)
		}
	}
}

// TestIntegrationDetectClusterMode 验证本机测试 Redis 被探测为单机（非集群）。
// 集群形态只能在线上验证；本测试用 STRESSBOT_TEST_REDIS 指向的单机 Redis 守住"判单机"回归，
// 确保 detectClusterMode/isClusterInfoReply 不会误把单机认成集群。
func TestIntegrationDetectClusterMode(t *testing.T) {
	cfg := testRedisConfig(t)
	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	c := redis.NewClient(&redis.Options{
		Addr:        fmt.Sprintf("%s:%d", resolved.Host, resolved.Port),
		DialTimeout: resolved.DialTimeout,
	})
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), resolved.DialTimeout)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if detectClusterMode(c) {
		t.Fatal("本机测试 Redis 应探测为单机，实际判集群")
	}
}
