package admin

import (
	"context"
	"testing"
)

func TestAdminManagementListenerAddress(t *testing.T) {
	server := &Server{cfg: Config{
		Server: ServerConfig{
			ListenHost: "127.0.0.1",
			Port:       7718,
		},
		ControlPlane: ControlPlaneConfig{
			ListenHost: "10.0.0.2",
			Port:       7720,
		},
	}}

	if got := server.newManagementServer().Addr; got != "127.0.0.1:7718" {
		t.Fatalf("management Addr = %q", got)
	}
}

func TestServerShutdownIsIdempotent(t *testing.T) {
	server := &Server{stopCh: make(chan struct{})}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestServerShutdownStopsRuntimeBeforeWaitingForPool(t *testing.T) {
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	server := &Server{
		runtimeCtx:    runtimeCtx,
		runtimeCancel: runtimeCancel,
		stopCh:        make(chan struct{}),
	}
	server.shutdownPool = func() {
		select {
		case <-runtimeCtx.Done():
		default:
			t.Error("等待工作池前未取消 Admin 运行时上下文")
		}
		select {
		case <-server.stopCh:
		default:
			t.Error("等待工作池前未发出 Admin 停止信号")
		}
	}

	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
