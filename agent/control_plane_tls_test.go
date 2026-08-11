package agent

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"stressbot/controlplane"
)

func TestNewRejectsInvalidControlPlaneTLS(t *testing.T) {
	cfg := &ResolvedConfig{
		MetricsInterval: time.Second,
		ControlPlaneTLS: controlplane.TLSConfig{CertFile: "missing.crt"},
	}
	if _, err := New(cfg, nil); err == nil {
		t.Fatal("New() 应拒绝不完整的控制面 TLS 配置")
	}
}

func TestAdminClientUsesControlPlaneTLS(t *testing.T) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13}
	client := NewAdminClientWithTLS("https://admin.example", "agent", time.Second, time.Second, tlsConfig)
	transport, ok := client.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型 = %T", client.client.Transport)
	}
	if transport.TLSClientConfig != tlsConfig {
		t.Fatal("AdminClient 未使用控制面 TLS 配置")
	}
}
