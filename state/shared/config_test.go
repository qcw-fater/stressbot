package shared

import (
	"strings"
	"testing"
	"time"

	"stressbot/config"
)

// 时长契约：留空回退默认；非法或 <=0 一律报错；connMaxLifetime 以 0 表示「不限制」属例外。
func TestRedisConfigResolveDurationContract(t *testing.T) {
	base := func() RedisConfig {
		return RedisConfig{Host: "127.0.0.1", Pool: config.ConnPoolConfig{}}
	}

	t.Run("留空回退默认", func(t *testing.T) {
		got, err := base().Resolve()
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got.DefaultClaimTTL != 30*time.Second {
			t.Fatalf("defaultClaimTTL = %v, want 30s", got.DefaultClaimTTL)
		}
		if got.OpTimeout != 2*time.Second || got.DialTimeout != 5*time.Second ||
			got.ReadTimeout != 2*time.Second || got.WriteTimeout != 2*time.Second {
			t.Fatalf("超时默认值不符合: %+v", got)
		}
		if got.ConnMaxLifetime != 0 {
			t.Fatalf("connMaxLifetime 默认应为 0（不限制），得到 %v", got.ConnMaxLifetime)
		}
	})

	t.Run("零与负数一律报错", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*RedisConfig)
		}{
			{"defaultClaimTTL", func(c *RedisConfig) { c.DefaultClaimTTL = "0s" }},
			{"opTimeout", func(c *RedisConfig) { c.OpTimeout = "-1s" }},
			{"pool.dialTimeout", func(c *RedisConfig) { c.Pool.DialTimeout = "0s" }},
			{"pool.readTimeout", func(c *RedisConfig) { c.Pool.ReadTimeout = "-2s" }},
			{"pool.writeTimeout", func(c *RedisConfig) { c.Pool.WriteTimeout = "0" }},
		}
		for _, tc := range cases {
			cfg := base()
			tc.mutate(&cfg)
			if _, err := cfg.Resolve(); err == nil {
				t.Fatalf("%s 为非正值时应报错", tc.name)
			}
		}
	})

	t.Run("非法文本报错且指明字段", func(t *testing.T) {
		cfg := base()
		cfg.OpTimeout = "abc"
		_, err := cfg.Resolve()
		if err == nil || !strings.Contains(err.Error(), "redis.opTimeout") {
			t.Fatalf("期望包含字段名的报错，得到 %v", err)
		}
	})

	t.Run("connMaxLifetime 零合法负数报错", func(t *testing.T) {
		cfg := base()
		cfg.Pool.ConnMaxLifetime = "0"
		got, err := cfg.Resolve()
		if err != nil {
			t.Fatalf("connMaxLifetime=0 表示不限制，应合法: %v", err)
		}
		if got.ConnMaxLifetime != 0 {
			t.Fatalf("connMaxLifetime = %v, want 0", got.ConnMaxLifetime)
		}

		cfg.Pool.ConnMaxLifetime = "-1h"
		if _, err := cfg.Resolve(); err == nil {
			t.Fatal("connMaxLifetime 为负数应报错")
		}
	})
}
