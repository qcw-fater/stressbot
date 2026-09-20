package config

import (
	"strings"
	"testing"
	"time"
)

// TestParseDuration_Unset 未填时回退 fallback，不报错。
func TestParseDuration_Unset(t *testing.T) {
	for _, raw := range []string{"", "   ", "\t"} {
		got, err := ParseDuration(raw, 7*time.Second, "x.y")
		if err != nil {
			t.Fatalf("ParseDuration(%q) 意外报错: %v", raw, err)
		}
		if got != 7*time.Second {
			t.Fatalf("ParseDuration(%q) = %v，期望回退 7s", raw, got)
		}
	}
}

// TestParseDuration_Zero 0 是合法显式值（如 MySQL 超时的「不限制」）。
func TestParseDuration_Zero(t *testing.T) {
	got, err := ParseDuration("0s", 7*time.Second, "x.y")
	if err != nil {
		t.Fatalf("ParseDuration(\"0s\") 意外报错: %v", err)
	}
	if got != 0 {
		t.Fatalf("ParseDuration(\"0s\") = %v，期望显式 0 而非回退默认", got)
	}
}

// TestParseDuration_Invalid 写错的值必须报错，不能静默退化成默认值。
func TestParseDuration_Invalid(t *testing.T) {
	for _, raw := range []string{"5秒", "abc", "10", "-3s"} {
		if _, err := ParseDuration(raw, 7*time.Second, "x.y"); err == nil {
			t.Fatalf("ParseDuration(%q) 应报错，实际静默通过", raw)
		}
	}
}

// TestParsePositiveDuration 间隔/超时类：未填回退、正值照办、0 与负数报错。
func TestParsePositiveDuration(t *testing.T) {
	got, err := ParsePositiveDuration("", 7*time.Second, "x.y")
	if err != nil || got != 7*time.Second {
		t.Fatalf("未填应回退 7s，实际 (%v, %v)", got, err)
	}
	got, err = ParsePositiveDuration("250ms", 7*time.Second, "x.y")
	if err != nil || got != 250*time.Millisecond {
		t.Fatalf("显式 250ms 应照办，实际 (%v, %v)", got, err)
	}
	for _, raw := range []string{"0", "0s", "-1s", "bad"} {
		if _, err := ParsePositiveDuration(raw, 7*time.Second, "x.y"); err == nil {
			t.Fatalf("ParsePositiveDuration(%q) 应报错，实际静默通过", raw)
		}
	}
}

// TestParseDuration_ErrorMentionsKey 报错要指出是哪个配置项，否则用户拿到错误也不知道改哪。
func TestParseDuration_ErrorMentionsKey(t *testing.T) {
	_, err := ParsePositiveDuration("nope", time.Second, "agent.metricsInterval")
	if err == nil {
		t.Fatal("期望报错")
	}
	if !strings.Contains(err.Error(), "agent.metricsInterval") {
		t.Fatalf("错误信息未包含配置项名: %v", err)
	}
}
