package engine

import (
	"testing"
	"time"
)

// TestClassifyListenWait 监听等待时长的三态判定。
//
// 三态而非「算不出就记 0」：监听队列会缓存消息，动作开始等待时消息可能早就到了。
// 把这种情况记成 0ms，会和「服务端瞬间响应」混为一谈——前者说明测量起点选错，
// 后者才是服务端快，两者对压测的含义完全相反。
func TestClassifyListenWait(t *testing.T) {
	base := time.Now()

	cases := []struct {
		name      string
		waitStart time.Time
		recvAt    time.Time
		wantWait  time.Duration
		wantKind  ListenWaitKind
	}{
		{
			name:      "帧在等待开始后到达则可测",
			waitStart: base,
			recvAt:    base.Add(1500 * time.Millisecond),
			wantWait:  1500 * time.Millisecond,
			wantKind:  ListenWaitMeasured,
		},
		{
			name:      "帧早于等待开始说明消息已在队列里",
			waitStart: base,
			recvAt:    base.Add(-2 * time.Second),
			wantKind:  ListenWaitReady,
		},
		{
			name:      "同一时刻算已就绪而非 0ms 样本",
			waitStart: base,
			recvAt:    base,
			wantKind:  ListenWaitReady,
		},
		{
			name:      "没有到达时刻则不计",
			waitStart: base,
			recvAt:    time.Time{},
			wantKind:  ListenWaitUnknown,
		},
		{
			name:      "没有等待起点则不计",
			waitStart: time.Time{},
			recvAt:    base,
			wantKind:  ListenWaitUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wait, kind := ClassifyListenWait(tc.waitStart, tc.recvAt)
			if kind != tc.wantKind {
				t.Fatalf("kind = %v, want %v", kind, tc.wantKind)
			}
			if wait != tc.wantWait {
				t.Fatalf("wait = %v, want %v", wait, tc.wantWait)
			}
		})
	}
}

// TestAddListenHitRoutesByMeasurability 只有可测的命中才进等待时长样本。
func TestAddListenHitRoutesByMeasurability(t *testing.T) {
	var timing ActionTiming

	timing.AddListenHit(2*time.Second, ListenWaitMeasured)
	timing.AddListenHit(0, ListenWaitReady)
	timing.AddListenHit(0, ListenWaitUnknown)
	timing.AddListenTimeout()

	if len(timing.ListenWaits) != 1 || timing.ListenWaits[0] != 2*time.Second {
		t.Fatalf("ListenWaits = %v, want [2s]", timing.ListenWaits)
	}
	if timing.ListenReady != 1 {
		t.Fatalf("ListenReady = %d, want 1", timing.ListenReady)
	}
	if timing.ListenTimeouts != 1 {
		t.Fatalf("ListenTimeouts = %d, want 1", timing.ListenTimeouts)
	}
}
