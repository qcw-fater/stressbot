package admin

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	adminapi "stressbot/admin/api"
	"stressbot/controlplane"
)

func TestControlPlaneSpecFreezesSixteenOperations(t *testing.T) {
	spec, err := adminapi.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatalf("embedded OpenAPI invalid: %v", err)
	}

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
	if got := len(operationIDs); got != 16 {
		t.Fatalf("operation count = %d, want 16", got)
	}
}

func TestControlPlaneValidatorRejectsMissingRequiredFieldBeforeHandler(t *testing.T) {
	server := &contractStrictServer{}
	handler := controlplane.NewOpenAPIHandler(server, func(r *http.Request) bool {
		return strings.HasPrefix(r.URL.Path, "/sbot/agent/")
	})
	req := httptest.NewRequest(http.MethodPost, "/sbot/agent/register", strings.NewReader(`{
		"agentId":"node-1",
		"name":"node",
		"address":"https://127.0.0.1:7721",
		"appVersion":"test",
		"maxBots":100,
		"stressInterval":"1s",
		"systemInterval":"2s"
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if server.registerCalls != 0 {
		t.Fatalf("register handler calls = %d, want 0", server.registerCalls)
	}
}

func TestControlPlaneSuccessResponseConformsToEmbeddedSpec(t *testing.T) {
	server := &contractStrictServer{}
	handler := controlplane.NewOpenAPIHandler(server, nil)
	req := httptest.NewRequest(http.MethodPost, "/sbot/agent/register", bytes.NewBufferString(validRegisterJSON()))
	req.Header.Set("Content-Type", "application/json")
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if server.registerCalls != 1 {
		t.Fatalf("register handler calls = %d, want 1", server.registerCalls)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	spec, err := adminapi.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	router, err := legacy.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	route, pathParams, err := router.FindRoute(req)
	if err != nil {
		t.Fatal(err)
	}
	input := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
	}
	responseInput := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: input,
		Status:                 recorder.Code,
		Header:                 recorder.Header(),
	}
	responseInput.SetBodyBytes(recorder.Body.Bytes())
	if err := openapi3filter.ValidateResponse(context.Background(), responseInput); err != nil {
		t.Fatalf("response violates embedded OpenAPI: %v", err)
	}
}

func TestControlPlaneRejectsWrongMethod(t *testing.T) {
	handler := controlplane.NewOpenAPIHandler(&contractStrictServer{}, nil)
	req := httptest.NewRequest(http.MethodPut, "/sbot/agent/register", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

type contractStrictServer struct {
	controlplane.UnimplementedStrictServer
	registerCalls int
}

func (s *contractStrictServer) RegisterAgent(context.Context, adminapi.RegisterAgentRequestObject) (adminapi.RegisterAgentResponseObject, error) {
	s.registerCalls++
	return adminapi.RegisterAgent200JSONResponse{
		AgentId:        "node-1",
		HeartbeatTtl:   "15s",
		StressEndpoint: "/sbot/agent/stress",
		SystemEndpoint: "/sbot/agent/system",
	}, nil
}

func validRegisterJSON() string {
	return `{
		"agentId":"node-1",
		"name":"node",
		"address":"https://127.0.0.1:7721",
		"appVersion":"test",
		"maxBots":100,
		"stressInterval":"1s",
		"systemInterval":"2s",
		"staticInfo":{
			"hostname":"host",
			"os":"windows",
			"arch":"amd64",
			"numCpu":8,
			"memTotalBytes":1024,
			"goVersion":"go1.26",
			"kernelVer":"test",
			"startedAt":"` + time.Unix(0, 0).UTC().Format(time.RFC3339) + `"
		}
	}`
}
