# TOML And Envsubst Dependency Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace BurntSushi TOML and Drone envsubst with Pelletier TOML v2 and FluxCD envsubst v1.7.0 without expanding stressbot's public configuration syntax.

**Architecture:** Keep the existing `LoadTOML → ExpandConfigStrings` pipeline. Enable Pelletier's decoder strict mode and preserve its structured unknown-field error in the error chain; replace Drone's local AST preflight with FluxCD's built-in strict evaluation.

**Tech Stack:** Go 1.26, `github.com/pelletier/go-toml/v2`, `github.com/fluxcd/pkg/envsubst v1.7.0`, standard `testing` package.

---

### Task 1: Lock Dependency Versions And Add Migration Tests

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Test: `config/load_test.go`

- [x] **Step 1: Make the new test imports available**

Run:

```powershell
go get github.com/pelletier/go-toml/v2@v2.4.3 github.com/fluxcd/pkg/envsubst@v1.7.0
```

Expected: both modules are added to `go.mod`/`go.sum`; production code still uses the old dependencies.

- [x] **Step 2: Write the failing strict-decoder test**

Add `errors` and `github.com/pelletier/go-toml/v2` imports, then extend `TestLoadTOML_UnknownFieldError`:

```go
var strictErr *toml.StrictMissingError
if !errors.As(err, &strictErr) {
	t.Fatalf("错误链应包含 *toml.StrictMissingError，得到: %T: %v", err, err)
}
if len(strictErr.Errors) != 1 || strings.Join(strictErr.Errors[0].Key(), ".") != "misspelled" {
	t.Fatalf("未知字段详情不正确: %#v", strictErr.Errors)
}
```

Extend the table in `TestExpandString` with a defined-empty bare variable and add `wantErrContains` so the missing-variable case requires the variable name in the error.

- [x] **Step 3: Run the focused test and verify RED**

Run:

```powershell
go test ./config -run TestLoadTOML_UnknownFieldError -count=1
```

Expected: FAIL because the current BurntSushi decoder error chain does not contain `*toml.StrictMissingError`.

### Task 2: Replace Decoder And Expansion Implementations

**Files:**
- Modify: `config/load.go`

- [x] **Step 1: Replace imports**

Use:

```go
import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/fluxcd/pkg/envsubst"
	"github.com/pelletier/go-toml/v2"
)
```

- [x] **Step 2: Enable strict Pelletier decoding**

Replace the BurntSushi metadata check with:

```go
dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
if err := dec.Decode(&cfg); err != nil {
	var strictErr *toml.StrictMissingError
	if errors.As(err, &strictErr) {
		keys := make([]string, 0, len(strictErr.Errors))
		for i := range strictErr.Errors {
			keys = append(keys, strings.Join(strictErr.Errors[i].Key(), "."))
		}
		return nil, fmt.Errorf("配置文件 %s 包含未知字段: %s: %w",
			path, strings.Join(keys, ", "), err)
	}
	return nil, fmt.Errorf("解析配置文件 %s: %w", path, err)
}
```

- [x] **Step 3: Use FluxCD strict evaluation**

Keep the dollar-sign fast path and replace the two-stage Drone logic with:

```go
result, err := envsubst.EvalEnv(s, true)
if err != nil {
	return s, fmt.Errorf("展开环境变量表达式 %q: %w", s, err)
}
return result, nil
```

Delete `defaultOps`, `collectUndefinedNoDefault`, the Drone parse import, and their comments.

- [x] **Step 4: Run focused tests and verify GREEN**

Run:

```powershell
go test ./config -count=1
```

Expected: PASS.

### Task 3: Remove Old Modules And Verify The Repository

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [x] **Step 1: Normalize the module graph**

Run:

```powershell
go mod tidy
```

Expected: BurntSushi TOML and Drone envsubst disappear; Pelletier v2.4.3 and FluxCD v1.7.0 remain.

- [x] **Step 2: Verify backend behavior**

Run:

```powershell
go test ./...
go build ./...
```

Expected: both commands exit 0.

- [x] **Step 3: Verify frontend compatibility**

Run from `cmd/web`:

```powershell
npx.cmd tsc -b
npm.cmd run test
```

Expected: type checking and Vitest exit 0.

- [x] **Step 4: Review the final diff**

Confirm that only the dependency migration, tests, and already-approved design/plan documentation changed. Do not commit without separate user authorization required by the repository Git policy.
