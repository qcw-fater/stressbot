/**
 * switch 节点编辑器：cases[] / defaultNext。
 *
 * 排版：每个 case 一个 Collapse 折叠面板（与 action binding 同构），便于 case 较多时收起；
 * 折叠态标题显示「Case N · 条件摘要 → 目标」便于识别；操作按钮（上移/下移/删除）在 header extra，折叠时也可用。
 * default 分支用虚线块单独区分（兜底语义）。
 */

import type { CSSProperties } from 'react';
import { Button, Collapse, Space, type CollapseProps } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { ConditionInput } from './shared/ConditionInput';
import { NodeIdSelect } from './shared/NodeIdSelect';
import { useFlowStore } from '../store/flowStore';
import type { SwitchCase } from '@/types/flow';

export interface SwitchEditorProps {
  nodeId: string;
}

const labelStyle: CSSProperties = {
  fontSize: 11,
  color: 'var(--text-tertiary)',
  marginBottom: 2,
};

/** 折叠态的条件摘要：剥离 state:/lua: 前缀，便于一眼识别 */
function conditionSummary(cond: string): string {
  const s = (cond ?? '').replace(/^(state:|lua:)/, '').trim();
  return s || '未配置条件';
}

export function SwitchEditor({ nodeId }: SwitchEditorProps) {
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const updateNode = useFlowStore((s) => s.updateNode);
  if (!node) return null;
  const cases = node.cases ?? [];

  const setCases = (next: SwitchCase[]) => updateNode(nodeId, { cases: next });
  const patchCase = (i: number, patch: Partial<SwitchCase>) => {
    const arr = cases.slice();
    arr[i] = { ...arr[i], ...patch };
    setCases(arr);
  };
  const moveCase = (i: number, dir: -1 | 1) => {
    const t = i + dir;
    if (t < 0 || t >= cases.length) return;
    const arr = cases.slice();
    const [item] = arr.splice(i, 1);
    arr.splice(t, 0, item);
    setCases(arr);
  };

  const items: CollapseProps['items'] = cases.map((c, i) => ({
    key: String(i),
    label: (
      <Space size={6}>
        <strong style={{ fontSize: 12 }}>Case {i + 1}</strong>
        <span style={{ color: 'var(--text-tertiary)', fontSize: 11 }}>
          {conditionSummary(c.condition)} → {c.next || '未选择'}
        </span>
      </Space>
    ),
    extra: (
      <Space size={2} onClick={(e) => e.stopPropagation()}>
        <Button
          size="small"
          icon={<ArrowUpOutlined />}
          disabled={i === 0}
          onClick={() => moveCase(i, -1)}
        />
        <Button
          size="small"
          icon={<ArrowDownOutlined />}
          disabled={i === cases.length - 1}
          onClick={() => moveCase(i, 1)}
        />
        <Button
          size="small"
          danger
          icon={<DeleteOutlined />}
          onClick={() => setCases(cases.filter((_, j) => j !== i))}
        />
      </Space>
    ),
    children: (
      <>
        <div style={{ marginBottom: 8 }}>
          <div style={labelStyle}>条件</div>
          <ConditionInput
            value={c.condition}
            onChange={(v) => patchCase(i, { condition: v })}
            placeholder="如 level >= 10 || vip >= 5"
          />
        </div>
        <div style={{ marginBottom: 8 }}>
          <div style={labelStyle}>目标节点</div>
          <NodeIdSelect
            value={c.next || undefined}
            onChange={(v) => patchCase(i, { next: v ?? '' })}
            excludeId={nodeId}
            placeholder="选择命中后执行的节点"
          />
        </div>
      </>
    ),
  }));

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <strong>分支条件（{cases.length}）</strong>
        <Button
          icon={<PlusOutlined />}
          size="small"
          onClick={() => setCases([...cases, { condition: 'state:', next: '' }])}
        >
          添加分支
        </Button>
      </div>

      {items.length > 0 ? (
        // ≤3 条默认全展开（小 switch 不回归）；>3 条默认全折叠（紧凑，点开要编辑的那条）
        <Collapse
          items={items}
          size="small"
          defaultActiveKey={cases.length <= 3 ? cases.map((_, i) => String(i)) : []}
        />
      ) : (
        <div style={{ color: 'var(--text-tertiary)', fontSize: 12, padding: 16, textAlign: 'center' }}>
          尚无分支，点击「添加分支」
        </div>
      )}

      <div
        style={{
          marginTop: 12,
          padding: 10,
          borderRadius: 6,
          background: 'var(--hover-bg, rgba(0,0,0,0.03))',
          border: '1px dashed var(--border-color, rgba(0,0,0,0.12))',
        }}
      >
        <div style={{ fontSize: 11, color: 'var(--text-tertiary)', marginBottom: 4 }}>默认分支（可选，推荐）</div>
        <NodeIdSelect
          value={node.defaultNext || undefined}
          onChange={(v) => updateNode(nodeId, { defaultNext: v ?? '' })}
          excludeId={nodeId}
          placeholder="所有条件未命中时执行；留空 = 静默结束"
        />
      </div>
    </div>
  );
}
