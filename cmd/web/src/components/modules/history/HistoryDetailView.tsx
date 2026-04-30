/**
 * 历史记录详情：摘要 + 配置归档下载 + 备注/标签编辑 + 时序回放折线 + 最终动作汇总。
 */

import { App, Button, Card, Col, Descriptions, Empty, Input, Row, Space, Spin, Tag } from 'antd';
import { CopyOutlined, DownloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import ReactECharts from 'echarts-for-react';
import { useEffect, useMemo, useState } from 'react';
import { historyApi, showApiError } from '@/services';
import type { HistoryDetail, TimeseriesPoint, StressSnapshot } from '@/types/api';
import { ApdexCell } from '@/components/monitoring/shared/ApdexCell';

export interface HistoryDetailViewProps {
  id: string;
  onChange: () => void;
}

export function HistoryDetailView({ id, onChange }: HistoryDetailViewProps) {
  const { message } = App.useApp();
  const [detail, setDetail] = useState<HistoryDetail | null>(null);
  const [timeseries, setTimeseries] = useState<{ stress: TimeseriesPoint[]; system: TimeseriesPoint[] } | null>(null);
  const [loading, setLoading] = useState(true);
  const [note, setNote] = useState('');
  const [tags, setTags] = useState<string[]>([]);

  useEffect(() => {
    setLoading(true);
    Promise.all([historyApi.getHistory(id), historyApi.getHistoryTimeseries(id)])
      .then(([d, t]) => {
        setDetail(d);
        setTimeseries({ stress: t.stress, system: t.system });
        setNote(d.note ?? '');
        setTags(d.tags);
      })
      .catch(showApiError)
      .finally(() => setLoading(false));
  }, [id]);

  const saveMeta = async () => {
    try {
      await historyApi.updateHistory(id, { note, tags });
      message.success('已保存');
      onChange();
    } catch (err) {
      showApiError(err);
    }
  };

  const downloadConfig = async () => {
    try {
      const archive = await historyApi.getHistoryConfig(id);
      const blob = new Blob([JSON.stringify(archive, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${detail?.name ?? id}-config.json`;
      a.click();
      URL.revokeObjectURL(url);
    } catch (err) {
      showApiError(err);
    }
  };

  const cloneTask = async () => {
    try {
      const resp = await historyApi.cloneHistory(id);
      message.success(`已克隆为新任务 ${resp.id.slice(0, 8)}`);
    } catch (err) {
      showApiError(err);
    }
  };

  const trendsOption = useMemo(() => {
    if (!timeseries || timeseries.stress.length === 0) return null;
    const x = timeseries.stress.map((p) => `${p.elapsedSec}s`);
    const qpsData = timeseries.stress.map((p) => {
      const snap = p.snapshot as StressSnapshot;
      return snap.actions.reduce((sum, a) => sum + a.avgQps, 0);
    });
    const apdexData = timeseries.stress.map((p) => {
      const snap = p.snapshot as StressSnapshot;
      // 加权平均
      let total = 0, w = 0;
      for (const a of snap.actions) {
        total += a.apdex * a.sampleCount;
        w += a.sampleCount;
      }
      return w > 0 ? +(total / w).toFixed(3) : 0;
    });
    return {
      title: { text: '运行趋势', textStyle: { fontSize: 12, fontWeight: 600 } },
      tooltip: { trigger: 'axis' },
      legend: { right: 0, textStyle: { fontSize: 11 } },
      grid: { left: 40, right: 40, top: 30, bottom: 24 },
      xAxis: { type: 'category', data: x, axisLabel: { fontSize: 10, hideOverlap: true } },
      yAxis: [
        { type: 'value', name: 'QPS', axisLabel: { fontSize: 10 } },
        { type: 'value', name: 'Apdex', max: 1, min: 0, axisLabel: { fontSize: 10 } },
      ],
      series: [
        { name: 'QPS', type: 'line', smooth: true, symbol: 'none', data: qpsData, itemStyle: { color: '#1677ff' } },
        { name: 'Apdex', type: 'line', smooth: true, symbol: 'none', data: apdexData, yAxisIndex: 1, itemStyle: { color: '#52c41a' } },
      ],
    };
  }, [timeseries]);

  if (loading) return <Spin />;
  if (!detail) return <Empty description="加载失败" />;

  const finalSnap = detail.finalSnapshot;
  const finalSys = detail.finalSystem;

  return (
    <Space direction="vertical" size={12} style={{ width: '100%' }}>
      <Card size="small" title={detail.name}>
        <Descriptions size="small" column={2} bordered>
          <Descriptions.Item label="任务 ID">
            <code>{detail.id}</code>
          </Descriptions.Item>
          <Descriptions.Item label="状态">
            {detail.state === 'failed' ? <Tag color="error">失败</Tag> : <Tag color="success">完成</Tag>}
          </Descriptions.Item>
          <Descriptions.Item label="机器人">
            {detail.totalBots} bots × {detail.agentCount} agents
          </Descriptions.Item>
          <Descriptions.Item label="时长">{Math.floor(detail.durationSec / 60)} 分 {detail.durationSec % 60} 秒</Descriptions.Item>
          <Descriptions.Item label="开始">{detail.startedAt ? dayjs(detail.startedAt).format('YYYY-MM-DD HH:mm:ss') : '—'}</Descriptions.Item>
          <Descriptions.Item label="结束">{detail.stoppedAt ? dayjs(detail.stoppedAt).format('YYYY-MM-DD HH:mm:ss') : '—'}</Descriptions.Item>
          <Descriptions.Item label="配置摘要" span={2}>
            authAddr=<code>{detail.configSummary.authAddr}</code> · concurrency={detail.configSummary.concurrency} · timeout={detail.configSummary.timeoutSec}s · flow={detail.configSummary.flowSizeKB}KB · proto×{detail.configSummary.protoCount} · script×{detail.configSummary.scriptCount}
          </Descriptions.Item>
          {detail.errorMsg && (
            <Descriptions.Item label="错误信息" span={2}>
              <pre style={{ margin: 0, color: '#f5222d', fontSize: 11 }}>{detail.errorMsg}</pre>
            </Descriptions.Item>
          )}
        </Descriptions>
        <Space style={{ marginTop: 8 }}>
          <Button icon={<DownloadOutlined />} onClick={downloadConfig}>下载配置归档</Button>
          <Button icon={<CopyOutlined />} onClick={cloneTask}>克隆为新任务</Button>
        </Space>
      </Card>

      <Card size="small" title="备注与标签">
        <Space direction="vertical" style={{ width: '100%' }}>
          <Input
            placeholder="按 Enter 添加标签"
            onPressEnter={(e) => {
              const v = (e.target as HTMLInputElement).value.trim();
              if (v && !tags.includes(v)) setTags([...tags, v]);
              (e.target as HTMLInputElement).value = '';
            }}
          />
          <Space wrap>
            {tags.map((t) => (
              <Tag key={t} closable onClose={() => setTags(tags.filter((x) => x !== t))}>
                {t}
              </Tag>
            ))}
          </Space>
          <Input.TextArea
            placeholder="备注（任意文本，对比时可见）"
            rows={2}
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
          <Button type="primary" size="small" onClick={saveMeta}>保存</Button>
        </Space>
      </Card>

      <Card size="small" title="集群最终快照">
        <Row gutter={12}>
          <Col span={6}>
            <strong>累计动作</strong>
            <div>{finalSnap.totalActions}</div>
          </Col>
          <Col span={6}>
            <strong>uptime</strong>
            <div>{Math.floor(finalSnap.uptimeSeconds / 60)} 分</div>
          </Col>
          <Col span={6}>
            <strong>错误连接</strong>
            <div>{finalSnap.connections.failed} / {finalSnap.connections.dropped}</div>
          </Col>
          <Col span={6}>
            <strong>最终 CPU%</strong>
            <div>{finalSys ? `${finalSys.avgCpuPercent.toFixed(1)}%` : '—'}</div>
          </Col>
        </Row>
      </Card>

      {trendsOption && (
        <Card size="small">
          <ReactECharts option={trendsOption} style={{ height: 240 }} notMerge lazyUpdate />
        </Card>
      )}

      <Card size="small" title={`动作汇总（${finalSnap.actions.length} 类）`}>
        <div style={{ maxHeight: 240, overflow: 'auto' }}>
          {finalSnap.actions
            .slice()
            .sort((a, b) => b.sampleCount - a.sampleCount)
            .map((a) => (
              <div
                key={a.name}
                style={{
                  display: 'grid',
                  gridTemplateColumns: '1fr 70px 70px 90px 70px',
                  gap: 8,
                  padding: '4px 0',
                  borderBottom: '1px dashed rgba(0,0,0,0.06)',
                  fontSize: 12,
                  alignItems: 'center',
                }}
              >
                <code>{a.name}</code>
                <span>{a.sampleCount}</span>
                <span>{(a.successRate * 100).toFixed(1)}%</span>
                <ApdexCell value={a.apdex} />
                <span>{a.latency.p99Ms.toFixed(0)}ms</span>
              </div>
            ))}
        </div>
      </Card>
    </Space>
  );
}
