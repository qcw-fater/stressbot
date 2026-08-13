# Rename Config Validation Package Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 `config/schema` 重命名为语义明确的 `config/validation`，保持 JSON Schema 内容和所有校验行为不变。

**Architecture:** 目录与 Go 包名同步从 `schema` 改为 `validation`。后端调用统一使用 `validation.ValidateFlow`、`validation.ValidateCodec`；前端继续直接引用同一层级中的两份 JSON Schema，只调整路径。

**Tech Stack:** Go 1.26、JSON Schema Draft 2020、TypeScript/Vite/Vitest。

---

### Task 1: 迁移目录并制造旧引用失败

**Files:**
- Move: `config/schema/` → `config/validation/`

- [ ] **Step 1: Rename the directory without updating consumers**

确认 `config/schema` 存在且 `config/validation` 不存在后，将整个目录改名，文件内容不变。

- [ ] **Step 2: Verify the expected RED state**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./runner ./protocol/codec ./admin/template ./admin/httpapi`

Expected: FAIL，调用方无法解析 `stressbot/config/schema`。

### Task 2: 同步 Go 包和前端引用

**Files:**
- Modify: `config/validation/embed.go`
- Modify: `config/validation/validator.go`
- Modify: `config/validation/validator_test.go`
- Modify: `runner/resources.go`
- Modify: `protocol/codec/schema.go`
- Modify: `admin/template/flow_template.go`
- Modify: `admin/httpapi/routes.go`
- Modify: `admin/httpapi/codec.go`
- Modify: `cmd/web/src/services/schemaValidator.ts`

- [ ] **Step 1: Rename package and imports**

将三个 Go 文件的 `package schema` 改为 `package validation`；将所有 `stressbot/config/schema` 改为 `stressbot/config/validation`，调用标识符统一为 `validation`。前端 JSON 导入改为 `config/validation/*.schema.json`。

- [ ] **Step 2: Verify focused GREEN state**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./config/validation ./runner ./protocol/codec ./admin/template ./admin/httpapi`

Run: `npx.cmd tsc -b`（工作目录 `cmd/web`）

Expected: 全部 PASS。

### Task 3: 同步当前架构文档并全量验证

**Files:**
- Modify: `README.md`
- Modify: `AGENTS.md`
- Modify: `docs/superpowers/specs/2026-08-13-package-architecture-design.md`

- [ ] **Step 1: Update current documentation**

将当前文档中的 `config/schema` 改为 `config/validation`，说明其职责是 flow/codec 配置契约校验；历史设计文档保持原状。

- [ ] **Step 2: Verify no stale active references**

独立检索 Go、前端与当前架构文档，不得残留 `stressbot/config/schema` 或 `config/schema`。

- [ ] **Step 3: Run full verification**

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go build ./...`

Run: `$env:GOCACHE='D:\Gitee\stressbot\.tmp\gocache'; go test ./...`

Run: `npx.cmd tsc -b`、`npm.cmd run test`（工作目录 `cmd/web`）

Run: `git diff --check`

Expected: 全部 PASS。当前工作树不提交、不暂存。
