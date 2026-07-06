package robot

import (
	"context"
	"testing"

	"stressbot/adapter"
	"stressbot/codec"
	"stressbot/network"
	"stressbot/state"
)

func TestRegisterCodecHeartbeat_RequireSecretKey_PendingUntilSecretKey(t *testing.T) {
	resolver := adapter.NewCodecResolverWithHeartbeat(
		map[string]adapter.Adapter{"udp:battle": fakeAdapter{}},
		map[string]*codec.HeartbeatConfigDef{
			"udp:battle": {
				IntervalMs:       150,
				Route:            map[string]any{"cmd": float64(4), "act": float64(2)},
				RequireSecretKey: true,
			},
		},
	)
	r := &Robot{
		ctx:      context.Background(),
		client:   network.NewClient("bot_test", 0, ""),
		resolver: resolver,
		state:    state.NewStore(),
	}
	if !r.client.ConnectUDP("battle") {
		t.Fatal("创建 UDP 连接占位失败")
	}
	ns := &netSenderAdapter{robot: r}

	if err := ns.RegisterCodecHeartbeat("udp", "battle"); err != nil {
		t.Fatalf("RegisterCodecHeartbeat 失败: %v", err)
	}
	if len(r.pendingHeartbeats) != 1 {
		t.Fatalf("requireSecretKey=true 时应暂存心跳等待密钥，pending=%d", len(r.pendingHeartbeats))
	}

	ns.SetUDPSecretKey("battle", []byte("12345678901234567890123456789012"))
	if len(r.pendingHeartbeats) != 0 {
		t.Fatalf("设置 UDP 密钥后应启动并移除 pending 心跳，pending=%d", len(r.pendingHeartbeats))
	}
}

func TestRegisterCodecHeartbeat_NoRequireSecretKey_StartsImmediately(t *testing.T) {
	resolver := adapter.NewCodecResolverWithHeartbeat(
		map[string]adapter.Adapter{"tcp:logic": fakeAdapter{}},
		map[string]*codec.HeartbeatConfigDef{
			"tcp:logic": {
				IntervalMs: 5000,
				Route:      map[string]any{"cmd": float64(2), "act": float64(1)},
			},
		},
	)
	r := &Robot{
		ctx:      context.Background(),
		client:   network.NewClient("bot_test", 0, ""),
		resolver: resolver,
		state:    state.NewStore(),
	}
	if !r.client.ConnectTCP("logic") {
		t.Fatal("创建 TCP 连接占位失败")
	}
	ns := &netSenderAdapter{robot: r}

	if err := ns.RegisterCodecHeartbeat("tcp", "logic"); err != nil {
		t.Fatalf("RegisterCodecHeartbeat 失败: %v", err)
	}
	if len(r.pendingHeartbeats) != 0 {
		t.Fatalf("requireSecretKey=false 时不应暂存心跳，pending=%d", len(r.pendingHeartbeats))
	}
}
