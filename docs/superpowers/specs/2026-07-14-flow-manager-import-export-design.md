# 流程管理界面增加「导入/导出流程」按钮

## 背景

当前「导入流程 JSON」与「导出流程 JSON」只存在于编辑器顶部「文件」下拉菜单（`Toolbar.tsx`）。
用户想导入/导出流程时，下意识会去「流程管理」界面找，但那里只有「另存为 / 打开 / 覆盖 / 删除」
等服务器流程库操作，没有本地文件导入导出。

目标：把「文件」菜单里的导入/导出流程逻辑，在「流程管理」弹窗（`FlowManagerModal.tsx`）也暴露出来。
仅限「流程」本身的导入导出，不含模板库（template）导入导出。

## 现状

- `Toolbar.tsx`：
  - 「文件」下拉含「导入流程 JSON…」（触发隐藏 `<input type=file>` → `handleImportFile`）
    与「导出流程 JSON」（`onExport`）。
  - `handleImportFile(file)`：读文件 → `JSON.parse` 为 `FlowJson` → `loadFromTaskFlow`
    → 成功提示 → `syncScriptsAfterLoad`（引用脚本的 gap-fill + 缺失告警）。
  - `onExport()`：`useFlowStore.getState().toTaskFlow()` → blob → 下载 `flow.json`。
  - `syncScriptsAfterLoad(flow, action)` 同时被「导入」和「加载基线流程」(`onLoadDefault`) 复用。
- `FlowManagerModal.tsx`：弹窗，头部一行（保存名输入 + 「另存为新流程」）+ 已存流程表格
  （打开 / 覆盖 / 删除）。无导入导出。

## 方案

抽共享 hook，Toolbar 与 FlowManagerModal 都消费，逻辑单一来源，避免漂移。

### 1. 新增共享 hook：`FlowEditor/panels/useFlowFileIO.ts`

```ts
export function useFlowFileIO() {
  const { message } = AntApp.useApp();
  const loadFromTaskFlow = useFlowStore((s) => s.loadFromTaskFlow);

  const syncScriptsAfterLoad = async (flow: FlowJson) => { /* gap-fill + 缺失告警，吞异常不阻塞 */ };

  // 返回 true 表示导入成功，调用方据此决定是否关弹窗
  const importFlow = async (file: File): Promise<boolean> => {
    try {
      const parsed = JSON.parse(await file.text()) as FlowJson;
      loadFromTaskFlow(parsed);
      message.success(`已加载 ${file.name}`);
      await syncScriptsAfterLoad(parsed);
      return true;
    } catch (e) {
      message.error(`导入失败：${(e as Error).message}`);
      return false;
    }
  };

  const exportFlow = () => { /* toTaskFlow() → blob → 下载 flow.json */ };

  return { importFlow, exportFlow, syncScriptsAfterLoad };
}
```

- `message` / store 留在 hook 内部，不向外透传。
- `syncScriptsAfterLoad` 同时返回，供 Toolbar 的「加载基线流程」继续复用（保持单一来源）。
- 把原 `Toolbar` 内关于脚本 gap-fill 的说明注释迁到 hook，符合「重构须同步改注释」。

### 2. 重构 `Toolbar.tsx`（行为不变）

- `const { importFlow, exportFlow, syncScriptsAfterLoad } = useFlowFileIO();`
- 删除本地 `handleImportFile` / `onExport` / `syncScriptsAfterLoad`。
- 「文件」菜单导入项 → 隐藏 input → `importFlow(f)`（Toolbar 不需要关弹窗，忽略返回值）。
- 导出项 → `exportFlow()`。「加载基线流程」→ `syncScriptsAfterLoad(flow)`。
- 隐藏 input、模板库导入导出保持原样，不在本次范围内。

### 3. `FlowManagerModal.tsx` 加按钮

头部一行内，「另存为新流程」之后、靠右（`marginLeft: auto`）：

- **「导入流程」**：图标 `ImportOutlined`，tooltip「从本地 JSON 文件导入流程」。
  点击触发组件内隐藏 `<input type=file accept="application/json,.json">`；
  `onChange` → `const ok = await importFlow(f); if (ok) onClose();`（与「打开」一致：加载后关弹窗回画布）。
- **「导出流程」**：图标 `DownloadOutlined`，tooltip「导出当前流程为 flow.json」。点击 → `exportFlow()`。

短文案（导入流程 / 导出流程）保证 700px 弹窗宽度内一行不溢出。
沿用弹窗现有约定（打开 / 覆盖 / 删除均不做 readOnly 判断），导入在此也不按 readOnly 禁用。

## 影响面

- 新增 1 文件 `useFlowFileIO.ts`。
- 改 2 文件：`Toolbar.tsx`（消费 hook，行为不变）、`FlowManagerModal.tsx`（加 2 按钮 + 1 隐藏 input）。

## 验证

1. `cd cmd/web && npx tsc -b` 无类型错误。
2. `cd cmd/web && npm run test`（Vitest）全绿。
3. 编辑器内打开「流程管理」：点「导入流程」选 JSON → 弹窗关闭、画布加载该流程、引用脚本缺失时有告警；
   点「导出流程」→ 下载 `flow.json`。
4. 回归「文件」菜单的导入/导出/加载基线流程仍正常。
