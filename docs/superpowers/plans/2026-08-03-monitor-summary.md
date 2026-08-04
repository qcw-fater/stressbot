# Monitor Summary Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make backend snapshot summary the sole source for cross-action Apdex and latency metrics while preserving timing-detail diagnostics.

**Architecture:** `monitor` builds one summary from action raw counters and merged histograms. Admin persists/projects that summary, and React maps it directly to view models. Per-action applicability uses a backend-provided Apdex denominator.

**Tech Stack:** Go, React 18, TypeScript, Vitest, Ant Design 5

---

### Task 1: Monitor summary contract

**Files:** `monitor/snapshot.go`, `monitor/apdex_scope_test.go`, `monitor/snapshot_test.go`

- [ ] Add failing tests for a failed-only network action, cross-action Apdex denominator, merged P95/P99, and lowest `timingDetail`.
- [ ] Run `go test ./monitor` with repository-local `GOCACHE` and confirm the new assertions fail.
- [ ] Add `SnapshotSummary`, `rttApdexSampleCount`, `timingDetail`, and one summary builder shared by local and distributed snapshots.
- [ ] Run `go test ./monitor` and confirm the package passes.

### Task 2: Admin history consumes summary

**Files:** `admin/sampler.go`, `admin/sampler_test.go`, `admin/types.go`, `admin/history.go`

- [ ] Add a failing sampler test whose action-weighted percentile differs from the supplied backend summary.
- [ ] Make the sampler copy summary values and make history projection preserve summary, timing level, denominator, and phase fields.
- [ ] Run focused Admin tests and package compilation.

### Task 3: Frontend uses summary

**Files:** `cmd/web/src/types/api.ts`, `cmd/web/src/components/monitoring/shared/liveMetrics.ts`, `cmd/web/src/components/monitoring/shared/liveMetrics.test.ts`, `cmd/web/src/components/modules/AgentsPanel.tsx`, `cmd/web/src/components/modules/history/HistoryDetailView.tsx`, `cmd/web/src/components/modules/history/report/ReportHtml.tsx`, `cmd/web/src/services/metricsBinding.ts`

- [ ] Add a failing Vitest showing the live model must prefer backend summary over action-derived values.
- [ ] Extend API types and replace frontend cross-action aggregation with direct summary mapping.
- [ ] Remove `computeWeightedMetrics` and its obsolete tests/exports.
- [ ] Run the focused Vitest and TypeScript compiler.

### Task 4: Timing-aware diagnostic table

**Files:** `cmd/web/src/components/monitoring/shared/ActionMetricsTable.tsx`, `cmd/web/src/components/monitoring/shared/ActionMetricsTable.test.tsx`, `cmd/web/src/components/FlowEditor/nodes/shared/MetricsBadge.tsx`, live/history callers

- [ ] Add failing tests for failed-only Apdex visibility and timing-level column availability.
- [ ] Use `rttApdexSampleCount`, rename the switch to `耗时拆分`, display cancellation independently, and select phase columns by `timingDetail`.
- [ ] Run focused component tests and TypeScript compilation.

### Task 5: Remove obsolete monitoring code and update docs

**Files:** dead files under `cmd/web/src/components/monitoring/tabs/`, `README.md`, `docs/monitoring-system.md`, `docs/frontend-api.md`

- [ ] Delete tabs with no import or route consumer.
- [ ] Update Apdex denominator, backend summary, percentile, and timing-detail documentation from current code.
- [ ] Search for stale action-weighted Apdex/P95 implementations and outdated “failed requests excluded” text.

### Task 6: Full verification

- [ ] Run `go build ./...` with repository-local `GOCACHE`.
- [ ] Run `npx.cmd tsc -b` and `npm.cmd run test -- --run` in `cmd/web`.
- [ ] Validate `conf/flow/flow.json` through the existing frontend validator tests.
- [ ] Run the Agent for 2-5 minutes, stop it, and inspect the log for unexpected error/warn entries.
- [ ] Review the final diff without reverting unrelated user changes.
