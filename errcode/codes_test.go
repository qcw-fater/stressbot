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

// 钉住框架码总数：新增/删除错误码时必须同步改 code.go 顶部的分组数量注释。
func TestAllCodesCountMatchesDoc(t *testing.T) {
	const documentedCount = 29
	if got := len(AllCodes()); got != documentedCount {
		t.Fatalf("框架错误码共 %d 个，与 code.go 注释声明的 %d 个不一致，请同步更新注释", got, documentedCount)
	}
}

// 各码段的实际数量必须与 code.go 顶部注释逐段对齐。
func TestCodeSegmentCountsMatchDoc(t *testing.T) {
	segments := []struct {
		name       string
		low, high  uint64
		documented int
	}{
		{"网络", 1, 10, 5}, // 码 5（CONN_DROPPED）已退役，不得复用
		{"协议", 11, 20, 2},
		{"构建", 21, 30, 4},
		{"监听", 31, 40, 2},
		{"配置", 41, 50, 10},
		{"Lua", 51, 60, 4},
		{"回调", 61, 70, 2},
	}
	counted := 0
	for _, segment := range segments {
		got := 0
		for _, code := range AllCodes() {
			if code.Code >= segment.low && code.Code <= segment.high {
				got++
			}
		}
		counted += got
		if got != segment.documented {
			t.Errorf("%s 段（%d-%d）实际 %d 个，注释声明 %d 个",
				segment.name, segment.low, segment.high, got, segment.documented)
		}
	}
	if counted != len(AllCodes()) {
		t.Errorf("有错误码落在已声明码段之外：分段合计 %d，注册表共 %d", counted, len(AllCodes()))
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
