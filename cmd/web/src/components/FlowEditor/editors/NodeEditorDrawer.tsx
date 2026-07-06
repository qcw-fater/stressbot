/**
 * 节点编辑浮动窗口：根据 node.type 路由到具体 Editor。
 *
 * 双击主画布上的节点 → editorStore.activePanel.kind = 'nodeEdit' → 此窗口打开。
 */

import { App as AntApp, Button, Form, Input, Popconfirm, Space, Tag, Tooltip } from 'antd';
import { UndoOutlined } from '@ant-design/icons';
import { useState, useEffect, useRef, useMemo } from 'react';
import { useEditorStore } from '../store/editorStore';
import { useFlowStore } from '../store/flowStore';
import { isValidNodeId } from '../utils/nodeIdGen';
import { SequenceEditor } from './SequenceEditor';
import { LoopEditor } from './LoopEditor';
import { BooleanEditor } from './BooleanEditor';
import { SwitchEditor } from './SwitchEditor';
import { WeightedEditor } from './WeightedEditor';
import { WaitEditor } from './WaitEditor';
import { ActionEditor } from './ActionEditor';
import { FloatingWindow } from '../panels/FloatingWindow';
import type { FlowNode, NodeType } from '@/types/flow';

const nodeTypeTagColor: Record<NodeType, string> = {
  sequence: 'blue',
  action: 'geekblue',
  loop: 'green',
  boolean: 'lime',
  switch: 'magenta',
  weighted: 'purple',
  wait: 'red',
  break: 'orange',
  continue: 'cyan',
};
import type { ActionDef } from '@/types/action';

