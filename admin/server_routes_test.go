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
}

func TestControlPlaneRoutesExcludeManagement(t *testing.T) {
	server := &AdminServer{}
	handler := server.registerControlPlaneRoutes()

	assertStatus(t, handler, http.MethodGet, "/sbot/tasks", http.StatusNotFound)
	assertStatus(t, handler, http.MethodGet, "/", http.StatusNotFound)
	assertNotStatus(t, handler, http.MethodPost, "/sbot/agent/register", http.StatusNotFound)
}

func TestAdminServerListenersUseSeparateAddresses(t *testing.T) {
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
	if got := server.newControlPlaneServer().Addr; got != "10.0.0.2:7720" {
		t.Fatalf("control plane Addr = %q", got)
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
