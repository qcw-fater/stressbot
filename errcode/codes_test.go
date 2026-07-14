package errcode

import "testing"

func TestAllCodes_HasNoKind(t *testing.T) {
	codes := AllCodes()
	if len(codes) == 0 {
		t.Fatal("AllCodes 不应为空")
	}
	for _, c := range codes {
		if c.Code >= 100 {
			t.Fatalf("框架码应 < 100，实际 %d", c.Code)
		}
		if c.Name == "" {
			t.Fatalf("码 %d 名称空", c.Code)
		}
	}
}

func TestString_FrameworkCode(t *testing.T) {
	if got := ErrRecvTimeout.String(); got != "RECV_TIMEOUT" {
		t.Fatalf("ErrRecvTimeout.String()=%q want RECV_TIMEOUT", got)
	}
	if got := ErrorCode(99999).String(); got != "" {
		t.Fatalf("未注册码应空串，实际 %q", got)
	}
}

func TestStateConfigCodeRegistered(t *testing.T) {
	if ErrStateConfig != 50 {
		t.Fatalf("ErrStateConfig=%d want 50", ErrStateConfig)
	}
	if got := ErrStateConfig.String(); got != "STATE_CONFIG" {
		t.Fatalf("ErrStateConfig.String()=%q want STATE_CONFIG", got)
	}
}

func TestAllCodesUnique(t *testing.T) {
	seen := make(map[uint64]string)
	for _, code := range AllCodes() {
		if previous, ok := seen[code.Code]; ok {
			t.Fatalf("框架错误码 %d 重复注册: %s / %s", code.Code, previous, code.Name)
		}
		seen[code.Code] = code.Name
	}
}
