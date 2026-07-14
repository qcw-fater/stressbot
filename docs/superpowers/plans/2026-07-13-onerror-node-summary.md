# Action 节点 onError 重点标签 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Action 节点笼统的 `onError` 徽章替换为独立的 `retry:N` 和非默认策略徽章。

**Architecture:** 在独立纯函数模块中把 `FlowNode.onError` 转换成按顺序排列的标签描述，ActionNode 只负责渲染这些描述。这样隐藏项和重点项的边界可由 Vitest 直接验证，无需搭建 React Flow 渲染环境。

**Tech Stack:** React 18、TypeScript 5.6、Ant Design Tooltip、Vitest

---

## 文件结构

- Create: `cmd/web/src/components/FlowEditor/nodes/onErrorBadges.ts` — 定义重点标签描述类型及纯函数生成规则。
- Create: `cmd/web/src/components/FlowEditor/nodes/onErrorBadges.test.ts` — 覆盖显示、隐藏与组合规则。
- Modify: `cmd/web/src/components/FlowEditor/nodes/ActionNode.tsx` — 渲染独立标签并更新布局注释。

### Task 1: 用测试固定重点标签规则

**Files:**
- Create: `cmd/web/src/components/FlowEditor/nodes/onErrorBadges.test.ts`
- Create: `cmd/web/src/components/FlowEditor/nodes/onErrorBadges.ts`

- [ ] **Step 1: 写失败测试**

测试 `buildOnErrorBadges(node)`：无配置和仅隐藏配置返回空数组；重试返回 `retry:3`；`resume` 隐藏；`skip`、`abort` 返回对应策略；重试和策略组合返回两个独立描述，并断言 tooltip 文案。

- [ ] **Step 2: 运行定向测试并确认失败**

Run: `cd cmd/web; npm run test -- src/components/FlowEditor/nodes/onErrorBadges.test.ts`

Expected: FAIL，因为 `./onErrorBadges` 尚未存在。

- [ ] **Step 3: 实现最小纯函数**

```ts
import type { FlowNode } from '@/types/flow';

export interface OnErrorBadge {
  label: string;
  tooltip: string;
}

export function buildOnErrorBadges(node: FlowNode): OnErrorBadge[] {
  const badges: OnErrorBadge[] = [];
  const maxRetries = node.onError?.retry?.maxRetries ?? 0;
  if (maxRetries > 0) {
    badges.push({
      label: `retry:${maxRetries}`,
      tooltip: `失败后最多额外重试 ${maxRetries} 次`,
    });
  }

  const strategy = node.onError?.strategy;
  if (strategy === 'skip') {
    badges.push({ label: 'skip', tooltip: '错误处理和重试结束后，跳过当前层级' });
  } else if (strategy === 'abort') {
    badges.push({ label: 'abort', tooltip: '错误处理和重试结束后，中止当前流程' });
  }
  return badges;
}
```

- [ ] **Step 4: 重跑定向测试并确认通过**

Run: `cd cmd/web; npm run test -- src/components/FlowEditor/nodes/onErrorBadges.test.ts`

Expected: PASS。

### Task 2: 接入 Action 节点渲染

**Files:**
- Modify: `cmd/web/src/components/FlowEditor/nodes/ActionNode.tsx:8-11,27-76`

- [ ] **Step 1: 引入并调用 `buildOnErrorBadges`**

删除 `hasOnErrorConfig`，使用 `const onErrorBadges = buildOnErrorBadges(node)`。

- [ ] **Step 2: 将单一 onError 徽章改为独立映射**

```tsx
{onErrorBadges.map((badge) => (
  <Tooltip key={badge.label} title={badge.tooltip}>
    <span className="onerror-badge">{badge.label}</span>
  </Tooltip>
))}
```

- [ ] **Step 3: 同步修正注释**

将第二行说明改为“pattern 标签 + onError 重点标签 + listen 徽章”，避免继续描述已删除的组合摘要。

- [ ] **Step 4: 执行前端类型检查与完整单测**

Run: `cd cmd/web; npx tsc -b; npm run test`

Expected: TypeScript 无错误，Vitest 全部通过。

### Task 3: 浏览器验收

**Files:**
- 无

- [ ] **Step 1: 在本地编辑器构造含 retry 与 skip/abort 的 Action 节点**

确认画布分别显示独立的 `retry:N` 和策略标签，不再出现 `onError` 标签。

- [ ] **Step 2: 检查隐藏项与悬浮说明**

确认仅配置 handler/ignoreCodes/retryDelayMs 时无标签；悬浮重点标签时显示对应中文解释。

- [ ] **Step 3: 检查换行与控制台**

确认标签沿现有 flex 布局自然换行，浏览器控制台没有本次变更引入的错误。

> 按用户要求，本计划只修改本地工作区，不创建分支、不提交、不推送。