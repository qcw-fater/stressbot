# Resource Monitor Accuracy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make live host and stressbot process resource metrics task-scoped, fresh, nullable, and semantically consistent from Agent collection through Admin aggregation to React display.

**Architecture:** `agent.SystemMonitor` emits explicit nullable byte-precision measurements through a testable probe. `admin.MetricsAggregator` filters an explicit assigned-node set by server-side freshness and aggregates each metric only from valid samples. The frontend validates required stress coverage fields and renders system coverage, host network, host resources, and process resources without fallback compatibility.

**Tech Stack:** Go 1.26, gopsutil/v4, Sonic JSON wrapper, React 18, TypeScript 5.6, Vitest.

---

### Task 1: Agent resource snapshots

**Files:**
- Modify: `agent/types.go`
- Modify: `agent/sysmon.go`
- Test: `agent/sysmon_test.go`

- [ ] Add failing probe-driven tests for baseline-only first samples, nullable failures, exact byte fields, normalized process CPU, and network reset.
- [ ] Run `go test ./agent -run 'TestSystemMonitor|TestSystemNetwork' -count=1` and confirm the new tests fail for missing nullable semantics.
- [ ] Introduce the minimal gopsutil probe and explicit nullable `SystemSnapshot` fields.
- [ ] Re-run the focused Agent tests until green.

### Task 2: Task-scoped fresh Admin aggregation

**Files:**
- Modify: `admin/types.go`
- Modify: `admin/aggregator.go`
- Modify: `admin/handlers.go`
- Modify: `admin/sampler.go`
- Modify: `admin/admin.go`
- Test: `admin/aggregator_accuracy_test.go`

- [ ] Add failing tests covering assigned-node filtering, server-time freshness, missing/stale coverage, field-level nulls, byte-weighted memory, CPU core weighting, process metrics and hot-node selection.
- [ ] Run `go test ./admin -run 'TestAggregateSystem' -count=1` and confirm expected failures.
- [ ] Implement explicit agent-ID scope, freshness threshold and nullable per-field aggregation.
- [ ] Update live handler, sampler and final archive callers to pass task assignments.
- [ ] Re-run focused Admin tests until green.

### Task 3: API boundary and live panel semantics

**Files:**
- Modify: `cmd/web/src/types/api.ts`
- Modify: `cmd/web/src/services/metricsApi.ts`
- Modify: `cmd/web/src/services/runtimeStore.ts`
- Modify: `cmd/web/src/pages/EditorPage.tsx`
- Modify: `cmd/web/src/components/monitoring/shared/liveMetrics.ts`
- Modify: `cmd/web/src/components/monitoring/MonitorDock.tsx`
- Modify: `cmd/web/src/components/monitoring/tabs/SystemTab.tsx`
- Test: `cmd/web/src/components/monitoring/shared/liveMetrics.test.ts`
- Test: `cmd/web/src/services/__tests__/metricsBinding.test.ts`

- [ ] Add failing tests for required coverage fields, null resource values, separate action and host bandwidth, system coverage, process CPU/RSS and max handles.
- [ ] Run the focused Vitest files and confirm expected failures.
- [ ] Align TypeScript types and service validation with the new backend JSON contract.
- [ ] Render explicit host/current/process labels and `—` for missing metrics; never render `undefined`.
- [ ] Re-run focused Vitest until green.

### Task 4: Documentation and full verification

**Files:**
- Modify: `docs/monitoring-system.md`
- Modify: `docs/frontend-api.md`

- [ ] Update resource metric scope, units, freshness and null semantics.
- [ ] Run `gofmt` on touched Go files.
- [ ] Run `go build ./...` with repository-local `GOCACHE`.
- [ ] Run `go test -race ./agent ./admin ./monitor -count=1`.
- [ ] Run `npx tsc -b`, `npm run test`, and `npm run build` under `cmd/web`.
- [ ] Run Agent for 2-5 minutes, inspect resource API and logs, and verify the panel in the browser.

### Task 5: Deferred database migration

- [ ] After live monitoring is verified, design the history schema, append the idempotent SQL to `deploy/upgrade.sql`, back up MySQL, apply the migration, and verify schema/data separately.
