# Configuration Backup and Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add selectable configuration backup and restore from the existing File menu, with full replacement, Windows-style merge conflict handling, transactional server flow snapshots, and recoverable browser-side compensation.

**Architecture:** The browser owns the versioned JSON file, section selection, duplicate planning, local IndexedDB writes, and recovery journal. The Admin server exposes a revisioned flow-template snapshot API and replaces the MySQL flow table in one transaction. Nine section adapters isolate flow, draft, resource, template, and notepad storage details from the UI and coordinator.

**Tech Stack:** Go 1.x, `net/http`, `database/sql`, MySQL, React 18, TypeScript 5.6, Ant Design 5, Zustand, `idb-keyval`, Vitest, Testing Library.

**Design Spec:** `docs/superpowers/specs/2026-07-23-config-backup-restore-design.md`

**Git rule:** Before every commit step, show the complete staged file list and cached diff summary and obtain user confirmation, as required by `.agents/skills/git-management/SKILL.md`. Never stage the unrelated `agent*.png` or `agent*.puml` files.

---

## File Structure

### Backend

- Create `admin/flow_snapshot.go`: snapshot types, deterministic revision, validation, transactional replacement, and HTTP handlers.
- Create `admin/flow_snapshot_test.go`: pure revision/validation tests plus SQL transaction tests.
- Create `admin/flow_snapshot_handler_test.go`: disabled-library, body-limit, revision-conflict, and capabilities tests.
- Modify `admin/flow_template.go`: add the shared read/write lock and route existing CRUD through locked private helpers.
- Modify `admin/handlers.go`: register snapshot routes and expose `flowLibrary` capability.
- Modify `admin/errors.go`: add `FLOW_SNAPSHOT_CONFLICT`.
- Modify `go.mod` and `go.sum`: add `github.com/DATA-DOG/go-sqlmock` as a test-only dependency.

### Frontend Core

- Create `cmd/web/src/services/configTransfer/types.ts`: stable backup, manifest, section, conflict, plan, and result contracts.
- Create `cmd/web/src/services/configTransfer/backupCodec.ts`: strict parse, validation, serialization, and file-size guard.
- Create `cmd/web/src/services/configTransfer/restorePlanner.ts`: duplicate detection, full/merge plan generation, copy naming, and conflict resolution.
- Create `cmd/web/src/services/configTransfer/sectionRegistry.ts`: nine section adapters and refresh hooks.
- Create `cmd/web/src/services/configTransfer/recoveryJournal.ts`: persistent `stressbot-config-recovery` journal.
- Create `cmd/web/src/services/configTransfer/restoreCoordinator.ts`: preflight, execution, reverse rollback, and interrupted-session recovery.
- Create `cmd/web/src/services/configTransfer/backupCodec.test.ts`.
- Create `cmd/web/src/services/configTransfer/restorePlanner.test.ts`.
- Create `cmd/web/src/services/configTransfer/sectionRegistry.test.ts`.
- Create `cmd/web/src/services/configTransfer/restoreCoordinator.test.ts`.

### Existing Frontend Stores and APIs

- Modify `cmd/web/src/services/resourcesStore.ts`: raw snapshot replacement APIs preserving `ResourceFile` metadata.
- Modify `cmd/web/src/components/FlowEditor/library/templateStore.ts`: exact batch replacement APIs with one change event.
- Modify `cmd/web/src/components/modules/notepad/notepadStore.ts`: flush-all, export-all, and exact replacement APIs.
- Modify `cmd/web/src/components/FlowEditor/store/persistDraft.ts`: public draft snapshot capture/read/write APIs.
- Modify `cmd/web/src/services/flowsApi.ts`: flow snapshot GET/PUT types and methods.
- Modify `cmd/web/src/services/capabilitiesApi.ts`: add `flowLibrary`.
- Create `cmd/web/src/services/errorMapValidation.ts`: reusable `errors.json` parse and reserved-range validation.
- Modify `cmd/web/src/components/modules/ErrorMapEditor.tsx`: consume and re-export the service-layer validation helpers.
- Create `cmd/web/src/services/__tests__/configTransferStorage.test.ts`.
- Modify `cmd/web/src/services/index.ts`: export the new public service APIs.

### Frontend UI

- Create `cmd/web/src/components/modules/configTransfer/ConfigBackupModal.tsx`.
- Create `cmd/web/src/components/modules/configTransfer/ConfigRestoreModal.tsx`.
- Create `cmd/web/src/components/modules/configTransfer/ConflictResolutionView.tsx`.
- Create `cmd/web/src/components/modules/configTransfer/RestoreResult.tsx`.
- Create `cmd/web/src/components/modules/configTransfer/RecoveryGuard.tsx`.
- Create `cmd/web/src/components/modules/configTransfer/ConfigBackupModal.test.tsx`.
- Create `cmd/web/src/components/modules/configTransfer/ConfigRestoreModal.test.tsx`.
- Create `cmd/web/src/components/modules/configTransfer/ConflictResolutionView.test.tsx`.
- Create `cmd/web/src/components/modules/configTransfer/RecoveryGuard.test.tsx`.
- Modify `cmd/web/src/components/FlowEditor/panels/Toolbar.tsx`: add File-menu launchers and lazy modal host.
- Modify `cmd/web/src/pages/EditorPage.tsx`: mount interrupted-recovery guard.

---

### Task 1: Define and Test the Backend Flow Snapshot Contract

**Files:**
- Create: `admin/flow_snapshot.go`
- Create: `admin/flow_snapshot_test.go`

- [ ] **Step 1: Write failing revision and validation tests**

Add tests that prove ordering does not change the revision and duplicate/invalid IDs are rejected:

```go
func TestComputeFlowSnapshotRevisionIsOrderIndependent(t *testing.T) {
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	a := FlowTemplateDetail{
		FlowTemplateSummary: FlowTemplateSummary{ID: "a", Name: "A", CreatedAt: now, UpdatedAt: now},
		Flow: json.RawMessage(`{"defaultDelayMs":1000,"nodes":{},"actions":{},"listens":{}}`),
	}
	b := FlowTemplateDetail{
		FlowTemplateSummary: FlowTemplateSummary{ID: "b", Name: "B", CreatedAt: now, UpdatedAt: now},
		Flow: json.RawMessage(`{"defaultDelayMs":1000,"nodes":{},"actions":{},"listens":{}}`),
	}

	r1, err := computeFlowSnapshotRevision([]FlowTemplateDetail{a, b})
	if err != nil { t.Fatal(err) }
	r2, err := computeFlowSnapshotRevision([]FlowTemplateDetail{b, a})
	if err != nil { t.Fatal(err) }
	if r1 != r2 { t.Fatalf("revision differs: %q != %q", r1, r2) }
}

func TestValidateFlowSnapshotItemsRejectsDuplicateID(t *testing.T) {
	items := []FlowTemplateDetail{
		{FlowTemplateSummary: FlowTemplateSummary{ID: "same", Name: "A"}, Flow: json.RawMessage(`{"nodes":{},"actions":{}}`)},
		{FlowTemplateSummary: FlowTemplateSummary{ID: "same", Name: "B"}, Flow: json.RawMessage(`{"nodes":{},"actions":{}}`)},
	}
	if err := validateFlowSnapshotItems(items); err == nil || !strings.Contains(err.Error(), "流程 ID 重复") {
		t.Fatalf("error = %v", err)
	}
}
```

