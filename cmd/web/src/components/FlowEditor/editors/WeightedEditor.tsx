/**
 * weighted 节点编辑器：options[] 表格（节点 ID + 权重）。
 */

import { Button, InputNumber, Space, Table } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { NodeIdSelect } from './shared/NodeIdSelect';
import { useFlowStore } from '../store/flowStore';
import type { WeightedOption } from '@/types/flow';

export interface WeightedEditorProps {
  nodeId: string;
}

export function WeightedEditor({ nodeId }: WeightedEditorProps) {
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const updateNode = useFlowStore((s) => s.updateNode);
  if (!node) return null;
  const opts = node.options ?? [];
  const total = opts.reduce((s, o) => s + Math.max(0, o.weight), 0);

  const setOpts = (next: WeightedOption[]) => updateNode(nodeId, { options: next });

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <strong>选项（按权重随机选择）</strong>
        <Button
          icon={<PlusOutlined />}
          size="small"
          onClick={() => setOpts([...opts, { node: '', weight: 1 }])}
        >
          添加选项
        </Button>
      </div>
      <Table
        size="small"
        dataSource={opts.map((o, i) => ({ ...o, _i: i }))}
        rowKey="_i"
        pagination={false}
        locale={{ emptyText: '尚无选项' }}
        columns={[
          {
            title: '节点',
            dataIndex: 'node',
            width: 280,
            render: (_, r) => (
              <NodeIdSelect
                value={r.node || undefined}
                onChange={(v) => {
                  const arr = [...opts];
                  arr[r._i] = { ...arr[r._i], node: v ?? '' };
                  setOpts(arr);
                }}
                excludeId={nodeId}
              />
            ),
          },
          {
            title: '权重',
            dataIndex: 'weight',
            width: 100,
            render: (_, r) => (
              <InputNumber
                min={0}
                value={r.weight}
                onChange={(v) => {
                  const arr = [...opts];
                  arr[r._i] = { ...arr[r._i], weight: (v as number) ?? 0 };
                  setOpts(arr);
                }}
                style={{ width: 90 }}
              />
            ),
          },
          {
            title: '占比',
            width: 80,
            render: (_, r) => (total > 0 ? `${((r.weight / total) * 100).toFixed(1)}%` : '-'),
          },
          {
            title: '操作',
            width: 140,
            render: (_, r) => (
              <Space>
                <Button
                  icon={<ArrowUpOutlined />}
                  size="small"
                  disabled={r._i === 0}
                  onClick={() => {
                    const arr = [...opts];
                    [arr[r._i - 1], arr[r._i]] = [arr[r._i], arr[r._i - 1]];
                    setOpts(arr);
                  }}
                />
                <Button
                  icon={<ArrowDownOutlined />}
                  size="small"
                  disabled={r._i === opts.length - 1}
                  onClick={() => {
                    const arr = [...opts];
                    [arr[r._i], arr[r._i + 1]] = [arr[r._i + 1], arr[r._i]];
                    setOpts(arr);
                  }}
                />
                <Button
                  icon={<DeleteOutlined />}
                  size="small"
                  danger
                  onClick={() => setOpts(opts.filter((_, j) => j !== r._i))}
                />
              </Space>
            ),
          },
        ]}
      />
      <div style={{ marginTop: 8, fontSize: 12, color: 'var(--text-tertiary)' }}>
        权重总和：{total}（占比按相对比例计算，无需归一化）
      </div>
    </div>
  );
}
