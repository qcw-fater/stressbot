package admin

import "testing"

func TestDefaultConfigAllowsPlaintextGRPCControlPlane(t *testing.T) {
	cfg := Defaults()
	if err := validateConfig(&cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}
