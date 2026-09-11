package grpcapi

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	adminagent "stressbot/admin/agent"
	adminmetrics "stressbot/admin/metrics"
	controlpb "stressbot/controlplane/pb"
	"stressbot/internal/stresslog"
	"stressbot/internal/workpool"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestNormalizeSessionErrorLeavesLoggingToSessionLifecycle(t *testing.T) {
	logs := observeControlServiceLogs(t)
	if err := normalizeSessionError(errors.New("读取控制流失败")); err == nil {
		t.Fatal("normalizeSessionError() = nil，期望保留错误")
	}
	if logs.Len() != 0 {
		t.Fatalf("normalizeSessionError 不应脱离会话上下文单独记录日志: %v", logs.All())
	}
}

type loggingCommandBus struct {
	replayErr error
}

type canceledSendStream struct {
	grpc.ServerStream
}

func (canceledSendStream) Send(*controlpb.AdminEvent) error {
	return status.Error(codes.Canceled, "客户端已关闭")
}

func (canceledSendStream) Recv() (*controlpb.AgentEvent, error) { return nil, io.EOF }

func (b loggingCommandBus) Replay(context.Context, *Session) error { return b.replayErr }
func (loggingCommandBus) PendingBatch(context.Context, *Session) ([]*controlpb.Command, error) {
	return nil, nil
}
func (loggingCommandBus) Acknowledge(context.Context, *controlpb.CommandAck) (*controlpb.Command, error) {
	return &controlpb.Command{
		CommandId: "cmd-001", AgentId: "agent-001", TaskId: "task-001", Sequence: 3,
		Body: &controlpb.Command_StopTask{StopTask: &controlpb.StopTask{}},
	}, nil
}

