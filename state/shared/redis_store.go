package shared

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// key 类型段。
const (
	typeKV      = "kv"
	typeCounter = "counter"
	typeClaim   = "claim"
	typeQueue   = "queue"
	typeHash    = "hash"
)

// cleanupBatchSize Cleanup 时每批 DEL 的 key 数量上限。
const cleanupBatchSize = 500

// dataTypes 是 del/exists/expire 跨命名空间作用的「数据类型」集合。
// 不含 claim：锁有独立的 release/renew 语义，不应被通用 del 绕过 owner 校验删除。
var dataTypes = []string{typeKV, typeCounter, typeQueue, typeHash}

// RedisStore 基于 Redis 的共享状态实现。
//
// 一个 RedisStore 对应一个任务运行实例（runID）。同一进程内所有 Robot 共享一个实例；
// 分布式下不同 Agent 各持有自己的实例但 runID 相同，从而落在同一命名空间。
type RedisStore struct {
	rdb   redis.UniversalClient
	cfg   ResolvedRedisConfig
	runID string
}

var _ Store = (*RedisStore)(nil)

// NewRedisStore 创建并连通 Redis。会做一次 PING 验证连接，失败返回错误。
// NewRedisStore 创建并连通 Redis，自动适配单机/集群形态。会做一次 PING + CLUSTER INFO
// 探测，失败返回错误。
//
// runID 须为不含花括号的稳定 token：它作为 {runID} hashtag 决定本 run 所有 key 的
// slot（Cluster 下保证数据 key 与索引集合 key 同 slot）。含 { 或 } 会截断 hashtag，
// 故在此 fail-fast。
func NewRedisStore(cfg ResolvedRedisConfig, runID string) (*RedisStore, error) {
	if runID == "" {
		return nil, fmt.Errorf("state/shared: runID 不能为空")
	}
	if strings.ContainsAny(runID, "{}") {
		return nil, fmt.Errorf("state/shared: runID %q 含花括号，会截断 {runID} hashtag 导致 Cluster 跨 slot", runID)
	}
	rdb, err := dialRedis(cfg)
	if err != nil {
		return nil, err
	}
	return &RedisStore{rdb: rdb, cfg: cfg, runID: runID}, nil
}

// dialRedis 连接 Redis 并探测部署形态，自动选择客户端类型（零配置）：
//   - CLUSTER INFO 响应 cluster_enabled:1 → 集群 → ClusterClient（单一入口地址自动发现拓扑）。
//   - 否则 → 单机 Client（含云上 proxy 模式集群：proxy 内部路由）。
//
// hashtag {runID} 在两种形态下都保证 EVAL 的多 KEYS 落同一 slot：
//   - 真集群：ClusterClient 跨节点路由，hashtag 防 CROSSSLOT；单机客户端此时会在 MOVED 上失败，故必须切换。
//   - 单机/proxy：单机客户端直连或经 proxy，hashtag 对单机无害、对 proxy 后的集群防 CROSSSLOT。
func dialRedis(cfg ResolvedRedisConfig) (redis.UniversalClient, error) {
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	single := redis.NewClient(singleClientOpts(addr, cfg))

	pingCtx, cancel := context.WithTimeout(context.Background(), cfg.DialTimeout)
	defer cancel()
	if err := single.Ping(pingCtx).Err(); err != nil {
		_ = single.Close()
		return nil, fmt.Errorf("state/shared: 连接 Redis 失败 (host=%s port=%d): %w", cfg.Host, cfg.Port, err)
	}

	if detectClusterMode(single) {
		_ = single.Close()
		copts := &redis.ClusterOptions{
			Addrs:           []string{addr},
			Username:        cfg.Username,
			Password:        cfg.Password,
			DialTimeout:     cfg.DialTimeout,
			ReadTimeout:     cfg.ReadTimeout,
			WriteTimeout:    cfg.WriteTimeout,
			ConnMaxLifetime: cfg.ConnMaxLifetime,
		}
		if cfg.MaxOpenConns > 0 {
			copts.PoolSize = cfg.MaxOpenConns
		}
		if cfg.MaxIdleConns > 0 {
			copts.MaxIdleConns = cfg.MaxIdleConns
		}
		return redis.NewClusterClient(copts), nil
	}
	return single, nil
}