- [ ] **Step 2: Run the focused test and verify the red state**

Run: `go test ./admin -run 'TestComputeFlowSnapshotRevision|TestValidateFlowSnapshotItems' -count=1`

Expected: FAIL because `FlowSnapshot` types and helper functions do not exist.

- [ ] **Step 3: Implement snapshot types, canonical revision, and validation**

Create `admin/flow_snapshot.go` with these contracts and helpers:

```go
package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	json "stressbot/utils/jsonx"
)

type FlowSnapshot struct {
	Revision string               `json:"revision"`
	Items    []FlowTemplateDetail `json:"items"`
}

type ReplaceFlowSnapshotRequest struct {
	ExpectedRevision string               `json:"expectedRevision"`
	Items            []FlowTemplateDetail `json:"items"`
}

type ReplaceFlowSnapshotResponse struct {
	Revision string `json:"revision"`
	Count    int    `json:"count"`
}

func computeFlowSnapshotRevision(items []FlowTemplateDetail) (string, error) {
	stable := append([]FlowTemplateDetail(nil), items...)
	sort.Slice(stable, func(i, j int) bool { return stable[i].ID < stable[j].ID })
	b, err := json.Marshal(stable)
	if err != nil { return "", fmt.Errorf("marshal flow snapshot: %w", err) }
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validateFlowSnapshotItems(items []FlowTemplateDetail) error {
	seen := make(map[string]struct{}, len(items))
	for i := range items {
		item := &items[i]
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" || len(item.ID) > 32 {
			return ErrInvalidArgument.WithMessage(fmt.Sprintf("第 %d 个流程 ID 无效", i+1))
		}
		if _, ok := seen[item.ID]; ok {
			return ErrInvalidArgument.WithMessage(fmt.Sprintf("流程 ID 重复：%s", item.ID))
		}
		seen[item.ID] = struct{}{}
		name, err := validateFlowTemplateName(item.Name)
		if err != nil { return err }
		item.Name = name
		nodes, actions, err := countFlowNodesActions(item.Flow)
		if err != nil { return err }
		item.NodeCount, item.ActionCount = nodes, actions
		if len(item.Layout) > 0 && !json.Valid(item.Layout) {
			return ErrInvalidArgument.WithMessage(fmt.Sprintf("流程 %s 的 layout 不是合法 JSON", item.Name))
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the focused tests and verify green**

Run: `go test ./admin -run 'TestComputeFlowSnapshotRevision|TestValidateFlowSnapshotItems' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit the contract after user approval**

Stage only `admin/flow_snapshot.go` and `admin/flow_snapshot_test.go`, show the staged list/diff, then after approval run:

```bash
git commit -m "feat(admin): define flow snapshot contract"
```

---

### Task 2: Add Transactional Flow Snapshot Storage and HTTP Endpoints

**Files:**
- Modify: `admin/flow_template.go`
- Modify: `admin/flow_snapshot.go`
- Modify: `admin/handlers.go`
- Modify: `admin/errors.go`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `cmd/web/src/services/capabilitiesApi.ts`
- Create: `admin/flow_snapshot_handler_test.go`
- Modify: `admin/flow_snapshot_test.go`

- [ ] **Step 1: Add the isolated SQL test dependency**

Run: `go get github.com/DATA-DOG/go-sqlmock@v1.5.2`

Expected: `go.mod` and `go.sum` add only the test-support module; production imports remain unchanged.

- [ ] **Step 2: Write failing transaction, conflict, route, and capability tests**

Use `sqlmock.New()` to set exact expectations. The central transaction test should follow this shape:

```go
func TestReplaceSnapshotUsesOneTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = db.Close() })
	store := NewFlowTemplateStore(db)
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	currentRows := sqlmock.NewRows([]string{"id", "name", "node_count", "action_count", "created_at", "updated_at", "flow_json", "layout_json"}).
		AddRow("old", "Old", 0, 0, now, now, []byte(`{"nodes":{},"actions":{}}`), nil)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, name, node_count").WillReturnRows(currentRows)
	mock.ExpectExec("DELETE FROM flow_template").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO flow_template").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	before, _ := computeFlowSnapshotRevision([]FlowTemplateDetail{{
		FlowTemplateSummary: FlowTemplateSummary{ID: "old", Name: "Old", CreatedAt: now, UpdatedAt: now},
		Flow: json.RawMessage(`{"nodes":{},"actions":{}}`),
	}})
	resp, err := store.ReplaceSnapshot(context.Background(), ReplaceFlowSnapshotRequest{
		ExpectedRevision: before,
		Items: []FlowTemplateDetail{{
			FlowTemplateSummary: FlowTemplateSummary{ID: "new", Name: "New", CreatedAt: now, UpdatedAt: now},
			Flow: json.RawMessage(`{"nodes":{},"actions":{}}`),
		}},
	})
	if err != nil { t.Fatal(err) }
	if resp.Count != 1 { t.Fatalf("count = %d", resp.Count) }
	if err := mock.ExpectationsWereMet(); err != nil { t.Fatal(err) }
}
```

Also add handler tests asserting:

```go
func TestCapabilitiesIncludesFlowLibrary(t *testing.T) {
	s := &AdminServer{flows: &FlowTemplateStore{}}
	rr := httptest.NewRecorder()
	s.handleCapabilities(rr, httptest.NewRequest(http.MethodGet, "/sbot/capabilities", nil))
	if !strings.Contains(rr.Body.String(), `"flowLibrary":true`) { t.Fatal(rr.Body.String()) }
}

func TestFlowSnapshotDisabled(t *testing.T) {
	s := &AdminServer{}
	rr := httptest.NewRecorder()
	s.handleGetFlowSnapshot(rr, httptest.NewRequest(http.MethodGet, "/sbot/flows/snapshot", nil))
	if rr.Code != http.StatusServiceUnavailable { t.Fatalf("status = %d", rr.Code) }
}
```

- [ ] **Step 3: Run the backend snapshot tests and verify failure**

