# Monitor Accuracy DDSketch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the lossy 16-bucket latency pipeline with mergeable DDSketch windows and make live, cumulative, distributed, history, connection, byte, error, and system-rate metrics mathematically consistent.

**Architecture:** Each Agent keeps eight sharded action accumulators with exact counters plus DDSketch distributions, atomically rotates an active reporting window into committed cumulative state, and retries the same sequenced window until Admin acknowledges it. Admin deduplicates windows per task and Agent, merges DDSketch payloads for live and history views, excludes stale Agents only from live views, and publishes JSON DTOs without internal sketch bytes.

**Tech Stack:** Go 1.26, `github.com/DataDog/sketches-go/ddsketch` v1.4.8, React 18, TypeScript 5.6, Ant Design 5, Vitest.

---

## File Map

- Create `monitor/ddsketch.go`: DDSketch construction, encoding, decoding, quantiles, strict merge, and invalid-sample handling.
- Create `monitor/action_accumulator.go`: eight sharded action writers, active/committed rotation, and timing-stage observation bits.
- Create `monitor/report_snapshot.go`: cumulative plus window report DTOs and public-copy sanitization.
- Modify `monitor/collector.go`, `monitor/snapshot.go`, `monitor/reporter.go`, `monitor/http.go`: route all writes and snapshots through the new accumulator and remove bucket-delta APIs.
- Modify `engine/action.go`, `engine/executor.go`: report observed timing stages and pending cancellations explicitly.
- Modify `agent/types.go`, `agent/reporter.go`, `agent/sysmon.go`: sequenced retryable reports and reset-safe system network rates.
- Create `admin/metrics_window_store.go`: per-task/per-Agent sequence deduplication, freshness, cumulative state, and pending history windows.
- Modify `admin/agent.go`, `admin/handlers.go`, `admin/aggregator.go`, `admin/sampler.go`, `admin/history.go`, `admin/mysql_schema.go`, `admin/types.go`: receive-time validation, exact distributed merge, stale filtering, and window-based history.
- Modify `network/connection.go`, `robot/robot.go`: explicit active/closed/dropped accounting and late callback delivery.
- Modify `cmd/web/src/types/api.ts` and monitoring/history components: consume server-provided windows, observed-stage counts, renamed fields, nullable rates, and remove browser-side conversion/staleness logic.

## Contract Locked by This Plan

```go
const (
	latencyRelativeAccuracy = 0.01
	latencyMaxBins          = 2048
	actionShardCount        = 8
)

type HistogramSnapshot struct {
	Count  uint64   `json:"count"`
	SumNs  float64  `json:"sumNs"`
	MinMs  *float64 `json:"minMs"`
	MaxMs  *float64 `json:"maxMs"`
	AvgMs  *float64 `json:"avgMs"`
	P50Ms  *float64 `json:"p50Ms"`
	P90Ms  *float64 `json:"p90Ms"`
	P95Ms  *float64 `json:"p95Ms"`
	P99Ms  *float64 `json:"p99Ms"`
	Sketch []byte   `json:"sketch,omitempty"`
}

type ReportMeta struct {
	Sequence                uint64    `json:"sequence"`
	StartedAt               time.Time `json:"startedAt"`
	EndedAt                 time.Time `json:"endedAt"`
	ExpectedIntervalSeconds float64   `json:"expectedIntervalSeconds"`
}

type ReportWindow struct {
	Sequence                uint64           `json:"sequence,omitempty"`
	StartedAt               time.Time        `json:"startedAt"`
	EndedAt                 time.Time        `json:"endedAt"`
	DurationSeconds         float64          `json:"durationSeconds"`
	ExpectedIntervalSeconds float64          `json:"expectedIntervalSeconds"`
	Summary                 SummarySnapshot  `json:"summary"`
	Actions                 []ActionSnapshot `json:"actions"`
	InvalidMetricSamples    uint64           `json:"invalidMetricSamples"`
}

type CollectorSnapshot struct {
	Summary SummarySnapshot  `json:"summary"`
	Actions []ActionSnapshot `json:"actions"`
	Window  *ReportWindow    `json:"window,omitempty"`
}
```

The internal wire report carries `HistogramSnapshot.Sketch`; `PublicCopy` always removes it and clears the per-Agent `Sequence` from the aggregated public window. Latency values enter DDSketch as nanoseconds, JSON presentation stays in milliseconds, and no old bucket fallback or `metricsVersion` branch is permitted. Top-level `InvalidMetricSamples` is cumulative; `window.invalidMetricSamples` belongs only to that interval.

### Task 1: DDSketch Distribution Core

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `monitor/ddsketch.go`
- Create: `monitor/ddsketch_test.go`
- Delete after migration: `monitor/histogram.go`

- [ ] **Step 1: Add the pinned dependency**

Run:

```powershell
go get github.com/DataDog/sketches-go@v1.4.8
go mod tidy
```

Expected: `go.mod` contains `github.com/DataDog/sketches-go v1.4.8`; no unrelated direct dependency changes.

- [ ] **Step 2: Write failing distribution tests**

Create tests that assert exact count/sum/min/max, bounded quantile rank error, merge equivalence, empty behavior, and rejection of `NaN`, infinities, and negative durations:

```go
func TestLatencySketchSnapshotAndMerge(t *testing.T) {
	left := mustNewLatencySketch(t)
	right := mustNewLatencySketch(t)
	for i := 1; i <= 10_000; i++ {
		target := left
		if i%2 == 0 { target = right }
		require.NoError(t, target.Add(float64(time.Duration(i)*time.Microsecond)))
	}
	require.NoError(t, left.Merge(right))
	s := requireSnapshot(t, left)
	require.Equal(t, uint64(10_000), s.Count)
	require.Equal(t, float64((10_000*10_001)/2)*float64(time.Microsecond), s.SumNs)
	require.Equal(t, 0.001, metricValue(t, s.MinMs))
	require.Equal(t, 10.0, metricValue(t, s.MaxMs))
	require.InDelta(t, 9.9, metricValue(t, s.P99Ms), 0.1)
	require.LessOrEqual(t, metricValue(t, s.P99Ms), metricValue(t, s.MaxMs))
}

func TestLatencySketchRejectsInvalidSamples(t *testing.T) {
	s := mustNewLatencySketch(t)
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1} {
		require.Error(t, s.Add(value))
	}
	require.Equal(t, uint64(0), requireSnapshot(t, s).Count)
}
```

