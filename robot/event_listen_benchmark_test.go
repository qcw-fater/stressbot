package robot

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stressbot/engine"
	"stressbot/monitor"
	"stressbot/network"
)

// BenchmarkEventListen10000Idle 验证 1 万个空闲 listener 在 120ms 窗口内只做边界检查，
// 期间没有消息事件就保持休眠。使用 -benchtime=1x 作为固定规模性能门禁。
func BenchmarkEventListen10000Idle(b *testing.B) {
	const listeners = 10_000
	for range b.N {
		ctx, cancel := context.WithCancel(context.Background())
		var checks atomic.Int64
		var wg sync.WaitGroup
		wg.Add(listeners)
		for range listeners {
			r := newWaitRobot(ctx)
			go func() {
				defer wg.Done()
				r.sched.wait(ctx, time.Now().Add(120*time.Millisecond), nil, func() *engine.NetExchange {
					checks.Add(1)
					return nil
				})
			}()
		}
		wg.Wait()
		cancel()
		if got := checks.Load(); got > listeners*2 {
			b.Fatalf("空闲 listener 发生周期检查：checks=%d, want <=%d", got, listeners*2)
		}
		b.ReportMetric(float64(checks.Load())/listeners, "checks/listener")
	}
}

// BenchmarkEventListenReady 对比“消息已在队列”时事件 API 与原非阻塞 Pop 的热路径成本。
// 两者都包含相同的 OnReceive/Message 分配，事件层本身不应增加 allocs/op。
func BenchmarkEventListenReady(b *testing.B) {
	setup := func() (*Robot, *netSenderAdapter, *network.Connection) {
		r := newWaitRobot(context.Background())
		r.client = network.NewClient("event-listen-bench", time.Second, monitor.TimingRTTOnly)
		r.client.ConnectTCP("logic")
		conn := r.client.GetTCPConn("logic")
		if err := conn.RegisterListen("push", nil, 1); err != nil {
			b.Fatal(err)
		}
		return r, &netSenderAdapter{robot: r}, conn
	}
	payload := []byte("payload")

	b.Run("direct-pop", func(b *testing.B) {
		_, sender, conn := setup()
		b.ReportAllocs()
		for range b.N {
			conn.OnReceive("push", payload, 0, len(payload), network.MessageTiming{RecvFrameAt: time.Now()})
			if sender.GetTCPListenResp("logic", "push") == nil {
				b.Fatal("未取到监听消息")
			}
		}
	})
	b.Run("event-ready", func(b *testing.B) {
		r, sender, conn := setup()
		b.ReportAllocs()
		for range b.N {
			conn.OnReceive("push", payload, 0, len(payload), network.MessageTiming{RecvFrameAt: time.Now()})
			if exchange, err := sender.TCPListen(r.ctx, "logic", "push", time.Second); err != nil || exchange == nil {
				b.Fatalf("TCPListen() exchange=%v error=%v", exchange, err)
			}
		}
	})
}
