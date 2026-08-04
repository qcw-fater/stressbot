import { App, Button, Empty, Input, Spin, Table, Tag, Timeline, Tooltip } from 'antd';
import {
  BugOutlined,
  CheckCircleOutlined,
  CopyOutlined,
  DownloadOutlined,
  FileTextOutlined,
  StarFilled,
  StarOutlined,
} from '@ant-design/icons';
import dayjs from 'dayjs';
import { EChartsReact } from '@/components/monitoring/shared/EChartsReact';
import { useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import type { EChartsOption } from 'echarts';
import { historyApi, showApiError } from '@/services';
import type {
  CleanupStatus,
  HistoryActionMetric,
  HistoryAgentReport,
  HistoryConfigSummary,
  HistoryDetail,
  HistoryTrendPoint,
} from '@/types/api';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { useFloatingWindowStore } from '@/components/FlowEditor/store/floatingWindowStore';
import { ActionMetricsTable, resolveKind } from '@/components/monitoring/shared/ActionMetricsTable';
import { fmtBytes, fmtMs } from '@/components/monitoring/shared/formats';
import { useReportCapture } from './report/useReportCapture';
import { formatStageLabel } from './stageLabel';
import './HistoryPanel.css';

export interface HistoryDetailViewProps {
  id: string;
  /** 阶段段落号（>0 时展示该 reset 段落详情）。 */
  stageIndex?: number;
  /** 阶段段落标签，如「第 2 轮 · S3-S4」。 */
  stageLabel?: string;
  onChange: () => void;
}

interface MetricTileProps {
  label: string;
  value: string;
  sub?: string;
  tone?: 'good' | 'warn' | 'bad' | 'blue' | 'purple';
}

interface ActionInsight {
  label: string;
  name: string;
  value: string;
  tone?: 'good' | 'warn' | 'bad' | 'blue' | 'purple';
}

interface DerivedSummary {
  totalSamples: number;
  totalSuccess: number;
  totalFailures: number;
  totalTimeouts: number;
  totalCanceled: number;
  totalErrors: number;
  successRate: number;
  rttApdex: number | null;
  avgQps: number;
  peakQps: number;
  peakCpu: number;
  peakAvgMem: number;
  peakMaxMem: number;
  peakBotsRunning: number;
  peakBotsErrored: number;
  offlinePoints: number;
  cleanupIssueCount: number;
  failedAgents: number;
  slowestAction?: HistoryActionMetric;
  worstApdexAction?: HistoryActionMetric;
  mostFailedAction?: HistoryActionMetric;
  busiestAction?: HistoryActionMetric;
}

export function HistoryDetailView({
  id,
  stageIndex,
  stageLabel,
  onChange,
}: HistoryDetailViewProps) {
  const { message } = App.useApp();
  const isStageView = (stageIndex ?? -1) > 0;
  const [detail, setDetail] = useState<HistoryDetail | null>(null);
  const [timeseries, setTimeseries] = useState<{
    points: HistoryTrendPoint[];
    sampled: boolean;
    originalCount: number;
  } | null>(null);
  const [loading, setLoading] = useState(true);
  const [note, setNote] = useState('');
  const [tags, setTags] = useState<string[]>([]);
  const [tagInput, setTagInput] = useState('');
  const [draggingTagIndex, setDraggingTagIndex] = useState<number | null>(null);
  const [dragOverTagIndex, setDragOverTagIndex] = useState<number | null>(null);
  const generateReport = useReportCapture(detail, timeseries);
  const [configInfo, setConfigInfo] = useState<HistoryConfigSummary | null>(null);
  const [stagesExpanded, setStagesExpanded] = useState(false);

  useEffect(() => {
    setLoading(true);
    Promise.all([
      historyApi.getHistory(id, stageIndex),
      historyApi.getHistoryTimeseries(id, 1200, stageIndex),
    ])
      .then(([d, t]) => {
        setDetail(d);
        setTimeseries({
          points: t?.points ?? [],
          sampled: t?.sampled ?? false,
          originalCount: t?.originalCount ?? 0,
        });
        setNote(d.note ?? '');
        setTags(d.tags ?? []);
        setTagInput('');
        setDraggingTagIndex(null);
        setDragOverTagIndex(null);
      })
      .catch(showApiError)
      .finally(() => setLoading(false));
  }, [id, stageIndex]);

  useEffect(() => {
    setConfigInfo(null);
    setStagesExpanded(false);
    historyApi
      .getHistoryConfig(id)
      .then(setConfigInfo)
      .catch(() => {});
  }, [id]);

  const persistMeta = async (nextNote: string, nextTags: string[]) => {
    try {
      const updated = await historyApi.updateHistory(
        id,
        { note: nextNote, tags: nextTags },
        stageIndex,
      );
      setDetail(updated);
      setNote(updated.note ?? '');
      setTags(updated.tags ?? []);
      message.success('已保存');
      onChange();
    } catch (err) {
      showApiError(err);
    }
  };

  const toggleStar = async () => {
    if (!detail) return;
    try {
      const updated = await historyApi.updateHistory(id, { starred: !detail.starred }, stageIndex);
      setDetail(updated);
      onChange();
    } catch (err) {
      showApiError(err);
    }
  };

  const saveMeta = () => {
    void persistMeta(note, tags);
  };

  const reorderTags = (from: number, to: number) => {
    if (from === to || from < 0 || to < 0 || from >= tags.length || to >= tags.length) return;
    const nextTags = [...tags];
    const [moved] = nextTags.splice(from, 1);
    nextTags.splice(to, 0, moved);
    setTags(nextTags);
    setDraggingTagIndex(null);
    setDragOverTagIndex(null);
    void persistMeta(note, nextTags);
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
  const stageMarks = useMemo(
    () =>
      computeStageLines(
        timeseries?.points ?? [],
        configInfo?.robotConfig?.rampUp ?? null,
        isStageView,
        { from: detail?.stageFrom, to: detail?.stageTo },
      ),
    [timeseries, configInfo, isStageView, detail?.stageFrom, detail?.stageTo],
  );
  const chartOptions = useMemo(
    () => buildChartOptions(timeseries?.points ?? [], theme, stageMarks),
    [timeseries, theme, stageMarks],
  );

  if (loading) return <Spin />;
  if (!detail) return <Empty description="加载失败" />;

  const finalSnap = detail.finalSnapshot;
  const finalSys = detail.finalSystem;
  const finalActions = finalSnap.actions;
  const finalConnections = finalSnap.connections;
  const finalRobots = finalSnap.robots || {
    started: detail.totalBots,
    running: 0,
    stopped: 0,
    errored: 0,
  };
  const finalBandwidth = finalSnap.bandwidth || {
    totalSendBytes: 0,
    totalRecvBytes: 0,
    sendMBps: 0,
    recvMBps: 0,
  };
  const cs = detail.configSummary;
  const failed = detail.state === 'failed';
  const summary = deriveSummary(detail, timeseries?.points ?? []);
  const agentEvents = detail.agentEvents ?? [];
  const agentReports = detail.agentReports ?? [];
  const diagnostics = buildDiagnostics(detail, summary, timeseries?.points ?? []);
  const actionInsights = buildActionInsights(summary);

  return (
    <div className="hp-detail-root hp-report-root">
      <section className={`hp-report-hero hp-glass${failed ? ' hp-report-hero--failed' : ''}`}>
        <div className="hp-report-hero__main">
          <div className="hp-report-hero__eyebrow">
            <span className={`hp-report-state hp-report-state--${failed ? 'bad' : 'good'}`}>
              {failed ? '失败' : '完成'}
            </span>
            <Tag
              className={`hp-report-mode-badge hp-report-mode-badge--${detail.debugMode ? 'debug' : 'test'}`}
              icon={detail.debugMode ? <BugOutlined /> : <CheckCircleOutlined />}
            >
              {detail.debugMode ? '调试' : '测试'}
            </Tag>
            <code>#{detail.id.slice(0, 8)}</code>
            {isStageView && (
              <Tag className="hp-report-stage-badge" color="warning">
                {formatStageLabel(stageLabel || detail.stageLabel, stageIndex)}
              </Tag>
            )}
            <span>
              {detail.startedAt
                ? dayjs(detail.startedAt).format('YYYY-MM-DD HH:mm')
                : '未记录开始时间'}
            </span>
            <span>耗时 {formatDuration(detail.durationSec)}</span>
          </div>
          <div className="hp-report-hero__title">{detail.name}</div>
          <div className="hp-report-hero__summary">
            总动作 {finalSnap.totalActions.toLocaleString()} · 成功率{' '}
            {fmtPercent(summary.successRate)} · QPS {summary.avgQps.toFixed(1)} · 失败{' '}
            {summary.totalErrors.toLocaleString()} · CPU {finalSys.avgCpuPercent.toFixed(1)}% /{' '}
            {summary.peakCpu.toFixed(1)}%
          </div>
        </div>
        <div className="hp-report-hero__actions">
          <Tooltip
            title={
              detail.starred
                ? isStageView
                  ? '取消收藏本段'
                  : '取消收藏'
                : isStageView
                  ? '收藏本段'
                  : '收藏'
            }
          >
            <Button
              size="small"
              icon={
                detail.starred ? (
                  <StarFilled style={{ color: 'var(--color-warning)' }} />
                ) : (
                  <StarOutlined />
                )
              }
              onClick={toggleStar}
            />
          </Tooltip>
          <Tooltip title="生成压测报告（在新标签页打开，可保存为 PDF）">
            <Button
              size="small"
              type="primary"
              ghost
              icon={<FileTextOutlined />}
              onClick={generateReport}
            >
              报告
            </Button>
          </Tooltip>
          <Tooltip title="下载完整任务配置归档 JSON（含全部阶段）">
            <Button size="small" icon={<DownloadOutlined />} onClick={downloadConfig}>
              下载
            </Button>
          </Tooltip>
          <Tooltip title="将完整任务配置（含全部阶段）克隆为新任务">
            <Button size="small" icon={<CopyOutlined />} onClick={cloneTask}>
              克隆
            </Button>
          </Tooltip>
        </div>
        {detail.errorMsg && (
          <div className="hp-hero-banner__error hp-report-error">
            <pre style={{ margin: 0 }}>{detail.errorMsg}</pre>
          </div>
        )}
      </section>

      <section className="hp-report-kpis">
        <MetricTile
          label="累计动作"
          value={finalSnap.totalActions.toLocaleString()}
          sub={`${finalActions.length} 类动作`}
          tone="blue"
        />
        <MetricTile
          label="整体成功率"
          value={fmtPercent(summary.successRate)}
          sub={`${summary.totalSuccess.toLocaleString()} 成功 / ${summary.totalSamples.toLocaleString()} 样本`}
          tone={summary.successRate >= 0.95 ? 'good' : summary.successRate >= 0.8 ? 'warn' : 'bad'}
        />
        <MetricTile
          label="RTT Apdex"
          value={fmtScore(summary.rttApdex)}
          sub={`阈值 T=${finalSnap.apdexT}ms`}
          tone={
            (summary.rttApdex ?? 0) >= 0.9
              ? 'good'
              : (summary.rttApdex ?? 0) >= 0.75
                ? 'warn'
                : 'bad'
          }
        />
        <MetricTile
          label="峰值 QPS"
          value={summary.peakQps.toFixed(1)}
          sub={`全程均值 ${summary.avgQps.toFixed(1)}`}
          tone="purple"
        />
        <MetricTile
          label="机器人"
          value={`${finalRobots.running}/${finalRobots.started}`}
          sub={`异常 ${Math.max(finalRobots.errored, summary.peakBotsErrored)}`}
          tone={Math.max(finalRobots.errored, summary.peakBotsErrored) > 0 ? 'bad' : 'good'}
        />
        <MetricTile
          label="连接"
          value={finalConnections.established.toLocaleString()}
          sub={`失败 ${finalConnections.failed} / 断开 ${finalConnections.dropped}`}
          tone={finalConnections.failed + finalConnections.dropped > 0 ? 'warn' : 'good'}
        />
        <MetricTile
          label="CPU"
          value={`均值 ${finalSys.avgCpuPercent.toFixed(1)}%`}
          sub={`最高节点 ${summary.peakCpu.toFixed(1)}%`}
          tone={summary.peakCpu >= 80 ? 'bad' : summary.peakCpu >= 60 ? 'warn' : 'blue'}
        />
        <MetricTile
          label="MEM"
          value={`集群 ${summary.peakAvgMem.toFixed(1)}%`}
          sub={`最高节点 ${summary.peakMaxMem.toFixed(1)}%`}
          tone={summary.peakMaxMem >= 85 ? 'bad' : summary.peakMaxMem >= 70 ? 'warn' : 'blue'}
        />
      </section>

      {timeseries?.sampled && (
        <div className="hp-sampled-note">
          趋势图已降采样展示：{timeseries.points.length} / {timeseries.originalCount} 个采样点
        </div>
      )}

      <ReportSection title="负载与性能趋势" subtitle="从历史采样中复盘压测过程、阶段推进与延迟变化">
        <div className="hp-report-grid hp-report-grid--charts">
          {/* 固定 8 格。监听等待占掉了原「节点在线」的位置——那张图的信息量本来就
              只有"有没有掉过节点"，并进机器人卡的角标即可，不值一格。 */}
          <TrendCard
            title="机器人"
            option={chartOptions.loadOption}
            value={`峰值 ${summary.peakBotsRunning.toLocaleString()} · ${summary.offlinePoints > 0 ? `${summary.offlinePoints} 个采样节点异常` : '节点全程在线'}`}
          />
          <TrendCard
            title="QPS"
            option={chartOptions.qpsOption}
            value={`峰值 ${summary.peakQps.toFixed(1)}`}
          />
          <TrendCard
            title="RTT Apdex"
            option={chartOptions.apdexOption}
            value={fmtScore(summary.rttApdex)}
          />
          <TrendCard title="监听等待" option={chartOptions.listenWaitOption} value="P99" />
          <TrendCard title="客户端成本" option={chartOptions.costOption} value="编码 / 解码" />
          <TrendCard
            title="CPU"
            option={chartOptions.cpuOption}
            value={`最高节点 ${summary.peakCpu.toFixed(1)}%`}
          />
          <TrendCard
            title="MEM"
            option={chartOptions.memOption}
            value={`最高节点 ${summary.peakMaxMem.toFixed(1)}%`}
          />
          <TrendCard
            title="带宽"
            option={chartOptions.bandwidthOption}
            value={`${fmtKBpsPeak(timeseries?.points ?? [], 'sendKBps')} 峰值`}
          />
        </div>
      </ReportSection>

      <ReportSection
        title="动作分析"
        subtitle="最终动作快照聚合，表格支持主指标/总耗时切换与耗时拆分"
      >
        {actionInsights.length > 0 && (
          <div className="hp-insight-strip">
            {actionInsights.map((item) => (
              <div key={item.label} className={`hp-insight hp-insight--${item.tone ?? 'blue'}`}>
                <span className="hp-insight__label">{item.label}</span>
                <Tooltip title={item.name} mouseEnterDelay={0.4}>
                  <span className="hp-insight__name">{item.name}</span>
                </Tooltip>
                <span className="hp-insight__value">{item.value}</span>
              </div>
            ))}
          </div>
        )}
        {finalActions.length === 0 ? (
          <Empty description="无最终动作数据" />
        ) : (
          <ActionMetricsTable
            rows={finalActions}
            size="small"
            scrollY={420}
            popupZIndex={popupZ}
            showClientBreakdown
            showCsvExport
            timingDetail={finalSnap.timingDetail}
          />
        )}
      </ReportSection>

      <ReportSection
        title="错误与稳定性"
        subtitle="从错误分布、连接、节点事件与清理结果中提取风险信号"
      >
        <div className="hp-finding-list">
          {diagnostics.map((item) => (
            <div
              key={`${item.label}:${item.value}`}
              className={`hp-finding-item hp-finding-item--${item.tone}`}
            >
              <span className="hp-finding-item__label">{item.label}</span>
              <span className="hp-finding-item__value">{item.value}</span>
            </div>
          ))}
        </div>
      </ReportSection>

      <ReportSection
        title="节点健康"
        subtitle={`${agentReports.length} 个节点结果，${agentEvents.length} 条节点事件`}
      >
        <div className="hp-health-grid">
          <MetricTile
            label="节点完成"
            value={`${agentReports.filter((r) => r.result === 'completed').length}/${agentReports.length}`}
            sub={`失败 ${summary.failedAgents}`}
            tone={summary.failedAgents > 0 ? 'bad' : 'good'}
          />
          <MetricTile
            label="清理异常"
            value={`${summary.cleanupIssueCount}`}
            sub="资源回收状态"
            tone={summary.cleanupIssueCount > 0 ? 'warn' : 'good'}
          />
          <MetricTile
            label="离线事件"
            value={`${agentEvents.filter((e) => e.type === 'offline').length}`}
            sub="运行期间节点变化"
            tone={agentEvents.some((e) => e.type === 'offline') ? 'bad' : 'good'}
          />
        </div>
        {agentEvents.length > 0 && (
          <Timeline
            className="hp-agent-timeline"
            items={agentEvents.map((evt, i) => ({
              key: i,
              color:
                evt.type === 'offline' || evt.type === 'restarted'
                  ? 'red'
                  : evt.type === 'reconnected'
                    ? 'green'
                    : 'gray',
              children: (
                <span style={{ fontSize: 12 }}>
                  <Tag color={eventColor(evt.type)} style={{ marginInlineEnd: 4 }}>
                    {eventLabel(evt.type)}
                  </Tag>
                  <strong>{evt.agentName || evt.agentId}</strong>
                  <span style={{ color: 'var(--text-tertiary)', marginLeft: 8 }}>
                    {dayjs(evt.timestamp).format('HH:mm:ss')}
                  </span>
                  {evt.detail && (
                    <span style={{ color: 'var(--text-tertiary)', marginLeft: 8 }}>
                      ({evt.detail})
                    </span>
                  )}
                </span>
              ),
            }))}
          />
        )}
        {agentReports.length > 0 && (
          <Table<HistoryAgentReport & { key: number }>
            size="small"
            className="hp-agent-table"
            dataSource={agentReports.map((r, i) => ({ ...r, key: i }))}
            pagination={false}
            columns={[
              {
                title: '节点',
                dataIndex: 'agentName',
                key: 'agentName',
                width: 180,
                render: (v: string, r) => v || r.agentId,
              },
              {
                title: '结果',
                dataIndex: 'result',
                key: 'result',
                width: 100,
                render: (v: string) => <Tag color={resultColor(v)}>{resultLabel(v)}</Tag>,
              },
              {
                title: '完成时间',
                dataIndex: 'finishedAt',
                key: 'finishedAt',
                width: 140,
                render: (v: string) => (v ? dayjs(v).format('HH:mm:ss') : '—'),
              },
              {
                title: '清理状态',
                dataIndex: 'cleanupStatus',
                key: 'cleanupStatus',
                width: 130,
                render: (cleanup: CleanupStatus | undefined) => renderCleanup(cleanup),
              },
              {
                title: '错误信息',
                dataIndex: 'errorMsg',
                key: 'errorMsg',
                ellipsis: true,
                render: (v: string) =>
                  v ? (
                    <Tooltip title={v}>
                      <span style={{ color: 'var(--color-error)' }}>{v}</span>
                    </Tooltip>
                  ) : (
                    '—'
                  ),
              },
            ]}
          />
        )}
      </ReportSection>

      <div className="hp-detail-bottom hp-report-bottom">
        <section className="hp-glass hp-glass-thin hp-notes-card hp-report-section">
          <div className="hp-report-section__header">
            <div>
              <div className="hp-report-section__title">备注与标签</div>
              <div className="hp-report-section__subtitle">
                {isStageView ? '分属当前阶段段落，仅标记本段' : '用于标记回归、异常或测试场景'}
              </div>
            </div>
          </div>
          <Input
            placeholder="按 Enter 添加并保存标签"
            value={tagInput}
            onChange={(e) => setTagInput(e.target.value)}
            onPressEnter={() => {
              const v = tagInput.trim();
              setTagInput('');
              if (v && !tags.includes(v)) {
                const nextTags = [...tags, v];
                setTags(nextTags);
                void persistMeta(note, nextTags);
              }
            }}
          />
          {tags.length > 0 && (
            <>
              <div className="hp-tag-hint">拖动标签可调整历史列表中的展示优先级</div>
              <div className="hp-edit-tags" onDragLeave={() => setDragOverTagIndex(null)}>
                {tags.map((t, index) => (
                  <Tag
                    key={t}
                    className={`hp-edit-tag${draggingTagIndex === index ? ' hp-edit-tag--dragging' : ''}${dragOverTagIndex === index && draggingTagIndex !== index ? ' hp-edit-tag--over' : ''}`}
                    closable
                    draggable
                    onDragStart={(e) => {
                      setDraggingTagIndex(index);
                      e.dataTransfer.effectAllowed = 'move';
                      e.dataTransfer.setData('text/plain', t);
                    }}
                    onDragOver={(e) => {
                      e.preventDefault();
                      e.dataTransfer.dropEffect = 'move';
                      setDragOverTagIndex(index);
                    }}
                    onDrop={(e) => {
                      e.preventDefault();
                      if (draggingTagIndex !== null) reorderTags(draggingTagIndex, index);
                    }}
                    onDragEnd={() => {
                      setDraggingTagIndex(null);
                      setDragOverTagIndex(null);
                    }}
                    onClose={() => {
                      const nextTags = tags.filter((x) => x !== t);
                      setTags(nextTags);
                      setDraggingTagIndex(null);
                      setDragOverTagIndex(null);
                      void persistMeta(note, nextTags);
                    }}
                  >
                    {t}
                  </Tag>
                ))}
              </div>
            </>
          )}
          <Input.TextArea
            placeholder="备注（任意文本，对比时可见）"
            rows={4}
            value={note}
            onChange={(e) => setNote(e.target.value)}
            style={{ marginTop: 10 }}
          />
          <Button type="primary" size="small" onClick={saveMeta} style={{ marginTop: 8 }}>
            保存
          </Button>
        </section>

        <section className="hp-glass hp-glass-thin hp-info-card hp-report-section">
          <div className="hp-report-section__header">
            <div>
              <div className="hp-report-section__title">配置档案</div>
              <div className="hp-report-section__subtitle">任务规模、协议资源与时间信息</div>
            </div>
          </div>
          <div className="hp-config-grid">
            <Fact label="机器人" value={detail.totalBots.toLocaleString()} />
            <Fact label="并发" value={cs.concurrency.toLocaleString()} />
            <Fact label="超时" value={`${cs.timeoutSec}s`} />
            <Fact label="流程" value={`${cs.flowSizeKB}KB`} />
            <Fact label="Proto" value={`${cs.protoCount} 个`} />
            <Fact label="Lua 脚本" value={`${cs.scriptCount} 个`} />
            <Fact
              label="阶段"
              value={
                detail.stageCount && detail.stageCount > 0 ? `${detail.stageCount} 阶段` : '无'
              }
            />
            <Fact
              label="带宽"
              value={`${fmtBytes(finalBandwidth.totalSendBytes)} / ${fmtBytes(finalBandwidth.totalRecvBytes)}`}
            />
            <Fact
              label="开始"
              value={detail.startedAt ? dayjs(detail.startedAt).format('MM-DD HH:mm') : '—'}
            />
            <Fact
              label="结束"
              value={detail.stoppedAt ? dayjs(detail.stoppedAt).format('MM-DD HH:mm') : '—'}
            />
          </div>

          {configInfo?.robotConfig?.rampUp &&
            (() => {
              const stages = configInfo.robotConfig.rampUp.stages;
              const total = stages.reduce((s, st) => s + (st.count || 0), 0);
              return (
                <div className="hp-rampup-section">
                  <div
                    className="hp-rampup-header"
                    onClick={() => setStagesExpanded(!stagesExpanded)}
                  >
                    <span className="hp-section-title" style={{ marginBottom: 0 }}>
                      渐进式加压 · {stages.length} 阶段 · 总计 {total} 机器人
                    </span>
                    <span className={`hp-rampup-chevron${stagesExpanded ? ' expanded' : ''}`}>
                      ▸
                    </span>
                  </div>
                  {stagesExpanded && (
                    <div className="hp-rampup-timeline">
                      {stages.map((stage, i) => (
                        <div key={i} className="hp-rampup-stage">
                          <div className="hp-rampup-dot">{i + 1}</div>
                          {i < stages.length - 1 && <div className="hp-rampup-line" />}
                          <div className="hp-rampup-stage-info">
                            <span className="hp-rampup-count">增量 {stage.count} 机器人</span>
                            {stage.concurrency ? <span>并发 {stage.concurrency}</span> : null}
                            {stage.holdSec ? <span>保持 {stage.holdSec}s</span> : null}
                            {stage.reset && (
                              <Tag
                                color="warning"
                                style={{
                                  marginInlineEnd: 0,
                                  fontSize: 10,
                                  lineHeight: '16px',
                                  padding: '0 4px',
                                }}
                              >
                                重置
                              </Tag>
                            )}
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              );
            })()}
        </section>
      </div>
    </div>
  );
}

function MetricTile({ label, value, sub, tone = 'blue' }: MetricTileProps) {
  return (
    <div className={`hp-kpi-tile hp-kpi-tile--${tone}`}>
      <div className="hp-kpi-tile__label">{label}</div>
      <div className="hp-kpi-tile__value">{value}</div>
      {sub && <div className="hp-kpi-tile__sub">{sub}</div>}
    </div>
  );
}

function ReportSection({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle?: string;
  children: ReactNode;
}) {
  return (
    <section className="hp-glass hp-glass-thin hp-report-section">
      <div className="hp-report-section__header">
        <div>
          <div className="hp-report-section__title">{title}</div>
          {subtitle && <div className="hp-report-section__subtitle">{subtitle}</div>}
        </div>
      </div>
      {children}
    </section>
  );
}

function TrendCard({
  title,
  option,
  value,
}: {
  title: string;
  option: EChartsOption | null;
  value?: string;
}) {
  return (
    <div className="hp-chart-card">
      <div className="hp-chart-card__header">
        <span>{title}</span>
        {value && <code>{value}</code>}
      </div>
      {option ? (
        <EChartsReact option={option} style={{ height: 170 }} notMerge lazyUpdate />
      ) : (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无数据" />
      )}
    </div>
  );
}

function Fact({ label, value }: { label: string; value: string }) {
  return (
    <div className="hp-config-fact">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

interface StageMark {
  x: string;
  label: string;
  reset: boolean;
}

// computeStageLines 计算趋势图阶段切换线 / reset 段落断点。
//   - 阶段段落视图（已按段过滤）：不画线。
//   - 有 reset 整体视图：按时序 stageIndex 变化点画 RESET 断点。
//   - 无 reset 渐进式：忽略时序 stageIndex，按 rampUp 配置累计 holdSec 近似画阶段切换线。
function computeStageLines(
  points: HistoryTrendPoint[],
  rampUp: { stages: Array<{ count?: number; holdSec?: number; reset?: boolean }> } | null,
  isStageView: boolean,
  stageRange?: { from?: number; to?: number },
): StageMark[] {
  if (points.length === 0) return [];

  const nearestX = (elapsed: number): string | null => {
    let best: HistoryTrendPoint | null = null;
    let bestDiff = Infinity;
    for (const p of points) {
      const diff = Math.abs(p.elapsedSec - elapsed);
      if (diff < bestDiff) {
        bestDiff = diff;
        best = p;
      }
    }
    return best ? `${best.elapsedSec}s` : null;
  };

  const hasReset = rampUp?.stages.some((s) => s.reset) ?? false;
  const rangeFrom = stageRange?.from ?? 0;
  const rangeTo = stageRange?.to ?? 0;

  // 1) 阶段段落详情已按「轮」过滤，但同一轮可能覆盖多个配置阶段，例如第 2 轮 · S2-S3。
  // 这种情况下仍需要在该轮内部画 S3 这类阶段切换线。
  if (isStageView) {
    if (!rampUp || rangeFrom <= 0 || rangeTo <= rangeFrom) return [];
    const marks: StageMark[] = [];
    let cum = points[0]?.elapsedSec ?? 0;
    for (let stageNo = rangeFrom; stageNo < rangeTo; stageNo++) {
      cum += Math.max(rampUp.stages[stageNo - 1]?.holdSec || 0, 30);
      const x = nearestX(cum);
      if (x) marks.push({ x, label: `S${stageNo + 1}`, reset: false });
    }
    return marks;
  }

  // 2) 只有 reset 任务才把时序 stageIndex 当作「第 N 轮」段落号。
  // 无 reset 任务的历史数据即使残留了 stageIndex，也不能显示成第 52/53 轮。
  const tagged = hasReset && points.some((p) => (p.stageIndex ?? -1) > 0);
  if (tagged) {
    const marks: StageMark[] = [];
    let prev = points[0]?.stageIndex ?? -1;
    for (const p of points) {
      const cur = p.stageIndex ?? -1;
      if (cur > 0 && cur !== prev) {
        marks.push({ x: `${p.elapsedSec}s`, label: `第 ${cur} 轮`, reset: true });
        prev = cur;
      }
    }
    return marks;
  }

  // 3) 非 reset ramp-up：按配置累计 holdSec 近似画阶段切换线。
  if (!hasReset && rampUp && rampUp.stages.length > 1) {
    const marks: StageMark[] = [];
    let cum = 0;
    for (let i = 0; i < rampUp.stages.length; i++) {
      if (i > 0) {
        const x = nearestX(cum);
        if (x) marks.push({ x, label: `S${i + 1}`, reset: !!rampUp.stages[i].reset });
      }
      cum += Math.max(rampUp.stages[i].holdSec || 0, 30);
    }
    return marks;
  }
  return [];
}

function buildChartOptions(
  points: HistoryTrendPoint[],
  theme: string,
  stageMarks: StageMark[] = [],
) {
  const hasPoints = points.length > 0;
  const x = points.map((p) => `${p.elapsedSec}s`);
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
  const palette = {
    blue: css('--chart-blue', '#1677ff'),
    cyan: css('--chart-cyan', '#13c2c2'),
    green: css('--chart-green', '#52c41a'),
    orange: css('--chart-orange', '#fa8c16'),
    purple: css('--chart-purple', '#722ed1'),
    red: css('--chart-red', '#f5222d'),
    lime: css('--chart-lime', '#bae637'),
  };
  const resetClr = css('--chart-orange', '#fa8c16');
  const markLine = stageMarks.length
    ? {
        silent: true,
        symbol: 'none' as const,
        data: stageMarks.map((m) => ({
          xAxis: m.x,
          label: { formatter: m.label, fontSize: 9, color: m.reset ? resetClr : labelClr },
          lineStyle: {
            color: m.reset ? resetClr : axisLine,
            type: 'dashed' as const,
            width: m.reset ? 1.5 : 1,
          },
        })),
      }
    : undefined;
  const line = (
    series: Array<{
      name: string;
      data: Array<number | null>;
      color: string;
      dashed?: boolean;
      area?: number;
    }>,
    yMax?: number,
  ): EChartsOption => ({
    tooltip,
    legend: { right: 0, top: 0, textStyle: { fontSize: 10, color: labelClr } },
    grid: { left: 34, right: 8, top: 26, bottom: 22 },
    xAxis: {
      type: 'category',
      data: x,
      axisLabel: { fontSize: 10, color: labelClr },
      axisLine: { lineStyle: { color: axisLine } },
    },
    yAxis: {
      type: 'value',
      max: yMax,
      axisLabel: { fontSize: 10, color: labelClr },
      splitLine: { lineStyle: { color: splitClr } },
    },
    series: series.map((s, i) => ({
      name: s.name,
      type: 'line',
      smooth: true,
      symbol: 'none',
      data: s.data,
      connectNulls: false,
      emphasis: { focus: 'series' },
      areaStyle: { opacity: s.area ?? 0.045 },
      itemStyle: { color: s.color },
      lineStyle: { width: s.dashed ? 1.5 : 1.8, type: s.dashed ? 'dashed' : 'solid' },
      ...(i === 0 && markLine ? { markLine } : {}),
    })),
  });

  const point = (v: number | null | undefined, digits = 2): number | null =>
    typeof v === 'number' && Number.isFinite(v) ? +v.toFixed(digits) : null;
  const rttApdex = points.map((p) => point(p.rttApdex, 3));
  return {
    qpsOption: hasPoints
      ? line([
          {
            name: 'QPS',
            data: points.map((p) => point(p.totalQps)),
            color: palette.blue,
            area: 0.06,
          },
        ])
      : null,
    // Apdex 只有 RTT 一种口径。监听等待另出分位数图——它是 ms 量纲，与 0~1 的分数同轴无意义。
    apdexOption: hasPoints
      ? line([{ name: 'RTT Apdex', data: rttApdex, color: palette.cyan, area: 0.055 }], 1)
      : null,
    listenWaitOption: hasPoints
      ? line([
          {
            name: '监听等待 P99',
            data: points.map((p) => point(p.listenWaitP99Ms)),
            color: palette.green,
            area: 0.055,
          },
        ])
      : null,
    loadOption: hasPoints
      ? line([
          {
            name: '运行',
            data: points.map((p) => point(p.botsRunning, 0)),
            color: palette.blue,
            area: 0.055,
          },
          {
            name: '异常',
            data: points.map((p) => point(p.botsErrored, 0)),
            color: palette.red,
            dashed: true,
          },
        ])
      : null,
    costOption: hasPoints
      ? line([
          {
            name: '客户端',
            data: points.map((p) => point(p.nonRTTAvgMs)),
            color: palette.purple,
            area: 0.05,
          },
          {
            name: '编码',
            data: points.map((p) => point(p.encodeAvgMs)),
            color: palette.orange,
            dashed: true,
          },
          {
            name: '解码',
            data: points.map((p) => point(p.decodeAvgMs)),
            color: palette.cyan,
            dashed: true,
          },
        ])
      : null,
    bandwidthOption: hasPoints
      ? line([
          {
            name: '↑发送',
            data: points.map((p) => point(p.sendKBps)),
            color: palette.cyan,
            area: 0.055,
          },
          {
            name: '↓接收',
            data: points.map((p) => point(p.recvKBps)),
            color: palette.purple,
            area: 0.035,
          },
        ])
      : null,
    cpuOption: hasPoints
      ? line(
          [
            {
              name: '集群均值',
              data: points.map((p) => point(p.avgCpuPercent)),
              color: palette.orange,
              area: 0.045,
            },
            {
              name: '最高节点',
              data: points.map((p) => point(p.maxCpuPercent)),
              color: palette.red,
              dashed: true,
            },
          ],
          100,
        )
      : null,
    memOption: hasPoints
      ? line(
          [
            {
              name: '集群使用率',
              data: points.map((p) => point(p.avgMemPercent)),
              color: palette.lime,
              area: 0.045,
            },
            {
              name: '最高节点',
              data: points.map((p) => point(p.maxMemPercent)),
              color: palette.purple,
              dashed: true,
            },
          ],
          100,
        )
      : null,
  };
}

function deriveSummary(detail: HistoryDetail, points: HistoryTrendPoint[]): DerivedSummary {
  const actions = detail.finalSnapshot.actions;
  const metrics = detail.finalSnapshot.summary;
  const totalSuccess = metrics.successCount;
  const totalFailures = metrics.failureCount;
  const totalTimeouts = metrics.timeoutCount;
  const totalCanceled = metrics.canceledCount;
  const totalSamples = metrics.sampleCount;
  const totalErrors = totalFailures + totalTimeouts + totalCanceled;
  const actionErrorScore = (a: HistoryActionMetric) =>
    (a.failureCount || 0) +
    (a.timeoutCount || 0) +
    (a.canceledCount || 0) +
    sum(a.errors ?? [], (e) => e.count);
  return {
    totalSamples,
    totalSuccess,
    totalFailures,
    totalTimeouts,
    totalCanceled,
    totalErrors,
    successRate: totalSamples > 0 ? metrics.successRate : 0,
    rttApdex: metrics.rttApdexSampleCount > 0 ? metrics.rttApdex : null,
    avgQps: metrics.avgQps,
    peakQps: max(points, (p) => p.totalQps),
    peakCpu: Math.max(
      detail.finalSystem.maxCpuPercent,
      max(points, (p) => p.maxCpuPercent || p.avgCpuPercent),
    ),
    peakAvgMem: Math.max(
      detail.finalSystem.avgMemPercent || 0,
      max(points, (p) => p.avgMemPercent),
    ),
    peakMaxMem: Math.max(
      detail.finalSystem.maxMemPercent || 0,
      max(points, (p) => p.maxMemPercent || p.avgMemPercent),
    ),
    peakBotsRunning: Math.max(
      detail.finalSnapshot.robots?.running ?? 0,
      max(points, (p) => p.botsRunning),
    ),
    peakBotsErrored: Math.max(
      detail.finalSnapshot.robots?.errored ?? 0,
      max(points, (p) => p.botsErrored),
    ),
    offlinePoints: points.filter((p) => p.offlineCount > 0).length,
    cleanupIssueCount: detail.agentReports.filter(
      (r) => r.cleanupStatus && r.cleanupStatus.status && r.cleanupStatus.status !== 'ok',
    ).length,
    failedAgents: detail.agentReports.filter((r) => r.result === 'failed').length,
    slowestAction: maxBy(
      actions.filter((a) => a.totalDurationSampleCount > 0 && a.totalDuration.p99Ms != null),
      (a) => a.totalDuration.p99Ms ?? -1,
    ),
    // 「最差 Apdex」只在往返类里找：其余类别没有分，参与比较等于拿 0 分去当最差。
    worstApdexAction: minBy(
      actions.filter((a) => resolveKind(a) === 'networked' && a.rttApdexSampleCount > 0),
      (a) => a.rttApdex,
    ),
    mostFailedAction: maxBy(
      actions.filter((a) => actionErrorScore(a) > 0),
      actionErrorScore,
    ),
    busiestAction: maxBy(actions, (a) => a.sampleCount),
  };
}

function buildActionInsights(summary: DerivedSummary): ActionInsight[] {
  const out: ActionInsight[] = [];
  if (summary.slowestAction)
    out.push({
      label: '最慢动作',
      name: summary.slowestAction.name,
      value: `P99 ${fmtMs(summary.slowestAction.totalDuration.p99Ms!)}`,
      tone: 'warn',
    });
  if (summary.worstApdexAction)
    out.push({
      label: '最差 RTT Apdex',
      name: summary.worstApdexAction.name,
      value: fmtScore(summary.worstApdexAction.rttApdex),
      tone: summary.worstApdexAction.rttApdex < 0.75 ? 'bad' : 'warn',
    });
  if (summary.mostFailedAction)
    out.push({
      label: '错误最多',
      name: summary.mostFailedAction.name,
      value: `${summary.mostFailedAction.failureCount + summary.mostFailedAction.timeoutCount + (summary.mostFailedAction.canceledCount ?? 0)} 次`,
      tone: 'bad',
    });
  if (summary.busiestAction)
    out.push({
      label: '样本最多',
      name: summary.busiestAction.name,
      value: summary.busiestAction.sampleCount.toLocaleString(),
      tone: 'blue',
    });
  return out;
}

function buildDiagnostics(
  detail: HistoryDetail,
  summary: DerivedSummary,
  points: HistoryTrendPoint[],
) {
  const items: Array<{ label: string; value: string; tone: 'good' | 'warn' | 'bad' | 'blue' }> = [];
  if (summary.totalErrors > 0)
    items.push({
      label: '失败/超时/取消',
      value: `${summary.totalErrors.toLocaleString()} 次`,
      tone: summary.totalErrors > 100 ? 'bad' : 'warn',
    });
  if (detail.finalSnapshot.connections.failed + detail.finalSnapshot.connections.dropped > 0)
    items.push({
      label: '连接异常',
      value: `失败 ${detail.finalSnapshot.connections.failed} / 断开 ${detail.finalSnapshot.connections.dropped}`,
      tone: 'warn',
    });
  if (summary.peakCpu >= 80)
    items.push({ label: 'CPU 高水位', value: `${summary.peakCpu.toFixed(1)}%`, tone: 'bad' });
  if (summary.peakMaxMem >= 85)
    items.push({ label: 'MEM 高水位', value: `${summary.peakMaxMem.toFixed(1)}%`, tone: 'bad' });
  if (summary.offlinePoints > 0)
    items.push({ label: '节点离线采样', value: `${summary.offlinePoints} 个点`, tone: 'bad' });
  if (summary.cleanupIssueCount > 0)
    items.push({ label: '清理异常', value: `${summary.cleanupIssueCount} 个节点`, tone: 'warn' });
  if (summary.slowestAction && (summary.slowestAction.totalDuration.p99Ms ?? 0) > 1000)
    items.push({
      label: '慢动作',
      value: `${summary.slowestAction.name} P99 ${fmtMs(summary.slowestAction.totalDuration.p99Ms!)}`,
      tone: 'warn',
    });
  if (detail.agentEvents?.some((e) => e.type === 'restarted'))
    items.push({
      label: '节点重启',
      value: `${detail.agentEvents.filter((e) => e.type === 'restarted').length} 次`,
      tone: 'bad',
    });
  if (items.length === 0)
    items.push({
      label: '稳定性',
      value: points.length > 0 ? '未发现明显异常' : '无采样异常',
      tone: 'good',
    });
  return items;
}

function renderCleanup(cleanup: CleanupStatus | undefined) {
  if (!cleanup || !cleanup.status)
    return <span style={{ color: 'var(--text-tertiary)' }}>未记录</span>;
  const map: Record<string, { color: string; label: string }> = {
    ok: { color: 'success', label: '清理完成' },
    partial: { color: 'warning', label: '部分清理' },
    timeout: { color: 'error', label: '清理超时' },
    unknown: { color: 'default', label: '未知' },
  };
  const info = map[cleanup.status as string] ?? { color: 'default', label: cleanup.status };
  const detailLines: string[] = [];
  if (cleanup.message) detailLines.push(cleanup.message);
  if (cleanup.timeoutRobots) detailLines.push(`超时机器人 ${cleanup.timeoutRobots}`);
  if (cleanup.luaSkipped) detailLines.push(`脚本运行时未归还 ${cleanup.luaSkipped}`);
  return (
    <Tooltip title={detailLines.join('；') || info.label}>
      <Tag color={info.color}>{info.label}</Tag>
    </Tooltip>
  );
}

function fmtKBpsPeak(points: HistoryTrendPoint[], key: 'sendKBps' | 'recvKBps') {
  const peak = max(points, (p) => p[key]);
  if (peak >= 1024) return `${(peak / 1024).toFixed(2)} MB/s`;
  return `${peak.toFixed(1)} KB/s`;
}

function fmtScore(v: number | null | undefined) {
  return typeof v === 'number' && Number.isFinite(v) ? v.toFixed(3) : '—';
}

function fmtPercent(v: number) {
  return `${(v * 100).toFixed(2)}%`;
}

function sum<T>(items: T[], pick: (item: T) => number) {
  return items.reduce((acc, item) => acc + (Number.isFinite(pick(item)) ? pick(item) : 0), 0);
}

function max<T>(items: T[], pick: (item: T) => number) {
  return items.reduce(
    (acc, item) => Math.max(acc, Number.isFinite(pick(item)) ? pick(item) : 0),
    0,
  );
}

function maxBy<T>(items: T[], pick: (item: T) => number): T | undefined {
  let best: T | undefined;
  let bestValue = -Infinity;
  for (const item of items) {
    const value = pick(item);
    if (Number.isFinite(value) && value > bestValue) {
      best = item;
      bestValue = value;
    }
  }
  return best;
}

function minBy<T>(items: T[], pick: (item: T) => number): T | undefined {
  let best: T | undefined;
  let bestValue = Infinity;
  for (const item of items) {
    const value = pick(item);
    if (Number.isFinite(value) && value < bestValue) {
      best = item;
      bestValue = value;
    }
  }
  return best;
}

function eventLabel(type: string) {
  if (type === 'offline') return '离线';
  if (type === 'restarted') return '重启丢任务';
  if (type === 'reconnected') return '恢复';
  return '注销';
}

function eventColor(type: string) {
  if (type === 'offline') return 'error';
  if (type === 'restarted') return 'warning';
  if (type === 'reconnected') return 'success';
  return 'default';
}

function resultLabel(v: string) {
  if (v === 'completed') return '完成';
  if (v === 'stopped') return '停止';
  if (v === 'failed') return '失败';
  return v;
}

function resultColor(v: string) {
  if (v === 'completed') return 'success';
  if (v === 'failed') return 'error';
  return 'default';
}

function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`;
  return `${(sec / 3600).toFixed(1)}h`;
}