Add table cases spanning `1ns`, microseconds, seconds, more than 60 seconds, and one hour. For P50/P90/P95/P99, compare against exact sorted quantiles and assert relative value error is at most 1% except values collapsed at the configured lowest-store boundary. Add a 100,000-value wide-range test that reaches the 2048-bin cap and still keeps high quantiles within the bound.

Test helpers in the same file construct `newLatencySketch()`, fail the test on constructor/snapshot errors, and define `metricValue(t, *float64) float64` using `require.NotNil` before dereference. Empty distributions have `count=0`, `sumNs=0`, and every millisecond field nil so public JSON produces `null` rather than a fabricated zero latency.

- [ ] **Step 3: Verify the tests fail for the missing implementation**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./monitor -run 'TestLatencySketch' -count=1`

Expected: FAIL because `newLatencySketch` and the DDSketch-backed type do not exist.

- [ ] **Step 4: Implement the focused wrapper**

Implement `latencySketch` with `LogCollapsingLowestDenseDDSketch(0.01, 2048)`, exact Kahan-style `sumNs`, exact min/max, `Encode(..., false)`, and `DecodeDDSketch(..., store.BufferedPaginatedStoreConstructor, nil)`. `Merge` must decode and merge sketches, add exact counters/sums, and never infer a quantile from bucket boundaries. Add `Reset()` using the DDSketch/store clear API so rotated action stats can be reused without allocating a new 2048-bin store each interval.

```go
func (s *latencySketch) Add(ns float64) error {
	if math.IsNaN(ns) || math.IsInf(ns, 0) || ns < 0 {
		return ErrInvalidMetricSample
	}
	if err := s.sketch.Add(ns); err != nil { return err }
	s.count++
	s.addSum(ns)
	if s.count == 1 || ns < s.minNs { s.minNs = ns }
	if s.count == 1 || ns > s.maxNs { s.maxNs = ns }
	return nil
}
```

Convert DDSketch nanosecond values to millisecond pointers only when building snapshots. Clamp returned quantiles into exact `[MinMs, MaxMs]` only as an invariant guard. Increment the collector's invalid-sample counter at the caller when `ErrInvalidMetricSample` is returned.

- [ ] **Step 5: Run focused tests and record an implementation checkpoint**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./monitor -run 'TestLatencySketch' -count=1`

Expected: PASS. After explicit commit authorization: `git add go.mod go.sum monitor/ddsketch.go monitor/ddsketch_test.go && git commit -m "refactor: add DDSketch latency distribution"`.

### Task 2: Sharded Action Accumulator and Timing Observation

**Files:**
- Create: `monitor/action_accumulator.go`
- Create: `monitor/action_accumulator_test.go`
- Modify: `monitor/collector.go`
- Modify: `monitor/types.go`
- Modify: `engine/action.go`

- [ ] **Step 1: Write failing concurrency and stage-observation tests**

Use eight goroutines writing distinct action names and eight writing the same name. Assert no lost counts under `go test -race`, and distinguish an unobserved stage from a measured zero-duration stage:

```go
func TestActionAccumulatorObservedStages(t *testing.T) {
	a := mustNewActionAccumulator(t)
	a.Record("login", MetricRecord{
		Result: Success,
		Timing: ClientTiming{RTT: 0, Observed: StageRTT | StageEncode},
	})
	s := requireAction(t, a.Cumulative(), "login")
	require.Equal(t, uint64(1), s.RTT.Count)
	require.Equal(t, uint64(1), s.Encode.Count)
	require.Equal(t, uint64(0), s.Build.Count)
}
```

Define `TimingStage` as a bitset with `StageRTT`, `StageListenWait`, `StageBuild`, `StageEncode`, `StageSend`, `StageDecodeWait`, `StageDecode`, `StageDispatchWait`, and `StageParseStore`. Add `Observed TimingStage` to `ClientTiming`; call sites set bits only where the stage was actually measured.

- [ ] **Step 2: Verify the tests fail**

Run: `$env:GOCACHE="$PWD\.gocache"; go test -race ./monitor -run 'TestActionAccumulator' -count=1`

Expected: FAIL because the accumulator and observation bitset do not exist.

- [ ] **Step 3: Implement eight shards**

Use `fnv32(actionName) % actionShardCount`; each shard owns a mutex and `map[string]*actionStats`. `actionStats` owns exact counters and one non-concurrent `latencySketch` per observed stage. No DDSketch instance may be mutated outside its shard lock.

```go
type actionShard struct {
	mu      sync.Mutex
	active  map[string]*actionStats
}

type actionAccumulator struct {
	transitionMu sync.Mutex
	shards       [actionShardCount]actionShard
	committed    map[string]*actionStats
}
```

Update all engine timing construction points to set the matching `Observed` bit. A stage with an observed zero duration contributes a sample; an unobserved stage contributes nothing.

- [ ] **Step 4: Run focused race tests**

Run: `$env:GOCACHE="$PWD\.gocache"; go test -race ./monitor ./engine -run 'TestActionAccumulator|Test.*Timing' -count=1`

Expected: PASS with no race report. After explicit commit authorization: commit `monitor/action_accumulator.go`, its tests, `monitor/collector.go`, `monitor/types.go`, and `engine/action.go` with message `refactor: shard action metric accumulation`.

### Task 3: Counter Semantics, Cancellations, Bytes, and Non-RTT Cost

**Files:**
- Modify: `monitor/collector.go`
- Modify: `monitor/snapshot.go`
- Modify: `monitor/types.go`
- Modify: `engine/executor.go`
- Test: `monitor/collector_test.go`
- Test: `monitor/apdex_scope_test.go`

- [ ] **Step 1: Add failing semantic tests**

Cover these exact invariants:

```go
func TestActionMetricSemantics(t *testing.T) {
	c := newTestCollector(t, 100*time.Millisecond)
	c.RecordAction("request", Success, ClientTiming{RTT: 80*time.Millisecond, Observed: StageRTT}, 90*time.Millisecond, 10, 20, nil)
	c.RecordAction("request", Failure, ClientTiming{RTT: 120*time.Millisecond, Observed: StageRTT}, 130*time.Millisecond, 30, 40, errors.New("business"))
	c.RecordPendingCanceled("request")
	s := requireAction(t, c.Snapshot(), "request")
	require.Equal(t, uint64(2), s.SampleCount)
	require.Equal(t, uint64(1), s.Success)
	require.Equal(t, uint64(1), s.Failure)
	require.Equal(t, uint64(1), s.Canceled)
	require.Equal(t, uint64(2), s.ByteSampleCount)
	require.Equal(t, uint64(40), s.SendBytes)
	require.Equal(t, uint64(60), s.RecvBytes)
	require.Equal(t, uint64(2), s.Apdex.Total)
	require.InDelta(t, 10.0, s.NonRTTAvgMs, 0.001)
}
```

