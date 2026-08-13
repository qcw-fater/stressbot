package shared

import (
	"context"
	"errors"
	"time"
)

// ErrNotEnabled 表示任务未启用共享状态（Context.Shared 为 nil 时由 Lua 层返回）。
var ErrNotEnabled = errors.New("shared state 未启用，请在服务器配置中启用 Redis")

// Store 共享状态后端接口。RedisStore 是唯一实现。
//
// 约定：
//   - ttl == 0 表示不设置过期（key 存活到任务结束清理或手动删除）。
//   - key 不存在不是错误：读取类返回 (零值, false, nil)。
//   - 所有 key 由实现内部加 keyPrefix:runId:type 前缀，调用方只传 userKey。
type Store interface {
	// KV
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	Get(ctx context.Context, key string) (value any, ok bool, err error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	Expire(ctx context.Context, key string, ttl time.Duration) (bool, error)

	// Counter
	Incr(ctx context.Context, key string, delta int64, ttl time.Duration) (int64, error)

	// Claim / Lock
	Claim(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, key string, owner string) (bool, error)
	Owner(ctx context.Context, key string) (owner string, ok bool, err error)
	Renew(ctx context.Context, key string, owner string, ttl time.Duration) (bool, error)

	// Queue / List（FIFO：尾部 push，头部 pop）
	QueuePush(ctx context.Context, key string, value any, ttl time.Duration) error
	QueuePop(ctx context.Context, key string) (value any, ok bool, err error)
	QueueLen(ctx context.Context, key string) (int64, error)
	QueueExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)

	// Hash
	HashSet(ctx context.Context, key, field string, value any, ttl time.Duration) error
	HashGet(ctx context.Context, key, field string) (value any, ok bool, err error)
	HashGetAll(ctx context.Context, key string) (m map[string]any, ok bool, err error)
	HashDelete(ctx context.Context, key, field string) error
	HashIncr(ctx context.Context, key, field string, delta int64, ttl time.Duration) (int64, error)
	HashExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)

	// DefaultClaimTTL 返回配置的默认 claim 租约时长（Lua 层不传 ttl 时使用）。
	DefaultClaimTTL() time.Duration
	// OpTimeout 返回单次操作超时，Lua 层据此派生 opCtx。
	OpTimeout() time.Duration

	// Cleanup 删除该 runId 下所有共享 key 与索引集合。
	Cleanup(ctx context.Context) error
	// Close 关闭底层 Redis 连接。
	Close() error
}
