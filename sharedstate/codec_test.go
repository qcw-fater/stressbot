package sharedstate

import (
	"reflect"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want any // decode 后期望（注意：整数会变 int64）
	}{
		{"nil", nil, nil},
		{"bool", true, true},
		{"string", "hello", "hello"},
		{"smallInt", float64(42), int64(42)},
		{"float", 3.14, 3.14},
		{"array", []any{float64(1), float64(2), "x"}, []any{int64(1), int64(2), "x"}},
		{"map", map[string]any{"a": float64(1), "b": "s"}, map[string]any{"a": int64(1), "b": "s"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			enc, err := encodeValue(c.in)
			if err != nil {
				t.Fatalf("encode error: %v", err)
			}
			got, err := decodeValue(enc)
			if err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("round trip mismatch: got %#v (%T), want %#v (%T)", got, got, c.want, c.want)
			}
		})
	}
}

func TestEncodeUnsupportedType(t *testing.T) {
	_, err := encodeValue(make(chan int))
	if err == nil {
		t.Fatal("expected error for unsupported type, got nil")
	}
}

func TestDecodeBigIntegerKeepsString(t *testing.T) {
	// 大于 2^53 的整数应保留为字符串，避免精度丢失。
	big := "123456789012345678" // > 2^53
	got, err := decodeValue(big)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected string for big integer, got %T (%v)", got, got)
	}
	if s != big {
		t.Fatalf("big integer mismatch: got %q want %q", s, big)
	}
}

func TestDecodeNestedBigInteger(t *testing.T) {
	got, err := decodeValue(`{"teamId":123456789012345678,"count":3}`)
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	if _, ok := m["teamId"].(string); !ok {
		t.Fatalf("teamId should be string, got %T (%v)", m["teamId"], m["teamId"])
	}
	if m["count"] != int64(3) {
		t.Fatalf("count should be int64(3), got %T (%v)", m["count"], m["count"])
	}
}

func TestResolveDefaults(t *testing.T) {
	cfg := RedisConfig{Host: "127.0.0.1", Port: 6379}
	r, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if r.KeyPrefix != defaultKeyPrefix {
		t.Fatalf("keyPrefix default mismatch: %q", r.KeyPrefix)
	}
	if r.DefaultClaimTTL != defaultClaimTTL {
		t.Fatalf("claimTTL default mismatch: %v", r.DefaultClaimTTL)
	}
	if r.OpTimeout != defaultOpTimeout {
		t.Fatalf("opTimeout default mismatch: %v", r.OpTimeout)
	}
	if r.Port != 6379 {
		t.Fatalf("port mismatch: %d", r.Port)
	}
}

func TestResolveEmptyHost(t *testing.T) {
	cfg := RedisConfig{}
	if cfg.Enabled() {
		t.Fatal("empty host should be disabled")
	}
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("expected error for empty host")
	}
}

func TestResolveDefaultPort(t *testing.T) {
	// Port 为 0 时应回退到 6379。
	cfg := RedisConfig{Host: "127.0.0.1"}
	r, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if r.Port != 6379 {
		t.Fatalf("default port mismatch: %d", r.Port)
	}
}