Also assert a truly started canceled action increases `Canceled` and `ByteSampleCount` and retains actual bytes, while pending cancellation changes only `Canceled`. Assert `SampleCount = Success + Failure + Timeout`, so canceled actions do not enter QPS, total actions, or success rate. Assert framework and business errors are aggregated only by numeric code, recent details remain capped at three, and timeouts/connection interruptions are Apdex-frustrated without inventing RTT samples.

- [ ] **Step 2: Verify the tests fail**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./monitor -run 'TestActionMetricSemantics|TestApdex' -count=1`

Expected: FAIL on old cancellation, byte, or client-cost semantics.

- [ ] **Step 3: Implement the locked semantics**

Rename `ClientAvgMs` to `NonRTTAvgMs` throughout Go DTOs. Calculate it as `(wallClock - RTT)` only for samples with observed RTT, clamp negative clock skew to zero, and average over the number of contributing samples. `RecordPendingCanceled` increments only `Canceled` and never increments `SampleCount`, in-flight, Apdex, error, `ByteSampleCount`, or bytes.

For started actions, `ByteSampleCount` and bytes reflect actual observed writes/reads on success, failure, timeout, or real cancellation. `AvgSendBytes` and `AvgRecvBytes` divide by `ByteSampleCount`. Apdex applies only to `networked`; business failures with RTT use that RTT, while timeout and connection loss increment frustrated and the Apdex denominator without adding a latency sample. Sum raw satisfied/tolerating/frustrated counts during merge; never average node scores.

When any measured duration is negative or DDSketch `Add` fails, omit only that duration sample, increment both cumulative and active-window `InvalidMetricSamples`, and still record the real action result/counters. Add a test proving the next rotated window contains one invalid sample while cumulative remains one after a later empty rotation.

- [ ] **Step 4: Run semantic tests**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./monitor ./engine -run 'TestActionMetricSemantics|TestApdex|Test.*Canceled' -count=1`

Expected: PASS. After explicit commit authorization: commit the Task 3 files with message `fix: align monitor counter semantics`.

### Task 4: Atomic Cumulative and Reporting Windows

**Files:**
- Create: `monitor/report_snapshot.go`
- Create: `monitor/report_snapshot_test.go`
- Modify: `monitor/collector.go`
- Modify: `monitor/reporter.go`
- Modify: `monitor/http.go`
- Modify: `monitor/snapshot.go`

- [ ] **Step 1: Write failing rotation tests**

The test writes known samples, rotates once, writes concurrently during a second rotation, and proves every started action appears exactly once in windows and once in cumulative state:

```go
func TestTakeReportSnapshotPartitionsSamplesExactlyOnce(t *testing.T) {
	c := newTestCollector(t, 100*time.Millisecond)
	for i := 0; i < 100; i++ { recordSuccess(c, "login", time.Millisecond) }
	first := c.TakeReportSnapshot(ReportMeta{Sequence: 1, StartedAt: unix(0), EndedAt: unix(5), ExpectedIntervalSeconds: 5})
	for i := 0; i < 25; i++ { recordSuccess(c, "login", 2*time.Millisecond) }
	second := c.TakeReportSnapshot(ReportMeta{Sequence: 2, StartedAt: unix(5), EndedAt: unix(10), ExpectedIntervalSeconds: 5})
	require.Equal(t, uint64(100), action(first.Window, "login").SampleCount)
	require.Equal(t, uint64(25), action(second.Window, "login").SampleCount)
	require.Equal(t, uint64(125), action(second, "login").SampleCount)
}
```

Add a race test with writers blocked at deterministic barriers around rotation. Assert `Snapshot()` is read-only and does not advance windows.

- [ ] **Step 2: Verify the tests fail**

Run: `$env:GOCACHE="$PWD\.gocache"; go test -race ./monitor -run 'TestTakeReportSnapshot|TestSnapshotIsReadOnly' -count=1`

Expected: FAIL because the old API accepts `prevCounts` and derives deltas outside the collector.

- [ ] **Step 3: Implement rotation under `transitionMu`**

`TakeReportSnapshot(meta)` locks `transitionMu`, locks shards in ascending index order, swaps every shard's active map with a cleared reusable inactive map, unlocks shards, merges the detached maps into committed cumulative state, and returns both the detached window and cumulative snapshot. Writers may pause during the swap but never observe partially rotated state. After encoding the immutable report, return detached action stats to a bounded reuse pool and call `latencySketch.Reset()` before reuse.

Replace:

```go
Snapshot(prevCounts map[string]uint64, periodSec float64)
```

with:

```go
func (c *Collector) Snapshot() CollectorSnapshot
func (c *Collector) TakeReportSnapshot(meta ReportMeta) CollectorSnapshot
```

Delete browser/report-side count differencing and old reporter `prevCounts`. `monitor/http.go` calls `Snapshot()` and returns `PublicCopy()`.

- [ ] **Step 4: Run rotation and monitor package tests**

Run: `$env:GOCACHE="$PWD\.gocache"; go test -race ./monitor -count=1`

Expected: PASS with no race report. After explicit commit authorization: commit Task 4 files with message `refactor: add atomic metric report windows`.

### Task 5: Strict Snapshot Merge and Public Sanitization

**Files:**
- Modify: `monitor/snapshot.go`
- Modify: `monitor/report_snapshot.go`
- Create: `monitor/snapshot_merge_test.go`
- Modify: `monitor/http_test.go`

- [ ] **Step 1: Write failing strict-merge tests**

Test merging three differently distributed sketches against a sketch built from all raw samples. Test malformed/missing sketch payloads, incompatible DDSketch mappings, and public JSON leakage:

```go
func TestMergeHistogramsUsesSketchPayload(t *testing.T) {
	parts := []HistogramSnapshot{
		histogramOf(t, 1*time.Millisecond, 2*time.Millisecond),
		histogramOf(t, 500*time.Millisecond, 600*time.Millisecond),
		histogramOf(t, 5*time.Second),
	}
	got, err := MergeHistograms(parts...)
	require.NoError(t, err)
	require.InDelta(t, 5000.0, metricValue(t, got.P99Ms), 50.0)
	require.LessOrEqual(t, metricValue(t, got.P99Ms), metricValue(t, got.MaxMs))
}

func TestPublicCopyRemovesAllSketchBytes(t *testing.T) {
	public := reportWithNestedSketches(t).PublicCopy()
	b, err := json.Marshal(public)
	require.NoError(t, err)
	require.NotContains(t, string(b), "sketch")
}
```