func TestControlServiceLogsSessionLifecycleAndCommandAcknowledgement(t *testing.T) {
	logs := observeControlServiceLogs(t)
	sessions := NewSessionRegistry()
	agents := adminagent.NewRegistry(adminagent.Config{}, nil)
	ingestor := adminmetrics.NewIngestor(
		func(_ string, _ uint64, fn func() error) error { return fn() },
		func(adminmetrics.StressReport, uint64) error { return nil },
		func(string, *controlpb.SystemMetricSnapshot) {},
	)
	service := &grpcControlService{deps: Dependencies{
		Sessions: sessions, Agents: agents, Commands: loggingCommandBus{}, Metrics: ingestor,
		HeartbeatInterval: 3 * time.Second, LeaseDuration: 15 * time.Second,
		OwnsActiveTask: func(string, string) bool { return false },
	}}

	listener := bufconn.Listen(1024 * 1024)
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
		_ = listener.Close()
		<-serveDone
	})
	conn, err := grpc.NewClient("passthrough:///admin-session-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatalf("创建测试连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	stream, err := controlpb.NewAgentControlServiceClient(conn).Session(t.Context())
	if err != nil {
		t.Fatalf("创建控制会话失败: %v", err)
	}
	if err := stream.Send(&controlpb.AgentEvent{Event: &controlpb.AgentEvent_Hello{Hello: &controlpb.Hello{
		ProtocolVersion: controlProtocolVersion, AgentId: "agent-001", Name: "测试节点", AppVersion: "test", MaxBots: 100,
	}}}); err != nil {
		t.Fatalf("发送 Hello 失败: %v", err)
	}
	if event, err := stream.Recv(); err != nil || event.GetWelcome() == nil {
		t.Fatalf("接收 Welcome 失败: event=%v err=%v", event, err)
	}
	if err := stream.Send(&controlpb.AgentEvent{Event: &controlpb.AgentEvent_CommandAck{CommandAck: &controlpb.CommandAck{
		CommandId: "cmd-001", Sequence: 3, AgentId: "agent-001", TaskId: "task-001",
		Status: controlpb.CommandAckStatus_COMMAND_ACK_STATUS_APPLIED,
	}}}); err != nil {
		t.Fatalf("发送命令 ACK 失败: %v", err)
	}
	if event, err := stream.Recv(); err != nil || event.GetCommandReceipt() == nil {
		t.Fatalf("接收命令回执失败: event=%v err=%v", event, err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("关闭客户端发送流失败: %v", err)
	}
	for {
		_, err = stream.Recv()
		if err != nil {
			break
		}
	}
	if err != io.EOF {
		t.Fatalf("控制会话结束错误 = %v，期望 EOF", err)
	}

	assertSingleStructuredLog(t, logs, zapcore.InfoLevel, "[ADMIN] gRPC 控制会话已建立", map[string]any{
		"agentID": "agent-001", "generation": uint64(1),
	})
	assertSingleStructuredLog(t, logs, zapcore.InfoLevel, "[ADMIN] 节点控制命令已确认", map[string]any{
		"agentID": "agent-001", "commandID": "cmd-001", "commandType": "stopTask", "taskID": "task-001",
		"sequence": uint64(3), "status": "applied",
	})
	assertSingleStructuredLog(t, logs, zapcore.InfoLevel, "[ADMIN] gRPC 控制会话已关闭", map[string]any{
		"agentID": "agent-001", "generation": uint64(1),
	})
}

func TestControlServiceTreatsWelcomeAsEstablishedBeforeReplay(t *testing.T) {
	logs := observeControlServiceLogs(t)
	conn := startLoggingControlClient(t, loggingCommandBus{replayErr: errors.New("重放命令失败")})
	stream, err := controlpb.NewAgentControlServiceClient(conn).Session(t.Context())
	if err != nil {
		t.Fatalf("创建控制会话失败: %v", err)
	}
	if err := stream.Send(testHelloEvent(controlProtocolVersion)); err != nil {
		t.Fatalf("发送 Hello 失败: %v", err)
	}
	if event, err := stream.Recv(); err != nil || event.GetWelcome() == nil {
		t.Fatalf("接收 Welcome 失败: event=%v err=%v", event, err)
	}
	_, _ = stream.Recv()

	assertSingleStructuredLog(t, logs, zapcore.InfoLevel, "[ADMIN] gRPC 控制会话已建立", map[string]any{
		"agentID": "agent-001", "generation": uint64(1),
	})
	if entries := logs.FilterMessage("[ADMIN] gRPC 控制会话建立失败").All(); len(entries) != 0 {
		t.Fatalf("Welcome 已发送后不应记录建立失败: %v", entries)
	}
}

func TestControlServiceLogsInvalidHandshake(t *testing.T) {
	logs := observeControlServiceLogs(t)
	conn := startLoggingControlClient(t, loggingCommandBus{})
	stream, err := controlpb.NewAgentControlServiceClient(conn).Session(t.Context())
	if err != nil {
		t.Fatalf("创建控制会话失败: %v", err)
	}
	if err := stream.Send(testHelloEvent(controlProtocolVersion + 1)); err != nil {
		t.Fatalf("发送无效 Hello 失败: %v", err)
	}
	_, _ = stream.Recv()

	assertSingleStructuredLog(t, logs, zapcore.WarnLevel, "[ADMIN] gRPC 控制会话握手失败", map[string]any{
		"agentID": "agent-001",
	})
}

func TestNormalizeSessionErrorTreatsExpectedGRPCClosureAsNormal(t *testing.T) {
	for _, code := range []codes.Code{codes.Canceled, codes.Aborted} {
		if err := normalizeSessionError(status.Error(code, "会话结束")); err != nil {
			t.Errorf("normalizeSessionError(%s) = %v，期望 nil", code, err)
		}
	}
}

func TestControlServiceSendLoopDoesNotWarnOnGRPCCancellation(t *testing.T) {
	logs := observeControlServiceLogs(t)
	sessions := NewSessionRegistry()
	session, err := sessions.Attach(context.Background(), "agent-001")
	if err != nil {
		t.Fatalf("创建测试会话失败: %v", err)
	}
	session.OfferControl(&controlpb.AdminEvent{})
	svc := &grpcControlService{deps: Dependencies{Sessions: sessions}}
	svc.runSendLoop(session, canceledSendStream{})

	if entries := logs.FilterMessage("[ADMIN] gRPC 控制会话发送中断").All(); len(entries) != 0 {
		t.Fatalf("正常 gRPC 取消不应记录发送中断 Warn: %v", entries)
	}
	if session.Context().Err() == nil {
		t.Fatal("发送循环结束后未取消会话")
	}
}

func observeControlServiceLogs(t *testing.T) *observer.ObservedLogs {
	t.Helper()
	original := stresslog.GetLogger()
	core, logs := observer.New(zapcore.DebugLevel)
	stresslog.ReplaceLogger(zap.New(core))
	t.Cleanup(func() { stresslog.ReplaceLogger(original) })
	return logs
}

func assertSingleStructuredLog(t *testing.T, logs *observer.ObservedLogs, level zapcore.Level, message string, want map[string]any) {
	t.Helper()
	entries := logs.FilterMessage(message).All()
	if len(entries) != 1 {
		t.Fatalf("%q 日志数量 = %d，期望 1；全部日志: %v", message, len(entries), logs.All())
	}
	if entries[0].Level != level {
		t.Fatalf("%q 日志级别 = %s，期望 %s", message, entries[0].Level, level)
	}
	fields := entries[0].ContextMap()
	for key, value := range want {
		if fields[key] != value {
			t.Fatalf("%q 字段 %s = %v，期望 %v；全部字段: %v", message, key, fields[key], value, fields)
		}
	}
}

func startLoggingControlClient(t *testing.T, commands CommandBus) *grpc.ClientConn {
	t.Helper()
	sessions := NewSessionRegistry()
	agents := adminagent.NewRegistry(adminagent.Config{}, nil)
	ingestor := adminmetrics.NewIngestor(
		func(_ string, _ uint64, fn func() error) error { return fn() },
		func(adminmetrics.StressReport, uint64) error { return nil },
		func(string, *controlpb.SystemMetricSnapshot) {},
	)
	service := &grpcControlService{deps: Dependencies{
		Sessions: sessions, Agents: agents, Commands: commands, Metrics: ingestor,
		HeartbeatInterval: 3 * time.Second, LeaseDuration: 15 * time.Second,
		OwnsActiveTask: func(string, string) bool { return false },
	}}
	listener := bufconn.Listen(1024 * 1024)
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
		_ = listener.Close()
		<-serveDone
	})
	conn, err := grpc.NewClient("passthrough:///admin-session-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatalf("创建测试连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func testHelloEvent(version uint32) *controlpb.AgentEvent {
	return &controlpb.AgentEvent{Event: &controlpb.AgentEvent_Hello{Hello: &controlpb.Hello{
		ProtocolVersion: version, AgentId: "agent-001", Name: "测试节点", AppVersion: "test", MaxBots: 100,
	}}}
}
