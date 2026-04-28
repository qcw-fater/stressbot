/**
 * sequence 节点编辑器：next[] 列表（可拖拽排序、增删）。
 */

import { Button, List, Space } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { useFlowStore } from '../store/flowStore';
import { NodeIdSelect } from './shared/NodeIdSelect';

export interface SequenceEditorProps {
  nodeId: string;
}

export function SequenceEditor({ nodeId }: SequenceEditorProps) {
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const updateNode = useFlowStore((s) => s.updateNode);
  if (!node) return null;
  const next = node.next ?? [];

  const setNext = (n: string[]) => updateNode(nodeId, { next: n });

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <strong>子节点（按序执行）</strong>
        <Button
          icon={<PlusOutlined />}
          size="small"
          onClick={() => setNext([...next, ''])}
        >
          添加
        </Button>
      </div>
      <List
        dataSource={next.map((id, i) => ({ id, i }))}
        rowKey={(r) => `${r.i}-${r.id}`}
        size="small"
        bordered
        locale={{ emptyText: '尚无子节点，点击"添加"' }}
        renderItem={(item) => (
          <List.Item>
            <Space style={{ width: '100%' }} align="center">
              <span style={{ width: 24, color: 'var(--text-tertiary)', fontSize: 11 }}>{item.i + 1}.</span>
              <div style={{ flex: 1, minWidth: 220 }}>
                <NodeIdSelect
                  value={item.id || undefined}
                  onChange={(v) => {
                    const arr = [...next];
                    arr[item.i] = v ?? '';
                    setNext(arr);
                  }}
                  excludeId={nodeId}
                />
              </div>
              <Button
                icon={<ArrowUpOutlined />}
                size="small"
                disabled={item.i === 0}
                onClick={() => {
                  const arr = [...next];
                  [arr[item.i - 1], arr[item.i]] = [arr[item.i], arr[item.i - 1]];
                  setNext(arr);
                }}
              />
              <Button
                icon={<ArrowDownOutlined />}
                size="small"
                disabled={item.i === next.length - 1}
                onClick={() => {
                  const arr = [...next];
                  [arr[item.i], arr[item.i + 1]] = [arr[item.i + 1], arr[item.i]];
                  setNext(arr);
                }}
              />
              <Button
                icon={<DeleteOutlined />}
                size="small"
                danger
                onClick={() => setNext(next.filter((_, j) => j !== item.i))}
              />
            </Space>
          </List.Item>
        )}
      />
    </div>
  );
}