Malformed or absent sketch bytes for any non-empty histogram must return a typed merge error; there is no midpoint, max-clamp-only, or legacy bucket fallback.

- [ ] **Step 2: Verify the tests fail**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./monitor -run 'TestMergeHistograms|TestPublicCopy' -count=1`

Expected: FAIL because current merge adds fixed buckets and public DTOs do not recursively sanitize sketches.

- [ ] **Step 3: Implement strict merge and sanitization**

Make merge signatures return errors:

```go
func MergeHistograms(parts ...HistogramSnapshot) (HistogramSnapshot, error)
func MergeSnapshots(parts ...CollectorSnapshot) (CollectorSnapshot, error)
func (s CollectorSnapshot) PublicCopy() CollectorSnapshot
```

Decode every non-empty sketch, verify compatible relative-accuracy mappings through `MergeWith`, merge exact count/sum/min/max separately, recompute quantiles once, and propagate errors to Agent/Admin boundaries. `PublicCopy` deep-copies actions, stage distributions, RTT/listen/wall distributions, summary distributions, and both cumulative/window branches before clearing every `Sketch` slice.

- [ ] **Step 4: Run merge and HTTP tests**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./monitor -run 'TestMerge|TestPublicCopy|TestHTTP' -count=1`

Expected: PASS. After explicit commit authorization: commit Task 5 files with message `fix: merge latency distributions exactly`.

### Task 6: Agent Sequencing, Retry, and Stable Window Identity

**Files:**
- Modify: `agent/types.go`
- Modify: `agent/reporter.go`
- Create: `agent/reporter_test.go`
- Modify: `agent/client.go`

- [ ] **Step 1: Introduce a testable posting boundary and failing retry tests**

Define this local interface in `agent/reporter.go`:

```go
type stressReportPoster interface {
	PostStressReport(context.Context, StressReport) error
}
```

Use a scripted fake that fails twice and succeeds once. Assert all three calls carry byte-for-byte identical `window.sequence`, `window.startedAt`, `window.endedAt`, and window counters; after success, the next call advances the sequence and contains only new samples.

```go
func TestMetricsReporterRetriesSameWindowUntilAck(t *testing.T) {
	poster := &scriptedPoster{errors: []error{errTemporary, errTemporary, nil, nil}}
	r := newTestMetricsReporter(t, poster)
	recordSuccess(r.collector, "login", time.Millisecond)
	r.reportOnce(context.Background())
	r.reportOnce(context.Background())
	r.reportOnce(context.Background())
	requireSameWindow(t, poster.calls[0], poster.calls[1], poster.calls[2])
	recordSuccess(r.collector, "login", 2*time.Millisecond)
	r.reportOnce(context.Background())
	require.Equal(t, poster.calls[2].Snapshot.Window.Sequence+1, poster.calls[3].Snapshot.Window.Sequence)
	require.Equal(t, uint64(1), action(poster.calls[3].Snapshot.Window, "login").SampleCount)
}
```

The helper serializes the window portion to JSON before comparison, so pointer identity cannot hide mutation.

- [ ] **Step 2: Verify the tests fail**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./agent -run 'TestMetricsReporter' -count=1`

Expected: FAIL because each failed tick currently discards its reporting interval.

- [ ] **Step 3: Implement pending-window retry**

Keep sequence and interval bounds inside `StressReport.Snapshot.Window`; do not duplicate them on the outer report. Keep one immutable pending report. On a tick: if pending exists, resend it; otherwise rotate the collector with the next sequence and current interval bounds, then post. Clear pending only after a 2xx acknowledgment. Collector reset does not reset reporter sequence; a new task/reporter instance starts at sequence one.

```go
if r.pending == nil {
	r.pending = r.buildNextReport(now)
}
if err := r.poster.PostStressReport(ctx, *r.pending); err != nil { return err }
	r.lastAckedTo = r.pending.Snapshot.Window.EndedAt
	r.nextSequence++
	r.pending = nil
```

Use the existing HTTP client retry classification; do not rotate a second window while one is pending. For a deterministic 4xx validation rejection, retain the pending window, emit a rate-limited Chinese error containing task, Agent, sequence, and rejection detail, and keep retrying only at the normal report interval so invalid data is visible but does not flood logs.

- [ ] **Step 4: Run Agent tests**

Run: `$env:GOCACHE="$PWD\.gocache"; go test -race ./agent -run 'TestMetricsReporter' -count=1`

Expected: PASS. After explicit commit authorization: commit Task 6 files with message `fix: retry sequenced metric windows`.

### Task 7: Admin Idempotent Window Store

**Files:**
- Create: `admin/metrics_window_store.go`
- Create: `admin/metrics_window_store_test.go`
- Modify: `admin/admin.go`
- Modify: `admin/types.go`
- Modify: `admin/handlers.go`
- Modify: `admin/agent.go`

- [ ] **Step 1: Write failing deduplication and receive-time tests**

Create `MetricsWindowStore.Accept` tests for first receipt, duplicate receipt, sequence gap, task mismatch, and a forged future `ReportedAt`. The accepted result must distinguish `Accepted`, `Duplicate`, and `Rejected` so the handler can return 2xx for duplicates but 4xx for malformed input.

```go
func TestMetricsWindowStoreAcceptIsIdempotent(t *testing.T) {
	clock := newFakeClock(unix(100))
	s := NewMetricsWindowStore(clock.Now)
	report := validStressReport("task-1", "agent-1", 1, unix(90), unix(95))
	first, err := s.Accept(report, 5*time.Second, 100*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, Accepted, first.Status)
	duplicate, err := s.Accept(report, 5*time.Second, 100*time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, Duplicate, duplicate.Status)
	require.Equal(t, uint64(1), s.PendingHistoryCount("task-1"))
	require.Equal(t, unix(100), s.AgentState("task-1", "agent-1").ReceivedAt)
}
```

`validStressReport` constructs a non-empty DDSketch window using monitor test-visible builders, and the fake clock returns deterministic Admin receive time.

- [ ] **Step 2: Verify the tests fail**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./admin -run 'TestMetricsWindowStore' -count=1`

