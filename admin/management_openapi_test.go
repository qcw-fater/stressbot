package admin

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	openapispec "stressbot/api/openapi"
)

func TestManagementOpenAPIFreezesCurrentRoutes(t *testing.T) {
	spec := loadManagementOpenAPI(t)
	operationIDs := make(map[string]struct{})
	for path, item := range spec.Paths.Map() {
		for method, operation := range item.Operations() {
			if operation.OperationID == "" {
				t.Fatalf("%s %s missing operationId", method, path)
			}
			if _, exists := operationIDs[operation.OperationID]; exists {
				t.Fatalf("duplicate operationId %q", operation.OperationID)
			}
			operationIDs[operation.OperationID] = struct{}{}
		}
	}
	if got := len(operationIDs); got != 69 {
		t.Fatalf("operation count = %d, want 69", got)
	}
}

func TestManagementOpenAPIRejectsMissingJSONBodyBeforeHandler(t *testing.T) {
	calls := 0
	handler := managementOpenAPIValidator(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/sbot/codec/preview", nil)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	if calls != 0 {
		t.Fatalf("domain handler calls = %d, want 0", calls)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

func TestManagementOpenAPIAcceptsTaskMultipartContract(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("name", "smoke"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("totalBots", "10"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("flow.json", "flow.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(`{"nodes":{},"actions":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	calls := 0
	handler := managementOpenAPIValidator(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/sbot/tasks", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", recorder.Code, recorder.Body.String())
	}
	if calls != 1 {
		t.Fatalf("domain handler calls = %d, want 1", calls)
	}
}

func TestCapabilitiesResponseConformsToManagementOpenAPI(t *testing.T) {
	spec := loadManagementOpenAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/sbot/capabilities", nil)
	router, err := legacy.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	route, pathParams, err := router.FindRoute(req)
	if err != nil {
		t.Fatal(err)
	}
	input := &openapi3filter.RequestValidationInput{Request: req, PathParams: pathParams, Route: route}
	responseInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: input,
		Status:                 http.StatusOK,
		Header:                 http.Header{"Content-Type": []string{"application/json"}},
	}
	responseInput.SetBodyBytes([]byte(`{"sharedState":true,"sharedAddr":"***:6379","flowLibrary":true,"templateLibrary":true}`))
	if err := openapi3filter.ValidateResponse(context.Background(), responseInput); err != nil {
		t.Fatalf("response violates management OpenAPI: %v", err)
	}
}

func loadManagementOpenAPI(t *testing.T) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromData(openapispec.AdminSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("management OpenAPI invalid: %v", err)
	}
	return spec
}
