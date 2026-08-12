# Plaintext gRPC Control Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the Admin-Agent mTLS dependency and run the internal control plane over plaintext gRPC while preserving all delivery, lease, replay, and backoff behavior.

**Architecture:** The browser management plane remains behind Nginx/HTTPS. The Agent initiates a long-lived plaintext HTTP/2 gRPC connection to an Admin listener that must be bound to a controlled private address and protected by network policy; Agent IDs remain logical identifiers rather than cryptographic identities. The change removes only transport credentials and certificate-bound identity checks.

**Tech Stack:** Go 1.26, grpc-go, protobuf, Go testing, Buf.

---

## File map

- Delete `controlplane/tls.go` and `controlplane/tls_test.go`: the control plane no longer owns certificate loading or TLS policy.
- Modify `admin/config.go` and `agent/config.go`: remove TLS configuration fields and mandatory-certificate validation.
- Modify `admin/admin.go`, `admin/grpc_server.go`, `agent/agent.go`, and `agent/grpc_client.go`: remove TLS state and use plaintext gRPC transport credentials on the client.
- Modify `admin/grpc_identity.go`, `admin/grpc_control_service.go`, `admin/grpc_bundle_service.go`, and `admin/grpc_telemetry_service.go`: replace certificate identity binding with non-empty logical Agent ID validation.
- Modify `admin/log_proxy_transport.go`: remove the obsolete TLS-config parameter still used by the transitional log proxy.
- Modify `conf/admin-config.json` and `conf/agent-config.json`: remove certificate paths and make the local Admin sample listen on loopback.
- Add focused config/validation tests under `admin/` and `agent/`.

### Task 1: Make certificate-free configuration the tested contract

**Files:**
- Create: `admin/config_plaintext_grpc_test.go`
- Create: `agent/config_plaintext_grpc_test.go`
- Modify: `admin/config.go`
- Modify: `agent/config.go`
- Modify: `conf/admin-config.json`
- Modify: `conf/agent-config.json`

- [ ] **Step 1: Write failing configuration tests**

```go
func TestDefaultConfigAllowsPlaintextGRPCControlPlane(t *testing.T) {
	cfg := DefaultConfig()
	if err := validateConfig(&cfg); err != nil {
		t.Fatalf("validateConfig() error = %v", err)
	}
}

func TestResolveAllowsPlaintextGRPCControlPlane(t *testing.T) {
	cfg := Config{ID: "agent-001", AdminAddress: "127.0.0.1:7720"}
	if _, err := cfg.Resolve(); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
}
```

- [ ] **Step 2: Verify the tests fail because TLS is mandatory**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./admin ./agent -run 'Test(DefaultConfig|Resolve)AllowsPlaintextGRPCControlPlane' -count=1`

Expected: FAIL with `controlPlane.tls 必须配置` and `agent.tls 必须配置`.

- [ ] **Step 3: Remove TLS fields and validation**

Change `ControlPlaneConfig` to contain only `ListenHost`, `Port`, and `HeartbeatInterval`. Remove `TLS` from `agent.Config`, remove `ControlPlaneTLS` from `agent.ResolvedConfig`, delete both `Enabled()` validation branches, and remove the resolved assignment. Make the Admin default/sample listener `127.0.0.1`; production deployments must explicitly configure a private interface.

- [ ] **Step 4: Remove certificate blocks from sample configuration**

`conf/admin-config.json` must end the `controlPlane` object after `heartbeatInterval`; `conf/agent-config.json` must end the `agent` object after `metricsInterval`.

- [ ] **Step 5: Verify configuration tests pass**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./admin ./agent -run 'Test(DefaultConfig|Resolve)AllowsPlaintextGRPCControlPlane' -count=1`

Expected: PASS.

### Task 2: Replace mTLS transport with plaintext gRPC

**Files:**
- Delete: `controlplane/tls.go`
- Delete: `controlplane/tls_test.go`
- Modify: `admin/admin.go`
- Modify: `admin/grpc_server.go`
- Modify: `admin/log_proxy_transport.go`
- Modify: `agent/agent.go`
- Modify: `agent/grpc_client.go`

- [ ] **Step 1: Remove Admin TLS construction and state**

Delete `controlPlaneTLS *tls.Config`, both calls to `cfg.ControlPlane.TLS.Server/Client`, and their error paths. Construct the transitional log-proxy transports with `newAgentHTTPTransport()` and remove `TLSClientConfig` from that transport.