Expected: FAIL because Admin currently replaces `LatestStress` without sequence tracking.

- [ ] **Step 3: Implement the store and handler contract**

Key state by `(taskID, agentID)`:

```go
type agentMetricState struct {
	LastSequence uint64
	ReceivedAt   time.Time
	ExpectedEvery time.Duration
	Cumulative   monitor.CollectorSnapshot
	LatestWindow monitor.ReportWindow
}

type MetricsWindowStore struct {
	mu             sync.RWMutex
	byTask         map[string]map[string]*agentMetricState
	pendingHistory map[string][]monitor.ReportWindow
	now            func() time.Time
}
```

Rules:

- Sequence equal to `LastSequence` is an acknowledged duplicate and is not merged or queued again.
- Sequence below `LastSequence` is an acknowledged stale duplicate and is not merged.
- Sequence above `LastSequence+1` is rejected with a Chinese diagnostic because the missing window would make cumulative totals unknowable.
- A first report may start at sequence one only.
- Verify `StartedAt < EndedAt`, `DurationSeconds` matches the bounds within one millisecond, `ExpectedIntervalSeconds` matches the Agent's configured report interval, action/histogram invariants hold, configured `ApdexT` exactly matches the task, and task/Agent IDs match route context. Do not cap actual window duration: after an outage, new samples may legitimately accumulate in the active interval while an older pending interval is retried.
- Record freshness using `now()` only; do not trust Agent wall clock for staleness.
- Validate cumulative exact counters are monotonic relative to the prior accepted snapshot, replace the per-Agent cumulative snapshot with the report's top-level cumulative state, append its window to `pendingHistory` once, then update `LastSequence` atomically. Do not add cumulative snapshots together.

Wire `AdminServer.metricsWindows` in `admin/admin.go`. `handleAgentStressReport` validates the running assignment, calls `Accept`, updates Agent display metadata, and returns 200 for accepted or duplicate windows.

- [ ] **Step 4: Run Admin store and handler tests**

Run: `$env:GOCACHE="$PWD\.gocache"; go test -race ./admin -run 'TestMetricsWindowStore|TestHandleAgentStressReport' -count=1`

Expected: PASS with no duplicate growth. After explicit commit authorization: commit Task 7 files with message `feat: store metric windows idempotently`.

### Task 8: Accurate Distributed Live and Cumulative Aggregation

**Files:**
- Modify: `admin/aggregator.go`
- Modify: `admin/handlers.go`
- Modify: `admin/metrics_window_store.go`
- Create: `admin/aggregator_accuracy_test.go`
- Modify: `admin/aggregator_test.go`

- [ ] **Step 1: Write failing multi-Agent and staleness tests**

Use two Agents with disjoint latency distributions and report intervals. Assert cumulative totals retain both Agents, live rate includes only fresh windows, merged P99 comes from merged DDSketches, and stale facts do not disappear:

```go
func TestAggregatorSeparatesCumulativeFactsFromFreshLiveRates(t *testing.T) {
	clock := newFakeClock(unix(100))
	store := seededMetricStore(t, clock,
		agentWindow("b", unix(25), 1, unix(20), unix(25), 40, 2*time.Second),
		agentWindow("a", unix(95), 1, unix(90), unix(95), 100, 10*time.Millisecond),
	)
	clock.Set(unix(100))
	got, err := NewMetricsAggregator(store, clock.Now).AggregateTask("task-1")
	require.NoError(t, err)
	require.Equal(t, uint64(140), got.Summary.SampleCount)
	require.InDelta(t, 20.0, got.Window.Summary.QPS, 0.001)
	require.Equal(t, uint64(1), got.FreshAgents)
	require.Equal(t, uint64(1), got.StaleAgents)
	require.Greater(t, action(got, "login").RTT.P99Ms, 1000.0)
}
```

The seeded helper sets the fake Admin clock to each event's explicit receive time before accepting it through `MetricsWindowStore`; it does not derive freshness from Agent timestamps or mutate private store maps.

- [ ] **Step 2: Verify the tests fail**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./admin -run 'TestAggregatorSeparates|TestMetricsAggregator' -count=1`

Expected: FAIL because current aggregation includes every non-offline `LatestStress` and averages cumulative rates.

- [ ] **Step 3: Implement two aggregation planes**

Freshness threshold is `max(3*ExpectedEvery, 15*time.Second)` based on Admin `ReceivedAt`.

```go
type AggregatedMetrics struct {
	monitor.CollectorSnapshot
	FreshAgents uint64 `json:"freshAgents"`
	StaleAgents uint64 `json:"staleAgents"`
	AsOf        time.Time `json:"asOf"`
}
```

Merge every accepted per-Agent cumulative snapshot for totals and cumulative distributions, regardless of freshness while the Agent remains assigned to the task. Merge only fresh latest windows for `Window`, QPS, bandwidth rates, current in-flight, active connections, running robots, and system resources. Sum exact counters; calculate each Agent's rate as `count / ownDuration`, then sum the Agent rates. Merge latency sketches and raw Apdex counts across the same fresh windows. Propagate strict merge errors as HTTP 500 with Chinese logs; never publish guessed percentiles.

Expose `AssignedAgents`, `ReportingAgents`, and their coverage ratio. An Agent whose latest report was rejected for invalid sketch/mapping/Apdex T remains assigned but is not reporting in the current window. Public aggregate windows contain `StartedAt=min(fresh startedAt)`, `EndedAt=max(fresh endedAt)`, no per-Agent sequence, and null current values when no Agent is fresh.

`GET /sbot/tasks/:id/metrics` returns `PublicCopy()` of this result. Remove any Admin-side `metricsVersion` or bucket compatibility branch.

- [ ] **Step 4: Run aggregation tests**

Run: `$env:GOCACHE="$PWD\.gocache"; go test -race ./admin -run 'TestAggregator|TestMetricsAggregator' -count=1`

Expected: PASS. After explicit commit authorization: commit Task 8 files with message `fix: aggregate fresh windows and cumulative facts separately`.

### Task 9: Window-Based History Persistence and Reads

**Files:**
- Modify: `admin/mysql_schema.go`
- Modify: `admin/types.go`
- Modify: `admin/history.go`
- Modify: `admin/sampler.go`
- Modify: `admin/admin.go`
- Create: `admin/history_window_test.go`
- Modify: `admin/history_schema_test.go`
- Modify: `admin/sampler_summary_test.go`

- [ ] **Step 1: Write failing history schema and sampler tests**

Assert each accepted window is drained once into one timeseries row, duplicate reports do not duplicate rows, QPS and bandwidth are calculated from that row's window duration, pressure metrics are nullable when absent, and percentile fields are stored from merged DDSketches:

```go
func TestSamplerPersistsAcceptedWindowsExactlyOnce(t *testing.T) {
	store := acceptedWindows(t, twoAgentsSamePeriod())
	db := newHistoryRecorder()
	sampler := NewSampler(store, db, fixedTaskProvider("task-1"))
	require.NoError(t, sampler.SampleOnce(context.Background()))
	require.NoError(t, sampler.SampleOnce(context.Background()))
	require.Len(t, db.rows, 1)
	require.InDelta(t, 30.0, db.rows[0].QPS, 0.001)
	require.NotZero(t, db.rows[0].P99Ms)
}
```

Use a recorder implementing a narrow `HistoryWriter` interface instead of requiring MySQL in the unit test.

- [ ] **Step 2: Verify the tests fail**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./admin -run 'TestSamplerPersists|TestHistorySchema|TestSamplerSummary' -count=1`

