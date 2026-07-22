# Task Start Common Field Copy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将启动表单四个常用字段统一为短标签和一行短说明，并移除重复的长场景说明。

**Architecture:** 继续由 `TaskStartCommonFields` 独立负责四项常用字段。只调整标签、`Form.Item.extra` 和不再需要的展示 props，不改变输入范围、受控值、更新回调、响应式两列布局或任务提交数据。

**Tech Stack:** React 18、TypeScript 5.6、Ant Design 5、Vitest、Testing Library

---

### Task 1: 用失败测试定义统一文案

**Files:**
- Modify: `cmd/web/src/components/runtime/TaskStartCommonFields.test.tsx`
- Test: `cmd/web/src/components/runtime/TaskStartCommonFields.test.tsx`

- [ ] **Step 1: 更新布局测试中的标签与辅助说明断言**

```tsx
expect(within(scaleRow).getByText('机器人数')).toBeTruthy();
expect(within(scaleRow).getByText('集群总容量 500')).toBeTruthy();
expect(within(scaleRow).getByText('并发')).toBeTruthy();
expect(within(scaleRow).getByText('每秒新建机器人数')).toBeTruthy();
expect(within(accountRow).getByText('账号前缀')).toBeTruthy();
expect(within(accountRow).getByText('默认 bot_')).toBeTruthy();
expect(within(accountRow).getByText('起始编号')).toBeTruthy();
expect(within(accountRow).getByText('默认 0')).toBeTruthy();
expect(within(accountRow).queryByText(/账号格式/)).toBeNull();
expect(within(accountRow).queryByText(/避免撞车/)).toBeNull();
```

- [ ] **Step 2: 运行聚焦测试并确认旧文案导致失败**

Run: `npm.cmd run test -- src/components/runtime/TaskStartCommonFields.test.tsx`

Expected: FAIL，提示找不到“机器人数”或对应短说明。

### Task 2: 实现短标签和一行短说明

**Files:**
- Modify: `cmd/web/src/components/runtime/TaskStartCommonFields.tsx`
- Modify: `cmd/web/src/components/runtime/TaskStartModal.tsx`
- Test: `cmd/web/src/components/runtime/TaskStartCommonFields.test.tsx`

- [ ] **Step 1: 统一四项 `Form.Item` 文案**

```tsx
<Form.Item label="机器人数" extra={`集群总容量 ${totalCapacity}`}>...</Form.Item>
<Form.Item label="并发" extra="每秒新建机器人数">...</Form.Item>
<Form.Item label="账号前缀" extra="默认 bot_">...</Form.Item>
<Form.Item label="起始编号" extra="默认 0">...</Form.Item>
```

阶段性测试开启时，机器人数仍显示原有只读汇总，但辅助说明保持为同一条集群容量信息。

- [ ] **Step 2: 删除旧辅助文案专用 props**

从 `TaskStartCommonFieldsProps`、参数解构和 `TaskStartModal` 调用处删除：

```ts
debugMode
peakBots
hasResetStage
```

同时删除不再使用的 `Tag` 导入。`rampUpEnabled` 和 `rampUpSum` 继续用于人数输入与阶段汇总切换。

- [ ] **Step 3: 运行聚焦测试并确认通过**

Run: `npm.cmd run test -- src/components/runtime/TaskStartCommonFields.test.tsx`

Expected: 1 个测试文件全部 PASS。

### Task 3: 完整验证

**Files:**
- Verify: `cmd/web/src/components/runtime/TaskStartCommonFields.tsx`
- Verify: `cmd/web/src/components/runtime/TaskStartCommonFields.test.tsx`
- Verify: `cmd/web/src/components/runtime/TaskStartModal.tsx`

- [ ] **Step 1: 运行格式和 TypeScript 检查**

Run: `npx.cmd prettier --check src/components/runtime/TaskStartCommonFields.tsx src/components/runtime/TaskStartCommonFields.test.tsx`

Expected: 两个文件均符合 Prettier 格式。

Run: `npx.cmd tsc -b`

Expected: exit code 0，无类型错误。

- [ ] **Step 2: 运行前端完整测试**

Run: `npm.cmd run test`

Expected: 全部 Vitest 测试通过。

- [ ] **Step 3: 运行 Go 编译检查**

Run: `go build ./...`

Expected: exit code 0。

- [ ] **Step 4: 核对最终差异**

Run: `git diff --check -- cmd/web/src/components/runtime/TaskStartModal.tsx`

Expected: 无空白错误；最终差异只包含短文案、精简 props 及对应测试。