Run: `go test ./admin -run 'TestReplaceSnapshot|TestCapabilitiesIncludesFlowLibrary|TestFlowSnapshotDisabled' -count=1`

Expected: FAIL because store methods, handlers, route capability, and error code are missing.

- [ ] **Step 4: Implement store locking and snapshot reads/replacement**

Change `FlowTemplateStore` to own `sync.RWMutex`, add private query helpers that accept `*sql.DB` or `*sql.Tx`, and keep public methods locked. The replacement method must have this control flow:

```go
func (s *FlowTemplateStore) ReplaceSnapshot(ctx context.Context, req ReplaceFlowSnapshotRequest) (*ReplaceFlowSnapshotResponse, error) {
	items := append([]FlowTemplateDetail(nil), req.Items...)
	if err := validateFlowSnapshotItems(items); err != nil { return nil, err }

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return nil, fmt.Errorf("begin replace flow snapshot: %w", err) }
	defer tx.Rollback()

	current, err := listFlowDetails(ctx, tx)
	if err != nil { return nil, err }
	currentRevision, err := computeFlowSnapshotRevision(current)
	if err != nil { return nil, err }
	if req.ExpectedRevision != currentRevision {
		return nil, ErrFlowSnapshotConflict.WithDetails(map[string]any{
			"expectedRevision": req.ExpectedRevision,
			"actualRevision": currentRevision,
		})
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM flow_template`); err != nil {
		return nil, fmt.Errorf("delete flow snapshot: %w", err)
	}
	for i := range items {
		it := &items[i]
		if _, err := tx.ExecContext(ctx, `INSERT INTO flow_template
			(id,name,flow_json,layout_json,node_count,action_count,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?)`, it.ID, it.Name, []byte(it.Flow), layoutArg(it.Layout),
			it.NodeCount, it.ActionCount, it.CreatedAt, it.UpdatedAt); err != nil {
			return nil, fmt.Errorf("insert flow snapshot: %w", err)
		}
	}
	if err := tx.Commit(); err != nil { return nil, fmt.Errorf("commit flow snapshot: %w", err) }
	revision, err := computeFlowSnapshotRevision(items)
	if err != nil { return nil, err }
	return &ReplaceFlowSnapshotResponse{Revision: revision, Count: len(items)}, nil
}
```

Ensure `Create`, `Update`, and `Delete` take the write lock; `List`, `Get`, and `Snapshot` take the read lock. Avoid recursive locking by having `Update` call a private unlocked `getFlowDetail` helper.

- [ ] **Step 5: Implement handlers, body limit, routes, error, and capability**

Add:

```go
var ErrFlowSnapshotConflict = NewError("FLOW_SNAPSHOT_CONFLICT", http.StatusConflict)

type CapabilitiesResponse struct {
	SharedState bool   `json:"sharedState"`
	SharedAddr  string `json:"sharedAddr,omitempty"`
	FlowLibrary bool   `json:"flowLibrary"`
}
```

Set `FlowLibrary: s.flows != nil`, register literal routes before the parameterized flow routes, and decode the PUT body through:

```go
r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
dec := json.NewDecoder(r.Body)
if err := dec.Decode(&req); err != nil {
	writeError(w, ErrInvalidArgument.WithMessage("备份中的流程快照不是合法 JSON"))
	return
}
```

Update the frontend capability interface simultaneously:

```ts
export interface CapabilitiesResponse {
  sharedState: boolean;
  sharedAddr?: string;
  flowLibrary: boolean;
}
```

- [ ] **Step 6: Run backend tests and compilation**

Run: `gofmt -w admin/flow_snapshot.go admin/flow_snapshot_test.go admin/flow_snapshot_handler_test.go admin/flow_template.go admin/handlers.go admin/errors.go`

Run: `go test ./admin -run 'FlowSnapshot|CapabilitiesIncludesFlowLibrary' -count=1`

Run: `go test ./admin -count=1`

Expected: all PASS.

- [ ] **Step 7: Commit backend snapshot support after user approval**

Stage the backend files, `go.mod`, `go.sum`, and `capabilitiesApi.ts`; show the full staged list/diff, then after approval run:

```bash
git commit -m "feat(admin): add transactional flow snapshots"
```

---

### Task 3: Define and Strictly Parse the Frontend Backup Format

**Files:**
- Create: `cmd/web/src/services/configTransfer/types.ts`
- Create: `cmd/web/src/services/configTransfer/backupCodec.ts`
- Create: `cmd/web/src/services/configTransfer/backupCodec.test.ts`

- [ ] **Step 1: Write failing format tests**

Cover valid partial sections, explicit empty sections, omitted sections, bad kind, higher version, count mismatch, unknown section, and 100 MiB guard:

```ts
it('distinguishes omitted sections from selected empty sections', () => {
  const bundle = parseBackupText(JSON.stringify({
    kind: 'stressbot-config-backup',
    schemaVersion: 1,
    exportedAt: '2026-07-23T10:00:00.000Z',
    manifest: { includedSections: ['protoFiles'], counts: { protoFiles: 0 } },
    data: { protoFiles: [] },
  }));
  expect(bundle.data.protoFiles).toEqual([]);
  expect('luaFiles' in bundle.data).toBe(false);
});

it('rejects a newer schema without writing anything', () => {
  expect(() => parseBackupText(JSON.stringify({
    kind: 'stressbot-config-backup', schemaVersion: 2, exportedAt: '',
    manifest: { includedSections: [], counts: {} }, data: {},
  }))).toThrow('备份格式版本 2 高于当前支持版本 1');
});
```

- [ ] **Step 2: Run the test and verify red**

Run: `cd cmd/web && npx.cmd vitest run src/services/configTransfer/backupCodec.test.ts`

Expected: FAIL because files do not exist.

- [ ] **Step 3: Implement stable contracts**

Define the exact section keys and core types:

```ts
export const BACKUP_KIND = 'stressbot-config-backup' as const;
export const BACKUP_SCHEMA_VERSION = 1 as const;
export const MAX_BACKUP_BYTES = 100 * 1024 * 1024;

export const BACKUP_SECTIONS = [
  'flows', 'draft', 'protoFiles', 'luaFiles', 'codecFiles', 'errorMap',
  'actionTemplates', 'listenTemplates', 'notepadFiles',
] as const;
export type BackupSection = (typeof BACKUP_SECTIONS)[number];

export interface BackupManifest {
  includedSections: BackupSection[];
  counts: Partial<Record<BackupSection, number>>;
}