// detectClusterMode 通过 CLUSTER INFO 探测是否为集群。
// 集群节点返回 cluster_enabled:1；单机节点对该命令返回 ERR（不识别 cluster 子命令）。
func detectClusterMode(c *redis.Client) bool {
	res, err := c.ClusterInfo(context.Background()).Result()
	return isClusterInfoReply(res, err)
}

// isClusterInfoReply 判读 CLUSTER INFO 响应是否表示集群已启用。拆为纯函数便于单元测试。
func isClusterInfoReply(res string, err error) bool {
	return err == nil && strings.Contains(res, "cluster_enabled:1")
}

// singleClientOpts 构造单机客户端选项（探测与最终单机连接共用）。
func singleClientOpts(addr string, cfg ResolvedRedisConfig) *redis.Options {
	opts := &redis.Options{
		Addr:         addr,
		Username:     cfg.Username,
		Password:     cfg.Password,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}
	if cfg.MaxOpenConns > 0 {
		opts.PoolSize = cfg.MaxOpenConns
	}
	if cfg.MaxIdleConns > 0 {
		opts.MaxIdleConns = cfg.MaxIdleConns
	}
	if cfg.ConnMaxLifetime > 0 {
		opts.ConnMaxLifetime = cfg.ConnMaxLifetime
	}
	return opts
}

// key 构造数据 key：<prefix>:{<runID>}:<type>:<userKey>。
// {runID} 是 Redis Cluster hashtag：本 run 所有数据 key 与索引集合 key 共用同一
// hashtag → 落同一 slot → EVAL 的多 KEYS（数据 key + 索引 key）不会 CROSSSLOT。
// 单机模式下 hashtag 被视为普通字符，行为不受影响。
func (s *RedisStore) key(typ, userKey string) string {
	return fmt.Sprintf("%s:{%s}:%s:%s", s.cfg.KeyPrefix, s.runID, typ, userKey)
}

// indexKey 返回任务级 key 索引集合的 Redis key，供 Cleanup 定位本 run 写入的所有 key。
// 与 key() 共用 {runID} hashtag，Cluster 下与所有数据 key 同 slot。
func (s *RedisStore) indexKey() string {
	return fmt.Sprintf("%s:{%s}:keys", s.cfg.KeyPrefix, s.runID)
}

// dataKeys 返回某个用户 key 在所有数据类型命名空间下的真实 Redis key。
func (s *RedisStore) dataKeys(userKey string) []string {
	out := make([]string, len(dataTypes))
	for i, t := range dataTypes {
		out[i] = s.key(t, userKey)
	}
	return out
}

func ttlSeconds(ttl time.Duration) int64 {
	if ttl <= 0 {
		return 0
	}
	sec := int64(ttl / time.Second)
	if sec <= 0 {
		sec = 1 // 不足 1 秒按 1 秒，避免立即过期
	}
	return sec
}

func (s *RedisStore) DefaultClaimTTL() time.Duration { return s.cfg.DefaultClaimTTL }

func (s *RedisStore) OpTimeout() time.Duration { return s.cfg.OpTimeout }

// ── KV ────────────────────────────────────────────────

func (s *RedisStore) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	enc, err := encodeValue(value)
	if err != nil {
		return err
	}
	k := s.key(typeKV, key)
	return setScript.Run(ctx, s.rdb, []string{k, s.indexKey()}, enc, ttlSeconds(ttl)).Err()
}

func (s *RedisStore) Get(ctx context.Context, key string) (any, bool, error) {
	k := s.key(typeKV, key)
	raw, err := s.rdb.Get(ctx, k).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	v, err := decodeValue(raw)
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// Delete 删除该 key 在所有数据类型命名空间（kv/counter/queue/hash）下的值。
func (s *RedisStore) Delete(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, s.dataKeys(key)...).Err()
}

