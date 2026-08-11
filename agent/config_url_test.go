package agent

import (
	"testing"

	"stressbot/controlplane"
)

func TestConfigResolveValidatesControlPlaneURLs(t *testing.T) {
	tests := []struct {
		name      string
		adminURL  string
		publicURL string
		tls       bool
		wantErr   bool
	}{
		{name: "https", adminURL: "https://admin.example:7720/base", publicURL: "https://agent.example:7719"},
		{name: "http compatibility", adminURL: "http://127.0.0.1:7718", publicURL: "http://127.0.0.1:7719"},
		{name: "admin missing scheme", adminURL: "127.0.0.1:7718", wantErr: true},
		{name: "admin invalid scheme", adminURL: "ftp://127.0.0.1:7718", wantErr: true},
		{name: "public missing scheme", adminURL: "http://127.0.0.1:7718", publicURL: "127.0.0.1:7719", wantErr: true},
		{name: "tls requires https admin", adminURL: "http://127.0.0.1:7720", publicURL: "https://127.0.0.1:7719", tls: true, wantErr: true},
		{name: "tls requires https public", adminURL: "https://127.0.0.1:7720", publicURL: "http://127.0.0.1:7719", tls: true, wantErr: true},
		{name: "tls https", adminURL: "https://127.0.0.1:7720", publicURL: "https://127.0.0.1:7719", tls: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				AdminUrl:          tt.adminURL,
				PublicURL:         tt.publicURL,
				MetricsInterval:   "5s",
				HeartbeatInterval: "10s",
				HeartbeatTimeout:  "5s",
				RequestTimeout:    "30s",
			}
			if tt.tls {
				cfg.TLS = controlplane.TLSConfig{CertFile: "agent.crt"}
			}
			resolved, err := cfg.Resolve()
			if tt.wantErr {
				if err == nil {
					t.Fatal("Resolve() 应返回 URL 校验错误")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resolved.AdminUrl != tt.adminURL || resolved.Address != tt.publicURL {
				t.Fatalf("URL 未被保留: admin=%q address=%q", resolved.AdminUrl, resolved.Address)
			}
		})
	}
}