- [ ] **Step 2: Remove gRPC server credentials**

Construct the Admin server without `grpc.Creds`:

```go
server := grpc.NewServer(
	grpc.MaxRecvMsgSize(maxControlMessageSize),
	grpc.MaxSendMsgSize(maxControlMessageSize),
	// existing keepalive options remain unchanged
)
```

- [ ] **Step 3: Remove Agent TLS construction and state**

Delete `controlPlaneTLS *tls.Config`, the call to `cfg.ControlPlaneTLS.Client`, and the certificate-loading error path from `agent.New`.

- [ ] **Step 4: Dial with grpc-go plaintext transport credentials**

Replace `credentials.NewTLS(...)` with:

```go
grpc.WithTransportCredentials(insecure.NewCredentials())
```

Keep connection reuse, message-size bounds, keepalive, retry classification, and shutdown behavior unchanged.

- [ ] **Step 5: Delete the TLS implementation and tests**

Delete `controlplane/tls.go` and `controlplane/tls_test.go`; no certificate-generation helper or compatibility mode remains.

- [ ] **Step 6: Compile the affected packages**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./controlplane/... ./admin ./agent -run '^$'`

Expected: all affected packages compile.

### Task 3: Replace certificate identity binding with logical ID validation

**Files:**
- Modify: `admin/grpc_identity.go`
- Modify: `admin/grpc_control_service.go`
- Modify: `admin/grpc_bundle_service.go`
- Modify: `admin/grpc_telemetry_service.go`
- Create: `admin/grpc_identity_test.go`

- [ ] **Step 1: Write a failing logical-ID validation test**

```go
func TestRequireAgentID(t *testing.T) {
	if err := requireAgentID("agent-001"); err != nil {
		t.Fatalf("requireAgentID(valid) error = %v", err)
	}
	if err := requireAgentID(""); err == nil {
		t.Fatal("requireAgentID(empty) error = nil")
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./admin -run TestRequireAgentID -count=1`

Expected: build failure because `requireAgentID` does not exist.

- [ ] **Step 3: Implement the minimal validator**

```go
func requireAgentID(agentID string) error {
	if strings.TrimSpace(agentID) == "" {
		return fmt.Errorf("agentId 不能为空")
	}
	return nil
}
```

Remove all x509, peer, and TLS credential inspection. Call the validator from Session, Bundle, and the first Telemetry envelope; map an empty ID to `codes.InvalidArgument` rather than `codes.PermissionDenied`.

- [ ] **Step 4: Verify GREEN and service regressions**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./admin -run 'TestRequireAgentID|TestGRPC' -count=1`

Expected: PASS.

### Task 4: Verify the completed control-plane change

**Files:**
- Modify: `docs/superpowers/specs/2026-08-11-grpc-control-and-file-logging-design.md`
- Modify: `docs/superpowers/plans/2026-08-11-grpc-control-plane-implementation.md`

- [ ] **Step 1: Remove stale mTLS instructions from the active gRPC plan**

Document plaintext internal gRPC, loopback/private-interface binding, firewall assumptions, and the accepted absence of cryptographic Agent identity. Do not rewrite historical roadmap decisions outside the active implementation documents.

- [ ] **Step 2: Verify protobuf generation remains deterministic**

Run: `$env:BUF_CACHE_DIR='D:\Gitee\stressbot\.tmp\bufcache'; .\.tmp\bin\buf.exe lint; .\.tmp\bin\buf.exe generate; .\.tmp\bin\buf.exe build`

Expected: all commands exit 0 and generated files remain consistent.

- [ ] **Step 3: Run backend build and full tests**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go build ./...`

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./... -count=1`

Expected: exit 0 for both commands.

- [ ] **Step 4: Run frontend verification because shared configuration/docs remain part of the current phase**

Run from `cmd/web`: `npx.cmd tsc -b`

Run from `cmd/web`: `npm.cmd run test`

Expected: TypeScript exits 0 and all Vitest files pass.

- [ ] **Step 5: Inspect the exact diff and leave the phase uncommitted until the full gRPC phase passes review**

Run: `git diff --check`

Run: `git status --short`

Expected: no whitespace errors; unrelated user changes in `cmd/web/src/components/FlowEditor/store/flowManagerStore.ts` and `cmd/web/src/services/usePolling.ts` remain untouched and unstaged.
