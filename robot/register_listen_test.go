package robot

import (
	"strings"
	"testing"

	"stressbot/engine"
)

// TestEffectiveListenQueueSize 验证 ListenRef.QueueSize 的有效值解析（2-A2.1）。
//
// effectiveListenQueueSize 把「缺省 vs 显式」与「<=0 报错」抽成可单测纯函数，
// 由 robotActionHandler.RegisterListen 主流程调用。
//
// 语义：
//   - QueueSize == nil（未写）→ 默认 1
//   - QueueSize 显式 > 0 → 取该值
//   - QueueSize 显式 <= 0 → 配置错误，返回中文 error（不静默 clamp）
func TestEffectiveListenQueueSize(t *testing.T) {
	zero := 0
	neg := -1
	one := 1
	three := 3

	tests := []struct {
		name    string
		ref     engine.ListenRef
		want    int
		wantErr bool
		errSub  string
	}{
		{
			name: "nil_缺省为1",
			ref:  engine.ListenRef{Server: "tcp:logic", Listen: "L"},
			want: 1,
		},
		{
			name: "显式3",
			ref:  engine.ListenRef{Server: "tcp:logic", Listen: "L", QueueSize: &three},
			want: 3,
		},
		{
			name: "显式1",
			ref:  engine.ListenRef{Server: "tcp:logic", Listen: "L", QueueSize: &one},
			want: 1,
		},
		{
			name:    "显式0_报错",
			ref:     engine.ListenRef{Server: "tcp:logic", Listen: "L", QueueSize: &zero},
			wantErr: true,
			errSub:  "queueSize",
		},
		{
			name:    "显式负数_报错",
			ref:     engine.ListenRef{Server: "tcp:logic", Listen: "L", QueueSize: &neg},
			wantErr: true,
			errSub:  "queueSize",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := effectiveListenQueueSize(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望 error，实际 nil")
				}
				if !strings.Contains(err.Error(), tt.errSub) {
					t.Fatalf("error %q 不含子串 %q", err.Error(), tt.errSub)
				}
				// 错误信息应带 server+listen 上下文。
				if !strings.Contains(err.Error(), "tcp:logic") || !strings.Contains(err.Error(), `"L"`) {
					t.Fatalf("error %q 应含 server+listen 上下文", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("不期望 error，实际 %v", err)
			}
			if got != tt.want {
				t.Fatalf("effectiveListenQueueSize = %d，want %d", got, tt.want)
			}
		})
	}
}
