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
	"net"
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
	Addr            string `json:"addr"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	DB              int    `json:"db"`
	KeyPrefix       string `json:"keyPrefix"`
	DefaultClaimTTL string `json:"defaultClaimTTL"`
	OpTimeout       string `json:"opTimeout"`
	DialTimeout     string `json:"dialTimeout"`
	ReadTimeout     string `json:"readTimeout"`
	WriteTimeout    string `json:"writeTimeout"`
	PoolSize        int    `json:"poolSize"`
}

// ResolvedRedisConfig 已解析（duration 转 time.Duration、填充默认值）的配置。
type ResolvedRedisConfig struct {
	Addr            string
	Username        string
	Password        string
	DB              int
	KeyPrefix       string
	DefaultClaimTTL time.Duration
	OpTimeout       time.Duration
	DialTimeout     time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	PoolSize        int
}

// Enabled 返回是否配置了 Redis 地址（空地址表示不启用共享状态）。
func (c RedisConfig) Enabled() bool {
	return c.Addr != ""
}

// Resolve 校验并解析配置，填充默认值。
// addr 为空时返回错误（调用方应在 Resolve 前用 Enabled() 判断）。
func (c RedisConfig) Resolve() (ResolvedRedisConfig, error) {
	if c.Addr == "" {
		return ResolvedRedisConfig{}, fmt.Errorf("sharedstate: redis.addr 为空，未启用共享状态")
	}

	out := ResolvedRedisConfig{
		Addr:      c.Addr,
		Username:  c.Username,
		Password:  c.Password,
		DB:        c.DB,
		KeyPrefix: c.KeyPrefix,
		PoolSize:  c.PoolSize,
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
	return out, nil
}

// AddrMasked 返回脱敏后的地址（用于 capabilities 展示）。
func (c ResolvedRedisConfig) AddrMasked() string {
	return MaskAddr(c.Addr)
}

// MaskAddr 对 host:port 地址脱敏：隐藏主机（避免泄露内网细节），仅保留端口。
// 解析失败时返回固定占位。空地址返回空串。
func MaskAddr(addr string) string {
	if addr == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "***"
	}
	return "***:" + port
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
