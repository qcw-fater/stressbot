package admin

import (
	"crypto/tls"
	"net/http"
	"testing"

	"stressbot/controlplane"
)

func TestNewAdminServerRejectsInvalidControlPlaneTLS(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MySQL = nil
	cfg.ControlPlane.TLS = controlplane.TLSConfig{CertFile: "missing.crt"}

	if _, err := NewAdminServer(cfg); err == nil {
		t.Fatal("NewAdminServer() 应拒绝不完整的控制面 TLS 配置")
	}
}

func TestAgentDispatcherUsesControlPlaneTLS(t *testing.T) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	dispatcher := NewAgentDispatcherWithTLS(tlsConfig)
	transport, ok := dispatcher.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型 = %T", dispatcher.httpClient.Transport)
	}
	if transport.TLSClientConfig != tlsConfig {
		t.Fatal("dispatcher 未使用控制面 TLS 配置")
	}
}