export function NodeEditorDrawer() {
  const { message } = AntApp.useApp();
  const activePanel = useEditorStore((s) => s.activePanel.nodeEdit);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);
  const closePanel = useEditorStore((s) => s.closePanel);

  const nodeId = activePanel?.kind === 'nodeEdit' ? activePanel.nodeId : null;
  const node = useFlowStore((s) => (nodeId ? s.nodes[nodeId] : undefined));
  const renameNode = useFlowStore((s) => s.renameNode);
  const removeNode = useFlowStore((s) => s.removeNode);
  const replaceNode = useFlowStore((s) => s.replaceNode);
  const replaceAction = useFlowStore((s) => s.replaceAction);

  const [draftId, setDraftId] = useState(nodeId ?? '');
  useEffect(() => {
    setDraftId(nodeId ?? '');
  }, [nodeId]);

  // === 打开快照：用于"还原本次修改" ===
  const snapshot = useRef<{
    nodeId: string;
    node: FlowNode;
    actionName: string | null;
    action: ActionDef | null;
  } | null>(null);

  useEffect(() => {
    if (!nodeId) {
      snapshot.current = null;
      return;
    }
    const flow = useFlowStore.getState();
    const n = flow.nodes[nodeId];
    if (!n) {
      snapshot.current = null;
      return;
    }
    const actionName = n.type === 'action' ? n.action ?? null : null;
    const actionDef = actionName ? flow.actions[actionName] : null;
    snapshot.current = {
      nodeId,
      node: JSON.parse(JSON.stringify(n)),
      actionName,
      action: actionDef ? JSON.parse(JSON.stringify(actionDef)) : null,
    };
  }, [nodeId]);

  const action = useFlowStore((s) =>
    snapshot.current?.actionName ? s.actions[snapshot.current.actionName] : undefined,
  );
  const dirty = useMemo(() => {
    if (!snapshot.current || !node) return false;
    if (JSON.stringify(node) !== JSON.stringify(snapshot.current.node)) return true;
    if (snapshot.current.action) {
      if (JSON.stringify(action ?? {}) !== JSON.stringify(snapshot.current.action)) return true;
    }
    return false;
  }, [node, action]);

  const onRevert = () => {
    const s = snapshot.current;
    if (!s) return;
    replaceNode(s.nodeId, JSON.parse(JSON.stringify(s.node)));
    if (s.actionName && s.action) {
      replaceAction(s.actionName, JSON.parse(JSON.stringify(s.action)));
    }
    message.success('已还原本次打开后的所有修改');
  };

  const open = !!nodeId && !!node;

  const editor = (() => {
    if (!nodeId || !node) return null;
    switch (node.type) {
      case 'sequence':
        return <SequenceEditor nodeId={nodeId} />;
      case 'loop':
        return <LoopEditor nodeId={nodeId} />;
      case 'boolean':
        return <BooleanEditor nodeId={nodeId} />;
      case 'switch':
        return <SwitchEditor nodeId={nodeId} />;
      case 'weighted':
        return <WeightedEditor nodeId={nodeId} />;
      case 'wait':
        return <WaitEditor nodeId={nodeId} />;
      case 'action':
        return <ActionEditor nodeId={nodeId} />;
      default:
        return null;
    }
  })();

  const onApplyRename = () => {
    if (draftId === nodeId) return;
    if (!isValidNodeId(draftId)) {
      message.error('节点名称不合法（不可为空、不含空白、不以 __ 开头）');
      return;
    }
    if (useFlowStore.getState().nodes[draftId]) {
      message.error('节点名称已存在');
      return;
    }
    if (!nodeId) return;
    renameNode(nodeId, draftId);
    message.success('已重命名，所有引用同步更新');
    setActivePanel({ kind: 'nodeEdit', nodeId: draftId });
  };

  const titleNode = node;
  const drawerWidth = titleNode?.type === 'action' ? 720 : 520;

  return (
    <FloatingWindow
      windowId="nodeEdit"
      title={
        titleNode ? (
          <Space>
            <Tag color={nodeTypeTagColor[titleNode.type]}>{titleNode.type}</Tag>
            <span>编辑节点 {nodeId}</span>
          </Space>
        ) : (
          '编辑节点'
        )
      }
      defaultSize={{ width: drawerWidth, height: 560 }}
      minSize={{ width: 400, height: 350 }}
      open={open}
      onClose={() => closePanel('nodeEdit')}
      extra={
        <Space size={6}>
          <Popconfirm
            title="还原本次修改"
            description="将该节点恢复到本次打开编辑面板时的状态（含 action 定义）。"
            onConfirm={onRevert}
            disabled={!dirty}
          >
            <Tooltip title={dirty ? '还原本次打开后的所有修改' : '尚未修改任何内容'} mouseEnterDelay={0.4}>
              <Button
                icon={<UndoOutlined />}
                size="small"
                disabled={!dirty}
              >
                还原
              </Button>
            </Tooltip>
          </Popconfirm>
          <Popconfirm
            title="确认删除"
            description="删除节点将同步移除所有指向它的引用（next / body / trueNext 等）。"
            onConfirm={() => {
              removeNode(nodeId!);
              setActivePanel(null);
              message.success(`已删除节点 ${nodeId}`);
            }}
          >
            <Button danger size="small">
              删除
            </Button>
          </Popconfirm>
        </Space>
      }
    >
      <Form layout="vertical">
        <Form.Item label="节点名称">
          <Space.Compact style={{ width: '100%' }}>
            <Input value={draftId} onChange={(e) => setDraftId(e.target.value)} />
            <Button onClick={onApplyRename} disabled={draftId === nodeId}>
              重命名
            </Button>
          </Space.Compact>
        </Form.Item>
        <Form.Item label="描述">
          <Input.TextArea
            value={node?.description ?? ''}
            onChange={(e) =>
              nodeId && useFlowStore.getState().updateNode(nodeId, { description: e.target.value })
            }
            placeholder="可选注释，显示在节点面板上"
            autoSize={{ minRows: 1, maxRows: 3 }}
          />
        </Form.Item>
      </Form>
      <div style={{ marginTop: 8 }}>{editor}</div>
    </FloatingWindow>
  );
}
