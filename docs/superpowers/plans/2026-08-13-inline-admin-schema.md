# Inline Admin Schema Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Admin 第一版数据库建表定义从外部 `schema.sql` 移入 Go 源码，同时保持表结构和启动初始化行为不变。

**Architecture:** `schema.go` 只负责按顺序执行建表语句；新增 `schema_definition.go`，通过 `schemaStatement{table, ddl}` 切片保存十一张表的完整 DDL。删除 `go:embed`、外部 SQL 文件及按分号切割逻辑。

**Tech Stack:** Go 1.26、database/sql、go-sqlmock。

---

### Task 1: 用测试定义代码内 Schema 契约

**Files:**
- Modify: `admin/mysql/schema_test.go`
- Modify: `admin/mysql/schema_contract_test.go`

- [ ] **Step 1: Write the failing test**

将测试入口改为直接遍历 `currentSchema`，并断言每项都有非空表名、DDL 以对应的 `CREATE TABLE IF NOT EXISTS <table>` 开头，表顺序仍为现有十一张表。

- [ ] **Step 2: Run test to verify it fails**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./admin/mysql`

Expected: FAIL，提示 `currentSchema` 未定义。

### Task 2: 将 DDL 移入 Go 定义

**Files:**
- Create: `admin/mysql/schema_definition.go`
- Modify: `admin/mysql/schema.go`
- Delete: `admin/mysql/schema.sql`

- [ ] **Step 1: Write minimal implementation**

定义：

```go
type schemaStatement struct {
    table string
    ddl   string
}

var currentSchema = []schemaStatement{
    {table: "task_history", ddl: `CREATE TABLE IF NOT EXISTS task_history (...) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},
    // 其余十项逐字迁入现有 schema.sql，每张表一个切片元素。
}
```

`InitializeSchema` 遍历 `currentSchema`，执行 `statement.ddl`，错误信息携带 `statement.table`；移除 `embed`、`strings` 和 `splitSchemaStatements`。

- [ ] **Step 2: Run focused tests**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./admin/mysql`

Expected: PASS。

### Task 3: 验证重构无行为回归

**Files:**
- Verify: `admin/mysql/schema.go`
- Verify: `admin/mysql/schema_definition.go`

- [ ] **Step 1: Format and run backend verification**

Run: `gofmt -w admin/mysql/schema.go admin/mysql/schema_definition.go admin/mysql/schema_test.go admin/mysql/schema_contract_test.go`

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go build ./...`

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./...`

Expected: 全部 PASS。

- [ ] **Step 2: Verify repository consistency**

Run: `git diff --check`

Expected: exit code 0；`admin/mysql/schema.sql` 删除且没有 `go:embed schema.sql` 残留。

本计划不创建提交，等待用户决定工作树的提交方式。
