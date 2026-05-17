/**
 * 历史记录详情：紧凑头部 + 全宽动作表（主角）+ 底部信息条。
 */

import { App, Button, Empty, Input, Space, Spin, Switch, Table, Tag, Timeline, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import { CopyOutlined, DownloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import ReactECharts from 'echarts-for-react';
import { useEffect, useMemo, useState } from 'react';
import { historyApi, showApiError } from '@/services';
import type { ActionMetric, HistoryDetail, TimeseriesPoint, StressSnapshot } from '@/types/api';
import { ApdexCell } from '@/components/monitoring/shared/ApdexCell';
import { fmtBytesPlain, fmtMs, NUMERIC_STYLE } from '@/components/monitoring/shared/formats';

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
  const [actionSearch, setActionSearch] = useState('');
  const [actionsOnly, setActionsOnly] = useState(false);

  useEffect(() => {
    setLoading(true);
    Promise.all([historyApi.getHistory(id), historyApi.getHistoryTimeseries(id)])
      .then(([d, t]) => {
        setDetail(d);
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

  const trendsOption = useMemo(() => {
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

  const actionTable = useMemo(() => {
    const finalSnap = (detail?.finalSnapshot ?? {}) as Partial<StressSnapshot>;
    let rows = finalSnap.actions ?? [];
    if (actionsOnly) rows = rows.filter((a) => !a.name.startsWith('callback:'));
    if (actionSearch) {
      const lo = actionSearch.toLowerCase();
      rows = rows.filter((a) => a.name.toLowerCase().includes(lo));
    }
    const columns: ColumnsType<ActionMetric> = [
      {
        title: '动作', dataIndex: 'name', key: 'name', width: 200, fixed: 'left', ellipsis: true,
        sorter: (a, b) => a.name.localeCompare(b.name),
        render: (v: string) => {
          const isCallback = v.startsWith('callback:');
          const display = isCallback ? v.slice('callback:'.length) : v;
          return (
            <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              {isCallback && <Tag color="orange" style={{ marginInlineEnd: 0 }}>推送</Tag>}
              <Tooltip title={display} mouseEnterDelay={0.4}>
                <code style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{display}</code>
              </Tooltip>
            </div>
          );
        },
      },
      { title: '样本', dataIndex: 'sampleCount', key: 'sampleCount', width: 70, sorter: (a, b) => a.sampleCount - b.sampleCount, defaultSortOrder: 'descend' as const, render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span> },
      { title: '成功', dataIndex: 'successCount', key: 'successCount', width: 70, sorter: (a, b) => a.successCount - b.successCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: 'var(--color-success)' }}>{v}</span> },
      { title: '失败', dataIndex: 'failureCount', key: 'failureCount', width: 70, sorter: (a, b) => a.failureCount - b.failureCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: v > 0 ? 'var(--color-error)' : 'var(--text-tertiary)' }}>{v}</span> },
      { title: '超时', dataIndex: 'timeoutCount', key: 'timeoutCount', width: 70, sorter: (a, b) => a.timeoutCount - b.timeoutCount, render: (v: number) => <span style={{ ...NUMERIC_STYLE, color: v > 0 ? 'var(--color-orange)' : 'var(--text-tertiary)' }}>{v}</span> },
      { title: '跳过', dataIndex: 'skippedCount', key: 'skippedCount', width: 70, sorter: (a, b) => a.skippedCount - b.skippedCount, render: (v: number) => <span style={NUMERIC_STYLE}>{v}</span> },
      { title: 'Apdex', dataIndex: 'apdex', key: 'apdex', width: 80, sorter: (a, b) => a.apdex - b.apdex, render: (v: number) => <ApdexCell value={v} /> },
      { title: 'QPS', dataIndex: 'avgQps', key: 'avgQps', width: 78, sorter: (a, b) => a.avgQps - b.avgQps, render: (v: number) => <span style={NUMERIC_STYLE}>{v.toFixed(1)}</span> },
      { title: '↑avg(B)', dataIndex: 'avgSendBytes', key: 'avgSendBytes', width: 86, sorter: (a, b) => a.avgSendBytes - b.avgSendBytes, render: (v: number) => <span style={NUMERIC_STYLE}>{fmtBytesPlain(v)}</span> },
      { title: '↓avg(B)', dataIndex: 'avgRecvBytes', key: 'avgRecvBytes', width: 86, sorter: (a, b) => a.avgRecvBytes - b.avgRecvBytes, render: (v: number) => <span style={NUMERIC_STYLE}>{fmtBytesPlain(v)}</span> },
      { title: 'avg(ms)', key: 'avgMs', width: 76, sorter: (a, b) => a.latency.avgMs - b.latency.avgMs, render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.avgMs)}</span> },
      { title: 'p50(ms)', key: 'p50Ms', width: 76, sorter: (a, b) => a.latency.p50Ms - b.latency.p50Ms, render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p50Ms)}</span> },
      { title: 'p95(ms)', key: 'p95Ms', width: 76, sorter: (a, b) => a.latency.p95Ms - b.latency.p95Ms, render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p95Ms)}</span> },
      { title: 'p99(ms)', key: 'p99Ms', width: 76, sorter: (a, b) => a.latency.p99Ms - b.latency.p99Ms, render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.p99Ms)}</span> },
      { title: 'max(ms)', key: 'maxMs', width: 76, sorter: (a, b) => a.latency.maxMs - b.latency.maxMs, render: (_, r) => <span style={NUMERIC_STYLE}>{fmtMs(r.latency.maxMs)}</span> },
      { title: '超均(ms)', dataIndex: 'timeoutAvgMs', key: 'timeoutAvgMs', width: 84, sorter: (a, b) => a.timeoutAvgMs - b.timeoutAvgMs, render: (v: number) => <span style={NUMERIC_STYLE}>{fmtMs(v)}</span> },
      {
        title: '错误', key: 'errors', width: 70, fixed: 'right',
        sorter: (a, b) => (a.errors?.length ?? 0) - (b.errors?.length ?? 0),
        render: (_, r) => r.errors && r.errors.length > 0 ? <Tag color="error" style={{ marginInlineEnd: 0 }}>{r.errors.length}</Tag> : <span style={{ color: 'var(--text-tertiary)' }}>—</span>,
      },
    ];
    return { dataSource: rows, columns };
  }, [detail, actionsOnly, actionSearch]);

  if (loading) return <Spin />;
  if (!detail) return <Empty description="加载失败" />;

  const finalSnap = (detail.finalSnapshot ?? {}) as Partial<StressSnapshot>;
  const finalSys = detail.finalSystem;
  const finalActions = finalSnap.actions ?? [];
  const finalConnections = finalSnap.connections ?? { established: 0, failed: 0, dropped: 0 };
  const finalTotalActions = finalSnap.totalActions ?? 0;
  const finalUptimeSeconds = finalSnap.uptimeSeconds ?? 0;
  const cs = detail.configSummary;
  const failed = detail.state === 'failed';

  return (
    <div className="hp-detail-root">
      {/* ── 紧凑头部 ── */}
      <div className="hp-glass hp-detail-header">
        <div className="hp-hero-banner__header">
          <div>
            <div className="hp-hero-banner__title">{detail.name}</div>
            <div className="hp-hero-banner__id-line">
              <code>{detail.id.slice(0, 8)}</code> · {formatDuration(detail.durationSec)} · {detail.totalBots} 机器人 · {detail.agentCount} 节点 · 并发 {cs.concurrency} · 超时 {cs.timeoutSec}s
            </div>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span className="hp-chip" style={{
              color: failed ? 'var(--color-error)' : 'var(--color-success)',
              background: failed ? 'color-mix(in srgb, var(--color-error) 10%, transparent)' : 'color-mix(in srgb, var(--color-success) 10%, transparent)',
              fontSize: 11, lineHeight: '20px', padding: '0 8px',
            }}>
              {failed ? '失败' : '完成'}
            </span>
            <Tooltip title="下载完整配置归档 JSON">
              <Button size="small" icon={<DownloadOutlined />} onClick={downloadConfig}>下载</Button>
            </Tooltip>
            <Tooltip title="将此记录的配置克隆为新任务">
              <Button size="small" icon={<CopyOutlined />} onClick={cloneTask}>克隆</Button>
            </Tooltip>
          </div>
        </div>
        {detail.errorMsg && (
          <div className="hp-hero-banner__error">
            <pre style={{ margin: 0 }}>{detail.errorMsg}</pre>
          </div>
        )}
      </div>

      {/* ── 运行趋势 — 全宽 ── */}
      {trendsOption && (
        <div className="hp-glass hp-glass-thin hp-trends-card">
          <div className="hp-section-title">运行趋势</div>
          <ReactECharts option={trendsOption} style={{ height: 220 }} notMerge lazyUpdate />
        </div>
      )}

      {/* ── 动作汇总表 — 全宽主角 ── */}
      <div className="hp-glass hp-actions-card">
        <div className="hp-actions-card__header">
          <div className="hp-section-title" style={{ marginBottom: 0 }}>
            动作汇总（{finalActions.length} 类）
          </div>
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
        </div>
        {actionTable.dataSource.length === 0 ? (
          <Empty description={finalActions.length === 0 ? '无最终动作数据' : '无符合条件的记录'} />
        ) : (
          <Table<ActionMetric>
            rowKey="name"
            size="small"
            dataSource={actionTable.dataSource}
            columns={actionTable.columns}
            pagination={false}
            scroll={{ x: 'max-content', y: 400 }}
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
      </div>

      {/* ── 节点事件 + 节点结果 ── */}
      {(detail.agentEvents?.length ?? 0) > 0 && (
        <div className="hp-glass hp-glass-thin" style={{ padding: 16 }}>
          <div className="hp-section-title">节点事件</div>
          <Timeline
            style={{ marginTop: 8 }}
            items={detail.agentEvents!.map((evt, i) => ({
              key: i,
              color: evt.type === 'offline' ? 'red' : evt.type === 'reconnected' ? 'green' : 'gray',
              children: (
                <span style={{ fontSize: 12 }}>
                  <Tag color={evt.type === 'offline' ? 'error' : evt.type === 'reconnected' ? 'success' : 'default'} style={{ marginInlineEnd: 4 }}>
                    {evt.type === 'offline' ? '离线' : evt.type === 'reconnected' ? '恢复' : '注销'}
                  </Tag>
                  <strong>{evt.agentName || evt.agentId}</strong>
                  <span style={{ color: 'var(--text-tertiary)', marginLeft: 8 }}>
                    {dayjs(evt.timestamp).format('HH:mm:ss')}
                  </span>
                  {evt.detail && <span style={{ color: 'var(--text-tertiary)', marginLeft: 8 }}>({evt.detail})</span>}
                </span>
              ),
            }))}
          />
        </div>
      )}

      {detail.agentReports && detail.agentReports.length > 0 && (
        <div className="hp-glass hp-glass-thin" style={{ padding: 16 }}>
          <div className="hp-section-title">节点结果</div>
          <Table
            size="small"
            style={{ marginTop: 8 }}
            dataSource={detail.agentReports.map((r, i) => ({ ...r, key: i }))}
            pagination={false}
            columns={[
              { title: '节点', dataIndex: 'agentName', key: 'agentName', width: 160, render: (v: string, r: any) => v || r.agentId },
              {
                title: '结果', dataIndex: 'result', key: 'result', width: 100,
                render: (v: string) => {
                  const map: Record<string, { color: string; label: string }> = {
                    completed: { color: 'success', label: '完成' },
                    stopped: { color: 'default', label: '停止' },
                    failed: { color: 'error', label: '失败' },
                  };
                  const info = map[v] ?? { color: 'default', label: v };
                  return <Tag color={info.color}>{info.label}</Tag>;
                },
              },
              {
                title: '完成时间', dataIndex: 'finishedAt', key: 'finishedAt', width: 140,
                render: (v: string) => v ? dayjs(v).format('HH:mm:ss') : '—',
              },
              {
                title: '错误信息', dataIndex: 'errorMsg', key: 'errorMsg', ellipsis: true,
                render: (v: string) => v ? <Tooltip title={v}><span style={{ color: 'var(--color-error)' }}>{v}</span></Tooltip> : '—',
              },
            ]}
          />
        </div>
      )}

      {/* ── 底部双栏：备注 | 快照+配置+时间 ── */}
      <div className="hp-detail-bottom">
        {/* 备注与标签 */}
        <div className="hp-glass hp-glass-thin hp-notes-card">
          <div className="hp-section-title">备注与标签</div>
          <Input
            placeholder="按 Enter 添加标签"
            onPressEnter={(e) => {
              const v = (e.target as HTMLInputElement).value.trim();
              if (v && !tags.includes(v)) setTags([...tags, v]);
              (e.target as HTMLInputElement).value = '';
            }}
          />
          {tags.length > 0 && (
            <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', marginTop: 8 }}>
              {tags.map((t) => (
                <Tag key={t} closable onClose={() => setTags(tags.filter((x) => x !== t))}>{t}</Tag>
              ))}
            </div>
          )}
          <Input.TextArea
            placeholder="备注（任意文本，对比时可见）"
            rows={3}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            style={{ marginTop: 10 }}
          />
          <Button type="primary" size="small" onClick={saveMeta} style={{ marginTop: 8 }}>保存</Button>
        </div>

        {/* 快照 · 配置 · 时间 */}
        <div className="hp-glass hp-glass-thin hp-info-card">
          <div className="hp-section-title">集群快照</div>
          <div className="hp-hero-row" style={{ marginTop: 4 }}>
            <div className="hp-hero-box">
              <div className="hp-hero-value hp-hero-value-sm" style={{ color: 'var(--color-blue)' }}>
                {finalTotalActions.toLocaleString()}
              </div>
              <div className="hp-hero-title">累计动作</div>
            </div>
            <div className="hp-hero-divider" />
            <div className="hp-hero-box">
              <div className="hp-hero-value hp-hero-value-sm" style={{ color: 'var(--color-purple)' }}>
                {Math.floor(finalUptimeSeconds / 60)}m
              </div>
              <div className="hp-hero-title">UPTIME</div>
            </div>
          </div>
          <div className="hp-grid" style={{ marginTop: 10 }}>
            <div className="hp-grid-item">
              <span className="hp-grid-label">错误连接</span>
              <span className="hp-grid-value" style={{ color: finalConnections.failed > 0 ? 'var(--color-error)' : undefined }}>
                {finalConnections.failed} / {finalConnections.dropped}
              </span>
            </div>
            <div className="hp-grid-item">
              <span className="hp-grid-label">建连数</span>
              <span className="hp-grid-value">{finalConnections.established}</span>
            </div>
            <div className="hp-grid-item">
              <span className="hp-grid-label">CPU%</span>
              <span className="hp-grid-value">{finalSys ? `${(finalSys.avgCpuPercent ?? 0).toFixed(1)}%` : '—'}</span>
            </div>
            <div className="hp-grid-item">
              <span className="hp-grid-label">动作类型</span>
              <span className="hp-grid-value">{finalActions.length}</span>
            </div>
          </div>

          <div className="hp-section-title" style={{ marginTop: 12 }}>配置</div>
          <div className="hp-grid">
            <div className="hp-grid-item">
              <span className="hp-grid-label">并发</span>
              <span className="hp-grid-value">{cs.concurrency}</span>
            </div>
            <div className="hp-grid-item">
              <span className="hp-grid-label">超时</span>
              <span className="hp-grid-value">{cs.timeoutSec}s</span>
            </div>
            <div className="hp-grid-item">
              <span className="hp-grid-label">流程</span>
              <span className="hp-grid-value">{cs.flowSizeKB}KB</span>
            </div>
            <div className="hp-grid-item">
              <span className="hp-grid-label">脚本</span>
              <span className="hp-grid-value">{cs.scriptCount} 个</span>
            </div>
          </div>

          <div className="hp-section-title" style={{ marginTop: 12 }}>时间</div>
          <div className="hp-grid">
            <div className="hp-grid-item">
              <span className="hp-grid-label">开始</span>
              <span className="hp-grid-value">{detail.startedAt ? dayjs(detail.startedAt).format('MM-DD HH:mm') : '—'}</span>
            </div>
            <div className="hp-grid-item">
              <span className="hp-grid-label">结束</span>
              <span className="hp-grid-value">{detail.stoppedAt ? dayjs(detail.stoppedAt).format('MM-DD HH:mm') : '—'}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`;
  return `${(sec / 3600).toFixed(1)}h`;
}