Expected: FAIL because the sampler currently snapshots cumulative `AvgQPS`/bandwidth and cannot atomically drain accepted windows.

- [ ] **Step 3: Add precise history columns and transactional drain**

Extend `task_timeseries` with window identity and accuracy fields:

```sql
window_from DATETIME(6) NOT NULL,
window_to DATETIME(6) NOT NULL,
sample_count BIGINT UNSIGNED NOT NULL,
p50_ms DOUBLE NULL,
p90_ms DOUBLE NULL,
p95_ms DOUBLE NULL,
p99_ms DOUBLE NULL,
active_connections BIGINT NULL,
closed_connections BIGINT NULL,
dropped_connections BIGINT NULL,
net_send_bytes_per_sec DOUBLE NULL,
net_recv_bytes_per_sec DOUBLE NULL,
assigned_agents BIGINT UNSIGNED NULL,
reporting_agents BIGINT UNSIGNED NULL,
reporting_coverage DOUBLE NULL,
history_batch_token VARBINARY(32) NULL
```

Preserve existing task history columns that are still semantically valid. Add an idempotent startup migration that queries `information_schema.columns` and issues only the required `ALTER TABLE` additions for existing databases. Add a logical unique key `(task_id, history_batch_token)` without a foreign key. Compute `history_batch_token` as SHA-256 over sorted `(agentID, firstSequence, lastSequence)` tuples, so a retry of the same drained batch upserts the same row even when Agent window bounds differ.

Implement `MetricsWindowStore.PeekHistory(taskID)` and `AckHistory(taskID, throughToken)` so a database failure leaves windows pending. On each sampler tick, group all pending windows by Agent; for each Agent, sum counts and durations and calculate `agentRate = totalCount / totalDuration`; then sum Agent rates and merge all sketches/raw Apdex counts into one cluster row. Set row `window_from` to the minimum included start, `window_to` to the maximum included end, write transactionally, then acknowledge only after commit. If there are no accepted windows, write no metric row; the read API returns null/no data rather than a fabricated zero point. Missing pressure measurements and coverage in old rows become SQL `NULL`, not zero.

- [ ] **Step 4: Update history read DTOs**

Expose nullable values with pointers:

```go
type TaskTimeseriesPoint struct {
	Timestamp       time.Time `json:"timestamp"`
	QPS             float64   `json:"qps"`
	P50Ms           *float64  `json:"p50Ms"`
	P90Ms           *float64  `json:"p90Ms"`
	P95Ms           *float64  `json:"p95Ms"`
	P99Ms           *float64  `json:"p99Ms"`
	ActiveConnections *uint64 `json:"activeConnections"`
	NetSendBytesPerSec *float64 `json:"netSendBytesPerSec"`
	NetRecvBytesPerSec *float64 `json:"netRecvBytesPerSec"`
	AssignedAgents      *uint64  `json:"assignedAgents"`
	ReportingAgents     *uint64  `json:"reportingAgents"`
	ReportingCoverage   *float64 `json:"reportingCoverage"`
}
```

- [ ] **Step 5: Run history tests**

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./admin -run 'TestSampler|TestHistory' -count=1`

Expected: PASS. After explicit commit authorization: commit Task 9 files with message `fix: persist monitoring history from accepted windows`.

### Task 10: Connection Lifecycle and Reset-Safe System Rates

**Files:**
- Modify: `monitor/collector.go`
- Modify: `monitor/snapshot.go`
- Modify: `network/connection.go`
- Modify: `robot/robot.go`
- Modify: `agent/sysmon.go`
- Create: `network/connection_lifecycle_test.go`
- Create: `agent/sysmon_test.go`
- Test: `monitor/collector_test.go`

- [ ] **Step 1: Write failing lifecycle tests**

Cover clean close, disconnect, duplicate callbacks, and callback registration after the event occurred:

```go
func TestConnectionLifecycleDeliversLateCallbackOnce(t *testing.T) {
	c := newTestConnection()
	c.publishClosed(nil)
	var calls atomic.Uint64
	c.SetOnClosed(func(error) { calls.Add(1) })
	c.SetOnClosed(func(error) { calls.Add(1) })
	require.Equal(t, uint64(1), calls.Load())
}

