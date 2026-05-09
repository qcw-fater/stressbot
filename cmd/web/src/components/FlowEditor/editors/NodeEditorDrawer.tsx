/**
 * 节点编辑抽屉：根据 node.type 路由到具体 Editor。
 *
 * 双击主画布上的节点 → editorStore.activePanel.kind = 'nodeEdit' → 此抽屉打开。
 */

import { Button, Drawer, Form, Input, Popconfirm, Space, Tag, message } from 'antd';
import { UndoOutlined } from '@ant-design/icons';
import { useState, useEffect, useRef, useMemo } from 'react';
import { useEditorStore } from '../store/editorStore';
import { useFlowStore } from '../store/flowStore';
import { isValidNodeId } from '../utils/nodeIdGen';
import { SequenceEditor } from './SequenceEditor';
import { LoopEditor } from './LoopEditor';
import { BooleanEditor } from './BooleanEditor';
import { WeightedEditor } from './WeightedEditor';
import { WaitEditor } from './WaitEditor';
import { ActionEditor } from './ActionEditor';
import type { FlowNode } from '@/types/flow';
import type { ActionDef } from '@/types/action';

export function NodeEditorDrawer() {
  const activePanel = useEditorStore((s) => s.activePanel);
  const setActivePanel = useEditorStore((s) => s.setActivePanel);

  const nodeId = activePanel.kind === 'nodeEdit' ? activePanel.nodeId : null;
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
  // 在 nodeId 改变时（即面板切换到新节点时）记录 node + 关联 action 的深拷贝。
  // 之后用户的所有编辑都会改变 flowStore 中的实际数据；点还原 → 用快照覆盖回去。
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

  // 是否允许还原：当前数据与快照不一致时才显示按钮可点击
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

  if (!nodeId || !node) {
    return <Drawer open={false} onClose={() => setActivePanel({ kind: 'none' })} />;
  }

  const editor = (() => {
    switch (node.type) {
      case 'sequence':
        return <SequenceEditor nodeId={nodeId} />;
      case 'loop':
        return <LoopEditor nodeId={nodeId} />;
      case 'boolean':
        return <BooleanEditor nodeId={nodeId} />;
      case 'weighted':
        return <WeightedEditor nodeId={nodeId} />;
      case 'wait':
        return <WaitEditor nodeId={nodeId} />;
      case 'action':
        return <ActionEditor nodeId={nodeId} />;
      case 'break':
      case 'continue':
        return (
          <div style={{ color: 'var(--text-secondary)' }}>
            该节点类型无可编辑字段。仅作为控制流终止信号。
          </div>
        );
      default:
        return null;
    }
  })();

  const onApplyRename = () => {
    if (draftId === nodeId) return;
    if (!isValidNodeId(draftId)) {
      message.error('节点 ID 不合法（不可为空、不含空白、不以 __ 开头）');
      return;
    }
    if (useFlowStore.getState().nodes[draftId]) {
      message.error('节点 ID 已存在');
      return;
    }
    renameNode(nodeId, draftId);
    message.success('已重命名，所有引用同步更新');
    // 选中切换到新 ID
    setActivePanel({ kind: 'nodeEdit', nodeId: draftId });
  };

  // action 节点（含完整 ActionEditor）需要更宽的抽屉，避免 BindingsTable 横向拥挤
  const drawerWidth = node.type === 'action' ? 800 : 520;

  return (
    <Drawer
      title={
        <Space>
          <Tag color="blue">{node.type}</Tag>
          <span>编辑节点</span>
        </Space>
      }
      open
      onClose={() => setActivePanel({ kind: 'none' })}
      width={drawerWidth}
      mask={false}
      extra={
        <Space size={6}>
          <Popconfirm
            title="还原本次修改"
            description="将该节点恢复到本次打开编辑面板时的状态（含 action 定义）。"
            onConfirm={onRevert}
            disabled={!dirty}
          >
            <Button
              icon={<UndoOutlined />}
              size="small"
              disabled={!dirty}
              title={dirty ? '还原本次打开后的所有修改' : '尚未修改任何内容'}
            >
              还原修改
            </Button>
          </Popconfirm>
          <Popconfirm
            title="确认删除"
            description="删除节点将同步移除所有指向它的引用（next / body / trueNext 等）。"
            onConfirm={() => {
              removeNode(nodeId);
              // 同时清理引用此 ID 的字段
              const flow = useFlowStore.getState();
              for (const [id, n] of Object.entries(flow.nodes)) {
                const partial: Partial<typeof n> = {};
                if (n.next?.includes(nodeId)) partial.next = n.next.filter((x) => x !== nodeId);
                if (n.body === nodeId) partial.body = '';
                if (n.trueNext === nodeId) partial.trueNext = '';
                if (n.falseNext === nodeId) partial.falseNext = '';
                if (n.options?.some((o) => o.node === nodeId)) {
                  partial.options = n.options.filter((o) => o.node !== nodeId);
                }
                if (Object.keys(partial).length > 0) flow.updateNode(id, partial);
              }
              setActivePanel({ kind: 'none' });
              message.success(`已删除节点 ${nodeId}`);
            }}
          >
            <Button danger size="small">
              删除节点
            </Button>
          </Popconfirm>
        </Space>
      }
    >
      <Form layout="vertical">
        <Form.Item label="节点 ID" help="重命名会同步更新所有引用">
          <Space.Compact style={{ width: '100%' }}>
            <Input value={draftId} onChange={(e) => setDraftId(e.target.value)} />
            <Button onClick={onApplyRename} disabled={draftId === nodeId}>
              重命名
            </Button>
          </Space.Compact>
        </Form.Item>
        <Form.Item
          label="描述"
          help="可选注释，显示在节点卡片上，不参与运行时逻辑"
        >
          <Input.TextArea
            value={node.description ?? ''}
            onChange={(e) =>
              useFlowStore.getState().updateNode(nodeId, { description: e.target.value })
            }
            placeholder="可选，比如：开始战斗匹配 / 玩家心跳轮询"
            autoSize={{ minRows: 1, maxRows: 3 }}
          />
        </Form.Item>
      </Form>
      <div style={{ marginTop: 8 }}>{editor}</div>
    </Drawer>
  );
}
