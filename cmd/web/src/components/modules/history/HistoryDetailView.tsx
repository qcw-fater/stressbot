/**
 * 历史记录详情：摘要 + 配置归档下载 + 备注/标签编辑 + 时序回放折线 + 最终动作汇总。
 */

import { App, Button, Card, Col, Descriptions, Empty, Input, Row, Space, Spin, Switch, Table, Tag } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { CopyOutlined, DownloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import ReactECharts from 'echarts-for-react';
import { useEffect, useMemo, useState } from 'react';
import { historyApi, showApiError } from '@/services';
import type { ActionMetric, HistoryDetail, TimeseriesPoint, StressSnapshot } from '@/types/api';
import { ApdexCell } from '@/components/monitoring/shared/ApdexCell';
import { fmtBytes, fmtMs, NUMERIC_STYLE } from '@/components/monitoring/shared/formats';

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
  // 动作汇总表的本地 UI 状态：与 ActionsTab 行为对齐（搜索 + 仅展示动作）
  const [actionSearch, setActionSearch] = useState('');
  const [actionsOnly, setActionsOnly] = useState(false);

  useEffect(() => {
    setLoading(true);
    Promise.all([historyApi.getHistory(id), historyApi.getHistoryTimeseries(id)])
      .then(([d, t]) => {
        setDetail(d);
        // 后端 timeseries 可能返回 stress/system = null（任务夭折、采样器还没跑过等）。
        // 这里统一兜底成空数组，下面 trendsOption / 其他渲染就不再判 null 了。
        setTimeseries({ stress: t?.stress ?? [], system: t?.system ?? [] });
        setNote(d.note ?? '');
        setTags(d.tags ?? []);
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

  // ⚠️ 所有 hooks 必须在任何 early return 之前调用，遵守 React Rules of Hooks。
  // detail / loading 的 early return 在文件下方，因此 trendsOption / actionTable 这两个
  // useMemo 都必须能在 detail===null 的初次渲染期照常 hooks-call，
  // 内部用 `?.` / `?? []` 容忍空值（loading 期返回的占位结果不会被渲染到，但保证 hooks 链稳定）。
  const trendsOption = useMemo(() => {
    // 双重防御：useEffect 里已 ?? []，但 setTimeseries 在 race 下可能短暂带着旧值；
    // 这里保留 ?? [] 确保任何时刻都不会因 stress=null 崩溃。
    const stressTs = timeseries?.stress ?? [];
    if (stressTs.length === 0) return null;
    const x = stressTs.map((p) => `${p.elapsedSec}s`);
    const qpsData = stressTs.map((p) => {
      const snap = (p.snapshot ?? {}) as Partial<StressSnapshot>;
      return (snap.actions ?? []).reduce((sum, a) => sum + a.avgQps, 0);
    });
    const apdexData = stressTs.map((p) => {
      const snap = (p.snapshot ?? {}) as Partial<StressSnapshot>;
      let total = 0, w = 0;
      for (const a of snap.actions ?? []) {
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

  // 历史详情中的"动作汇总"采用与 ActionsTab 完全一致的列设计：
  //   - 这是用户复盘任务最重要的数据视图，必须能看到 成/败/超/跳/字节/p50/p95/p99/max/超均/错误；
  //   - 表头标 (ms) 单位，单元格只放数字，避免每格重复单位拖宽列；
  //   - 没有"成功率"列（与运行时面板对齐，避免冗余）；
  //   - 默认按 sampleCount 降序，错误列固定到最右、动作列固定到最左以便横向滚动时仍可定位。
  //
  // ⚠️ 这个 useMemo 必须放在 early return 之前（与 trendsOption 同区），否则首次 loading=true
  // 直接 return <Spin /> 会跳过该 hook，loading=false 渲染时再调用，hooks 数量变化会触发
  // "Rendered more hooks than during the previous render"。内部用 detail?.finalSnapshot 兜底，
  // loading 期算出来的占位结果不会被实际渲染（下方 early return 会先生效）。
  const actionTable = useMemo(() => {
    const finalSnap = (detail?.finalSnapshot ?? {}) as Partial<StressSnapshot>;
    let rows = finalSnap.actions ?? [];
    if (actionsOnly) {
      rows = rows.filter((a) => !a.name.startsWith('callback:'));
    }
    if (actionSearch) {
      const lo = actionSearch.toLowerCase();
      rows = rows.filter((a) => a.name.toLowerCase().includes(lo));
    }
    const columns: ColumnsType<ActionMetric> = [
      {
        title: '动作',
        dataIndex: 'name',
        key: 'name',
        width: 200,
        fixed: 'left',
        ellipsis: true,
        sorter: (a, b) => a.name.localeCompare(b.name),
        render: (v: string) => {
          const isCallback = v.startsWith('callback:');
          const display = isCallback ? v.slice('callback:'.length) : v;
          return (
            <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              {isCallback && <Tag color="orange" style={{ marginInlineEnd: 0 }}>推送</Tag>}
              <code style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={display}>
                {display}
              </code>
            </div>
          );
        },
      },
      {
        title: '样本',
        dataIndex: 'sampleCount',
        key: 'sampleCount',
        width: 70,
        sorter: (a, b) => a.sampleCount - b.sampleCount,
        defaultSortOrder: 'descend',
        render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span>,
      },
      {
        title: '成功',
        dataIndex: 'successCount',
        key: 'successCount',
        width: 70,
        sorter: (a, b) => a.successCount - b.successCount,
        render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: '#52c41a' }}>{v}</span>,
      },
      {
        title: '失败',
        dataIndex: 'failureCount',
        key: 'failureCount',
        width: 70,
        sorter: (a, b) => a.failureCount - b.failureCount,
        render: (v: number) => (
          <span style={{ ...NUMERIC_STYLE, color: v > 0 ? '#f5222d' : 'var(--text-tertiary)' }}>{v}</span>
        ),
      },
      {
        title: '超时',
        dataIndex: 'timeoutCount',
        key: 'timeoutCount',
        width: 70,
        sorter: (a, b) => a.timeoutCount - b.timeoutCount,
        render: (v: number) => (
          <span style={{ ...NUMERIC_STYLE, color: v > 0 ? '#fa8c16' : 'var(--text-tertiary)' }}>{v}</span>
        ),
      },
      {
        title: '跳过',
        dataIndex: 'skippedCount',
        key: 'skippedCount',
        width: 70,
        sorter: (a, b) => a.skippedCount - b.skippedCount,
        render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--text-tertiary)' }}>{v}</span>,
      },
      {
        title: 'Apdex',
        dataIndex: 'apdex',
        key: 'apdex',
        width: 80,
        sorter: (a, b) => a.apdex - b.apdex,
        render: (v: number) => <ApdexCell value={v} />,
      },
      {
        title: 'QPS',
        dataIndex: 'avgQps',
        key: 'avgQps',
        width: 78,
        sorter: (a, b) => a.avgQps - b.avgQps,
        render: (v: number) => <span style={NUMERIC_STYLE}>{v.toFixed(1)}</span>,
      },
      {
        title: '↑avg',
        dataIndex: 'avgSendBytes',
        key: 'avgSendBytes',
        width: 78,
        sorter: (a, b) => a.avgSendBytes - b.avgSendBytes,
        render: (v: number) => <span style={NUMERIC_STYLE}>{fmtBytes(v)}</span>,
      },
      {
        title: '↓avg',
        dataIndex: 'avgRecvBytes',
        key: 'avgRecvBytes',
        width: 78,
        sorter: (a, b) => a.avgRecvBytes - b.avgRecvBytes,
        render: (v: number) => <span style={NUMERIC_STYLE}>{fmtBytes(v)}</span>,
      },
      {
        title: 'avg(ms)',
        key: 'avgMs',
        width: 76,
        sorter: (a, b) => a.latency.avgMs - b.latency.avgMs,
        render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.avgMs)}</span>,
      },
      {
        title: 'p50(ms)',
        key: 'p50Ms',
        width: 76,
        sorter: (a, b) => a.latency.p50Ms - b.latency.p50Ms,
        render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p50Ms)}</span>,
      },
      {
        title: 'p95(ms)',
        key: 'p95Ms',
        width: 76,
        sorter: (a, b) => a.latency.p95Ms - b.latency.p95Ms,
        render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p95Ms)}</span>,
      },
      {
        title: 'p99(ms)',
        key: 'p99Ms',
        width: 76,
        sorter: (a, b) => a.latency.p99Ms - b.latency.p99Ms,
        render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p99Ms)}</span>,
      },
      {
        title: 'max(ms)',
        key: 'maxMs',
        width: 76,
        sorter: (a, b) => a.latency.maxMs - b.latency.maxMs,
        render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.maxMs)}</span>,
      },
      {
        title: '超均(ms)',
        dataIndex: 'timeoutAvgMs',
        key: 'timeoutAvgMs',
        width: 84,
        sorter: (a, b) => a.timeoutAvgMs - b.timeoutAvgMs,
        render: (v: number) => <span style={NUMERIC_STYLE}>{fmtMs(v)}</span>,
      },
      {
        title: '错误',
        key: 'errors',
        width: 70,
        fixed: 'right',
        sorter: (a, b) => (a.errors?.length ?? 0) - (b.errors?.length ?? 0),
        render: (_, r) =>
          r.errors && r.errors.length > 0 ? (
            <Tag color="error" style={{ marginInlineEnd: 0 }}>{r.errors.length}</Tag>
          ) : (
            <span style={{ color: 'var(--text-tertiary)' }}>—</span>
          ),
      },
    ];
    return { dataSource: rows, columns };
  }, [detail, actionsOnly, actionSearch]);

  // ── early return：所有 hooks 调用完毕之后才能根据 loading/detail 决定渲染分支 ──
  if (loading) return <Spin />;
  if (!detail) return <Empty description="加载失败" />;

  // 防御：归档为空（任务夭折 / 极短运行）时后端可能返回 finalSnapshot=null 或缺少子字段，
  // 直接 .actions / .connections 会抛 TypeError 把整个 detail 视图打挂（用户感知"卡住空白"）。
  // 这里统一兜底成"空快照"，UI 退化为"暂无数据"提示，避免崩溃。
  const finalSnap = (detail.finalSnapshot ?? {}) as Partial<StressSnapshot>;
  const finalSys = detail.finalSystem;
  const finalActions = finalSnap.actions ?? [];
  const finalConnections = finalSnap.connections ?? { established: 0, failed: 0, dropped: 0 };
  const finalTotalActions = finalSnap.totalActions ?? 0;
  const finalUptimeSeconds = finalSnap.uptimeSeconds ?? 0;

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
            <div>{finalTotalActions}</div>
          </Col>
          <Col span={6}>
            <strong>uptime</strong>
            <div>{Math.floor(finalUptimeSeconds / 60)} 分</div>
          </Col>
          <Col span={6}>
            <strong>错误连接</strong>
            <div>{finalConnections.failed} / {finalConnections.dropped}</div>
          </Col>
          <Col span={6}>
            <strong>最终 CPU%</strong>
            <div>{finalSys ? `${(finalSys.avgCpuPercent ?? 0).toFixed(1)}%` : '—'}</div>
          </Col>
        </Row>
      </Card>

      {trendsOption && (
        <Card size="small">
          <ReactECharts option={trendsOption} style={{ height: 240 }} notMerge lazyUpdate />
        </Card>
      )}

      <Card
        size="small"
        title={`动作汇总（${finalActions.length} 类）`}
        extra={
          <Space size={8}>
            <Input.Search
              placeholder="按动作名搜索"
              size="small"
              value={actionSearch}
              onChange={(e) => setActionSearch(e.target.value)}
              allowClear
              style={{ width: 200 }}
            />
            <Space size={4}>
              <span style={{ fontSize: 12, color: 'var(--text-secondary)' }}>仅动作</span>
              <Switch checked={actionsOnly} onChange={setActionsOnly} size="small" />
            </Space>
          </Space>
        }
      >
        {actionTable.dataSource.length === 0 ? (
          <Empty description={finalActions.length === 0 ? '无最终动作数据' : '无符合条件的记录'} />
        ) : (
          <Table<ActionMetric>
            rowKey="name"
            size="small"
            dataSource={actionTable.dataSource}
            columns={actionTable.columns}
            pagination={false}
            scroll={{ x: 'max-content', y: 360 }}
            expandable={{
              rowExpandable: (r) => !!r.errors && r.errors.length > 0,
              expandedRowRender: (r) => (
                <div style={{ fontSize: 12 }}>
                  <strong>错误明细：</strong>
                  {r.errors!.map((e) => (
                    <div key={e.msg} style={{ marginTop: 2 }}>
                      <Tag color="error">×{e.count}</Tag>
                      {e.msg}
                    </div>
                  ))}
                </div>
              ),
            }}
          />
        )}
      </Card>
    </Space>
  );
}
