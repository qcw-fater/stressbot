package sharedstate

import (
	"context"
	"fmt"
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
	rdb      *redis.Client
	cfg      ResolvedRedisConfig
	runID    string
	indexKey string
}

var _ Store = (*RedisStore)(nil)

// NewRedisStore 创建并连通 Redis。会做一次 PING 验证连接，失败返回错误。
func NewRedisStore(cfg ResolvedRedisConfig, runID string) (*RedisStore, error) {
	if runID == "" {
		return nil, fmt.Errorf("sharedstate: runID 不能为空")
	}
	opts := &redis.Options{
		Addr:         fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Username:     cfg.Username,
		Password:     cfg.Password,
		DB:           cfg.DBIndex,
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
	rdb := redis.NewClient(opts)

	pingCtx, cancel := context.WithTimeout(context.Background(), cfg.DialTimeout)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("sharedstate: 连接 Redis 失败 (host=%s port=%d dbIndex=%d): %w", cfg.Host, cfg.Port, cfg.DBIndex, err)
	}

	s := &RedisStore{
		rdb:      rdb,
		cfg:      cfg,
		runID:    runID,
		indexKey: fmt.Sprintf("%s:%s:keys", cfg.KeyPrefix, runID),
	}
	return s, nil
}

func (s *RedisStore) key(typ, userKey string) string {
	return fmt.Sprintf("%s:%s:%s:%s", s.cfg.KeyPrefix, s.runID, typ, userKey)
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
	return setScript.Run(ctx, s.rdb, []string{k, s.indexKey}, enc, ttlSeconds(ttl)).Err()
}

func (s *RedisStore) Get(ctx context.Context, key string) (any, bool, error) {
	k := s.key(typeKV, key)
	raw, err := s.rdb.Get(ctx, k).Result()
	if err == redis.Nil {
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
// 秒数用 int64(ttl/time.Second) 复刻 go-redis 原 Expire 的 formatSec 截断语义
// （含 ttl<=0 时传非正秒数触发立即删除，与旧逐 key 调用行为一致）。
func (s *RedisStore) Expire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	n, err := expireScript.Run(ctx, s.rdb, s.dataKeys(key), int64(ttl/time.Second)).Int64()
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
	return incrScript.Run(ctx, s.rdb, []string{k, s.indexKey}, delta, ttlSeconds(ttl)).Int64()
}

// ── Claim / Lock ──────────────────────────────────────

func (s *RedisStore) Claim(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = s.cfg.DefaultClaimTTL
	}
	k := s.key(typeClaim, key)
	n, err := claimScript.Run(ctx, s.rdb, []string{k, s.indexKey}, owner, ttlSeconds(ttl)).Int64()
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
	if err == redis.Nil {
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
	return queuePushScript.Run(ctx, s.rdb, []string{k, s.indexKey}, enc, ttlSeconds(ttl)).Err()
}

func (s *RedisStore) QueuePop(ctx context.Context, key string) (any, bool, error) {
	k := s.key(typeQueue, key)
	raw, err := s.rdb.LPop(ctx, k).Result()
	if err == redis.Nil {
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
	return s.rdb.Expire(ctx, s.key(typeQueue, key), ttl).Result()
}

// ── Hash ──────────────────────────────────────────────

func (s *RedisStore) HashSet(ctx context.Context, key, field string, value any, ttl time.Duration) error {
	enc, err := encodeValue(value)
	if err != nil {
		return err
	}
	k := s.key(typeHash, key)
	return hashSetScript.Run(ctx, s.rdb, []string{k, s.indexKey}, field, enc, ttlSeconds(ttl)).Err()
}

func (s *RedisStore) HashGet(ctx context.Context, key, field string) (any, bool, error) {
	k := s.key(typeHash, key)
	raw, err := s.rdb.HGet(ctx, k, field).Result()
	if err == redis.Nil {
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
	return hashIncrScript.Run(ctx, s.rdb, []string{k, s.indexKey}, field, delta, ttlSeconds(ttl)).Int64()
}

func (s *RedisStore) HashExpire(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return s.rdb.Expire(ctx, s.key(typeHash, key), ttl).Result()
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
		keys, next, err := s.rdb.SScan(ctx, s.indexKey, cursor, "", int64(cleanupBatchSize)).Result()
		if err != nil {
			return fmt.Errorf("sharedstate: SSCAN 索引失败 (runId=%s): %w", s.runID, err)
		}
		for _, k := range keys {
			batch = append(batch, k)
			if len(batch) >= cleanupBatchSize {
				if ferr := flush(); ferr != nil {
					return fmt.Errorf("sharedstate: 批量删除失败 (runId=%s): %w", s.runID, ferr)
				}
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if err := flush(); err != nil {
		return fmt.Errorf("sharedstate: 批量删除失败 (runId=%s): %w", s.runID, err)
	}
	if err := s.rdb.Del(ctx, s.indexKey).Err(); err != nil {
		return fmt.Errorf("sharedstate: 删除索引集合失败 (runId=%s): %w", s.runID, err)
	}
	return nil
}

func (s *RedisStore) Close() error {
	if s.rdb == nil {
		return nil
	}
	return s.rdb.Close()
}
