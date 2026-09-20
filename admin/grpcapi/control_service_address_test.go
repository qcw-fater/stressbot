package grpcapi

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	adminagent "stressbot/admin/agent"
	adminmetrics "stressbot/admin/metrics"
	controlpb "stressbot/controlplane/pb"
	"stressbot/internal/workpool"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestAgentNodeFromHelloLeavesAddressEmptyWithoutPeer(t *testing.T) {
	node := agentNodeFromHello(&controlpb.Hello{AgentId: "agent-001", Name: "测试节点"}, "")
	if node.Address != "" {
		t.Fatalf("取不到对端地址时 Address = %q，期望空串", node.Address)
	}
}

// TestControlServiceRecordsPeerAddressAndRefreshesOnReconnect 用两条同时存活的控制
// 连接验证：注册表地址等于本次会话的对端地址，且第二次 Hello 会刷新它。两条连接并存
// 保证客户端临时端口必然不同，不依赖端口回收时序。
func TestControlServiceRecordsPeerAddressAndRefreshesOnReconnect(t *testing.T) {
	logs := observeControlServiceLogs(t)
	agents, target := startAddressControlServer(t)

	addresses := make([]string, 0, 2)
	for round := 1; round <= 2; round++ {
		recorder := &peerAddressRecorder{}
		conn, err := grpc.NewClient("passthrough:///admin-address-test",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(recorder.dialTo(target)))
		if err != nil {
			t.Fatalf("第 %d 次创建测试连接失败: %v", round, err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		stream, err := controlpb.NewAgentControlServiceClient(conn).Session(t.Context())
		if err != nil {
			t.Fatalf("第 %d 次创建控制会话失败: %v", round, err)
		}
		if err := stream.Send(testHelloEvent(controlProtocolVersion)); err != nil {
			t.Fatalf("第 %d 次发送 Hello 失败: %v", round, err)
		}
		if event, err := stream.Recv(); err != nil || event.GetWelcome() == nil {
			t.Fatalf("第 %d 次接收 Welcome 失败: event=%v err=%v", round, event, err)
		}

		node, ok := agents.Get("agent-001")
		if !ok {
			t.Fatalf("第 %d 次握手后注册表缺少节点", round)
		}
		if want := recorder.latest(); node.Address != want {
			t.Fatalf("第 %d 次注册地址 = %q，期望控制连接对端 %q", round, node.Address, want)
		}
		addresses = append(addresses, node.Address)
	}

	if addresses[0] == addresses[1] {
		t.Fatalf("重新 Hello 后节点地址未刷新，两次均为 %q", addresses[0])
	}
	if entries := logs.FilterMessage("[ADMIN] 无法获取节点控制连接地址，节点地址留空").All(); len(entries) != 0 {
		t.Fatalf("对端地址可用时不应记录退化告警: %v", entries)
	}
}

// peerAddressRecorder 记录最近一次拨号的客户端本地地址，即服务端看到的对端地址。
type peerAddressRecorder struct {
	mu    sync.Mutex
	local string
}

func (r *peerAddressRecorder) dialTo(target string) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		r.local = conn.LocalAddr().String()
		r.mu.Unlock()
		return conn, nil
	}
}

func (r *peerAddressRecorder) latest() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.local
}

// startAddressControlServer 在回环 TCP 上起控制面服务，使对端地址是真实 host:port
// （bufconn 的对端没有端口，无法验证刷新语义），并返回可直接断言的注册表。
func startAddressControlServer(t *testing.T) (*adminagent.Registry, string) {
	t.Helper()
	agents := adminagent.NewRegistry(adminagent.Config{}, nil)
	ingestor := adminmetrics.NewIngestor(
		func(_ string, _ uint64, fn func() error) error { return fn() },
		func(adminmetrics.StressReport, uint64) error { return nil },
		func(string, *controlpb.SystemMetricSnapshot) {},
	)
	service := &grpcControlService{deps: Dependencies{
		Sessions: NewSessionRegistry(), Agents: agents, Commands: loggingCommandBus{}, Metrics: ingestor,
		HeartbeatInterval: 3 * time.Second, LeaseDuration: 15 * time.Second,
		OwnsActiveTask: func(string, string) bool { return false },
	}}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听测试端口失败: %v", err)
	}
	server := grpc.NewServer()
	controlpb.RegisterAgentControlServiceServer(server, service)
	serveDone := make(chan struct{})
	if err := workpool.Default().Submit(func() {
		_ = server.Serve(listener)
		close(serveDone)
	}); err != nil {
		t.Fatalf("启动测试 gRPC 服务失败: %v", err)
	}
	t.Cleanup(func() {
		server.Stop()
		<-serveDone
	})
	return agents, listener.Addr().String()
}
