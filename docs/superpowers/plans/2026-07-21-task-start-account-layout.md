# Task Start Account Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将启动任务表单的人数、并发、账号前缀和账号编号起点排列为响应式两行两列常用参数区。

**Architecture:** 新增一个无外部服务依赖的 `TaskStartCommonFields` 展示组件，接收现有状态值和更新回调，内部使用 Ant Design `Row`/`Col` 组织两行等宽字段。`TaskStartModal` 继续负责 store、模式切换和提交，只用该组件替换原有分散字段，并从高级设置删除重复账号字段。

**Tech Stack:** React 18、TypeScript 5.6、Ant Design 5、Vitest、Testing Library

---

### Task 1: 为常用参数两行两列布局建立失败测试

**Files:**
- Create: `cmd/web/src/components/runtime/TaskStartCommonFields.test.tsx`
- Create: `cmd/web/src/components/runtime/TaskStartCommonFields.tsx`

- [ ] **Step 1: 编写两行字段归属与响应式列测试**

```tsx
const view = render(<Form><TaskStartCommonFields {...defaultProps} /></Form>);
const scaleRow = view.getByTestId('task-start-scale-row');
const accountRow = view.getByTestId('task-start-account-row');

expect(within(scaleRow).getByText('集群总机器人数')).toBeTruthy();
expect(within(scaleRow).getByText('并发（每秒新建机器人数）')).toBeTruthy();
expect(within(accountRow).getByText('账号前缀')).toBeTruthy();
expect(within(accountRow).getByText('账号编号起点')).toBeTruthy();
expect(scaleRow.querySelectorAll('.ant-col-sm-12')).toHaveLength(2);
expect(accountRow.querySelectorAll('.ant-col-sm-12')).toHaveLength(2);
expect(accountRow.querySelectorAll('.ant-col-xs-24')).toHaveLength(2);
```

- [ ] **Step 2: 运行聚焦测试并确认测试入口尚未建立**

Run: `npm.cmd run test -- src/components/runtime/TaskStartCommonFields.test.tsx`

Expected: ERROR，提示找不到 `TaskStartCommonFields` 模块；此时仅证明测试指向预期的新边界。

- [ ] **Step 3: 创建只导出空占位的展示组件**

```tsx
export function TaskStartCommonFields() {
  return null;
}
```

- [ ] **Step 4: 再次运行聚焦测试并确认布局断言失败**

Run: `npm.cmd run test -- src/components/runtime/TaskStartCommonFields.test.tsx`

Expected: FAIL，提示找不到 `task-start-scale-row`。

### Task 2: 实现常用参数组件并接入启动弹窗

**Files:**
- Modify: `cmd/web/src/components/runtime/TaskStartCommonFields.tsx`
- Modify: `cmd/web/src/components/runtime/TaskStartModal.tsx`
- Test: `cmd/web/src/components/runtime/TaskStartCommonFields.test.tsx`

- [ ] **Step 1: 使用 `Row gutter={16}` 和 `Col xs={24} sm={12}` 实现两行布局**

组件 props 包含 `totalBots`、`totalCapacity`、`debugMode`、`rampUpEnabled`、`rampUpSum`、`peakBots`、`hasResetStage`、`robotConfig` 以及对应更新回调。第一行渲染人数和并发；第二行渲染账号前缀和账号编号起点。所有输入沿用现有最小值、最大值、默认值和说明。

- [ ] **Step 2: 在 `TaskStartModal` 中替换原人数/并发字段**

```tsx
<TaskStartCommonFields
  totalBots={totalBots}
  totalCapacity={totalCapacity}
  debugMode={debugMode}
  rampUpEnabled={rampUpEnabled}
  rampUpSum={rampUpSum}
  peakBots={peakBots}
  hasResetStage={rampUpStages.some((stage) => stage.reset)}
  robotConfig={robotConfig}
  onTotalBotsChange={setTotalBots}
  onRobotConfigChange={setRobotConfig}
/>
```

- [ ] **Step 3: 从高级设置删除账号前缀和账号编号起点字段**

保留 `State 扩展字段` 作为高级设置首项，后续主服务、超时、日志与自动停止字段顺序不变。

- [ ] **Step 4: 运行聚焦测试并确认通过**

Run: `npm.cmd run test -- src/components/runtime/TaskStartCommonFields.test.tsx`

Expected: 1 个测试文件全部 PASS。

### Task 3: 完整验证

**Files:**
- Verify: `cmd/web/src/components/runtime/TaskStartCommonFields.tsx`
- Verify: `cmd/web/src/components/runtime/TaskStartModal.tsx`

- [ ] **Step 1: 运行 TypeScript 编译检查**

Run: `npx.cmd tsc -b`

Expected: exit code 0，无类型错误。

- [ ] **Step 2: 运行前端完整测试**

Run: `npm.cmd run test`

Expected: 全部 Vitest 测试通过，无未处理异常。

- [ ] **Step 3: 检查最终差异**

Run: `git diff -- cmd/web/src/components/runtime/TaskStartCommonFields.tsx cmd/web/src/components/runtime/TaskStartCommonFields.test.tsx cmd/web/src/components/runtime/TaskStartModal.tsx`

Expected: 仅包含常用字段组件、布局测试及弹窗接入，不改变 store 或提交协议。