export interface ConfigBackupBundle {
  kind: typeof BACKUP_KIND;
  schemaVersion: typeof BACKUP_SCHEMA_VERSION;
  exportedAt: string;
  manifest: BackupManifest;
  data: Partial<Record<BackupSection, unknown>>;
}
```

Also define `RestoreMode`, `ConflictChoice`, `SectionStats`, `RestoreConflict`, `RestorePlan`, and `RestoreResult` in this file so later tasks import one source of truth.

- [ ] **Step 4: Implement strict top-level parsing and serialization**

The parser must verify records, exact section membership, one-to-one manifest/data keys, non-negative integer counts, ISO export time, and section counts. Use an injected `validateSection` callback so nested module validation stays in section adapters:

```ts
export function parseBackupText(
  text: string,
  validateSection: SectionValidator = defaultSectionValidator,
): ConfigBackupBundle {
  const raw: unknown = JSON.parse(text);
  assertRecord(raw, '备份文件');
  if (raw.kind !== BACKUP_KIND) throw new Error('不是 stressbot 配置备份文件');
  if (raw.schemaVersion !== BACKUP_SCHEMA_VERSION) {
    throw new Error(`备份格式版本 ${String(raw.schemaVersion)} 高于当前支持版本 ${BACKUP_SCHEMA_VERSION}`);
  }
  assertRecord(raw.manifest, 'manifest');
  assertRecord(raw.data, 'data');
  const included = assertSectionList(raw.manifest.includedSections);
  const dataKeys = Object.keys(raw.data);
  if (!sameStringSet(included, dataKeys)) throw new Error('manifest 与 data 分区不一致');
  for (const section of included) validateSection(section, raw.data[section]);
  assertManifestCounts(raw.manifest.counts, included, raw.data);
  return raw as unknown as ConfigBackupBundle;
}

export function assertBackupFileSize(file: Pick<File, 'size'>): void {
  if (file.size > MAX_BACKUP_BYTES) throw new Error('备份文件超过 100 MiB，无法导入');
}
```

- [ ] **Step 5: Run parser tests and type-check**

Run: `cd cmd/web && npx.cmd vitest run src/services/configTransfer/backupCodec.test.ts`

Run: `cd cmd/web && npx.cmd tsc -b`

Expected: PASS.

- [ ] **Step 6: Commit format support after user approval**

Stage only the three config-transfer format files, show the staged list/diff, then after approval run:

```bash
git commit -m "feat(web): define configuration backup format"
```

---

### Task 4: Build the Pure Restore Planner and Windows-Style Conflict Rules

**Files:**
- Create: `cmd/web/src/services/configTransfer/restorePlanner.ts`
- Create: `cmd/web/src/services/configTransfer/restorePlanner.test.ts`
- Modify: `cmd/web/src/services/configTransfer/types.ts`

- [ ] **Step 1: Write failing collection and singleton planning tests**

Tests must cover filename duplication, ID duplication, name duplication, ID/name ambiguity, overwrite-all, skip-all, keep-copy, complete replacement, and singleton restrictions:

```ts
it('reports ambiguity when id and name hit different targets', () => {
  const current = [item('id-1', 'A'), item('id-2', 'B')];
  const incoming = [item('id-1', 'B')];
  const plan = planCollectionMerge(current, incoming, flowIdentity, 'overwrite');
  expect(plan.conflicts[0]).toMatchObject({ kind: 'ambiguous', sourceId: 'id-1' });
  expect(plan.finalItems).toEqual(current);
});

it('full restore removes target-only items', () => {
  const plan = planCollectionReplace([item('old', 'Old')], [item('new', 'New')]);
  expect(plan.finalItems.map((v) => v.id)).toEqual(['new']);
  expect(plan.stats).toMatchObject({ added: 1, deleted: 1 });
});

it('keep-copy allocates a unique name before the extension', () => {
  expect(nextCopyName('login.lua', new Set(['login.lua', 'login（副本）.lua'])))
    .toBe('login（副本 2）.lua');
});
```

- [ ] **Step 2: Run planner tests and verify red**

Run: `cd cmd/web && npx.cmd vitest run src/services/configTransfer/restorePlanner.test.ts`

Expected: FAIL because planner functions do not exist.

- [ ] **Step 3: Implement identity-aware collection planning**

Use explicit identity callbacks rather than hard-coded module switches:

```ts
export interface CollectionIdentity<T> {
  id: (item: T) => string;
  name: (item: T) => string;
  clone: (item: T, nextId: string, nextName: string) => T;
  createId: () => string;
}

export function findDuplicate<T>(
  current: T[], incoming: T, identity: CollectionIdentity<T>,
): { kind: 'none' | 'one' | 'ambiguous'; matches: T[] } {
  const byID = current.find((item) => identity.id(item) === identity.id(incoming));
  const byName = current.find((item) => identity.name(item) === identity.name(incoming));
  const matches = [...new Set([byID, byName].filter((v): v is T => Boolean(v)))];
  return matches.length === 0
    ? { kind: 'none', matches }
    : matches.length === 1
      ? { kind: 'one', matches }
      : { kind: 'ambiguous', matches };
}
```

Implement `planCollectionReplace`, `planCollectionMerge`, `applyConflictChoices`, `planSingleton`, and `nextCopyName`. Ambiguous matches must always remain unresolved even when the default is overwrite-all.

- [ ] **Step 4: Implement deterministic statistics**

Return per-section values for `added`, `overwritten`, `deleted`, `skipped`, and `copied`. Sort conflicts by section then source name so UI and tests are stable.

- [ ] **Step 5: Run planner tests and type-check**

Run: `cd cmd/web && npx.cmd vitest run src/services/configTransfer/restorePlanner.test.ts`

Run: `cd cmd/web && npx.cmd tsc -b`

Expected: PASS.

- [ ] **Step 6: Commit planner support after user approval**

Stage the planner, planner test, and shared types; show the staged list/diff, then after approval run:

```bash
git commit -m "feat(web): plan configuration restore conflicts"
```

---

### Task 5: Add Exact Snapshot APIs to Local Stores

**Files:**
- Modify: `cmd/web/src/services/resourcesStore.ts`
- Modify: `cmd/web/src/components/FlowEditor/library/templateStore.ts`
- Modify: `cmd/web/src/components/modules/notepad/notepadStore.ts`
- Modify: `cmd/web/src/components/FlowEditor/store/persistDraft.ts`
- Create: `cmd/web/src/services/__tests__/configTransferStorage.test.ts`

- [ ] **Step 1: Write failing raw round-trip tests with the existing IDB mock pattern**

Use one in-memory `idb-keyval` mock and assert metadata/IDs survive exact replacement:

```ts
it('replaces Proto files without regenerating baseHash or uploadedAt', async () => {
  const file = {
    name: 'login.proto', content: 'syntax="proto3";', size: 16,
    uploadedAt: '2026-01-02T03:04:05Z', baseHash: 'sha256:original',
  };
  await replaceProtoFiles([file]);
  expect(await listProto()).toEqual([file]);
});

