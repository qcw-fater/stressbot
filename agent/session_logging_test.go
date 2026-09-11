package agent

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"stressbot/agent/command"
	"stressbot/agent/metrics"
	agentsession "stressbot/agent/session"
	controlpb "stressbot/controlplane/pb"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type welcomeThenCloseServer struct {
	controlpb.UnimplementedAgentControlServiceServer
}

func (welcomeThenCloseServer) Session(stream controlpb.AgentControlService_SessionServer) error {
	event, err := stream.Recv()
	if err != nil {
		return err
	}
	if event.GetHello() == nil {
		return nil
	}
	return stream.Send(&controlpb.AdminEvent{Event: &controlpb.AdminEvent_Welcome{Welcome: &controlpb.Welcome{
		ProtocolVersion:        agentsession.ProtocolVersion,
		Generation:             7,
		HeartbeatIntervalNanos: int64(3 * time.Second),
		LeaseDurationNanos:     int64(15 * time.Second),
	}}})
}

type idleMetricsServer struct {
	controlpb.UnimplementedAgentMetricsServiceServer
}

func (idleMetricsServer) Report(stream controlpb.AgentMetricsService_ReportServer) error {
	<-stream.Context().Done()
	return stream.Context().Err()
}

func TestRunConnectionLogsEstablishedControlSession(t *testing.T) {
	logs := observeAgentLogs(t)
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	controlpb.RegisterAgentControlServiceServer(server, welcomeThenCloseServer{})
	controlpb.RegisterAgentMetricsServiceServer(server, idleMetricsServer{})
	serveDone := make(chan struct{})
	if err := workpool.Default().Submit(func() {
		_ = server.Serve(listener)
		close(serveDone)
	}); err != nil {
		t.Fatalf("启动测试 gRPC 服务失败: %v", err)
	}
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		<-serveDone
	})

	conn, err := grpc.NewClient("passthrough:///agent-session-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatalf("创建测试连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	a := &Agent{
		id: "agent-001",
		cfg: &ResolvedConfig{
			ID: "agent-001", Name: "测试节点", AdminAddress: "127.0.0.1:7720", AppVersion: "test",
			MaxBots: 100, MetricsInterval: time.Second, HeartbeatInterval: 3 * time.Second,
		},
		sysmon:       &metrics.SystemMonitor{},
		metrics:      metrics.NewLatestMetrics(),
		reportOutbox: agentsession.NewReportOutbox(8),
		status:       StatusIdle,
	}
	a.executor = command.NewExecutor(a, nil)

	if err := a.runConnection(t.Context(), conn); err == nil || !strings.Contains(err.Error(), "循环") {
		t.Fatalf("runConnection() = %v，期望带来源的会话循环结束错误", err)
	}
	entries := logs.FilterMessage("[AGENT] gRPC 控制会话已建立").All()
	if len(entries) != 1 {
		t.Fatalf("控制会话建立日志数量 = %d，期望 1；全部日志: %v", len(entries), logs.All())
	}
	fields := entries[0].ContextMap()
	if entries[0].Level != zapcore.InfoLevel {
		t.Fatalf("控制会话建立日志级别 = %s，期望 info", entries[0].Level)
	}
	if fields["agentID"] != "agent-001" || fields["adminAddress"] != "127.0.0.1:7720" {
		t.Fatalf("控制会话建立日志身份字段不完整: %v", fields)
	}
	if fields["generation"] != uint64(7) {
		t.Fatalf("generation = %v，期望 7", fields["generation"])
	}
	if fields["heartbeatInterval"] != 3*time.Second || fields["leaseDuration"] != 15*time.Second {
		t.Fatalf("控制会话时间字段不完整: %v", fields)
	}
}

func observeAgentLogs(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	original := stresslog.GetLogger()
	core, logs := observer.New(zapcore.DebugLevel)
	stresslog.ReplaceLogger(zap.New(core))
	t.Cleanup(func() { stresslog.ReplaceLogger(original) })
	return logs
}