func TestCollectorConnectionCounters(t *testing.T) {
	c := newTestCollector(t, 100*time.Millisecond)
	c.ConnectionOpened()
	c.ConnectionClosed()
	c.ConnectionOpened()
	c.ConnectionDropped()
	s := c.Snapshot().Summary.Connections
	require.Equal(t, uint64(0), s.Active)
	require.Equal(t, uint64(2), s.Closed)
	require.Equal(t, uint64(1), s.Dropped)
}
```

Also test a counter reset where current OS network bytes are lower than the previous sample; the emitted rates must be unavailable rather than underflowing to a huge value.

- [ ] **Step 2: Verify the tests fail**

Run: `$env:GOCACHE="$PWD\.gocache"; go test -race ./network ./agent ./monitor -run 'TestConnectionLifecycle|TestCollectorConnection|TestSystemNetworkCounterReset' -count=1`

Expected: FAIL because callbacks registered after close are lost, all closes are counted as drops, and unsigned subtraction underflows.

- [ ] **Step 3: Implement once-only lifecycle delivery**

Under the existing connection mutex, store event state and delivery flags:

```go
type lifecycleEvent struct {
	occurred bool
	err      error
	delivered bool
}
```

`publishClosed` and `publishDisconnected` capture a callback and mark it delivered while locked, then invoke outside the lock. Setters install the callback and, if the event already occurred but was not delivered, perform the same capture-and-mark operation. Replacing a setter after delivery must not replay the event.

Robot accounting rules:

- Successful connection increments `Established` and `Active`; connection setup failure increments `Failed` only.
- Every terminal close invokes `onClosed`, decrements `Active`, and increments `Closed`.
- Unexpected disconnect additionally invokes `onDisconnect` and increments `Dropped`; therefore `Dropped` is a subset of `Closed` and does not decrement `Active` a second time.
- A single connection instance can produce only one terminal transition.

- [ ] **Step 4: Implement reset-safe system rates**

Change network rate fields to nullable pointers. If there is no previous sample, elapsed time is non-positive, or a cumulative OS byte counter decreases, return `nil` for that rate and replace the baseline. Otherwise calculate `(current-previous)/seconds` with float64 after the monotonicity check.

```go
func counterRate(current, previous uint64, elapsed time.Duration) *float64 {
	if elapsed <= 0 || current < previous { return nil }
	v := float64(current-previous) / elapsed.Seconds()
	return &v
}
```

- [ ] **Step 5: Run lifecycle and system tests**

Run: `$env:GOCACHE="$PWD\.gocache"; go test -race ./network ./robot ./agent ./monitor -run 'TestConnection|TestSystemNetwork' -count=1`

Expected: PASS. After explicit commit authorization: commit Task 10 files with message `fix: report connection and system counters accurately`.

### Task 11: Frontend Live Metrics Consume Server Windows

**Files:**
- Modify: `cmd/web/src/types/api.ts`
- Modify: `cmd/web/src/services/metricsApi.ts`
- Modify: `cmd/web/src/services/metricsBinding.ts`
- Modify: `cmd/web/src/components/monitoring/shared/liveMetrics.ts`
- Modify: `cmd/web/src/components/monitoring/shared/liveMetrics.test.ts`
- Modify: `cmd/web/src/components/monitoring/shared/ActionMetricsTable.tsx`
- Modify: `cmd/web/src/components/monitoring/shared/ActionMetricsTable.test.ts`
- Modify: `cmd/web/src/components/monitoring/MonitorDock.tsx`
- Modify: `cmd/web/src/services/__tests__/metricsBinding.test.ts`

- [ ] **Step 1: Update tests first for the new DTO contract**

Add a fixture with cumulative totals that differ from its window values. Assert current QPS, current bandwidth, current success rate, and per-action QPS come only from `window`; assert all-time average QPS comes only from cumulative `summary.avgQps`.

```ts
it('uses backend windows without browser differencing', () => {
  const model = buildLiveMetrics(fixture({
    summary: { started: 10_000, avgQps: 50 },
    window: { summary: { started: 200, qps: 40, successRate: 0.97 } },
  }));
  expect(model.currentQps).toBe(40);
  expect(model.averageQps).toBe(50);
  expect(model.successRate).toBe(0.97);
});
```

Update table tests so `timingDetail='rtt'` shows only RTT-level diagnostic columns, `codec` shows codec columns, and `full` shows all observed columns without a toggle. Assert stage coverage uses `stage.count / action.byteSampleCount` and renders `—` when the stage count is zero, the executed-action denominator is zero, or the value is null.

- [ ] **Step 2: Verify frontend tests fail**

Run: `Set-Location cmd/web; npm run test -- --run src/components/monitoring/shared/liveMetrics.test.ts src/components/monitoring/shared/ActionMetricsTable.test.ts src/services/__tests__/metricsBinding.test.ts`

Expected: FAIL because current code derives interval QPS from browser history and owns `advancedDiagnostics` state.

- [ ] **Step 3: Align TypeScript types exactly**

Represent internal sketch bytes nowhere in frontend public types. Add cumulative/window types, exact counts, observed stage counts, connection lifecycle counters, fresh/stale Agent counts, nullable rates and latency fields, and rename `clientAvgMs` to `nonRTTAvgMs`.

```ts
export interface MetricWindow {
  sequence?: number;
  startedAt: string;
  endedAt: string;
  durationSeconds: number;
  expectedIntervalSeconds: number;
  summary: WindowSummary;
  actions: ActionMetric[];
  invalidMetricSamples: number;
}

