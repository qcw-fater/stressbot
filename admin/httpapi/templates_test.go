package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func templateLibraryCapability(t *testing.T, server *Handler) bool {
	t.Helper()
	rr := httptest.NewRecorder()
	server.handleCapabilities(rr, httptest.NewRequest(http.MethodGet, "/sbot/capabilities", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	value, ok := body["templateLibrary"].(bool)
	if !ok {
		t.Fatalf("templateLibrary missing or not bool: %s", rr.Body.String())
	}
	return value
}

func TestCapabilitiesTemplateLibraryDisabledWithoutBothStores(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if templateLibraryCapability(t, &Handler{}) {
		t.Fatal("expected disabled without MySQL stores")
	}
	if templateLibraryCapability(t, &Handler{actionTemplates: newActionTemplateStore(db)}) {
		t.Fatal("expected disabled when Listen store is missing")
	}
	if templateLibraryCapability(t, &Handler{listenTemplates: newListenTemplateStore(db)}) {
		t.Fatal("expected disabled when Action store is missing")
	}
}

func TestCapabilitiesTemplateLibraryEnabledWithBothStores(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := &Handler{
		actionTemplates: newActionTemplateStore(db),
		listenTemplates: newListenTemplateStore(db),
	}
	if !templateLibraryCapability(t, server) {
		t.Fatal("expected enabled with both stores")
	}
}

func TestTemplateLibraryDisabled(t *testing.T) {
	server := &Handler{staticDir: t.TempDir()}
	for _, path := range []string{"/sbot/action-templates", "/sbot/listen-templates"} {
		rr := httptest.NewRecorder()
		server.registerManagementRoutes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "TEMPLATE_LIBRARY_DISABLED") {
			t.Fatalf("path=%s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestTemplateCRUDRouteRejectsBodyOverLimit(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := &Handler{actionTemplates: newActionTemplateStore(db)}
	body := `{"name":"x","pattern":"tcpRequest","data":{},"padding":"` + strings.Repeat("x", (1<<20)+1) + `"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sbot/action-templates", strings.NewReader(body))
	server.handleCreateActionTemplate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestTemplateSnapshotDisabled(t *testing.T) {
	server := &Handler{staticDir: t.TempDir()}
	for _, path := range []string{"/sbot/action-templates/snapshot", "/sbot/listen-templates/snapshot"} {
		rr := httptest.NewRecorder()
		server.registerManagementRoutes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "TEMPLATE_LIBRARY_DISABLED") {
			t.Fatalf("path=%s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}
