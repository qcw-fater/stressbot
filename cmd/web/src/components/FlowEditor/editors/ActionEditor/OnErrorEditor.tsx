import { Form, InputNumber, Select } from 'antd';
import type { FlowNode, OnErrorDef, OnErrorStrategy } from '@/types/flow';
import { useFlowStore } from '../../store/flowStore';
import { normalizeOnError } from '../../utils/onError';

interface OnErrorEditorProps {
  nodeId: string;
  node: FlowNode;
}

const STRATEGY_OPTIONS: Array<{ value: OnErrorStrategy; label: string }> = [
  { value: 'resume', label: 'resume（继续原流程）' },
  { value: 'skip', label: 'skip（跳过当前层级）' },
  { value: 'abort', label: 'abort（中止当前流程）' },
];

export function OnErrorEditor({ nodeId, node }: OnErrorEditorProps) {
  const nodes = useFlowStore((s) => s.nodes);
  const updateNode = useFlowStore((s) => s.updateNode);

  const onError = node.onError ?? {};
  const handlerOptions = Object.entries(nodes)
    .filter(([id]) => id !== nodeId)
    .map(([id, n]) => ({ value: id, label: `${id} (${n.type})` }));

  const updateOnError = (patch: OnErrorDef) => {
    updateNode(nodeId, { onError: normalizeOnError({ ...onError, ...patch }) });
  };

  return (
    <>
      <Form.Item label="最终策略 strategy" tooltip="错误链路和重试结束后的收束方式；不配置等同 resume">
        <Select
          value={onError.strategy ?? 'resume'}
          onChange={(strategy: OnErrorStrategy) => updateOnError({ strategy })}
          options={STRATEGY_OPTIONS}
        />
      </Form.Item>
      <Form.Item label="忽略错误码 ignoreCodes" tooltip="命中后流程继续，但监控仍记录本次失败">
        <Select
          mode="tags"
          value={(onError.ignoreCodes ?? []).map(String)}
          onChange={(values) => updateOnError({ ignoreCodes: values.map((v) => Number(v)).filter((v) => Number.isInteger(v) && v > 0) })}
          tokenSeparators={[',', '，', ' ']}
          placeholder="输入正整数错误码，例如 1001"
        />
      </Form.Item>
      <Form.Item label="错误处理节点 handler" tooltip="也可以从 action 节点的错误出口拖线设置；这是调用边，不写入 next">
        <Select
          allowClear
          showSearch
          value={onError.handler || undefined}
          onChange={(handler?: string) => updateOnError({ handler })}
          options={handlerOptions}
          placeholder="选择普通节点"
        />
      </Form.Item>
      <Form.Item label="最大重试次数 maxRetries" tooltip="额外重试次数；2 表示最多执行 1+2 次">
        <InputNumber
          min={0}
          precision={0}
          value={onError.retry?.maxRetries ?? 0}
          onChange={(v) => updateOnError({ retry: { ...(onError.retry ?? {}), maxRetries: Number(v ?? 0) } })}
          style={{ width: '100%' }}
        />
      </Form.Item>
      <Form.Item label="重试等待 retryDelayMs" tooltip="失败后确认重试时的协作式等待毫秒数">
        <InputNumber
          min={0}
          precision={0}
          value={onError.retry?.retryDelayMs ?? 0}
          onChange={(v) => updateOnError({ retry: { ...(onError.retry ?? {}), retryDelayMs: Number(v ?? 0) } })}
          style={{ width: '100%' }}
        />
      </Form.Item>
    </>
  );
}