// Exists 判断该 key 在任一数据类型命名空间下是否存在。
func (s *RedisStore) Exists(ctx context.Context, key string) (bool, error) {
	n, err := s.rdb.Exists(ctx, s.dataKeys(key)...).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Expire 为该 key 在所有数据类型命名空间下设置过期时间，任一设置成功即返回 true。
//
// 单条 EXPIRE 只能作用一个 key，故用 Lua 脚本在服务端遍历 4 个数据类型命名空间，
// 把原先「每命名空间一次 RTT」（4 次）合并为单次脚本调用（1 次 RTT）。
// 秒数经 ttlSeconds 归一：契约规定 ttl<=0 表示「不设置过期」，此处按 no-op（不改动现有 TTL）
// 返回 false，与写路径一致；直接把 0 传给 EXPIRE 会立即删 key，违反契约。
// 正数 ttl 不足 1 秒按 1 秒，避免亚秒截断成 0 后误删。
func (s *RedisStore) Expire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, nil
	}
	n, err := expireScript.Run(ctx, s.rdb, s.dataKeys(key), ttlSeconds(ttl)).Int64()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ── Counter ───────────────────────────────────────────

// Incr 原子增减计数器并返回新值。delta 由调用方决定（Lua 层负责缺省值），
// 这里不改写 delta=0，使「+0 读取当前值」成为合法用法。
func (s *RedisStore) Incr(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error) {
	k := s.key(typeCounter, key)
	return incrScript.Run(ctx, s.rdb, []string{k, s.indexKey()}, delta, ttlSeconds(ttl)).Int64()
}

// ── Claim / Lock ──────────────────────────────────────

func (s *RedisStore) Claim(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = s.cfg.DefaultClaimTTL
	}
	k := s.key(typeClaim, key)
	n, err := claimScript.Run(ctx, s.rdb, []string{k, s.indexKey()}, owner, ttlSeconds(ttl)).Int64()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *RedisStore) Release(ctx context.Context, key, owner string) (bool, error) {
	k := s.key(typeClaim, key)
	n, err := releaseScript.Run(ctx, s.rdb, []string{k}, owner).Int64()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *RedisStore) Owner(ctx context.Context, key string) (string, bool, error) {
	k := s.key(typeClaim, key)
	v, err := s.rdb.Get(ctx, k).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *RedisStore) Renew(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = s.cfg.DefaultClaimTTL
	}
	k := s.key(typeClaim, key)
	n, err := renewScript.Run(ctx, s.rdb, []string{k}, owner, ttlSeconds(ttl)).Int64()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ── Queue / List ──────────────────────────────────────

func (s *RedisStore) QueuePush(ctx context.Context, key string, value any, ttl time.Duration) error {
	enc, err := encodeValue(value)
	if err != nil {
		return err
	}
	k := s.key(typeQueue, key)
	return queuePushScript.Run(ctx, s.rdb, []string{k, s.indexKey()}, enc, ttlSeconds(ttl)).Err()
}

func (s *RedisStore) QueuePop(ctx context.Context, key string) (any, bool, error) {
	k := s.key(typeQueue, key)
	raw, err := s.rdb.LPop(ctx, k).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	v, err := decodeValue(raw)
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (s *RedisStore) QueueLen(ctx context.Context, key string) (int64, error) {
	return s.rdb.LLen(ctx, s.key(typeQueue, key)).Result()
}

func (s *RedisStore) QueueExpire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	// 契约：ttl<=0 表示不设置过期 → no-op；直接传 0 给 EXPIRE 会立即删 key。正数亚秒按 1 秒。
	if ttl <= 0 {
		return false, nil
	}
	return s.rdb.Expire(ctx, s.key(typeQueue, key), time.Duration(ttlSeconds(ttl))*time.Second).Result()
}

