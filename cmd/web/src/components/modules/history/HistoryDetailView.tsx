/**
 * 历史记录详情：紧凑头部 + 全宽趋势 + 全宽动作表 + 底部信息条。
 */

import { App, Button, Empty, Input, Spin, Table, Tag, Timeline, Tooltip } from 'antd';
import { CopyOutlined, DownloadOutlined, FileTextOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { EChartsReact } from '@/components/monitoring/shared/EChartsReact';
import { useEffect, useMemo, useState } from 'react';
import { historyApi, showApiError } from '@/services';
import type { HistoryDetail, HistoryConfigSummary, HistoryTrendPoint } from '@/types/api';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { useFloatingWindowStore } from '@/components/FlowEditor/store/floatingWindowStore';
import { ActionMetricsTable } from '@/components/monitoring/shared/ActionMetricsTable';
import { useReportCapture } from './report/useReportCapture';
import './HistoryPanel.css';

export interface HistoryDetailViewProps {
  id: string;
  onChange: () => void;
}

export function HistoryDetailView({ id, onChange }: HistoryDetailViewProps) {
  const { message } = App.useApp();
  const [detail, setDetail] = useState<HistoryDetail | null>(null);
  const [timeseries, setTimeseries] = useState<{ points: HistoryTrendPoint[]; sampled: boolean; originalCount: number } | null>(null);
  const [loading, setLoading] = useState(true);
  const [note, setNote] = useState('');
  const [tags, setTags] = useState<string[]>([]);
  const generateReport = useReportCapture(detail, timeseries);
  const [configInfo, setConfigInfo] = useState<HistoryConfigSummary | null>(null);
  const [stagesExpanded, setStagesExpanded] = useState(false);

  useEffect(() => {
    setLoading(true);
    Promise.all([historyApi.getHistory(id), historyApi.getHistoryTimeseries(id)])
      .then(([d, t]) => {
        setDetail(d);
        setTimeseries({ points: t?.points ?? [], sampled: t?.sampled ?? false, originalCount: t?.originalCount ?? 0 });
        setNote(d.note ?? '');
        setTags(d.tags ?? []);
      })
      .catch(showApiError)
      .finally(() => setLoading(false));
  }, [id]);

  useEffect(() => {
    setConfigInfo(null);
    setStagesExpanded(false);
    historyApi.getHistoryConfig(id).then(setConfigInfo).catch(() => {});
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
      const archive = await historyApi.getHistoryConfigArchive(id);
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

  const theme = useEditorStore((s) => s.theme);
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  const { qpsOption, apdexOption, cpuOption, bwOption } = useMemo(() => {
    const points = timeseries?.points ?? [];
    const hasPoints = points.length > 0;

    const x = points.map((p) => `${p.elapsedSec}s`);
    const qps = points.map((p) => +p.totalQps.toFixed(2));
    const hasTotalDurationApdex = points.some((p) => p.totalDurationApdex !== null && Number.isFinite(p.totalDurationApdex));
    const hasRttApdex = points.some((p) => p.rttApdex !== null && Number.isFinite(p.rttApdex));
    const totalDurationApdex = points.map((p) =>
      p.totalDurationApdex !== null && Number.isFinite(p.totalDurationApdex) ? +p.totalDurationApdex.toFixed(3) : null,
    );
    const rttApdex = points.map((p) =>
      p.rttApdex !== null && Number.isFinite(p.rttApdex) ? +p.rttApdex.toFixed(3) : null,
    );
    const sendKBps = points.map((p) => +p.sendKBps.toFixed(2));
    const recvKBps = points.map((p) => +p.recvKBps.toFixed(2));
    const cpu = points.map((p) => +p.avgCpuPercent.toFixed(2));

    const isDark = theme === 'dark';
    const root = document.documentElement;
    const css = (v: string, fb: string) => getComputedStyle(root).getPropertyValue(v).trim() || fb;
    const axisLine = isDark ? 'rgba(255,255,255,0.25)' : 'rgba(0,0,0,0.15)';
    const labelClr = isDark ? 'rgba(255,255,255,0.65)' : 'rgba(0,0,0,0.65)';
    const splitClr = isDark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.06)';
    const tipBg = isDark ? 'rgba(20,20,28,0.92)' : 'rgba(255,255,255,0.96)';
    const tipBorder = isDark ? 'rgba(255,255,255,0.12)' : 'rgba(0,0,0,0.09)';
    const tipText = isDark ? '#e0e0e0' : '#333';

    const tooltip = {
      trigger: 'axis' as const,
      backgroundColor: tipBg,
      borderColor: tipBorder,
      textStyle: { color: tipText, fontSize: 11 },
    };

    const line = (series: Array<{ name: string; data: Array<number | null>; color: string }>, x: string[], yMax?: number) => ({
      tooltip,
      legend: { right: 0, top: 0, textStyle: { fontSize: 11, color: labelClr } },
      grid: { left: 36, right: 8, top: 24, bottom: 24 },
      xAxis: { type: 'category', data: x, axisLabel: { fontSize: 10, color: labelClr }, axisLine: { lineStyle: { color: axisLine } } },
      yAxis: { type: 'value', max: yMax, axisLabel: { fontSize: 10, color: labelClr }, splitLine: { lineStyle: { color: splitClr } } },
      series: series.map((s) => ({
        name: s.name, type: 'line', smooth: true, symbol: 'none', data: s.data,
        connectNulls: false,
        areaStyle: { opacity: 0.08 }, itemStyle: { color: s.color },
      })),
    });

    const apdexSeries = [
      hasTotalDurationApdex
        ? { name: '总耗时 Apdex', data: totalDurationApdex, color: css('--chart-green', '#52c41a') }
        : null,
      hasRttApdex
        ? { name: 'RTT Apdex', data: rttApdex, color: css('--chart-red', '#f5222d') }
        : null,
    ].filter((s): s is { name: string; data: Array<number | null>; color: string } => s !== null);

    return {
      qpsOption: hasPoints ? line([{ name: 'QPS', data: qps, color: css('--chart-blue', '#1677ff') }], x) : null,
      apdexOption: hasPoints && apdexSeries.length > 0 ? line(apdexSeries, x, 1) : null,
      cpuOption: hasPoints ? line([{ name: 'CPU%', data: cpu, color: css('--chart-orange', '#fa8c16') }], x) : null,
      bwOption: hasPoints ? line([
        { name: '↑ 发送', data: sendKBps, color: css('--chart-cyan', '#13c2c2') },
        { name: '↓ 接收', data: recvKBps, color: css('--chart-purple', '#722ed1') },
      ], x) : null,
    };
  }, [timeseries, theme]);


  if (loading) return <Spin />;
  if (!detail) return <Empty description="加载失败" />;

  const finalSnap = detail.finalSnapshot;
  const finalSys = detail.finalSystem;
  const finalActions = finalSnap.actions;
  const finalConnections = finalSnap.connections;
  const finalTotalActions = finalSnap.totalActions;
  const finalUptimeSeconds = finalSnap.uptimeSeconds;
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
              <code>{detail.id.slice(0, 8)}</code> · {formatDuration(detail.durationSec)} · {detail.totalBots} 机器人 · {detail.activeAgentCount}/{detail.agentCount} 节点 · {detail.stageCount && detail.stageCount > 0 ? `渐进式 ${detail.stageCount} 阶段 · ` : ''}并发 {cs.concurrency} · 超时 {cs.timeoutSec}s
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
            <Tooltip title="生成压测报告（在新标签页打开，可保存为 PDF）">
              <Button size="small" type="primary" ghost icon={<FileTextOutlined />} onClick={generateReport}>报告</Button>
            </Tooltip>
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

      {/* ── 运行趋势 — 2×2 网格 ── */}
      {(qpsOption || cpuOption) && (
        <>
          {timeseries?.sampled && (
            <div style={{ color: 'var(--text-tertiary)', fontSize: 12, marginBottom: 8 }}>
              趋势图已降采样展示：{timeseries.points.length} / {timeseries.originalCount} 个采样点
            </div>
          )}
          <div className="hp-trends-grid">
          {qpsOption && (
            <div className="hp-glass hp-glass-thin hp-trends-card">
              <div className="hp-section-title">QPS</div>
              <EChartsReact option={qpsOption} style={{ height: 180 }} notMerge lazyUpdate />
            </div>
          )}
          {apdexOption && (
            <div className="hp-glass hp-glass-thin hp-trends-card">
              <div className="hp-section-title">Apdex</div>
              <EChartsReact option={apdexOption} style={{ height: 180 }} notMerge lazyUpdate />
            </div>
          )}
          {cpuOption && (
            <div className="hp-glass hp-glass-thin hp-trends-card">
              <div className="hp-section-title">CPU</div>
              <EChartsReact option={cpuOption} style={{ height: 180 }} notMerge lazyUpdate />
            </div>
          )}
          {bwOption && (
            <div className="hp-glass hp-glass-thin hp-trends-card">
              <div className="hp-section-title">带宽 (KB/s)</div>
              <EChartsReact option={bwOption} style={{ height: 180 }} notMerge lazyUpdate />
            </div>
          )}
          </div>
        </>
      )}

      {/* ── 动作汇总表 — 全宽主角 ── */}
      <div className="hp-glass hp-actions-card">
        <div className="hp-actions-card__header">
          <div className="hp-section-title" style={{ marginBottom: 0 }}>
            动作汇总（{finalActions.length} 类）
          </div>
        </div>
        {finalActions.length === 0 ? (
          <Empty description="无最终动作数据" />
        ) : (
          <ActionMetricsTable
            rows={finalActions}
            size="small"
            scrollY={400}
            popupZIndex={popupZ}
            showClientBreakdown
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
              color:
                evt.type === 'offline' || evt.type === 'restarted'
                  ? 'red'
                  : evt.type === 'reconnected'
                  ? 'green'
                  : 'gray',
              children: (
                <span style={{ fontSize: 12 }}>
                  <Tag
                    color={
                      evt.type === 'offline'
                        ? 'error'
                        : evt.type === 'restarted'
                        ? 'warning'
                        : evt.type === 'reconnected'
                        ? 'success'
                        : 'default'
                    }
                    style={{ marginInlineEnd: 4 }}
                  >
                    {evt.type === 'offline'
                      ? '离线'
                      : evt.type === 'restarted'
                      ? '重启丢任务'
                      : evt.type === 'reconnected'
                      ? '恢复'
                      : '注销'}
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

          {configInfo?.robotConfig?.rampUp && (() => {
            const stages = configInfo.robotConfig.rampUp.stages;
            const total = stages.reduce((s, st) => s + (st.count || 0), 0);
            return (
              <div className="hp-rampup-section">
                <div className="hp-rampup-header" onClick={() => setStagesExpanded(!stagesExpanded)}>
                  <span className="hp-section-title" style={{ marginBottom: 0 }}>
                    渐进式加压 · {stages.length} 阶段 · 总计 {total} 机器人
                  </span>
                  <span className={`hp-rampup-chevron${stagesExpanded ? ' expanded' : ''}`}>▸</span>
                </div>
                {stagesExpanded && (
                  <div className="hp-rampup-timeline">
                    {stages.map((stage, i) => (
                      <div key={i} className="hp-rampup-stage">
                        <div className="hp-rampup-dot">{i + 1}</div>
                        {i < stages.length - 1 && <div className="hp-rampup-line" />}
                        <div className="hp-rampup-stage-info">
                          <span className="hp-rampup-count">+{stage.count} 机器人</span>
                          {stage.concurrency ? <span>并发 {stage.concurrency}</span> : null}
                          {stage.holdSec ? <span>保持 {stage.holdSec}s</span> : null}
                          {stage.reset && <Tag color="warning" style={{ marginInlineEnd: 0, fontSize: 10, lineHeight: '16px', padding: '0 4px' }}>重置</Tag>}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            );
          })()}

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