export interface StageMetric {
  count: number;
  avgMs: number | null;
  p50Ms: number | null;
  p90Ms: number | null;
  p95Ms: number | null;
  p99Ms: number | null;
  maxMs: number | null;
}
```

Empty API values use `window: null`, nullable latency/rate fields, and zero exact counters. Keep `timingDetail: 'rtt'` as the default instrumentation level; it is not an Apdex mode.

- [ ] **Step 4: Remove frontend calculations and the advanced toggle**

Delete `deriveIntervalQps`, browser timestamp-based stale detection, history-count subtraction, `advancedDiagnostics`, `setAdvancedDiagnostics`, and its `Switch`. Join cumulative actions with `window.actions` by action name; use zero current counts when a cumulative action has no current window entry. Display diagnostic columns solely from backend `timingDetail` and each stage's observed count.

No component may calculate P50/P90/P95/P99, Apdex, or stale state. Render missing/null as `—`, never `0 ms`.

- [ ] **Step 5: Run focused frontend tests and TypeScript**

Run:

```powershell
Set-Location cmd/web
npm run test -- --run src/components/monitoring/shared/liveMetrics.test.ts src/components/monitoring/shared/ActionMetricsTable.test.ts src/services/__tests__/metricsBinding.test.ts
npx tsc -b
```

Expected: all selected Vitest files PASS and TypeScript exits 0. After explicit commit authorization: commit Task 11 files with message `refactor: bind live monitoring to backend windows`.

### Task 12: History UI, Benchmarks, Documentation, and End-to-End Verification

**Files:**
- Modify: `cmd/web/src/components/modules/history/HistoryDetailView.tsx`
- Modify: `cmd/web/src/components/modules/history/HistoryCompareView.tsx`
- Modify: `cmd/web/src/components/modules/history/report/ReportHtml.tsx`
- Modify: `cmd/web/src/services/historyApi.ts`
- Create: `cmd/web/src/components/modules/history/HistoryDetailView.test.tsx`
- Create: `monitor/benchmark_test.go`
- Modify: `AGENTS.md`

- [ ] **Step 1: Write failing history presentation tests**

Use three interval points with QPS `10, 80, 30`, nullable pressure data, canceled actions, and one error-code distribution. Assert peak QPS is 80, canceled is not added to error totals, the error-code distribution is counted once, and null pressure/latency renders `—`.

```ts
it('uses interval peaks and does not count cancellations as errors', () => {
  render(<HistoryDetailView detail={historyFixture({
    points: [{ qps: 10 }, { qps: 80 }, { qps: 30 }],
    summary: { failureCount: 4, timeoutCount: 2, canceledCount: 7 },
  })} />);
  expect(screen.getByText('80.00')).toBeInTheDocument();
  expect(screen.getByText('6')).toBeInTheDocument();
  expect(screen.queryByText('13')).not.toBeInTheDocument();
});
```

- [ ] **Step 2: Verify the history test fails**

Run: `Set-Location cmd/web; npm run test -- --run src/components/modules/history/HistoryDetailView.test.tsx`

Expected: FAIL because current history peak/error calculations use cumulative averages and include cancellation.

- [ ] **Step 3: Correct history and report rendering**

Bind charts and reports to server-persisted interval QPS/bandwidth/percentiles. Compute display-only peak as `Math.max(...points.map(p => p.qps))`. Total errors are `failureCount + timeoutCount`; do not add canceled and do not add per-code counts a second time. Compare views use the same nullable handling and unit labels.

- [ ] **Step 4: Add hot-path benchmarks**

Benchmark one action and 1,000 action names at 1, 8, and 32 writer goroutines. Warm the name maps before `ResetTimer`; use `ReportAllocs` and assert through benchmark review that the steady single-action record path is zero allocations and contention remains bounded.

```go
func BenchmarkRecordActionDDSketch(b *testing.B) {
	for _, writers := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("writers_%d", writers), func(b *testing.B) {
			c := newBenchmarkCollector()
			recordSuccess(c, "login", time.Millisecond)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() { recordSuccess(c, "login", time.Millisecond) }
			})
		})
	}
}
```

Run: `$env:GOCACHE="$PWD\.gocache"; go test ./monitor -run '^$' -bench 'BenchmarkRecordActionDDSketch' -benchmem -count=3`

Expected: steady one-action record path reports `0 allocs/op`; retain the three-run output in the task handoff for comparison.

- [ ] **Step 5: Update repository monitoring documentation**

In `AGENTS.md`, replace the fixed 16-bucket description with DDSketch 1% relative accuracy, eight shards, active/committed windows, Agent sequence retry, Admin deduplication, freshness policy, and the cumulative/live separation. Document that `timingDetail: "rtt"` remains valid only as a diagnostic instrumentation level and does not affect Apdex.

- [ ] **Step 6: Run complete automated verification**

Run from the repository root:

```powershell
$env:GOCACHE="$PWD\.gocache"
go test -race ./monitor ./agent ./admin ./network ./robot ./engine -count=1
go test ./... -count=1
go build ./...
Set-Location cmd/web
npx tsc -b
npm run test -- --run
npm run build
```

Expected: every command exits 0; Vitest has no new snapshot/type failures; Vite production build completes.

- [ ] **Step 7: Validate flow configuration in the application**

Start Admin and frontend development servers, open the existing `conf/flow/flow.json` in FlowEditor, and confirm the validation report contains zero errors. This is a regression check only; no flow or Lua business logic changes are part of this plan.

- [ ] **Step 8: Run the required 2–5 minute backend smoke test**

Use PowerShell-native log cleanup, then run Agent with repository-local cache:

```powershell
Remove-Item -LiteralPath log/stressbot.log -ErrorAction SilentlyContinue
$env:GOCACHE="$PWD\.gocache"
go run ./cmd/agent -config conf/config.json
```

Let the configured scenario run for 2–5 minutes, stop it cleanly, then review:

```powershell
Select-String -Path log/stressbot.log -Pattern 'error|warn|失败' -CaseSensitive:$false | Where-Object { $_.Line -notmatch 'headError' }
```

Expected: no unexplained error/warn/failure lines. Query the monitoring endpoint during the run and confirm `P50 <= P90 <= P95 <= P99 <= Max`, non-empty internal reports merge across at least two windows, public JSON contains no `sketch`, and current QPS drops to zero/null after the freshness threshold without erasing cumulative totals.

- [ ] **Step 9: Run the 8000-robot profile comparison**

Before the final checkpoint, run one production-scale 8000-robot task with the same flow/proto/scripts/adapter and server target used for the last accepted performance baseline. Keep `monitor.timingDetail="rtt"`, `monitor.http.port=6061`, and `pprof.port=6060`; change only task bot count/concurrency through the existing Admin task form. During the steady phase, capture:

```powershell
$env:GOCACHE="$PWD\.gocache"
go tool pprof -top -seconds 60 http://127.0.0.1:6060/debug/pprof/profile
go tool pprof -top -seconds 60 http://127.0.0.1:6060/debug/pprof/mutex
go tool pprof -top http://127.0.0.1:6060/debug/pprof/heap
Invoke-RestMethod http://127.0.0.1:6061/metrics
```

Expected: no monitor/DDSketch function becomes the dominant CPU or mutex hotspot; DDSketch stores stay within 2048 bins per distribution; live memory remains bounded across at least six report rotations; percentile order and public-sketch invariants hold. Record CPU top entries, monitor-related mutex entries, live heap, and the corresponding pre-change baseline values in the implementation handoff. Accuracy is the priority, but any dominant monitor hotspot blocks completion and must be profiled before changing accuracy settings.

- [ ] **Step 10: Record the final checkpoint**

Review `git diff --check` and `git status --short`, list all pre-existing unrelated worktree changes separately, and do not stage them. After explicit commit authorization, commit only this feature's files with message `feat: make monitoring metrics accuracy-first`.

## Completion Criteria

- The old 16-bucket implementation and every bucket-based merge field are absent.
- No `metricsVersion`, percentile fallback, frontend percentile calculation, browser interval differencing, or browser stale timer remains.
- Every non-empty latency distribution on the Agent/Admin wire contains a valid DDSketch payload; public and history responses contain no payload bytes.
- Cumulative totals, report windows, retries, duplicates, stale Agents, and history rows have tests proving no loss or double count.
- `timingDetail: "rtt"` still works and only controls which diagnostic stages are measured and shown.
- Connection active/closed/dropped counts and reset-safe nullable system rates are covered by tests.
- Backend race tests, all Go tests/build, TypeScript, Vitest, frontend build, flow validation, and the 2–5 minute runtime/log review pass.
