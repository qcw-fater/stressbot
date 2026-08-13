package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagementRoutesServeOpenAPIDocument(t *testing.T) {
	handler := (&Handler{staticDir: t.TempDir()}).Routes()
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/sbot/openapi.yaml", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "yaml") {
		t.Fatalf("Content-Type = %q, want YAML", got)
	}
	if !strings.HasPrefix(recorder.Body.String(), "openapi: 3.0.3") {
		t.Fatalf("response is not the embedded OpenAPI document: %q", recorder.Body.String())
	}
}

func TestManagementRoutesServeSwaggerUI(t *testing.T) {
	handler := (&Handler{staticDir: t.TempDir()}).Routes()
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/sbot/docs", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "SwaggerUIBundle") {
		t.Fatalf("response does not initialize Swagger UI: %s", body)
	}
	if !strings.Contains(body, "/sbot/openapi.yaml") {
		t.Fatalf("Swagger UI does not use the embedded contract: %s", body)
	}
}
