package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagementRoutesExcludeControlPlane(t *testing.T) {
	server := &AdminServer{cfg: Config{StaticDir: t.TempDir()}}
	handler := server.registerManagementRoutes()

	assertStatus(t, handler, http.MethodPost, "/sbot/agent/register", http.StatusNotFound)
	assertNotStatus(t, handler, http.MethodGet, "/sbot/capabilities", http.StatusNotFound)

	for _, path := range []string{
		"/sbot/logs/admin",
		"/sbot/logs/agents/agent-1",
		"/sbot/logs/admin/files",
		"/sbot/logs/admin/files/admin.log",
		"/sbot/logs/agents/agent-1/files",
		"/sbot/logs/agents/agent-1/files/agent.log",
	} {
		assertStatus(t, handler, http.MethodGet, path, http.StatusNotFound)
	}
}

func TestAdminManagementListenerAddress(t *testing.T) {
	server := &AdminServer{cfg: Config{
		ListenHost: "127.0.0.1",
		Port:       7718,
		ControlPlane: ControlPlaneConfig{
			ListenHost: "10.0.0.2",
			Port:       7720,
		},
	}}

	if got := server.newManagementServer().Addr; got != "127.0.0.1:7718" {
		t.Fatalf("management Addr = %q", got)
	}
}

func TestAdminServerShutdownIsIdempotent(t *testing.T) {
	server := &AdminServer{stopCh: make(chan struct{})}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAdminServerShutdownStopsRuntimeBeforeWaitingForWorkPool(t *testing.T) {
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	server := &AdminServer{
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

func assertStatus(t *testing.T, handler http.Handler, method, path string, want int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	if recorder.Code != want {
		t.Fatalf("%s %s status = %d, want %d", method, path, recorder.Code, want)
	}
}

func assertNotStatus(t *testing.T, handler http.Handler, method, path string, unwanted int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	if recorder.Code == unwanted {
		t.Fatalf("%s %s 不应返回 %d", method, path, unwanted)
	}
}
