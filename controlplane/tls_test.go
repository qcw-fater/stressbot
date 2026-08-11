package controlplane

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

type testIdentity struct {
	certFile string
	keyFile  string
}

func TestTLSConfigRejectsIncompleteFiles(t *testing.T) {
	tests := []TLSConfig{
		{},
		{CertFile: "cert.pem"},
		{CertFile: "cert.pem", KeyFile: "key.pem"},
	}
	for _, cfg := range tests {
		if _, err := cfg.Server(); err == nil {
			t.Fatalf("Server() 应拒绝不完整配置: %+v", cfg)
		}
		if _, err := cfg.Client(); err == nil {
			t.Fatalf("Client() 应拒绝不完整配置: %+v", cfg)
		}
	}
}

func TestMutualTLSAcceptsRoleSeparatedTrust(t *testing.T) {
	dir := t.TempDir()
	adminCA := newTestCA(t, "admin-ca")
	agentCA := newTestCA(t, "agent-ca")
	admin := newTestIdentity(t, dir, "admin", adminCA, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	agent := newTestIdentity(t, dir, "agent", agentCA, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	adminConfig := TLSConfig{CertFile: admin.certFile, KeyFile: admin.keyFile, PeerCAFile: writeCA(t, dir, "agent-ca.pem", agentCA)}
	agentConfig := TLSConfig{CertFile: agent.certFile, KeyFile: agent.keyFile, PeerCAFile: writeCA(t, dir, "admin-ca.pem", adminCA)}

	assertMutualTLSRequest(t, adminConfig, agentConfig, true)
	assertMutualTLSRequest(t, agentConfig, adminConfig, true)
}

func TestMutualTLSRejectsCertificateFromWrongRoleCA(t *testing.T) {
	dir := t.TempDir()
	adminCA := newTestCA(t, "admin-ca")
	agentCA := newTestCA(t, "agent-ca")
	admin := newTestIdentity(t, dir, "admin", adminCA, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	wrongAgent := newTestIdentity(t, dir, "wrong-agent", adminCA, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	adminConfig := TLSConfig{CertFile: admin.certFile, KeyFile: admin.keyFile, PeerCAFile: writeCA(t, dir, "agent-ca.pem", agentCA)}
	wrongAgentConfig := TLSConfig{CertFile: wrongAgent.certFile, KeyFile: wrongAgent.keyFile, PeerCAFile: writeCA(t, dir, "admin-ca.pem", adminCA)}

	assertMutualTLSRequest(t, adminConfig, wrongAgentConfig, false)
}

func TestMutualTLSRejectsExpiredCertificate(t *testing.T) {
	dir := t.TempDir()
	adminCA := newTestCA(t, "admin-ca")
	agentCA := newTestCA(t, "agent-ca")
	admin := newTestIdentity(t, dir, "admin", adminCA, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	expiredAgent := newTestIdentity(t, dir, "expired-agent", agentCA, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))

	adminConfig := TLSConfig{CertFile: admin.certFile, KeyFile: admin.keyFile, PeerCAFile: writeCA(t, dir, "agent-ca.pem", agentCA)}
	expiredConfig := TLSConfig{CertFile: expiredAgent.certFile, KeyFile: expiredAgent.keyFile, PeerCAFile: writeCA(t, dir, "admin-ca.pem", adminCA)}

	assertMutualTLSRequest(t, adminConfig, expiredConfig, false)
}

func assertMutualTLSRequest(t *testing.T, serverConfig, clientConfig TLSConfig, wantSuccess bool) {
	t.Helper()
	serverTLS, err := serverConfig.Server()
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := clientConfig.Client()
	if err != nil {
		t.Fatal(err)
	}
	if serverTLS.MinVersion != tls.VersionTLS13 || clientTLS.MinVersion != tls.VersionTLS13 {
		t.Fatal("控制面必须固定最低 TLS 1.3")
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = serverTLS
	server.StartTLS()
	defer server.Close()

	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: clientTLS,
		},
	}
	resp, err := client.Get(server.URL)
	if wantSuccess {
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		return
	}
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("错误或过期证书不应通过 mTLS")
	}
}

func newTestCA(t *testing.T, commonName string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          nextSerial(t),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert: cert, key: key, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func newTestIdentity(t *testing.T, dir, name string, ca testCA, notBefore, notAfter time.Time) testIdentity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: nextSerial(t),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(dir, name+".crt")
	keyFile := filepath.Join(dir, name+".key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return testIdentity{certFile: certFile, keyFile: keyFile}
}

func writeCA(t *testing.T, dir, name string, ca testCA) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, ca.certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func nextSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	return serial
}
