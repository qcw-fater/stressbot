/**
 * 通过 React Context 传递只读模式标志，避免 props 串联到深层节点编辑器。
 *
 * 默认 false（编辑态）；HomeShell 在 viewActive / running 时传 true。
 *
 * 使用方：
 * - FlowCanvas：onConnect / onNodesChange(只允许 select/move) / onEdgesChange / onDrop 直接 return；
 * - NodePalette：拖拽源 draggable=false，上方提示"运行中不可编辑"；
 * - NodeEditorDrawer：抽屉只显示 description，所有编辑控件 disabled；
 * - ResourcesDrawer / Toolbar 等：上传 / 修改类按钮置灰。
 */

import { createContext, useContext } from 'react';

export const FlowReadOnlyContext = createContext<boolean>(false);

/** 简洁 hook，直接拿到当前是否只读 */
export function useFlowReadOnly(): boolean {
  return useContext(FlowReadOnlyContext);
}