// ── Hash ──────────────────────────────────────────────

func (s *RedisStore) HashSet(ctx context.Context, key, field string, value any, ttl time.Duration) error {
	enc, err := encodeValue(value)
	if err != nil {
		return err
	}
	k := s.key(typeHash, key)
	return hashSetScript.Run(ctx, s.rdb, []string{k, s.indexKey()}, field, enc, ttlSeconds(ttl)).Err()
}

func (s *RedisStore) HashGet(ctx context.Context, key, field string) (any, bool, error) {
	k := s.key(typeHash, key)
	raw, err := s.rdb.HGet(ctx, k, field).Result()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	v, err := decodeValue(raw)
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (s *RedisStore) HashGetAll(ctx context.Context, key string) (map[string]any, bool, error) {
	k := s.key(typeHash, key)
	raw, err := s.rdb.HGetAll(ctx, k).Result()
	if err != nil {
		return nil, false, err
	}
	if len(raw) == 0 {
		// 区分「不存在」与「空 hash」
		n, eerr := s.rdb.Exists(ctx, k).Result()
		if eerr != nil {
			return nil, false, eerr
		}
		if n == 0 {
			return nil, false, nil
		}
		return map[string]any{}, true, nil
	}
	out := make(map[string]any, len(raw))
	for field, val := range raw {
		v, derr := decodeValue(val)
		if derr != nil {
			return nil, false, derr
		}
		out[field] = v
	}
	return out, true, nil
}

func (s *RedisStore) HashDelete(ctx context.Context, key, field string) error {
	return s.rdb.HDel(ctx, s.key(typeHash, key), field).Err()
}

// HashIncr 原子增减 hash 字段并返回新值。delta 由调用方决定（Lua 层负责缺省值）。
func (s *RedisStore) HashIncr(ctx context.Context, key, field string, delta int64, ttl time.Duration) (int64, error) {
	k := s.key(typeHash, key)
	return hashIncrScript.Run(ctx, s.rdb, []string{k, s.indexKey()}, field, delta, ttlSeconds(ttl)).Int64()
}

func (s *RedisStore) HashExpire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	// 契约：ttl<=0 表示不设置过期 → no-op；直接传 0 给 EXPIRE 会立即删 key。正数亚秒按 1 秒。
	if ttl <= 0 {
		return false, nil
	}
	return s.rdb.Expire(ctx, s.key(typeHash, key), time.Duration(ttlSeconds(ttl))*time.Second).Result()
}

// ── 生命周期 ──────────────────────────────────────────

// Cleanup 删除该 runID 下索引集合记录的所有 key，再删除索引集合本身。
// 用 SSCAN 迭代避免一次性拉回超大集合，DEL 分批执行避免单条命令参数过多。
func (s *RedisStore) Cleanup(ctx context.Context) error {
	var cursor uint64
	batch := make([]string, 0, cleanupBatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := s.rdb.Del(ctx, batch...).Err(); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for {
		keys, next, err := s.rdb.SScan(ctx, s.indexKey(), cursor, "", int64(cleanupBatchSize)).Result()
		if err != nil {
			return fmt.Errorf("state/shared: SSCAN 索引失败 (runId=%s): %w", s.runID, err)
		}
		for _, k := range keys {
			batch = append(batch, k)
			if len(batch) >= cleanupBatchSize {
				if ferr := flush(); ferr != nil {
					return fmt.Errorf("state/shared: 批量删除失败 (runId=%s): %w", s.runID, ferr)
				}
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if err := flush(); err != nil {
		return fmt.Errorf("state/shared: 批量删除失败 (runId=%s): %w", s.runID, err)
	}
	if err := s.rdb.Del(ctx, s.indexKey()).Err(); err != nil {
		return fmt.Errorf("state/shared: 删除索引集合失败 (runId=%s): %w", s.runID, err)
	}
	return nil
}

func (s *RedisStore) Close() error {
	if s.rdb == nil {
		return nil
	}
	return s.rdb.Close()
}