it('exports and replaces notepad files with stable ids and contents', async () => {
  await replaceNotepadFiles([{ id: 'n1', name: 'notes.md', language: 'markdown',
    content: '# Notes', createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-02T00:00:00Z' }]);
  expect(await exportNotepadFiles()).toEqual([expect.objectContaining({ id: 'n1', content: '# Notes' })]);
});
```

Also subscribe to the template event and assert one notification per batch replacement.

- [ ] **Step 2: Run the storage test and verify red**

Run: `cd cmd/web && npx.cmd vitest run src/services/__tests__/configTransferStorage.test.ts`

Expected: FAIL because replacement/export functions do not exist.

- [ ] **Step 3: Implement resource replacement helpers**

Add a shared exact-write helper and public section methods:

```ts
async function replaceResourceStore(
  store: ReturnType<typeof createStore>, files: ResourceFile[],
): Promise<void> {
  await clear(store);
  if (files.length > 0) await setMany(files.map((file) => [file.name, { ...file }]), store);
}

export async function replaceProtoFiles(files: ResourceFile[]): Promise<void> {
  await replaceResourceStore(protoStore, files);
  notify();
}

export async function replaceScriptFiles(files: ResourceFile[]): Promise<void> {
  await replaceResourceStore(scriptStore, files);
  notify();
}

export async function replaceCodecFiles(files: ResourceFile[]): Promise<void> {
  const errorMap = await getErrorMap();
  await replaceResourceStore(adapterStore, [...files, ...(errorMap ? [errorMap] : [])]);
  notify();
}

export async function replaceErrorMap(file: ResourceFile | null): Promise<void> {
  if (file) await set(ERRORS_JSON_KEY, { ...file }, adapterStore);
  else await del(ERRORS_JSON_KEY, adapterStore);
  notify();
}
```

Preserve the sibling adapter section when replacing codecs or the error map.

- [ ] **Step 4: Implement template and notepad exact replacement**

For templates, clear only the target template DB, write exact records, then emit once. For notepad, change pending saves to retain `{ timer, content }`, add `flushAllPendingSaves`, and write index plus `file:${id}` contents exactly:

```ts
export async function replaceNotepadFiles(files: NotepadFile[]): Promise<void> {
  await flushAllPendingSaves();
  const existing = await loadIndex();
  await Promise.all(existing.map((meta) => del(`file:${meta.id}`, notepadStore)));
  await Promise.all(files.map((file) => set(`file:${file.id}`, file.content, notepadStore)));
  await saveIndex(files.map(({ content: _content, ...meta }) => meta));
  useNotepadStore.setState({
    files: files.map(({ content: _content, ...meta }) => meta),
    activeFileId: null, activeContent: '', contentLoaded: false,
  });
}
```

- [ ] **Step 5: Expose exact draft snapshot operations**

Export the draft type and functions:

```ts
export interface DraftSnapshot {
  flow: FlowJson;
  layout: FlowLayout;
  savedAt: number;
}

export function captureCurrentDraft(): DraftSnapshot {
  const state = useFlowStore.getState();
  return { flow: state.toTaskFlow(), layout: structuredClone(state.layout), savedAt: Date.now() };
}

export function saveDraftSnapshot(snapshot: DraftSnapshot | null): void {
  if (!snapshot) { clearDraft(); return; }
  localStorage.setItem(KEY_FLOW, JSON.stringify({ ...snapshot.flow, savedAt: snapshot.savedAt }));
  localStorage.setItem(KEY_LAYOUT, JSON.stringify(snapshot.layout));
}
```

Keep `loadDraft()` as the persisted-reader API and verify its return type is `DraftSnapshot | null`.

- [ ] **Step 6: Run focused storage tests and type-check**

Run: `cd cmd/web && npx.cmd vitest run src/services/__tests__/configTransferStorage.test.ts src/services/__tests__/codecStorage.test.ts`

Run: `cd cmd/web && npx.cmd tsc -b`

Expected: PASS with existing resource behavior unchanged.

- [ ] **Step 7: Commit store snapshot APIs after user approval**

Stage only the four store modules and storage test; show the staged list/diff, then after approval run:

```bash
git commit -m "feat(web): add exact local configuration snapshots"
```

---

### Task 6: Implement Flow API and the Nine Section Adapters

**Files:**
- Modify: `cmd/web/src/services/flowsApi.ts`
- Create: `cmd/web/src/services/errorMapValidation.ts`
- Modify: `cmd/web/src/components/modules/ErrorMapEditor.tsx`
- Create: `cmd/web/src/services/configTransfer/sectionRegistry.ts`
- Create: `cmd/web/src/services/configTransfer/sectionRegistry.test.ts`
- Modify: `cmd/web/src/services/configTransfer/backupCodec.ts`
- Modify: `cmd/web/src/services/index.ts`

- [ ] **Step 1: Write failing adapter tests**

Mock all stores and flow APIs, then assert exact duplicate identities and validation:

```ts
it('uses id or exact name for saved-flow duplicates', () => {
  const adapter = createSectionRegistry().flows;
  const existing = flow('id-1', '登录流程');
  expect(adapter.identity?.id(existing)).toBe('id-1');
  expect(adapter.identity?.name(existing)).toBe('登录流程');
});

it('uses exact filename for resource duplicates', () => {
  const adapter = createSectionRegistry().protoFiles;
  expect(adapter.identity?.name(resource('Login.proto'))).toBe('Login.proto');
  expect(adapter.identity?.name(resource('login.proto'))).not.toBe('Login.proto');
});
```

Add one validation test per section family: flow/draft, `ResourceFile`, template, notepad, and singleton null.

- [ ] **Step 2: Run adapter tests and verify red**

Run: `cd cmd/web && npx.cmd vitest run src/services/configTransfer/sectionRegistry.test.ts`

Expected: FAIL because registry and flow snapshot APIs do not exist.

- [ ] **Step 3: Add flow snapshot APIs**

Extend `flowsApi.ts`:

```ts
export interface FlowSnapshot {
  revision: string;
  items: FlowTemplateDetail[];
}

export interface ReplaceFlowSnapshotRequest {
  expectedRevision: string;
  items: FlowTemplateDetail[];
}

export interface ReplaceFlowSnapshotResponse {
  revision: string;
  count: number;
}

export function getFlowSnapshot(): Promise<FlowSnapshot> {
  return getJson<FlowSnapshot>('/flows/snapshot');
}

export function replaceFlowSnapshot(req: ReplaceFlowSnapshotRequest): Promise<ReplaceFlowSnapshotResponse> {
  return putJson<ReplaceFlowSnapshotResponse>('/flows/snapshot', req);
}
```

- [ ] **Step 4: Implement adapter interfaces and registry**

Define:

```ts
export interface ConfigSectionAdapter<T> {
  key: BackupSection;
  label: string;
  kind: 'collection' | 'singleton';
  read: () => Promise<T>;
  replace: (value: T) => Promise<void>;
  validate: (value: unknown) => asserts value is T;
  count: (value: T) => number;
  identity?: T extends readonly (infer Item)[] ? CollectionIdentity<Item> : never;
  refresh?: (value: T) => Promise<void> | void;
}
```

Register all nine keys. Resource adapters preserve exact metadata; flow validation calls `validateFlow` for every item and rejects reports with errors; codec validation calls `validateCodecSchema`; template and notepad validators check stable IDs, names, timestamps, and shapes.

Move the pure `ErrorMapEntry`, `parseErrorMap`, `serializeErrorMap`, `nextBusinessCode`, `validateErrorDraft`, `parseErrorMapSafe`, `isDraftEngaged`, `matchesErrorQuery`, and `validateErrorMap` helpers from `ErrorMapEditor.tsx` to `services/errorMapValidation.ts`. Re-export them from the component for existing imports. The `errorMap` adapter must parse the resource content and reject any `validateErrorMap` result, including codes below 100 and empty descriptions.

Provide `createSectionRegistry(deps)` with injectable dependencies for tests and `defaultSectionRegistry` wired to real stores.

- [ ] **Step 5: Connect nested validation to the backup codec**

Implement:

```ts
export function parseBackupWithRegistry(
  text: string,
  registry: ConfigSectionRegistry = defaultSectionRegistry,
): ConfigBackupBundle {
  return parseBackupText(text, (section, value) => registry[section].validate(value));
}
```

- [ ] **Step 6: Run focused tests and type-check**

Run: `cd cmd/web && npx.cmd vitest run src/services/configTransfer/backupCodec.test.ts src/services/configTransfer/sectionRegistry.test.ts`

Run: `cd cmd/web && npx.cmd tsc -b`

Expected: PASS.

- [ ] **Step 7: Commit adapters after user approval**

Stage flow API, section registry/tests, backup codec, and service exports; show staged list/diff, then after approval run:

```bash
git commit -m "feat(web): register configuration backup sections"
```

---

### Task 7: Add the Persistent Recovery Journal and Restore Coordinator

**Files:**
- Create: `cmd/web/src/services/configTransfer/recoveryJournal.ts`
- Create: `cmd/web/src/services/configTransfer/restoreCoordinator.ts`
- Create: `cmd/web/src/services/configTransfer/restoreCoordinator.test.ts`
- Modify: `cmd/web/src/services/configTransfer/types.ts`

- [ ] **Step 1: Write failing execution and rollback tests**

Use injected fake adapters and a memory journal. Required cases: success, local section failure, flow failure, reverse rollback order, flow revision changed during compensation, and resume after reload:

```ts
it('rolls completed sections back in reverse order', async () => {
  const calls: string[] = [];
  const env = fakeEnvironment({
    protoFiles: fakeAdapter('protoFiles', calls),
    luaFiles: fakeAdapter('luaFiles', calls, { failOnApply: true }),
  });
  await expect(executeRestorePlan(planFor('protoFiles', 'luaFiles'), env)).rejects.toThrow('luaFiles');
  expect(calls).toEqual(['apply:protoFiles', 'apply:luaFiles', 'rollback:protoFiles']);
});

it('keeps the journal when flow compensation revision conflicts', async () => {
  const env = fakeEnvironment({ flowRollbackError: apiError('FLOW_SNAPSHOT_CONFLICT', 409) });
  const result = await recoverPendingRestore(env);
  expect(result.pendingSections).toContain('flows');
  expect(await env.journal.load()).not.toBeNull();
});
```

- [ ] **Step 2: Run coordinator tests and verify red**

Run: `cd cmd/web && npx.cmd vitest run src/services/configTransfer/restoreCoordinator.test.ts`

Expected: FAIL because journal and coordinator do not exist.

- [ ] **Step 3: Implement the recovery journal**

Use a dedicated DB and one active-operation key:

```ts
const recoveryStore = createStore('stressbot-config-recovery', 'data');
const ACTIVE_KEY = 'active';

export interface RecoveryJournal {
  operationId: string;
  startedAt: string;
  phase: 'prepared' | 'applying' | 'rollingBack';
  selectedSections: BackupSection[];
  before: Partial<Record<BackupSection, unknown>>;
  completedSections: BackupSection[];
  flowBefore?: FlowSnapshot;
  flowAppliedRevision?: string;
  pendingRollback: BackupSection[];
}

export const recoveryJournal = {
  load: () => get<RecoveryJournal>(ACTIVE_KEY, recoveryStore).then((v) => v ?? null),
  save: (value: RecoveryJournal) => set(ACTIVE_KEY, value, recoveryStore),
  clear: () => del(ACTIVE_KEY, recoveryStore),
};
```

Do not clear the journal on a failed rollback.

- [ ] **Step 4: Implement preflight and immutable plan creation**

`preflightRestore` must read only selected sections, fetch the flow revision when needed, call the pure planner, and return current snapshots plus conflicts. Re-read identity summaries immediately before execution; if changed, throw a `RestoreTargetChangedError` and require a new preflight.

- [ ] **Step 5: Implement execution and compensation**

The coordinator must save the journal before the first write, apply flows first, then local sections, and append to `completedSections` after each success:

```ts
export async function executeRestorePlan(
  plan: RestorePlan,
  env: RestoreEnvironment = defaultRestoreEnvironment,
): Promise<RestoreResult> {
  if (await env.journal.load()) throw new Error('存在未完成的配置恢复，请先完成回滚');
  const journal = makeJournal(plan);
  await env.journal.save(journal);
  try {
    await applyPlanSections(plan, journal, env);
    await refreshPlanSections(plan, env);
    await env.journal.clear();
    return { ok: true, stats: plan.stats, pendingSections: [] };
  } catch (error) {
    const pendingSections = await rollbackCompleted(journal, env);
    if (pendingSections.length === 0) await env.journal.clear();
    throw new RestoreExecutionError(error, pendingSections);
  }
}
```

Flow compensation must call the server with `expectedRevision: flowAppliedRevision`; a 409 remains pending and must not force overwrite later changes.

- [ ] **Step 6: Implement interrupted-session recovery**

`recoverPendingRestore` loads the journal, continues reverse rollback for `pendingRollback`, clears on complete, and returns a structured pending result on retryable network/revision failure.

- [ ] **Step 7: Run coordinator tests and type-check**

Run: `cd cmd/web && npx.cmd vitest run src/services/configTransfer/restoreCoordinator.test.ts`

Run: `cd cmd/web && npx.cmd tsc -b`

Expected: PASS.

- [ ] **Step 8: Commit recovery orchestration after user approval**

Stage journal/coordinator/tests/shared types; show staged list/diff, then after approval run:

```bash
git commit -m "feat(web): make configuration restore recoverable"
```

---

### Task 8: Build Selectable Configuration Backup UI and File-Menu Entry

**Files:**
- Create: `cmd/web/src/components/modules/configTransfer/ConfigBackupModal.tsx`
- Create: `cmd/web/src/components/modules/configTransfer/ConfigBackupModal.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/Toolbar.tsx`

- [ ] **Step 1: Write failing backup-modal component tests**

Mock section summaries and assert default selection, disabled flows, clear/all, empty selection, and successful one-file download:

```tsx
it('disables saved flows when the server has no flow library', async () => {
  render(<ConfigBackupModal open onClose={() => {}} flowLibrary={false} />);
  expect(await screen.findByText('已保存流程')).toBeInTheDocument();
  expect(screen.getByText('服务器未启用流程库')).toBeInTheDocument();
  expect(screen.getByRole('checkbox', { name: /已保存流程/ })).toBeDisabled();
});

it('does not download when nothing is selected', async () => {
  render(<ConfigBackupModal open onClose={() => {}} flowLibrary />);
  await userEvent.click(await screen.findByText('清空'));
  expect(screen.getByRole('button', { name: '下载备份' })).toBeDisabled();
});
```

- [ ] **Step 2: Run the component test and verify red**

Run: `cd cmd/web && npx.cmd vitest run src/components/modules/configTransfer/ConfigBackupModal.test.tsx`

Expected: FAIL because component does not exist.

- [ ] **Step 3: Implement backup collection and download helper**

Add a service function used by the modal:

```ts
export async function createBackupBundle(
  selected: BackupSection[],
  registry: ConfigSectionRegistry = defaultSectionRegistry,
): Promise<ConfigBackupBundle> {
  const entries = await Promise.all(selected.map(async (section) => {
    const value = await registry[section].read();
    return [section, value] as const;
  }));
  const data = Object.fromEntries(entries) as ConfigBackupBundle['data'];
  return buildBackupBundle(data, registry);
}
```

Use `captureCurrentDraft()` in edit mode and persisted `loadDraft()` outside edit mode. Download through one Blob/object URL and revoke it in `finally`.

- [ ] **Step 4: Implement the modal**

Use Ant Design `Modal`, grouped `Checkbox`, counts, estimated bytes, “全选”, “清空”, and a primary “下载备份” button. Render the security note as secondary text, not a feature-description card. Selected empty sections stay selectable and produce explicit empty values.

- [ ] **Step 5: Add the backup File-menu launcher without removing existing entries**

In `Toolbar.tsx`, add a divider plus:

```tsx
{
  key: 'backup-config',
  icon: <SaveOutlined />,
  label: '备份配置...',
  onClick: () => setBackupOpen(true),
},
```

Fetch capabilities when the backup modal opens and render `ConfigBackupModal`. Task 9 adds the restore item and modal only after the restore workflow is complete, so this task remains independently compilable.

- [ ] **Step 6: Run component tests and type-check**

Run: `cd cmd/web && npx.cmd vitest run src/components/modules/configTransfer/ConfigBackupModal.test.tsx`

Run: `cd cmd/web && npx.cmd tsc -b`

Expected: PASS.

- [ ] **Step 7: Commit backup UI after user approval**

Stage backup modal/tests, transfer service changes, and Toolbar; show staged list/diff, then after approval run:

```bash
git commit -m "feat(web): add selectable configuration backup"
```

---

### Task 9: Build Restore Preview, Conflict Resolution, and Results UI

**Files:**
- Create: `cmd/web/src/components/modules/configTransfer/ConfigRestoreModal.tsx`
- Create: `cmd/web/src/components/modules/configTransfer/ConflictResolutionView.tsx`
- Create: `cmd/web/src/components/modules/configTransfer/RestoreResult.tsx`
- Create: `cmd/web/src/components/modules/configTransfer/ConfigRestoreModal.test.tsx`
- Create: `cmd/web/src/components/modules/configTransfer/ConflictResolutionView.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/Toolbar.tsx`

- [ ] **Step 1: Write failing restore interaction tests**

Cover file-size rejection, parse failure, available-section selection, edit-mode guard, merge strategy, full-restore warning, conflict choices, and result counts:

```tsx
it('shows the deletion warning only for full restore', async () => {
  render(<ConfigRestoreModal open mode="edit" onClose={() => {}} flowLibrary />);
  await selectValidBackup();
  await userEvent.click(screen.getByText('完整恢复'));
  expect(screen.getByText(/会删除选中模块中备份不存在的内容/)).toBeInTheDocument();
});

it('blocks execution outside edit mode after allowing preview', async () => {
  render(<ConfigRestoreModal open mode="running" onClose={() => {}} flowLibrary />);
  await selectValidBackup();
  expect(screen.getByRole('button', { name: '开始恢复' })).toBeDisabled();
  expect(screen.getByText('请先返回编辑模式')).toBeInTheDocument();
});
```

- [ ] **Step 2: Run restore component tests and verify red**

Run: `cd cmd/web && npx.cmd vitest run src/components/modules/configTransfer/ConfigRestoreModal.test.tsx src/components/modules/configTransfer/ConflictResolutionView.test.tsx`

Expected: FAIL because components do not exist.

- [ ] **Step 3: Implement file preflight and section selection**

On file selection: call `assertBackupFileSize`, read text, `parseBackupWithRegistry`, show version/time/counts, and select all included/available sections. Disable `flows` when `flowLibrary` is false without blocking local sections.

- [ ] **Step 4: Implement mode and duplicate strategy controls**

Use a segmented control for `merge`/`replace`. In merge mode render radio options `overwrite`, `prompt`, `skip`. Recompute preflight statistics when selected sections or strategy change; full mode hides duplicate strategy and displays the destructive warning.

- [ ] **Step 5: Implement the conflict resolution view**

Render conflicts grouped by section with source/target name, timestamps, and summary. Each row uses a `Radio.Group` for overwrite/copy/skip; singleton rows omit copy. Add an “应用到剩余同类冲突” checkbox tied to the selected action.

- [ ] **Step 6: Implement confirmation, execution, and result rendering**

Require a final `modal.confirm` before writes. Pass the immutable resolved plan to `executeRestorePlan`. `RestoreResult` renders per-section `added`, `overwritten`, `deleted`, `skipped`, `copied`, rollback state, and any `pendingSections`.

- [ ] **Step 7: Add the restore File-menu item and mount the modal in Toolbar**

Add this item after “备份配置...”:

```tsx
{
  key: 'restore-config',
  icon: <ImportOutlined />,
  label: '恢复配置...',
  onClick: () => setRestoreOpen(true),
},
```

Pass the runtime `mode` from `useRuntimeStore`; preserve backup availability in all modes and disable only restore execution outside `edit`.

- [ ] **Step 8: Run component tests and type-check**

Run: `cd cmd/web && npx.cmd vitest run src/components/modules/configTransfer/ConfigRestoreModal.test.tsx src/components/modules/configTransfer/ConflictResolutionView.test.tsx`

Run: `cd cmd/web && npx.cmd tsc -b`

Expected: PASS.

- [ ] **Step 9: Commit restore UI after user approval**

Stage restore/conflict/result components and Toolbar; show staged list/diff, then after approval run:

```bash
git commit -m "feat(web): add configuration restore workflow"
```

---

### Task 10: Resume Interrupted Recovery at Application Startup

**Files:**
- Create: `cmd/web/src/components/modules/configTransfer/RecoveryGuard.tsx`
- Create: `cmd/web/src/components/modules/configTransfer/RecoveryGuard.test.tsx`
- Modify: `cmd/web/src/pages/EditorPage.tsx`
- Modify: `cmd/web/src/services/index.ts`

- [ ] **Step 1: Write failing guard tests**

```tsx
it('automatically retries an unfinished rollback and clears the warning on success', async () => {
  recoveryMock.mockResolvedValueOnce({ ok: true, pendingSections: [] });
  render(<RecoveryGuard />);
  await waitFor(() => expect(recoveryMock).toHaveBeenCalledTimes(1));
  expect(screen.queryByText('配置恢复未完成')).not.toBeInTheDocument();
});

it('keeps a persistent retry action when rollback is still pending', async () => {
  recoveryMock.mockResolvedValueOnce({ ok: false, pendingSections: ['flows'] });
  render(<RecoveryGuard />);
  expect(await screen.findByText('配置恢复未完成')).toBeInTheDocument();
  expect(screen.getByRole('button', { name: '重试恢复' })).toBeInTheDocument();
});
```

- [ ] **Step 2: Run guard tests and verify red**

Run: `cd cmd/web && npx.cmd vitest run src/components/modules/configTransfer/RecoveryGuard.test.tsx`

Expected: FAIL because guard does not exist.

- [ ] **Step 3: Implement the persistent recovery guard**

On mount, load the journal. If absent, render nothing. If present, call `recoverPendingRestore`; render a compact persistent `Alert` only when sections remain. The alert description names pending user-facing modules and exposes “重试恢复”. Do not expose IndexedDB or Admin terminology in UI text.

- [ ] **Step 4: Mount guard before normal editor interactions**

Add `<RecoveryGuard />` inside `HomeShellInner` at page-shell level so it is available before users can open a second restore. The coordinator remains the authority that rejects concurrent restores.

- [ ] **Step 5: Run guard tests, type-check, and focused suite**

Run: `cd cmd/web && npx.cmd vitest run src/components/modules/configTransfer/RecoveryGuard.test.tsx src/services/configTransfer/restoreCoordinator.test.ts`

Run: `cd cmd/web && npx.cmd tsc -b`

Expected: PASS.

- [ ] **Step 6: Commit startup recovery after user approval**

Stage guard/tests, EditorPage, and service exports; show staged list/diff, then after approval run:

```bash
git commit -m "feat(web): resume interrupted configuration recovery"
```

---

### Task 11: Complete Full Verification and Browser Acceptance

**Files:**
- Modify only files required by concrete failures found in this task.

- [ ] **Step 1: Format changed source files**

Run `gofmt -w` on changed Go files.

Run: `cd cmd/web && npx.cmd prettier --write src/services/configTransfer src/components/modules/configTransfer src/components/FlowEditor/panels/Toolbar.tsx src/pages/EditorPage.tsx src/services/resourcesStore.ts src/services/flowsApi.ts src/services/capabilitiesApi.ts src/components/FlowEditor/library/templateStore.ts src/components/modules/notepad/notepadStore.ts src/components/FlowEditor/store/persistDraft.ts`

Expected: formatter exits 0 without touching unrelated legacy files.

- [ ] **Step 2: Run backend verification sequentially**

Use a writable Go cache if the managed account rejects the default cache:

```powershell
$env:GOCACHE = Join-Path $env:TEMP 'stressbot-go-cache'
go build ./...
go test ./...
```

Expected: both commands exit 0. Do not run timing-sensitive Go tests concurrently with frontend builds.

- [ ] **Step 3: Run frontend verification sequentially**

Run: `cd cmd/web && npx.cmd tsc -b`

Run: `cd cmd/web && npm run test`

Expected: type-check and complete Vitest suite pass.

- [ ] **Step 4: Start the development server**

Run: `cd cmd/web && npm run dev -- --host 127.0.0.1`

Expected: Vite prints an available local URL. Keep the foreground execution session alive while testing.

- [ ] **Step 5: Perform browser acceptance**

Verify all of these with the local app:

1. File menu contains existing commands plus “备份配置...” and “恢复配置...”.
2. All nine sections can be selected independently; disabled flow library does not block local sections.
3. A partial export omits unselected keys; a selected empty section remains explicit.
4. Merge overwrite, merge skip, and per-conflict overwrite/copy/skip produce the previewed counts.
5. Full restore deletes only extra records in selected sections.
6. Current draft reloads into the editor and the validation report has zero errors.
7. Simulated failure leaves a recovery journal, reload shows persistent recovery state, and retry clears it.
8. No buttons, labels, or modal content overlap at desktop and narrow viewport widths.

- [ ] **Step 6: Run the project-required backend runtime check**

Remove only the generated runtime log, run the standalone Agent for 2-5 minutes, stop the exact foreground process/verified listener, then inspect `log/stressbot.log` for `error`, `warn`, or `失败` excluding `headError`.

Expected: no new abnormal output caused by the feature.

- [ ] **Step 7: Review the final diff before completion**

Run:

```bash
git status --short
git diff --check
git diff --stat
```

Expected: only planned source/test/document changes plus the user's pre-existing untracked `agent*` files; no build artifacts, logs, or debug code.

- [ ] **Step 8: Request focused backend and frontend review**

Use `backend-review` for the snapshot transaction, lock ordering, API errors, and capability field. Use `frontend-review` for type safety, centralized API calls, storage semantics, Ant Design behavior, and UI terminology. Resolve every validated finding and rerun the affected tests.

- [ ] **Step 9: Commit final verification fixes after user approval**

If verification required source fixes, stage only those files, show the staged list/diff, then after approval run:

```bash
git commit -m "fix: harden configuration backup restore"
```

If no fixes were needed, do not create an empty commit.
