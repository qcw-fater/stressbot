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
