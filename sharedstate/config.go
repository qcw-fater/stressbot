// Package sharedstate 提供 Redis-backed 的跨 Robot / 跨 Agent 共享状态能力。
//
// 设计要点：
//   - 只支持 Redis，不做进程内 memory 后端（避免误以为本地共享能跨 Agent 生效）。
//   - 所有 key 自动加 keyPrefix:runId:type:userKey 前缀，按 runId 做任务命名空间隔离。
//   - 写入时维护任务 key 索引集合（<prefix>:<runId>:keys），任务结束统一清理。
//   - 只开放受控的共享原语（KV / Counter / Claim / Queue / Hash），不是完整 Redis 客户端。
package sharedstate

import (
	"fmt"
	"time"
)

// 默认值常量。
const (
	defaultKeyPrefix       = "stressbot"
	defaultClaimTTL        = 30 * time.Second
	defaultOpTimeout       = 2 * time.Second
	defaultDialTimeout     = 5 * time.Second
	defaultReadWriteTimout = 2 * time.Second
)

// Config 共享状态总配置。对应 config.json / admin-config.json 的 shared 段。
type Config struct {
	Redis RedisConfig `json:"redis"`
}

// RedisConfig Redis 连接配置（原始字符串形态，duration 用字符串便于配置文件书写）。
type RedisConfig struct {
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	DBIndex         int    `json:"dbIndex"`
	KeyPrefix       string `json:"keyPrefix"`
	DefaultClaimTTL string `json:"defaultClaimTTL"`
	OpTimeout       string `json:"opTimeout"`
	DialTimeout     string `json:"dialTimeout"`
	ReadTimeout     string `json:"readTimeout"`
	WriteTimeout    string `json:"writeTimeout"`
	MaxOpenConns    int    `json:"maxOpenConns"`
	MaxIdleConns    int    `json:"maxIdleConns"`
	ConnMaxLifetime string `json:"connMaxLifetime"`
}

// ResolvedRedisConfig 已解析（duration 转 time.Duration、填充默认值）的配置。
type ResolvedRedisConfig struct {
	Host            string
	Port            int
	Username        string
	Password        string
	DBIndex         int
	KeyPrefix       string
	DefaultClaimTTL time.Duration
	OpTimeout       time.Duration
	DialTimeout     time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Enabled 返回是否配置了 Redis 地址（host 为空表示不启用）。
func (c RedisConfig) Enabled() bool {
	return c.Host != ""
}

// Resolve 校验并解析配置，填充默认值。
// host 为空时返回错误（调用方应在 Resolve 前用 Enabled() 判断）。
func (c RedisConfig) Resolve() (ResolvedRedisConfig, error) {
	if c.Host == "" {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: redis.host 为空，未启用共享状态")
	}

	port := c.Port
	if port == 0 {
		port = 6379
	}

	out := ResolvedRedisConfig{
		Host:         c.Host,
		Port:         port,
		Username:     c.Username,
		Password:     c.Password,
		DBIndex:      c.DBIndex,
		KeyPrefix:    c.KeyPrefix,
		MaxOpenConns: c.MaxOpenConns,
		MaxIdleConns: c.MaxIdleConns,
	}
	if out.KeyPrefix == "" {
		out.KeyPrefix = defaultKeyPrefix
	}

	var err error
	if out.DefaultClaimTTL, err = parseDurationDefault(c.DefaultClaimTTL, defaultClaimTTL); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 defaultClaimTTL 失败: %w", err)
	}
	if out.OpTimeout, err = parseDurationDefault(c.OpTimeout, defaultOpTimeout); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 opTimeout 失败: %w", err)
	}
	if out.DialTimeout, err = parseDurationDefault(c.DialTimeout, defaultDialTimeout); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 dialTimeout 失败: %w", err)
	}
	if out.ReadTimeout, err = parseDurationDefault(c.ReadTimeout, defaultReadWriteTimout); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 readTimeout 失败: %w", err)
	}
	if out.WriteTimeout, err = parseDurationDefault(c.WriteTimeout, defaultReadWriteTimout); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 writeTimeout 失败: %w", err)
	}
	if out.ConnMaxLifetime, err = parseDurationDefault(c.ConnMaxLifetime, 0); err != nil {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: 解析 connMaxLifetime 失败: %w", err)
	}
	return out, nil
}

func parseDurationDefault(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return def, nil
	}
	return d, nil
}
