/**
 * switch 节点编辑器：cases[] / defaultNext。
 */

import { Button, Form, Input, List, Space } from 'antd';
import { ArrowDownOutlined, ArrowUpOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { ConditionInput } from './shared/ConditionInput';
import { NodeIdSelect } from './shared/NodeIdSelect';
import { useFlowStore } from '../store/flowStore';
import type { SwitchCase } from '@/types/flow';

export interface SwitchEditorProps {
  nodeId: string;
}

export function SwitchEditor({ nodeId }: SwitchEditorProps) {
  const node = useFlowStore((s) => s.nodes[nodeId]);
  const updateNode = useFlowStore((s) => s.updateNode);
  if (!node) return null;
  const cases = node.cases ?? [];

  const setCases = (next: SwitchCase[]) => updateNode(nodeId, { cases: next });

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <strong>分支条件</strong>
        <Button
          icon={<PlusOutlined />}
          size="small"
          onClick={() => setCases([...cases, { condition: 'state:', next: '' }])}
        >
          添加分支
        </Button>
      </div>
      <List
        dataSource={cases.map((item, i) => ({ ...item, _i: i }))}
        rowKey={(r) => `${r._i}`}
        size="small"
        locale={{ emptyText: '尚无分支，点击"添加分支"' }}
        renderItem={(item) => (
          <List.Item>
            <div style={{ width: '100%' }}>
              <Space style={{ width: '100%', marginBottom: 8 }} align="center">
                <span style={{ width: 24, color: 'var(--text-tertiary)', fontSize: 11 }}>{item._i + 1}.</span>
                <div style={{ flex: 1 }}>
                  <ConditionInput
                    value={item.condition}
                    onChange={(v) => {
                      const arr = [...cases];
                      arr[item._i] = { ...arr[item._i], condition: v };
                      setCases(arr);
                    }}
                  />
                </div>
              </Space>
              <Space style={{ width: '100%' }} align="center">
                <span style={{ width: 24 }} />
                <div style={{ flex: 1, minWidth: 180 }}>
                  <NodeIdSelect
                    value={item.next || undefined}
                    onChange={(v) => {
                      const arr = [...cases];
                      arr[item._i] = { ...arr[item._i], next: v ?? '' };
                      setCases(arr);
                    }}
                    excludeId={nodeId}
                    placeholder="选择目标节点"
                  />
                </div>
                <Input
                  value={item.description}
                  onChange={(e) => {
                    const arr = [...cases];
                    arr[item._i] = { ...arr[item._i], description: e.target.value };
                    setCases(arr);
                  }}
                  placeholder="描述（可选）"
                  style={{ width: 160 }}
                />
                <Button
                  icon={<ArrowUpOutlined />}
                  size="small"
                  disabled={item._i === 0}
                  onClick={() => {
                    const arr = [...cases];
                    [arr[item._i - 1], arr[item._i]] = [arr[item._i], arr[item._i - 1]];
                    setCases(arr);
                  }}
                />
                <Button
                  icon={<ArrowDownOutlined />}
                  size="small"
                  disabled={item._i === cases.length - 1}
                  onClick={() => {
                    const arr = [...cases];
                    [arr[item._i], arr[item._i + 1]] = [arr[item._i + 1], arr[item._i]];
                    setCases(arr);
                  }}
                />
                <Button
                  icon={<DeleteOutlined />}
                  size="small"
                  danger
                  onClick={() => setCases(cases.filter((_, j) => j !== item._i))}
                />
              </Space>
            </div>
          </List.Item>
        )}
      />
      <Form layout="vertical" style={{ marginTop: 12 }}>
        <Form.Item label="默认分支">
          <NodeIdSelect
            value={node.defaultNext || undefined}
            onChange={(v) => updateNode(nodeId, { defaultNext: v ?? '' })}
            excludeId={nodeId}
            placeholder="留空 = 不跳转"
          />
        </Form.Item>
      </Form>
    </div>
  );
}
