# Action/Listen Template MySQL Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the shared Action and Listen template libraries from browser IndexedDB to Admin MySQL while preserving editing, standalone import/export, configuration backup/restore, merge conflict handling, and recoverable rollback.

**Architecture:** Add independent InnoDB-backed Action and Listen stores with ordinary CRUD and revisioned snapshot endpoints. Keep `templateStore.ts` as the frontend facade, backed by centralized API calls. Generalize the existing flow-only revision handling into an optional versioned-section interface used by flows and both template libraries.

**Tech Stack:** Go 1.26, `database/sql`, go-sql-driver/mysql, sqlmock, React 18, TypeScript 5.6, Ant Design 5, Vitest, Testing Library.

---

## File structure

Backend:

- `admin/template_common.go`: shared validation, duplicate-name mapping, safe error responses, snapshot policy and revision helpers.
- `admin/action_template.go`, `admin/listen_template.go`: domain DTOs, CRUD stores and handlers.
- `admin/action_template_snapshot.go`, `admin/listen_template_snapshot.go`: transactional snapshot stores and handlers.
- `admin/*template*_test.go`: store, handler, schema and transaction tests.
- `admin/mysql_schema.go`, `admin/errors.go`, `admin/admin.go`, `admin/handlers.go`: schema and server assembly.

Frontend:

- `cmd/web/src/services/templatesApi.ts`: typed Action/Listen CRUD and snapshot calls through `services/api.ts`.
- `cmd/web/src/components/FlowEditor/library/templateStore.ts`: component-facing facade and event bus; no IndexedDB fallback.
- `cmd/web/src/components/FlowEditor/library/useTemplateLibraryCapability.ts`: reusable availability hook.
- Existing template buttons, drawer, palette and toolbar: disabled/error/refresh behavior.
- `cmd/web/src/services/configTransfer/templateBundle.ts`: legacy standalone bundle compatibility.
- Existing config-transfer planner, registry, coordinator and journal: name-only template identity and generic versioned sections.

## Task 1: Add MySQL schema and stable errors

**Files:**
- Modify: `admin/mysql_schema.go:151`
- Modify: `admin/history_schema_test.go`
- Modify: `admin/errors.go:48`

- [ ] **Step 1: Write failing schema/error tests**

Add to `admin/history_schema_test.go`:

```go
func TestTemplateDDLUsesIndependentBinaryUniqueNames(t *testing.T) {
	for name, ddl := range map[string]string{
		"action": ddlActionTemplate,
		"listen": ddlListenTemplate,
	} {
		normalized := strings.ToLower(strings.Join(strings.Fields(ddl), " "))
		for _, fragment := range []string{
			"engine=innodb", "collate utf8mb4_bin", "unique index",
			"created_at datetime(3)", "updated_at datetime(3)",
		} {
			if !strings.Contains(normalized, fragment) {
				t.Errorf("%s DDL missing %q: %s", name, fragment, normalized)
			}
		}
	}
}

func TestTemplateErrorsHaveStableStatusCodes(t *testing.T) {
	checks := map[*Error]int{
		ErrTemplateLibraryDisabled: http.StatusServiceUnavailable,
		ErrActionTemplateNotFound: http.StatusNotFound,
		ErrListenTemplateNotFound: http.StatusNotFound,
		ErrTemplateNameConflict: http.StatusConflict,
		ErrTemplateSnapshotConflict: http.StatusConflict,
	}
	for apiErr, want := range checks {
		if apiErr.HTTPStatus != want { t.Errorf("%s status=%d want=%d", apiErr.Code, apiErr.HTTPStatus, want) }
	}
}
```

Import `net/http` in the test.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```powershell
go test ./admin -run 'TestTemplateDDL|TestTemplateErrors' -count=1
```

Expected: compilation fails because both DDL constants and five errors are undefined.

- [ ] **Step 3: Add exact DDL and error definitions**

Add to `admin/mysql_schema.go` and append both constants to `allDDL`:

