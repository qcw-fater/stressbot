# Action 节点 onError 重点标签设计

## 背景

Action 节点当前只用一个笼统的 `onError` 徽章表示存在错误处理配置。这个标签无法直接说明节点失败后的关键行为，用户必须打开编辑面板才能确认是否重试、最终是跳过还是中止。此前的组合摘要会把多项配置放入同一个标签，信息虽完整，但容易过长并撑宽画布节点。

## 目标

在不增加节点宽度负担的前提下，让画布直接呈现最影响控制流的 onError 配置：额外重试次数和非默认最终策略。

## 展示规则

Action 节点第二行不再显示笼统的 `onError` 徽章，而是按配置独立显示以下标签：

- `retry:N`：仅当 `onError.retry.maxRetries > 0` 时显示；`N` 是额外重试次数。
- `skip`：仅当 `onError.strategy === 'skip'` 时显示。
- `abort`：仅当 `onError.strategy === 'abort'` 时显示。
- 未配置 strategy 或显式配置 `resume` 时，不显示策略标签。

下列配置不占用画布标签空间：

- `handler`：通过错误连线和编辑面板体现。
- `ignoreCodes`：仅在编辑面板体现。
- `retryDelayMs`：作为重试的附属参数，不单独显示。

因此，当节点只配置 `handler`、`ignoreCodes` 或 `retryDelayMs` 时，画布不显示任何 onError 标签。

## 交互与文案

每个重点标签使用现有 onError 徽章视觉样式，并提供独立悬浮说明：

- `retry:N`：`失败后最多额外重试 N 次`。
- `skip`：`错误处理和重试结束后，跳过当前层级`。
- `abort`：`错误处理和重试结束后，中止当前流程`。

标签与 pattern、listen 徽章沿用现有 flex 换行布局。多个重点标签彼此独立，避免单个长标签造成过度横向扩张。

## 实现边界

修改范围限定在前端 Action 节点展示及其测试：

- `ActionNode.tsx` 从布尔型 `hasOnErrorConfig` 改为生成独立重点标签。
- 同步更新文件顶部布局注释，删除“onError 摘要”这种已不准确的描述。
- 不改变 `FlowNode`、`OnErrorDef`、编辑器、序列化、校验或后端执行语义。

标签生成逻辑应保持纯函数形式，使各种配置组合可以不依赖 React Flow 画布进行验证。

## 测试与验收

覆盖以下场景：

1. 无 onError：无重点标签。
2. 仅 handler、ignoreCodes 或 retryDelayMs：无重点标签。
3. `maxRetries: 3`：只显示 `retry:3`。
4. `strategy: 'skip'`：只显示 `skip`。
5. `strategy: 'abort'`：只显示 `abort`。
6. `strategy: 'resume'`：无策略标签。
7. retry 与非默认 strategy 同时配置：显示两个独立标签，并带各自悬浮说明。

完成后执行前端 TypeScript 编译和 Vitest，并在本地流程编辑器中确认节点标签不会合并成长摘要、换行行为正常。