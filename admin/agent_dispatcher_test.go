package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		path    string
		want    string
		wantErr bool
	}{
		{
			name:    "http",
			baseURL: "http://agent:7719",
			path:    "/agent/v1/version",
			want:    "http://agent:7719/agent/v1/version",
		},
		{
			name:    "https with base path",
			baseURL: "https://agent.example:7719/base",
			path:    "/agent/v1/version",
			want:    "https://agent.example:7719/base/agent/v1/version",
		},
		{
			name:    "missing scheme",
			baseURL: "agent:7719",
			path:    "/agent/v1/version",
			wantErr: true,
		},
		{
			name:    "unsupported scheme",
			baseURL: "ftp://agent:7719",
			path:    "/agent/v1/version",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := agentEndpoint(tt.baseURL, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("agentEndpoint(%q) 应返回错误", tt.baseURL)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("agentEndpoint() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAgentDispatcherPreservesHTTPS(t *testing.T) {
	var gotPath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"test"}`))
	}))
	defer server.Close()

	dispatcher := NewAgentDispatcher()
	dispatcher.httpClient = server.Client()
	version, err := dispatcher.Version(server.URL + "/base")
	if err != nil {
		t.Fatal(err)
	}
	if version != "test" {
		t.Fatalf("Version() = %q, want test", version)
	}
	if gotPath != "/base/agent/v1/version" {
		t.Fatalf("请求路径 = %q", gotPath)
	}
}

func TestAgentEndpointEscapesPathSegment(t *testing.T) {
	got, err := agentEndpoint(
		"https://agent.example:7719/base",
		"/agent/v1/logs/files",
		"admin one.log",
	)
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://agent.example:7719/base/agent/v1/logs/files/admin%20one.log"
	if got != want {
		t.Fatalf("agentEndpoint() = %q, want %q", got, want)
	}
}
