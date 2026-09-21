import { App, Button, Empty, Input, Spin, Table, Tag, Timeline, Tooltip } from 'antd';
import {
  BugOutlined,
  CheckCircleOutlined,
  DownloadOutlined,
  FileTextOutlined,
  StarFilled,
  StarOutlined,
} from '@ant-design/icons';
import dayjs from 'dayjs';
import { useEffect, useMemo, useState } from 'react';
import { historyApi, isAbortError, showApiError } from '@/services';
import type {
  CleanupStatus,
  HistoryAgentReport,
  HistoryConfigSummary,
  HistoryDetail,
  HistoryTrendPoint,
} from '@/types/api';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { useFloatingWindowStore } from '@/components/FlowEditor/store/floatingWindowStore';
import { ActionMetricsTable } from '@/components/monitoring/shared/ActionMetricsTable';
import { fmtBytes } from '@/components/monitoring/shared/formats';
import { useReportCapture } from './report/useReportCapture';
import { formatStageLabel } from './stageLabel';
import {
  MetricTile,
  ReportSection,
  TrendCard,
  Fact,
  computeStageLines,
  buildChartOptions,
  deriveSummary,
  buildActionInsights,
  buildDiagnostics,
  renderCleanup,
  fmtKBpsPeak,
  fmtScore,
  fmtPercent,
  successTone,
  apdexTone,
  eventLabel,
  eventColor,
  resultLabel,
  resultColor,
  formatDuration,
} from './HistoryDetailView.helpers';
import './HistoryPanel.css';

export interface HistoryDetailViewProps {
  id: string;
  /** 阶段段落号（>0 时展示该 reset 段落详情）。 */
  stageIndex?: number;
  /** 阶段段落标签，如「第 2 轮 · S3-S4」。 */
  stageLabel?: string;
  onChange: () => void;
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
  const { generateReport, generating: reportGenerating } = useReportCapture(detail, timeseries);
  const [configInfo, setConfigInfo] = useState<HistoryConfigSummary | null>(null);
  const [stagesExpanded, setStagesExpanded] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    const init = { signal: controller.signal };
    setLoading(true);
    setDetail(null);
    setTimeseries(null);
    Promise.all([
      historyApi.getHistory(id, stageIndex, init),
      historyApi.getHistoryTimeseries(id, 1200, stageIndex, init),
    ])
      .then(([d, t]) => {
        if (controller.signal.aborted) return;
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
      .catch((err: unknown) => {
        if (controller.signal.aborted || isAbortError(err)) return;
        showApiError(err);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [id, stageIndex]);

  useEffect(() => {
    const controller = new AbortController();
    setConfigInfo(null);
    setStagesExpanded(false);
    historyApi
      .getHistoryConfig(id, { signal: controller.signal })
      .then((config) => {
        if (!controller.signal.aborted) setConfigInfo(config);
      })
      .catch(() => {});
    return () => controller.abort();
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
  const chartOptions = useMemo(() => {
    // css() 实时读取主题变量，theme 仅用于主题切换时触发本 memo 重算
    void theme;
    return buildChartOptions(timeseries?.points ?? [], stageMarks);
  }, [timeseries, stageMarks, theme]);

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
              loading={reportGenerating}
              onClick={() => void generateReport()}
            >
              报告
            </Button>
          </Tooltip>
          <Tooltip title="下载完整任务配置归档 JSON（含全部阶段）">
            <Button size="small" icon={<DownloadOutlined />} onClick={downloadConfig}>
              下载
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
          tone={successTone(summary.successRate)}
        />
        <MetricTile
          label="RTT Apdex"
          value={fmtScore(summary.rttApdex)}
          sub={`阈值 T=${finalSnap.apdexT}ms`}
          tone={apdexTone(summary.rttApdex)}
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
            <Fact
              label="节点间隔"
              value={
                (configInfo?.robotConfig?.defaultDelayMs ?? 0) === 0
                  ? '默认 1000ms'
                  : configInfo?.robotConfig?.defaultDelayMs === -1
                    ? '禁用等待'
                    : `${configInfo?.robotConfig?.defaultDelayMs}ms`
              }
            />
            <Fact label="流程" value={`${cs.flowSizeKB}KB`} />
            <Fact label="Proto" value={`${cs.protoCount} 个`} />
            <Fact label="Lua 脚本" value={`${cs.scriptCount} 个`} />
            <Fact
              label="阶段"
              value={
                detail.stageCount && detail.stageCount > 0 ? `${detail.stageCount} 阶段` : '无'
              }
            />
            <Fact label="发送流量" value={fmtBytes(finalBandwidth.totalSendBytes)} />
            <Fact label="接收流量" value={fmtBytes(finalBandwidth.totalRecvBytes)} />
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
                        <div key={`stage-${i}-${stage.count}`} className="hp-rampup-stage">
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
