package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagementRoutesExcludeControlPlane(t *testing.T) {
	handler := (&Handler{staticDir: t.TempDir()}).Routes()

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
