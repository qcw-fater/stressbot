package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCapabilitiesIncludesFlowLibrary(t *testing.T) {
	s := &AdminServer{flows: &FlowTemplateStore{}}
	rr := httptest.NewRecorder()
	s.handleCapabilities(rr, httptest.NewRequest(http.MethodGet, "/sbot/capabilities", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"flowLibrary":true`) {
		t.Fatalf("body = %s", rr.Body.String())
	}
}

func TestFlowSnapshotDisabled(t *testing.T) {
	s := &AdminServer{}
	rr := httptest.NewRecorder()
	s.handleGetFlowSnapshot(rr, httptest.NewRequest(http.MethodGet, "/sbot/flows/snapshot", nil))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "FLOW_LIBRARY_DISABLED") {
		t.Fatalf("body = %s", rr.Body.String())
	}
}

func TestFlowSnapshotRouteReturnsSnapshot(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`(?s)SELECT id, name, node_count.*FROM flow_template`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "node_count", "action_count", "created_at", "updated_at", "flow_json", "layout_json",
		}))
	s := &AdminServer{cfg: Config{StaticDir: t.TempDir()}, flows: NewFlowTemplateStore(db)}
	rr := httptest.NewRecorder()
	s.registerManagementRoutes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/sbot/flows/snapshot", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"items":[]`) {
		t.Fatalf("body = %s", rr.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceFlowSnapshotRejectsBodyOverLimit(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &AdminServer{flows: NewFlowTemplateStore(db)}
	body := `{"expectedRevision":"sha256:test","items":[],"padding":"` + strings.Repeat("x", 50<<20) + `"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/sbot/flows/snapshot", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.handleReplaceFlowSnapshot(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "流程快照不是合法 JSON") {
		t.Fatalf("body = %s", rr.Body.String())
	}
}
