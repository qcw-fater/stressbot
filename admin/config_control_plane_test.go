package admin

import (
	"testing"

	"stressbot/controlplane"
)

func TestValidateConfigRequiresHTTPSWhenControlPlaneTLSEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MySQL = nil
	cfg.PublicURL = "https://admin.example"
	cfg.ControlPlane.PublicURL = "http://admin.example:7720"
	cfg.ControlPlane.TLS = controlplane.TLSConfig{CertFile: "admin.crt"}

	if err := validateConfig(&cfg); err == nil {
		t.Fatal("启用控制面 TLS 时应拒绝 http publicUrl")
	}

	cfg.ControlPlane.PublicURL = "https://admin.example:7720"
	if err := validateConfig(&cfg); err != nil {
		t.Fatalf("https 控制面地址应通过配置校验: %v", err)
	}
}