```go
const ddlActionTemplate = `
CREATE TABLE IF NOT EXISTS action_template (
    id VARCHAR(32) NOT NULL PRIMARY KEY,
    name VARCHAR(80) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    description VARCHAR(500) NULL,
    pattern VARCHAR(32) NOT NULL,
    data_json MEDIUMBLOB NOT NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    UNIQUE INDEX uq_action_template_name (name),
    INDEX idx_action_template_updated (updated_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

const ddlListenTemplate = `
CREATE TABLE IF NOT EXISTS listen_template (
    id VARCHAR(32) NOT NULL PRIMARY KEY,
    name VARCHAR(80) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    description VARCHAR(500) NULL,
    kind VARCHAR(32) NOT NULL,
    data_json MEDIUMBLOB NOT NULL,
    default_ref_json MEDIUMBLOB NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    UNIQUE INDEX uq_listen_template_name (name),
    INDEX idx_listen_template_updated (updated_at DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`
```

Add to `admin/errors.go`:

```go
ErrTemplateLibraryDisabled = NewError("TEMPLATE_LIBRARY_DISABLED", http.StatusServiceUnavailable)
ErrActionTemplateNotFound = NewError("ACTION_TEMPLATE_NOT_FOUND", http.StatusNotFound)
ErrListenTemplateNotFound = NewError("LISTEN_TEMPLATE_NOT_FOUND", http.StatusNotFound)
ErrTemplateNameConflict = NewError("TEMPLATE_NAME_CONFLICT", http.StatusConflict)
ErrTemplateSnapshotConflict = NewError("TEMPLATE_SNAPSHOT_CONFLICT", http.StatusConflict)
```

- [ ] **Step 4: Run schema tests and verify GREEN**

```powershell
go test ./admin -run 'TestInitMySQLSchema|TestTemplateDDL|TestTemplateErrors' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add admin/mysql_schema.go admin/history_schema_test.go admin/errors.go
git commit -m "feat: 添加动作与监听模板表"
```

## Task 2: Implement validation, CRUD and server assembly

**Files:**
- Create: `admin/template_common.go`
- Create: `admin/action_template.go`
- Create: `admin/listen_template.go`
- Create: `admin/action_template_test.go`
- Create: `admin/listen_template_test.go`
- Create: `admin/template_handler_test.go`
- Modify: `admin/admin.go:29`
- Modify: `admin/handlers.go:105`

- [ ] **Step 1: Write validation tests**

Use these valid fixtures:

```go
var validActionTemplateSave = ActionTemplateSaveRequest{
	Name: "登录请求", Pattern: engine.PatternTCPRequest,
	Data: json.RawMessage(`{"pattern":"tcpRequest","service":"logic"}`),
}

var validListenTemplateSave = ListenTemplateSaveRequest{
	Name: "登录推送", Kind: "declarative",
	Data: json.RawMessage(`{"s2cProto":"","store":[]}`),
}
```

Table cases must reject blank/81-rune names, 501-rune descriptions, non-object JSON, invalid Action patterns, mismatched `data.pattern`, invalid Listen kinds, mismatched raw Listen shape, and `queueSize <= 0`. The empty declarative draft above must remain valid.

For `defaultRef`, also reject a non-object value, blank `server`, a missing/invalid `route`, and a non-integer `queueSize`; accept omission of the whole field. Do not check whether referenced Proto/Lua resources currently exist.

- [ ] **Step 2: Run validation tests and verify RED**

```powershell
go test ./admin -run 'TestValidate(Action|Listen)Template' -count=1
```

Expected: compilation fails because the request types and validators are absent.

- [ ] **Step 3: Implement shared validation and safe errors**

Create `template_common.go` with these contracts:

```go
const (
	templateNameMax = 80
	templateDescriptionMax = 500
	templateCRUDMaxBytes = 1 << 20
	templateSnapshotMaxBytes = 50 << 20
)

type templateDefaultRef struct {
	Server string `json:"server"`
	Route any `json:"route"`
	QueueSize int `json:"queueSize,omitempty"`
}

func normalizeTemplateName(string) (string, error)
func normalizeTemplateDescription(string) (string, error)
func requireJSONObject(json.RawMessage, string) (map[string]json.RawMessage, error)
func mapTemplateWriteError(error) error
func writeTemplateStoreError(http.ResponseWriter, error)
```

Use `utf8.RuneCountInString`. Map `*mysql.MySQLError{Number:1062}` to `ErrTemplateNameConflict.WithMessage("同类模板名称已存在")`. Pass through `*Error`; log other errors and return `ErrInternal.WithMessage("模板库操作失败")` without SQL details.

- [ ] **Step 4: Define DTOs and validators**

Use these Action fields and equivalent Listen fields (`Kind`, `DefaultRef`):

```go
type ActionTemplate struct {
	ID string `json:"id"`
	Name string `json:"name"`
	Description string `json:"description,omitempty"`
	Pattern string `json:"pattern"`
	Data json.RawMessage `json:"data"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ActionTemplateSaveRequest struct {
	Name string `json:"name"`
	Description string `json:"description,omitempty"`
	Pattern string `json:"pattern"`
	Data json.RawMessage `json:"data"`
}
```

Unmarshal Action data into `engine.ActionDef` and check the 14 exported constants. Derive Listen kind from raw property presence: `script` → `lua`; else `s2cProto` or `store` → `declarative`; else `silent`.

- [ ] **Step 5: Write CRUD/handler tests before methods**

For both stores, use sqlmock to cover create, list order, get/update/delete not found, duplicate-name mapping, and internal SQL failure. Add:

```go
func TestTemplateLibraryDisabled(t *testing.T) {
	server := &AdminServer{cfg: Config{StaticDir: t.TempDir()}}
	for _, path := range []string{"/sbot/action-templates", "/sbot/listen-templates"} {
		rr := httptest.NewRecorder()
		server.registerRoutes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "TEMPLATE_LIBRARY_DISABLED") {
			t.Fatalf("path=%s status=%d body=%s", path, rr.Code, rr.Body.String())
		}
	}
}
```

Also add `TestTemplateCRUDRouteRejectsBodyOverLimit` and assert that invalid JSON/validation failures return HTTP 400 without executing SQL.

- [ ] **Step 6: Run CRUD tests and verify RED**

```powershell
go test ./admin -run 'Test(Action|Listen)Template(Store|CRUD)|TestTemplateLibraryDisabled' -count=1
```

Expected: compilation fails because stores/handlers/server fields are absent.

- [ ] **Step 7: Implement both CRUD stores**

Each store owns `db *sql.DB` and `sync.RWMutex`. Implement `Create`, `List`, `Get`, `Update`, `Delete` using:

```sql
INSERT INTO action_template
  (id, name, description, pattern, data_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)

SELECT id, name, description, pattern, data_json, created_at, updated_at
FROM action_template ORDER BY updated_at DESC

UPDATE action_template
SET name=?, description=?, pattern=?, data_json=?, updated_at=?
WHERE id=?

DELETE FROM action_template WHERE id=?
```

Use corresponding Listen columns. Create IDs with `generateID()`, scan nullable fields explicitly, inspect `RowsAffected`, and translate errors through shared helpers.

Create requests never accept identity/timestamps. Update is a last-write-wins replacement by path ID: keep the original creation time, set a new update time, return not-found instead of upserting, and do not add item revisions or edit locks. Add test coverage that the same exact name is allowed once in each independent table, while duplicates inside one table are rejected.

- [ ] **Step 8: Implement handlers and assembly**

Add both store fields to `AdminServer`, initialize them whenever global MySQL is configured, and register all CRUD routes. Handlers use a 1 MiB body limit, `r.PathValue("id")`, 201/200/204 status codes, the shared disabled error, and safe internal-error translation.

- [ ] **Step 9: Run backend tests and verify GREEN**

```powershell
go test ./admin -run 'Test(Action|Listen)Template|TestTemplateLibraryDisabled|TestCapabilities' -count=1
go test ./admin -count=1
```

Expected: both PASS.

- [ ] **Step 10: Commit**

```powershell
git add admin/template_common.go admin/action_template.go admin/listen_template.go admin/action_template_test.go admin/listen_template_test.go admin/template_handler_test.go admin/admin.go admin/handlers.go
git commit -m "feat: 添加共享动作与监听模板接口"
```

## Task 3: Add transactional snapshots and ID policies

**Files:**
- Create: `admin/action_template_snapshot.go`
- Create: `admin/listen_template_snapshot.go`
- Create: `admin/template_snapshot_test.go`
- Modify: `admin/template_common.go`
- Modify: `admin/handlers.go`
- Modify: `admin/template_handler_test.go`

- [ ] **Step 1: Write snapshot tests**

Create tests named:

```go
func TestTemplateSnapshotRevisionIsOrderIndependent(t *testing.T)
func TestActionSnapshotPreserveKeepsIDAndTimes(t *testing.T)
func TestActionSnapshotGenerateMissingCreatesServerID(t *testing.T)
func TestActionSnapshotGenerateMissingPreservesUnchangedTimes(t *testing.T)
func TestListenSnapshotGenerateMissingUpdatesChangedExistingItem(t *testing.T)
func TestTemplateSnapshotRejectsDuplicateNamesBeforeBegin(t *testing.T)
func TestTemplateSnapshotRejectsStaleRevisionWithoutDelete(t *testing.T)
func TestTemplateSnapshotRollsBackWhenInsertFails(t *testing.T)
func TestTemplateSnapshotRouteRejectsBodyOverLimit(t *testing.T)
func TestTemplateSnapshotAndCRUDDoNotInterleave(t *testing.T)
```

Assert generated IDs are non-empty and at most 32 characters. For stale revisions, expect rollback and no delete.

- [ ] **Step 2: Run snapshot tests and verify RED**

```powershell
go test ./admin -run 'Test(Action|Listen|Template)Snapshot' -count=1
```

Expected: compilation fails because snapshot contracts are absent.

- [ ] **Step 3: Add common snapshot contracts**

```go
type TemplateIDPolicy string

const (
	TemplateIDPreserve TemplateIDPolicy = "preserve"
	TemplateIDGenerateMissing TemplateIDPolicy = "generate-missing"
)

type TemplateSnapshot[T any] struct {
	Revision string `json:"revision"`
	Items []T `json:"items"`
}

type ReplaceTemplateSnapshotRequest[T any] struct {
	ExpectedRevision string `json:"expectedRevision"`
	IDPolicy TemplateIDPolicy `json:"idPolicy"`
	Items []T `json:"items"`
}

type ReplaceTemplateSnapshotResponse[T any] struct {
	Revision string `json:"revision"`
	Count int `json:"count"`
	Items []T `json:"items"`
}
```

Implement a generic revision helper that copies, sorts by supplied ID getter, JSON-marshals the complete normalized records (including content/defaultRef/timestamps), SHA-256 hashes, and prefixes `sha256:`.

- [ ] **Step 4: Implement both snapshot stores**

Each `ReplaceSnapshot` must validate the policy, normalize request fields, and reject duplicate IDs/names before opening a transaction. It then acquires the write lock, begins one transaction, reads current rows ordered by ID, compares the revision, resolves policy-dependent metadata, deletes, inserts, re-reads, computes the revision, commits, and returns persisted items.

Policy rules:

- `preserve`: every item requires an ID of 1–32 characters plus non-zero creation/update times; preserve all three.
- `generate-missing`: missing IDs/times are server-generated; present IDs must exist in current rows; unchanged items retain times; changed items retain creation time and receive current update time.

Reuse the ordinary Action/Listen validators for every snapshot item. Use exact content comparison including Action `Data` and Listen `Data`/`DefaultRef`. Return `ErrTemplateSnapshotConflict.WithMessage("模板库已被其他用户修改，请重新预检")` with expected/actual details. The process mutex provides the same single-Admin boundary as flow templates; do not claim cross-Admin snapshot coordination.

- [ ] **Step 5: Add routes/handlers**

Register snapshot routes before `{id}` routes. PUT handlers use the 50 MiB body limit and safe error translation. Extend the disabled-library handler test to both snapshot paths so every template endpoint returns HTTP 503 when the stores are absent.

- [ ] **Step 6: Run snapshots and all Go tests**

```powershell
go test ./admin -run 'Test(Action|Listen|Template)Snapshot' -count=1
go test ./admin -count=1
go test ./... -count=1
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```powershell
git add admin/template_common.go admin/action_template_snapshot.go admin/listen_template_snapshot.go admin/template_snapshot_test.go admin/template_handler_test.go admin/handlers.go
git commit -m "feat: 添加模板库事务快照"
```

## Task 4: Replace the browser template store with the server API

**Files:**
- Create: `cmd/web/src/services/templatesApi.ts`
- Create: `cmd/web/src/services/templatesApi.test.ts`
- Modify: `cmd/web/src/services/index.ts`
- Modify: `cmd/web/src/services/errorHandler.ts`
- Create: `cmd/web/src/services/errorHandler.template.test.ts`
- Modify: `cmd/web/src/components/FlowEditor/library/templateStore.ts`
- Create: `cmd/web/src/components/FlowEditor/library/templateStore.test.ts`

- [ ] **Step 1: Write failing service API tests**

Cover Action and Listen list/get/create/update/delete endpoints, snapshot GET/PUT, encoded path IDs, and propagation of structured `ApiError` values. Assert that the service uses the centralized request helpers from `services/api.ts`. Add failing tests that expect stable Chinese messages for disabled, not-found, duplicate-name, and stale-snapshot error codes.

- [ ] **Step 2: Run the API tests and verify RED**

```powershell
cd cmd/web
npx.cmd vitest run src/services/templatesApi.test.ts src/services/errorHandler.template.test.ts
```

Expected: module resolution fails because `templatesApi.ts` does not exist.

- [ ] **Step 3: Implement typed API contracts**

Define server DTOs with RFC 3339 string timestamps and JSON payloads, plus shared snapshot contracts:

```ts
export type TemplateIdPolicy = 'preserve' | 'generate-missing'

export interface TemplateSnapshot<T> {
  revision: string
  items: T[]
}

export interface ReplaceTemplateSnapshotRequest<T> {
  expectedRevision: string
  idPolicy: TemplateIdPolicy
  items: T[]
}
```

Expose separate Action and Listen CRUD/snapshot objects. Use full response DTOs with required identity/timestamps, but separate snapshot input DTOs where `id`, `createdAt`, and `updatedAt` are optional. When the component model uses numeric `0` to mark a merge-generated time, omit that wire field instead of serializing it as Unix epoch. Re-export the API from `services/index.ts`.

Add the four template error-code mappings to `errorHandler.ts`; keep unknown errors on the existing fallback path.

- [ ] **Step 4: Write failing facade tests**

Mock `templatesApi.ts` and assert that the existing `templateStore` public methods now call the server. Cover DTO conversion, numeric millisecond timestamps for existing components, `updatedAt`, exact-name lookup, mutation events after success only, and returned persisted items after snapshot replacement.

- [ ] **Step 5: Run facade tests and verify RED**

```powershell
cd cmd/web
npx.cmd vitest run src/components/FlowEditor/library/templateStore.test.ts
```

Expected: tests fail because the facade still reads IndexedDB and does not expose snapshot functions.

- [ ] **Step 6: Replace IndexedDB implementation with a server facade**

Remove `idb-keyval` and client-side `nanoid` usage from `templateStore.ts`. Preserve its component-facing CRUD/list/find functions and event target so existing UI callers need minimal changes. Add `updatedAt`, convert timestamps at the API boundary, and add Action/Listen snapshot read/replace functions that always return server-persisted items.

Do not delete or migrate old IndexedDB data; simply stop reading and writing it.

Keep the standalone `exportAllTemplates` wrapper backed by the new list calls. Keep `importTemplates` as a temporary snapshot-based compatibility wrapper so the toolbar still compiles; do not add a second merge algorithm. Task 9 replaces the toolbar's direct import path with the common restore planner.

- [ ] **Step 7: Run focused tests and type checking**

```powershell
cd cmd/web
npx.cmd vitest run src/services/templatesApi.test.ts src/services/errorHandler.template.test.ts src/components/FlowEditor/library/templateStore.test.ts src/components/FlowEditor/library/listenTemplateDefaults.test.ts
npx.cmd tsc -b
```

Expected: all PASS.

- [ ] **Step 8: Commit**

```powershell
git add cmd/web/src/services/templatesApi.ts cmd/web/src/services/templatesApi.test.ts cmd/web/src/services/index.ts cmd/web/src/services/errorHandler.ts cmd/web/src/services/errorHandler.template.test.ts cmd/web/src/components/FlowEditor/library/templateStore.ts cmd/web/src/components/FlowEditor/library/templateStore.test.ts
git commit -m "feat: 前端模板库接入服务器存储"
```

## Task 5: Add capability gating and cross-window refresh

**Files:**
- Modify: `admin/handlers.go`
- Modify: `admin/template_handler_test.go`
- Modify: `cmd/web/src/services/capabilitiesApi.ts`
- Create: `cmd/web/src/components/FlowEditor/library/useTemplateLibraryCapability.ts`
- Create: `cmd/web/src/components/FlowEditor/library/useTemplateLibraryCapability.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/library/SaveTemplateButton.tsx`
- Create: `cmd/web/src/components/FlowEditor/library/SaveTemplateButton.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/library/TemplateEditorDrawer.tsx`
- Create: `cmd/web/src/components/FlowEditor/library/TemplateEditorDrawer.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/NodePalette.tsx`
- Create: `cmd/web/src/components/FlowEditor/panels/NodePalette.templateLibrary.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/Toolbar.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/Toolbar.configBackup.test.tsx`

- [ ] **Step 1: Add failing capability tests**

Assert `/sbot/capabilities` returns `templateLibrary: true` only when both template stores are initialized, and `false` when global MySQL is unavailable. In the hook test, cover loading, disabled, enabled, retry, and focus refresh behavior.

- [ ] **Step 2: Run capability tests and verify RED**

```powershell
go test ./admin -run 'TestCapabilities.*TemplateLibrary' -count=1
cd cmd/web
npx.cmd vitest run src/components/FlowEditor/library/useTemplateLibraryCapability.test.tsx
```

Expected: backend assertion and frontend module resolution fail.

- [ ] **Step 3: Expose and consume the capability**

Add `templateLibrary` to backend and frontend capability types. Implement a reusable hook that loads capabilities, refreshes on `window.focus`, exposes manual `refresh`, and retains the last known state when refresh fails.

- [ ] **Step 4: Make all template entry points capability-aware**

- Disable save/edit/import/export actions with a clear Chinese explanation when unavailable.
- Keep Flow editing usable when the shared template library is disabled.
- Catch API failures in save/edit flows and show the centralized Chinese error message.
- In `NodePalette`, refresh on entry, successful local mutations, window focus, and a visible manual refresh action.
- If refresh fails after a successful load, keep stale entries visible and show a non-blocking warning.

- [ ] **Step 5: Add/update component tests**

In the three named component test files and the existing toolbar test, cover disabled state, manual refresh, stale-data preservation, retained editor values, and no unhandled promise rejection on failed save.

- [ ] **Step 6: Run focused checks**

```powershell
go test ./admin -run 'TestCapabilities.*TemplateLibrary' -count=1
cd cmd/web
npx.cmd vitest run src/components/FlowEditor/library/useTemplateLibraryCapability.test.tsx src/components/FlowEditor/library/SaveTemplateButton.test.tsx src/components/FlowEditor/library/TemplateEditorDrawer.test.tsx src/components/FlowEditor/panels/NodePalette.templateLibrary.test.tsx src/components/FlowEditor/panels/Toolbar.configBackup.test.tsx
npx.cmd tsc -b
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```powershell
git add admin/handlers.go admin/template_handler_test.go cmd/web/src/services/capabilitiesApi.ts cmd/web/src/components/FlowEditor/library/useTemplateLibraryCapability.ts cmd/web/src/components/FlowEditor/library/useTemplateLibraryCapability.test.tsx cmd/web/src/components/FlowEditor/library/SaveTemplateButton.tsx cmd/web/src/components/FlowEditor/library/SaveTemplateButton.test.tsx cmd/web/src/components/FlowEditor/library/TemplateEditorDrawer.tsx cmd/web/src/components/FlowEditor/library/TemplateEditorDrawer.test.tsx cmd/web/src/components/FlowEditor/panels/NodePalette.tsx cmd/web/src/components/FlowEditor/panels/NodePalette.templateLibrary.test.tsx cmd/web/src/components/FlowEditor/panels/Toolbar.tsx cmd/web/src/components/FlowEditor/panels/Toolbar.configBackup.test.tsx
git commit -m "feat: 增加共享模板库可用性与刷新"
```

## Task 6: Make restore planning match template duplicates by exact name

**Files:**
- Modify: `cmd/web/src/services/configTransfer/restorePlanner.ts`
- Modify: `cmd/web/src/services/configTransfer/restorePlanner.test.ts`
- Modify: `cmd/web/src/services/configTransfer/sectionRegistry.ts`
- Modify: `cmd/web/src/services/configTransfer/sectionRegistry.test.ts`

- [ ] **Step 1: Add failing duplicate-planning tests**

Cover both Action and Listen templates:

- same name/different ID is one conflict;
- same ID/different name is not a conflict;
- names differing only by case are distinct;
- overwrite keeps the destination ID and creation time;
- skip leaves the destination unchanged;
- keep-copy clears identity metadata so the server creates a new ID;
- full replace preserves imported IDs and timestamps exactly.

- [ ] **Step 2: Run planner tests and verify RED**

```powershell
cd cmd/web
npx.cmd vitest run src/services/configTransfer/restorePlanner.test.ts src/services/configTransfer/sectionRegistry.test.ts
```

Expected: assertions fail because collection identity currently matches by ID or name and copies imported metadata wholesale.

- [ ] **Step 3: Extend collection identity policy**

Add explicit collection hooks:

```ts
interface CollectionIdentity<T> {
  id(item: T): string
  name(item: T): string
  clone: (source: T, nextId: string, newName: string) => T
  createId: () => string
  matchBy?: 'id-or-name' | 'name'
  prepareAdd?: (source: T) => T
  prepareOverwrite?: (target: T, source: T) => T
  prepareCopy?: (source: T, newName: string) => T
}
```

Keep the current `clone`/`createId` path as the default for all existing sections. Configure Action/Listen templates with `matchBy: 'name'` and exact string equality. Their `prepareAdd` and `prepareCopy` clear ID/creation/update times; `prepareOverwrite` keeps the destination ID and creation time, copies the source content, and clears update time so the server owns it.

- [ ] **Step 4: Run planner and registry tests**

```powershell
cd cmd/web
npx.cmd vitest run src/services/configTransfer/restorePlanner.test.ts src/services/configTransfer/sectionRegistry.test.ts
npx.cmd tsc -b
```

Expected: all PASS and existing section behavior remains unchanged.

- [ ] **Step 5: Commit**

```powershell
git add cmd/web/src/services/configTransfer/restorePlanner.ts cmd/web/src/services/configTransfer/restorePlanner.test.ts cmd/web/src/services/configTransfer/sectionRegistry.ts cmd/web/src/services/configTransfer/sectionRegistry.test.ts
git commit -m "feat: 按模板名称规划合并冲突"
```

## Task 7: Generalize revision checks and crash recovery beyond flows

**Files:**
- Modify: `cmd/web/src/services/configTransfer/types.ts`
- Modify: `cmd/web/src/services/configTransfer/sectionRegistry.ts`
- Modify: `cmd/web/src/services/configTransfer/recoveryJournal.ts`
- Create: `cmd/web/src/services/configTransfer/recoveryJournal.test.ts`
- Modify: `cmd/web/src/services/configTransfer/restoreCoordinator.ts`
- Modify: `cmd/web/src/services/configTransfer/restoreCoordinator.test.ts`

- [ ] **Step 1: Write failing generic-version tests**

Add coordinator tests with two independent fake server sections. Assert that it:

- reads and stores an expected revision per selected versioned section;
- aborts before any writes when one preflight revision changed;
- applies versioned and local sections in deterministic order;
- rolls back applied server sections using their own latest revision;
- leaves a recoverable journal when rollback cannot prove ownership;
- preserves existing flow-only behavior through the generic path.

Add journal tests for v2 serialization and migration from the legacy flow-specific fields.

- [ ] **Step 2: Run tests and verify RED**

```powershell
cd cmd/web
npx.cmd vitest run src/services/configTransfer/recoveryJournal.test.ts src/services/configTransfer/restoreCoordinator.test.ts
```

Expected: tests fail because `RestorePlan` and the journal only carry `flowExpectedRevision` and flow-specific snapshots.

- [ ] **Step 3: Introduce generic versioned section contracts**

Replace the flow-only plan field with:

```ts
expectedRevisions: Partial<Record<BackupSection, string>>
```

Add optional versioned I/O to each section adapter:

```ts
interface VersionedSectionIO<T> {
  read(): Promise<{ revision: string; value: T }>
  replace(input: {
    expectedRevision: string
    value: T
    mode: RestoreMode
  }): Promise<{ revision: string; value: T }>
  fingerprint?(value: T, mode: RestoreMode): string
}
```

Move flow snapshot access into its registered section adapter. The coordinator must not import or name flow/template APIs directly.

- [ ] **Step 4: Upgrade the recovery journal to v2**

Store the restore `mode` plus maps keyed by section for versioned before snapshots, intended after fingerprints, and applied revisions:

```ts
interface RecoveryJournalV2 {
  version: 2
  operationId: string
  mode: RestoreMode
  before: Partial<Record<BackupSection, unknown>>
  versionedBefore: Partial<Record<BackupSection, { revision: string; value: unknown }>>
  afterFingerprints: Partial<Record<BackupSection, string>>
  appliedRevisions: Partial<Record<BackupSection, string>>
}
```

Retain the existing phase, selected/completed/pending section fields around this core state. Read legacy flow fields and normalize them into the v2 maps in memory; write only v2.

Recovery may roll back a server section only when its current fingerprint equals the intended after fingerprint. Otherwise keep the journal and report a Chinese conflict requiring user review.

- [ ] **Step 5: Refactor coordinator preflight/apply/rollback**

Use the registry contracts for all selected versioned sections. Before the first write, re-read every versioned section and compare every expected revision. On apply, capture the returned persisted value and revision, and use that persisted value for UI refresh rather than the immutable plan-time input. On failure, roll back in reverse apply order, using the current revision and the preserved before value.

- [ ] **Step 6: Run focused tests and type checking**

```powershell
cd cmd/web
npx.cmd vitest run src/services/configTransfer/recoveryJournal.test.ts src/services/configTransfer/restoreCoordinator.test.ts src/services/configTransfer/sectionRegistry.test.ts
npx.cmd tsc -b
```

Expected: all PASS.

- [ ] **Step 7: Commit**

```powershell
git add cmd/web/src/services/configTransfer/types.ts cmd/web/src/services/configTransfer/sectionRegistry.ts cmd/web/src/services/configTransfer/recoveryJournal.ts cmd/web/src/services/configTransfer/recoveryJournal.test.ts cmd/web/src/services/configTransfer/restoreCoordinator.ts cmd/web/src/services/configTransfer/restoreCoordinator.test.ts
git commit -m "refactor: 通用化配置恢复版本控制"
```

## Task 8: Wire Action and Listen snapshots into backup/merge/full restore

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/library/templateStore.ts`
- Modify: `cmd/web/src/components/FlowEditor/library/templateStore.test.ts`
- Modify: `cmd/web/src/services/configTransfer/sectionRegistry.ts`
- Modify: `cmd/web/src/services/configTransfer/sectionRegistry.test.ts`
- Modify: `cmd/web/src/services/configTransfer/restoreCoordinator.test.ts`

- [ ] **Step 1: Add failing integration-style adapter tests**

For each template section, assert:

- backup reads the server snapshot value;
- full replace sends `idPolicy: 'preserve'` and exact imported metadata;
- merge sends `idPolicy: 'generate-missing'` with new IDs/times left unset;
- persisted response items replace the plan-time items that had unset identity metadata;
- Action and Listen revisions are independent;
- a stale revision in either category blocks all selected writes in preflight.

Add a crash-recovery case where generated IDs differ from the plan-time empty IDs.

- [ ] **Step 2: Run focused tests and verify RED**

```powershell
cd cmd/web
npx.cmd vitest run src/components/FlowEditor/library/templateStore.test.ts src/services/configTransfer/sectionRegistry.test.ts src/services/configTransfer/restoreCoordinator.test.ts
```

Expected: tests fail because template sections are still unversioned list/replace adapters.

- [ ] **Step 3: Register both template sections as versioned server sections**

Connect `actionTemplates` and `listenTemplates` to their independent snapshot endpoints. Map restore modes as follows:

- `replace` -> `preserve`;
- `merge` -> `generate-missing`.

Continue using name-based conflict planning from Task 6. Always return the response's final items to the coordinator and UI.

- [ ] **Step 4: Add semantic fingerprints for safe recovery**

For merge-mode template fingerprints, sort by exact name and omit `id`, `createdAt`, and `updatedAt`, because the server generates those values after the request. For replace mode, fingerprint the complete sorted records so exact full-restore metadata is verified.

- [ ] **Step 5: Run focused tests and type checking**

```powershell
cd cmd/web
npx.cmd vitest run src/components/FlowEditor/library/templateStore.test.ts src/services/configTransfer/sectionRegistry.test.ts src/services/configTransfer/restoreCoordinator.test.ts
npx.cmd tsc -b
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```powershell
git add cmd/web/src/components/FlowEditor/library/templateStore.ts cmd/web/src/components/FlowEditor/library/templateStore.test.ts cmd/web/src/services/configTransfer/sectionRegistry.ts cmd/web/src/services/configTransfer/sectionRegistry.test.ts cmd/web/src/services/configTransfer/restoreCoordinator.test.ts
git commit -m "feat: 模板库接入完整备份与恢复"
```

## Task 9: Reuse the full restore UI for standalone template imports

**Files:**
- Create: `cmd/web/src/services/configTransfer/templateBundle.ts`
- Create: `cmd/web/src/services/configTransfer/templateBundle.test.ts`
- Modify: `cmd/web/src/services/configTransfer/backupCodec.ts`
- Modify: `cmd/web/src/services/configTransfer/backupCodec.test.ts`
- Modify: `cmd/web/src/components/modules/configTransfer/ConfigBackupModal.tsx`
- Modify: `cmd/web/src/components/modules/configTransfer/ConfigBackupModal.test.tsx`
- Modify: `cmd/web/src/components/modules/configTransfer/ConfigRestoreModal.tsx`
- Modify: `cmd/web/src/components/modules/configTransfer/ConfigRestoreModal.test.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/Toolbar.tsx`
- Modify: `cmd/web/src/components/FlowEditor/panels/Toolbar.configBackup.test.tsx`

- [ ] **Step 1: Add failing compatibility and modal tests**

Cover:

- standalone Action/Listen bundle version 1 and full configuration backup version 1 both parse legacy numeric template timestamps;
- missing `updatedAt` normalizes to `createdAt`;
- the normalized bundle becomes a standard `ConfigBackupBundle` with only its template section selected;
- full backup/restore modals hide or disable template sections when `templateLibrary` is false;
- `ConfigRestoreModal` accepts an initial bundle and starts at preview/conflict planning without another file selection;
- the toolbar's old template import entry opens the shared restore flow rather than directly replacing data.

- [ ] **Step 2: Run focused tests and verify RED**

```powershell
cd cmd/web
npx.cmd vitest run src/services/configTransfer/templateBundle.test.ts src/services/configTransfer/backupCodec.test.ts src/components/modules/configTransfer/ConfigBackupModal.test.tsx src/components/modules/configTransfer/ConfigRestoreModal.test.tsx src/components/FlowEditor/panels/Toolbar.configBackup.test.tsx
```

Expected: module resolution/prop assertions fail and the old toolbar path still imports directly.

- [ ] **Step 3: Implement legacy normalization without changing schema version**

Keep the existing standalone and full backup schema at version 1. Normalize old numeric records at the codec boundary, including `updatedAt ?? createdAt`, and reject malformed category/data fields with a user-facing Chinese validation error.

Convert a standalone template bundle into the same `ConfigBackupBundle` consumed by the normal restore planner. Do not create a second merge algorithm.

- [ ] **Step 4: Make backup/restore UI capability-aware**

Pass `templateLibrary` into both modals. Only offer Action/Listen sections when the server capability is available. Preserve all non-template section behavior.

Add optional `initialBundle` and contextual title support to `ConfigRestoreModal`. Route standalone template import through this prop so it receives replace/merge, overwrite/skip/one-by-one conflict handling, revision preflight, and recovery journal behavior.

- [ ] **Step 5: Run focused tests, all config-transfer tests, and type checking**

```powershell
cd cmd/web
npx.cmd vitest run src/services/configTransfer/templateBundle.test.ts src/services/configTransfer/backupCodec.test.ts src/services/configTransfer/restorePlanner.test.ts src/services/configTransfer/sectionRegistry.test.ts src/services/configTransfer/restoreCoordinator.test.ts src/services/configTransfer/recoveryJournal.test.ts src/components/modules/configTransfer/ConfigBackupModal.test.tsx src/components/modules/configTransfer/ConfigRestoreModal.test.tsx src/components/FlowEditor/panels/Toolbar.configBackup.test.tsx
npx.cmd tsc -b
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```powershell
git add cmd/web/src/services/configTransfer/templateBundle.ts cmd/web/src/services/configTransfer/templateBundle.test.ts cmd/web/src/services/configTransfer/backupCodec.ts cmd/web/src/services/configTransfer/backupCodec.test.ts cmd/web/src/components/modules/configTransfer/ConfigBackupModal.tsx cmd/web/src/components/modules/configTransfer/ConfigBackupModal.test.tsx cmd/web/src/components/modules/configTransfer/ConfigRestoreModal.tsx cmd/web/src/components/modules/configTransfer/ConfigRestoreModal.test.tsx cmd/web/src/components/FlowEditor/panels/Toolbar.tsx cmd/web/src/components/FlowEditor/panels/Toolbar.configBackup.test.tsx
git commit -m "feat: 统一模板导入与配置恢复流程"
```

## Task 10: Complete regression, runtime, and manual restore verification

**Files:**
- Verify: `admin/*.go`
- Verify: `cmd/web/src/services/templatesApi.ts`
- Verify: `cmd/web/src/components/FlowEditor/library/*.ts*`
- Verify: `cmd/web/src/services/configTransfer/*.ts`
- Verify: `cmd/web/src/components/modules/configTransfer/*.tsx`
- Verify: `cmd/web/src/components/FlowEditor/panels/*.tsx`

- [ ] **Step 1: Format only changed Go files**

```powershell
gofmt -w admin/mysql_schema.go admin/errors.go admin/template_common.go admin/action_template.go admin/listen_template.go admin/action_template_snapshot.go admin/listen_template_snapshot.go admin/admin.go admin/handlers.go admin/history_schema_test.go admin/action_template_test.go admin/listen_template_test.go admin/template_handler_test.go admin/template_snapshot_test.go
```

Review the changed file list first and adjust this command to the actual touched Go files; do not format unrelated dirty-worktree files.

- [ ] **Step 2: Run backend regression and build checks**

```powershell
go test ./admin -count=1
go test ./... -count=1
go build ./...
```

Expected: all commands exit 0.

- [ ] **Step 3: Run frontend type, focused, full, and production-build checks sequentially**

```powershell
cd cmd/web
npx.cmd tsc -b
npx.cmd vitest run src/services/templatesApi.test.ts src/services/errorHandler.template.test.ts src/components/FlowEditor/library/templateStore.test.ts src/components/FlowEditor/library/useTemplateLibraryCapability.test.tsx src/components/FlowEditor/library/SaveTemplateButton.test.tsx src/components/FlowEditor/library/TemplateEditorDrawer.test.tsx src/components/FlowEditor/panels/NodePalette.templateLibrary.test.tsx src/services/configTransfer/templateBundle.test.ts src/services/configTransfer/backupCodec.test.ts src/services/configTransfer/restorePlanner.test.ts src/services/configTransfer/sectionRegistry.test.ts src/services/configTransfer/restoreCoordinator.test.ts src/services/configTransfer/recoveryJournal.test.ts src/components/modules/configTransfer/ConfigBackupModal.test.tsx src/components/modules/configTransfer/ConfigRestoreModal.test.tsx src/components/FlowEditor/panels/Toolbar.configBackup.test.tsx
npm.cmd run test
npm.cmd run build
```

Run these sequentially to avoid the repository's known Vitest/TypeScript worker contention. If the full suite again stalls during collection, record the exact evidence and report it explicitly rather than claiming it passed.

- [ ] **Step 4: Verify disabled behavior without global MySQL**

Start Admin with a configuration that has no usable global MySQL. Confirm:

- Flow editing and all non-template configuration features remain usable;
- template save/edit/import/export entries explain that the shared template library is unavailable;
- capabilities report `templateLibrary: false`;
- no browser-local template fallback or implicit IndexedDB migration occurs.

- [ ] **Step 5: Verify shared behavior with MySQL in two browser contexts**

Start Admin with the configured MySQL, open two browser contexts, and verify:

- create/update/delete in one context appears in the other after focus or manual refresh;
- Action and Listen names are unique only within their own category;
- exact case-sensitive names are treated as distinct;
- a duplicate create/update produces a clear Chinese conflict message;
- refresh failure keeps the last successful list visible.

- [ ] **Step 6: Verify partial backup, merge, conflict choices, and exact full restore**

Exercise each category independently and together:

- export only Action templates, only Listen templates, and both;
- merge with overwrite-all, skip-all, and one-by-one choices;
- keep-copy creates a new server ID;
- overwrite retains destination ID/creation time;
- full replace exactly removes destination-only records and preserves backup IDs/timestamps;
- Action and Listen snapshot revisions change independently;
- a simulated stale revision blocks writes before any selected section changes;
- a forced later-section failure restores already-applied sections or leaves a visible recovery conflict journal.

- [ ] **Step 7: Validate an existing flow in the editor**

Open `conf/flow/flow.json` in the frontend editor and confirm the validation report has no errors. Insert an Action and Listen from the shared library, verify the generated flow data remains valid, then discard the manual test changes.

- [ ] **Step 8: Run backend for 2–5 minutes and inspect logs**

Use a safe test configuration and clear only the intended log file before start:

```powershell
if (Test-Path 'log/stressbot.log') { Remove-Item -LiteralPath 'log/stressbot.log' }
go run ./cmd/agent -config conf/config.json
```

After 2–5 minutes, stop the process normally and inspect:

```powershell
Select-String -Path 'log/stressbot.log' -Pattern 'error|warn|失败' -CaseSensitive:$false | Where-Object { $_.Line -notmatch 'headError' }
```

Expected: no unexpected output.

- [ ] **Step 9: Review scope and final diff**

```powershell
git status --short
git diff --check
git diff --stat
```

Separate pre-existing user changes from this feature. Confirm no IndexedDB deletion/migration, no physical foreign keys, no direct component `fetch`, no flow-only coordinator branches, and no unrelated edits.
