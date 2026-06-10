/**
 * 历史压测记录面板（FloatingWindow）：list / detail / compare 三种视图。
 *
 * - 进入：list 视图（搜索/筛选），点击卡片进 detail；
 * - 多选 2~5 张后点"对比"进 compare；
 * - detail 视图可：编辑备注/标签/收藏，下载完整配置归档，删除（强制），克隆为新任务。
 */

import { App, Button, Checkbox, Empty, Input, Pagination, Spin, Switch, Tooltip } from 'antd';
import {
  ArrowLeftOutlined,
  DeleteOutlined,
  DownOutlined,
  RightOutlined,
  StarFilled,
  StarOutlined,
  SwapOutlined,
} from '@ant-design/icons';
import { useCallback, useEffect, useMemo, useState } from 'react';
import dayjs from 'dayjs';
import { ApiError, historyApi, showApiError } from '@/services';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { useFloatingWindowStore } from '@/components/FlowEditor/store/floatingWindowStore';
import { FloatingWindow } from '@/components/FlowEditor/panels/FloatingWindow';
import type { HistoryRecord } from '@/types/api';
import { HistoryDetailView } from './HistoryDetailView';
import { HistoryCompareView } from './HistoryCompareView';
import { formatStageLabel } from './stageLabel';
import './HistoryPanel.css';

export interface HistoryModalProps {
  open: boolean;
  onClose: () => void;
}

/** 选中目标：整体记录或某条阶段段落。 */
interface SelTarget {
  id: string;
  stageIndex?: number;
  stageLabel?: string;
  name: string;
  starred: boolean;
}

function targetKey(t: { id: string; stageIndex?: number }): string {
  return (t.stageIndex ?? -1) > 0 ? `${t.id}#${t.stageIndex}` : t.id;
}

type View =
  | { kind: 'list' }
  | { kind: 'detail'; id: string; stageIndex?: number; stageLabel?: string }
  | { kind: 'compare'; targets: SelTarget[] };

export function HistoryModal({ open, onClose }: HistoryModalProps) {
  const { message, modal } = App.useApp();
  const popupZ = useFloatingWindowStore((s) => s._nextZ) + 100;
  const [view, setView] = useState<View>({ kind: 'list' });
  const [items, setItems] = useState<HistoryRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const [starredOnly, setStarredOnly] = useState(false);
  const [selected, setSelected] = useState<SelTarget[]>([]);
  const [page, setPage] = useState(1);
  const pageSize = 20;

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const resp = await historyApi.listHistory({
        search: search || undefined,
        starred: starredOnly ? true : undefined,
        limit: 100,
        includeStages: true,
      });
      setItems(resp.items);
      setTotal(resp.total);
      useEditorStore.getState().setHistoryEnabled(true);
    } catch (err) {
      if (err instanceof ApiError && err.code === 'HISTORY_DISABLED') {
        useEditorStore.getState().setHistoryEnabled(false);
      }
      showApiError(err);
    } finally {
      setLoading(false);
    }
  }, [search, starredOnly]);

  useEffect(() => {
    if (open && view.kind === 'list') {
      refresh();
    }
  }, [open, view.kind, refresh]);

  const onToggleStar = useCallback(
    async (record: HistoryRecord, e?: React.MouseEvent) => {
      e?.stopPropagation();
      try {
        await historyApi.updateHistory(record.id, { starred: !record.starred });
        refresh();
      } catch (err) {
        showApiError(err);
      }
    },
    [refresh],
  );

  // 阶段段落收藏：写入段落级元数据（task_meta，stage_index>=1）
  const onToggleStageStar = useCallback(
    async (child: HistoryRecord, e?: React.MouseEvent) => {
      e?.stopPropagation();
      const stageIndex = child.stageIndex;
      if ((stageIndex ?? -1) <= 0) {
        message.error('阶段索引缺失，无法更新收藏');
        return;
      }
      try {
        await historyApi.updateHistory(child.id, { starred: !child.starred }, stageIndex);
        refresh();
      } catch (err) {
        showApiError(err);
      }
    },
    [message, refresh],
  );

  const taskStarredMap = useMemo(() => {
    const map = new Map<string, boolean>();
    for (const r of items) {
      const anyStageStarred = (r.children ?? []).some((c) => c.starred);
      map.set(r.id, r.starred || anyStageStarred);
    }
    return map;
  }, [items]);

  const isTaskProtected = useCallback(
    (id: string, fallback = false) => taskStarredMap.get(id) ?? fallback,
    [taskStarredMap],
  );

  const onDelete = useCallback(
    (record: HistoryRecord, e?: React.MouseEvent) => {
      e?.stopPropagation();
      const protectedByStar = isTaskProtected(record.id, record.starred);
      modal.confirm({
        title: '确认删除？',
        content: protectedByStar ? '该任务或阶段已收藏，需要强制删除' : `将删除 ${record.name} 的所有数据`,
        zIndex: popupZ,
        okText: protectedByStar ? '强制删除' : '删除',
        okButtonProps: { danger: true },
        onOk: async () => {
          try {
            await historyApi.deleteHistory(record.id, protectedByStar);
            message.success('已删除');
            refresh();
          } catch (err) {
            showApiError(err);
          }
        },
      });
    },
    [isTaskProtected, modal, message, popupZ, refresh],
  );

  const toggleSelect = (target: SelTarget, e?: React.MouseEvent) => {
    e?.stopPropagation();
    const key = targetKey(target);
    setSelected((prev) => {
      if (prev.some((t) => targetKey(t) === key)) return prev.filter((t) => targetKey(t) !== key);
      if (prev.length >= 5) return prev;
      return [...prev, target];
    });
  };
  const isSelected = (target: { id: string; stageIndex?: number }) =>
    selected.some((t) => targetKey(t) === targetKey(target));

  // 批量删除：阶段段落子记录不单独删除，按所属父任务去重后删除。
  const uniqueParents = useMemo(() => {
    const map = new Map<string, SelTarget>();
    for (const t of selected) {
      const starred = isTaskProtected(t.id, t.starred);
      const existing = map.get(t.id);
      if (!existing) {
        map.set(t.id, { ...t, starred });
      } else if (starred && !existing.starred) {
        map.set(t.id, { ...existing, starred: true });
      }
    }
    return [...map.values()];
  }, [isTaskProtected, selected]);

  const onBatchDelete = useCallback(() => {
    const starredCount = uniqueParents.filter((r) => r.starred).length;
    const hasStageWithoutParent = selected.some(
      (t) =>
        (t.stageIndex ?? -1) > 0 &&
        !selected.some((p) => p.id === t.id && (p.stageIndex ?? -1) <= 0),
    );
    const content = hasStageWithoutParent
      ? starredCount > 0
        ? `阶段不能单独删除，将删除所属完整任务及全部阶段。其中 ${starredCount} 个已收藏，将强制删除。`
        : '阶段不能单独删除，将删除所属完整任务及全部阶段'
      : starredCount > 0
        ? `其中 ${starredCount} 个已收藏，将强制删除（含其全部阶段）`
        : '此操作不可恢复（含其全部阶段）';
    modal.confirm({
      title: `确认批量删除 ${uniqueParents.length} 个任务？`,
      content,
      zIndex: popupZ,
      okText: '删除',
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await Promise.all(uniqueParents.map((r) => historyApi.deleteHistory(r.id, r.starred)));
          message.success(`已删除 ${uniqueParents.length} 个任务`);
          setSelected([]);
          refresh();
        } catch (err) {
          showApiError(err);
          refresh();
        }
      },
    });
  }, [modal, message, popupZ, refresh, selected, uniqueParents]);

  const paged = items.slice((page - 1) * pageSize, page * pageSize);

  const titleLabel =
    view.kind === 'list' ? '历史压测记录' : view.kind === 'detail' ? '记录详情' : '对比记录';

  const titleContent = (
    <div className="hp-titlebar">
      {view.kind !== 'list' && (
        <Button size="small" type="text" icon={<ArrowLeftOutlined />} onClick={() => setView({ kind: 'list' })}>
          返回
        </Button>
      )}
      <span className="hp-titlebar__text">{titleLabel}</span>
    </div>
  );

  const defaultWindowSize = useMemo(
    () => ({
      width: Math.min(1720, Math.max(960, window.innerWidth - 32)),
      height: Math.min(860, Math.max(680, window.innerHeight - 72)),
    }),
    [],
  );

  return (
    <FloatingWindow
      windowId="history"
      title={titleContent}
      defaultSize={defaultWindowSize}
      minSize={{ width: 860, height: 420 }}
      open={open}
      onClose={onClose}
      extra={
        view.kind === 'list' && total > 0 ? (
          <span className="hp-titlebar__count">共 {total} 条</span>
        ) : undefined
      }
    >
      {view.kind === 'list' && (
        <div className="hp-shell hp-shell--list">
          <header className="hp-list-toolbar">
            <Input.Search
              className="hp-list-toolbar__search"
              placeholder="名称、ID、标签或备注"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onSearch={refresh}
              allowClear
            />
            <div className="hp-list-toolbar__filters">
              <label className="hp-filter-toggle">
                <span>仅收藏</span>
                <Switch checked={starredOnly} onChange={setStarredOnly} size="small" />
              </label>
              <Button
                type="primary"
                ghost
                icon={<SwapOutlined />}
                disabled={selected.length < 2 || selected.length > 5}
                onClick={() => setView({ kind: 'compare', targets: selected })}
              >
                对比 {selected.length > 0 ? `(${selected.length})` : ''}
              </Button>
              <Button
                danger
                icon={<DeleteOutlined />}
                disabled={selected.length === 0}
                onClick={onBatchDelete}
              >
                删除 {selected.length > 0 ? `(${uniqueParents.length})` : ''}
              </Button>
            </div>
          </header>

          <div className="hp-list-scroll">
            {loading && items.length === 0 ? (
              <div className="hp-list-state">
                <Spin />
              </div>
            ) : items.length === 0 ? (
              <div className="hp-list-state">
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无压测归档，完成任务后将出现在此处" />
              </div>
            ) : (
              <div className="hp-list">
                {paged.map((r) =>
                  r.hasResetStages && r.children && r.children.length > 0 ? (
                    <StageGroup
                      key={r.id}
                      record={r}
                      isSelected={isSelected}
                      onSelect={toggleSelect}
                      onStarStage={onToggleStageStar}
                      onDelete={onDelete}
                      onOpenStage={(stageIndex, stageLabel) =>
                        setView({ kind: 'detail', id: r.id, stageIndex, stageLabel })
                      }
                    />
                  ) : (
                    <HistoryCard
                      key={r.id}
                      record={r}
                      selected={isSelected({ id: r.id })}
                      onSelect={(e) => toggleSelect({ id: r.id, name: r.name, starred: r.starred }, e)}
                      onStar={onToggleStar}
                      onDelete={onDelete}
                      onClick={() => setView({ kind: 'detail', id: r.id })}
                    />
                  ),
                )}
              </div>
            )}
          </div>

          {items.length > pageSize && (
            <footer className="hp-list-pagination">
              <Pagination
                size="small"
                current={page}
                pageSize={pageSize}
                total={items.length}
                onChange={setPage}
                showSizeChanger={false}
              />
            </footer>
          )}
        </div>
      )}
      {view.kind === 'detail' && (
        <div className="hp-shell hp-shell--detail">
          <HistoryDetailView
            id={view.id}
            stageIndex={view.stageIndex}
            stageLabel={view.stageLabel}
            onChange={refresh}
          />
        </div>
      )}
      {view.kind === 'compare' && (
        <div className="hp-shell hp-shell--compare">
          <HistoryCompareView
            targets={view.targets.map((t) =>
              (t.stageIndex ?? -1) > 0 ? { id: t.id, stageIndex: t.stageIndex } : t.id,
            )}
          />
        </div>
      )}
    </FloatingWindow>
  );
}

/* ── 列表记录行 ── */

function HistoryCard({
  record: r,
  selected,
  onSelect,
  onStar,
  onDelete,
  onClick,
}: {
  record: HistoryRecord;
  selected: boolean;
  onSelect: (e?: React.MouseEvent) => void;
  onStar: (r: HistoryRecord, e?: React.MouseEvent) => void;
  onDelete: (r: HistoryRecord, e?: React.MouseEvent) => void;
  onClick: () => void;
}) {
  const tags = r.tags ?? [];
  const visibleTags = tags.slice(0, 3);
  const hiddenTagCount = Math.max(0, tags.length - visibleTags.length);
  const failed = r.state === 'failed';
  const cfg = r.configSummary;
  const started = r.startedAt ? dayjs(r.startedAt).format('MM-DD HH:mm') : '—';
  const note = (r.note ?? '').trim();

  return (
    <div
      className={`hp-record-row${selected ? ' hp-record-row--selected' : ''}${failed ? ' hp-record-row--failed' : ''}`}
      onClick={onClick}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          onClick();
        }
      }}
    >
      <div className="hp-record-select">
        <Checkbox
          checked={selected}
          onClick={(e) => onSelect(e as unknown as React.MouseEvent)}
        />
      </div>

      <div className="hp-record-identity">
        <span className={`hp-record-state hp-record-state--${failed ? 'bad' : 'ok'}`}>{failed ? '失败' : '完成'}</span>
        <Tooltip title={r.debugMode ? '调试模式' : '测试模式'} mouseEnterDelay={0.3}>
          <span className={`hp-mode-marker hp-mode-marker--${r.debugMode ? 'debug' : 'test'}`} />
        </Tooltip>
        <Tooltip title={r.name} mouseEnterDelay={0.4}>
          <span className="hp-record-name">{r.name}</span>
        </Tooltip>
        <Tooltip title={`记录 ID：${r.id}`} mouseEnterDelay={0.4}>
          <code className="hp-record-id">#{r.id.slice(0, 8)}</code>
        </Tooltip>
      </div>

      <div className="hp-record-time">
        <span className="hp-record-k">开始</span>
        <span className="hp-record-v">{started}</span>
      </div>

      <div className="hp-record-duration">
        <span className="hp-record-k">时长</span>
        <span className="hp-record-v hp-record-v--strong">{formatDuration(r.durationSec)}</span>
      </div>

      <div className="hp-record-load">
        <span>机器人 <b>{r.totalBots.toLocaleString()}</b></span>
        <span>节点 <b>{r.activeAgentCount}/{r.agentCount}</b></span>
        <span>并发 <b>{cfg.concurrency}</b></span>
        {!!r.stageCount && r.stageCount > 0 && <span>阶段 <b>{r.stageCount}</b></span>}
      </div>

      <div className="hp-record-config">
        <span>超时 {cfg.timeoutSec}s</span>
        <span>Flow {cfg.flowSizeKB}KB</span>
        <span>Proto {cfg.protoCount}</span>
        <span>Lua {cfg.scriptCount}</span>
      </div>

      <div className="hp-record-tags">
        {visibleTags.length > 0 ? (
          <>
            {visibleTags.map((t) => <span key={t} className="hp-tag-pill">{t}</span>)}
            {hiddenTagCount > 0 && (
              <Tooltip title={tags.slice(visibleTags.length).join('、')} mouseEnterDelay={0.3}>
                <span className="hp-tag-pill hp-tag-pill--more">+{hiddenTagCount}</span>
              </Tooltip>
            )}
          </>
        ) : (
          <span className="hp-record-empty">无标签</span>
        )}
      </div>

      <Tooltip title={note || '无备注'} mouseEnterDelay={0.4}>
        <div className={`hp-record-note${note ? '' : ' hp-record-note--empty'}`}>{note || '无备注'}</div>
      </Tooltip>

      <div className="hp-record-actions" onClick={(e) => e.stopPropagation()}>
        <Tooltip title={r.starred ? '取消收藏' : '收藏'}>
          <Button
            type="text"
            size="small"
            className="hp-record-icon-btn"
            icon={r.starred ? <StarFilled style={{ color: 'var(--color-warning)' }} /> : <StarOutlined />}
            onClick={(e) => onStar(r, e)}
          />
        </Tooltip>
        <Tooltip title="删除">
          <Button
            type="text"
            size="small"
            className="hp-record-icon-btn"
            danger
            icon={<DeleteOutlined />}
            onClick={(e) => onDelete(r, e)}
          />
        </Tooltip>
      </div>
    </div>
  );
}

/* ── 含 reset 的渐进式加压：阶段组（父任务 + 阶段段落子记录） ── */

function StageGroup({
  record: r,
  isSelected,
  onSelect,
  onStarStage,
  onDelete,
  onOpenStage,
}: {
  record: HistoryRecord;
  isSelected: (t: { id: string; stageIndex?: number }) => boolean;
  onSelect: (t: SelTarget, e?: React.MouseEvent) => void;
  onStarStage: (child: HistoryRecord, e?: React.MouseEvent) => void;
  onDelete: (r: HistoryRecord, e?: React.MouseEvent) => void;
  onOpenStage: (stageIndex: number, stageLabel: string) => void;
}) {
  const [expanded, setExpanded] = useState(true);
  const failed = r.state === 'failed';
  const children = r.children ?? [];
  const tags = r.tags ?? [];
  const visibleTags = tags.slice(0, 3);
  const hiddenTagCount = Math.max(0, tags.length - visibleTags.length);
  const cfg = r.configSummary;
  const started = r.startedAt ? dayjs(r.startedAt).format('MM-DD HH:mm') : '—';
  const note = (r.note ?? '').trim();

  return (
    <div
      className={`hp-stage-group${failed ? ' hp-stage-group--failed' : ''}${expanded ? ' hp-stage-group--open' : ''}`}
    >
      {/* 标题带：复用普通记录行布局，仅额外加 chevron + 阶段 badge + 展开折叠 */}
      <div
        className={`hp-record-row hp-record-row--group${failed ? ' hp-record-row--failed' : ''}${isSelected({ id: r.id }) ? ' hp-record-row--selected' : ''}`}
        role="button"
        tabIndex={0}
        onClick={() => setExpanded((v) => !v)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            setExpanded((v) => !v);
          }
        }}
      >
        <div className="hp-record-select" onClick={(e) => e.stopPropagation()}>
          <Checkbox
            checked={isSelected({ id: r.id })}
            onClick={(e) => onSelect({ id: r.id, name: r.name, starred: r.starred }, e as unknown as React.MouseEvent)}
          />
        </div>

        <div className="hp-record-identity">
          <span className="hp-stage-caption__chevron" aria-hidden>
            {expanded ? <DownOutlined /> : <RightOutlined />}
          </span>
          <span className={`hp-record-state hp-record-state--${failed ? 'bad' : 'ok'}`}>{failed ? '失败' : '完成'}</span>
          <Tooltip title={r.debugMode ? '调试模式' : '测试模式'} mouseEnterDelay={0.3}>
            <span className={`hp-mode-marker hp-mode-marker--${r.debugMode ? 'debug' : 'test'}`} />
          </Tooltip>
          <Tooltip title={r.name} mouseEnterDelay={0.4}>
            <span className="hp-record-name">{r.name}</span>
          </Tooltip>
          <span className="hp-stage-caption__badge">阶段任务 · {children.length} 段</span>
          <Tooltip title={`记录 ID：${r.id}`} mouseEnterDelay={0.4}>
            <code className="hp-record-id">#{r.id.slice(0, 8)}</code>
          </Tooltip>
        </div>

        <div className="hp-record-time">
          <span className="hp-record-k">开始</span>
          <span className="hp-record-v">{started}</span>
        </div>

        <div className="hp-record-duration">
          <span className="hp-record-k">时长</span>
          <span className="hp-record-v hp-record-v--strong">{formatDuration(r.durationSec)}</span>
        </div>

        <div className="hp-record-load">
          <span>机器人 <b>{r.totalBots.toLocaleString()}</b></span>
          <span>节点 <b>{r.activeAgentCount}/{r.agentCount}</b></span>
          <span>并发 <b>{cfg.concurrency}</b></span>
          <span>阶段 <b>{r.stageCount}</b></span>
        </div>

        <div className="hp-record-config">
          <span>超时 {cfg.timeoutSec}s</span>
          <span>Flow {cfg.flowSizeKB}KB</span>
          <span>Proto {cfg.protoCount}</span>
          <span>Lua {cfg.scriptCount}</span>
        </div>

        <div className="hp-record-tags">
          {visibleTags.length > 0 ? (
            <>
              {visibleTags.map((t) => <span key={t} className="hp-tag-pill">{t}</span>)}
              {hiddenTagCount > 0 && (
                <Tooltip title={tags.slice(visibleTags.length).join('、')} mouseEnterDelay={0.3}>
                  <span className="hp-tag-pill hp-tag-pill--more">+{hiddenTagCount}</span>
                </Tooltip>
              )}
            </>
          ) : (
            <span className="hp-record-empty">无标签</span>
          )}
        </div>

        <Tooltip title={note || '无备注'} mouseEnterDelay={0.4}>
          <div className={`hp-record-note${note ? '' : ' hp-record-note--empty'}`}>{note || '无备注'}</div>
        </Tooltip>

        <div className="hp-record-actions" onClick={(e) => e.stopPropagation()}>
          <Tooltip title="删除整个任务（含全部阶段）">
            <Button
              type="text"
              size="small"
              className="hp-record-icon-btn"
              danger
              icon={<DeleteOutlined />}
              onClick={(e) => onDelete(r, e)}
            />
          </Tooltip>
        </div>
      </div>

      {expanded && (
        <div className="hp-stage-track" role="list">
          {children.map((c) => {
            const stageIndex = c.stageIndex;
            if (stageIndex === undefined || stageIndex <= 0) return null;
            const sel = isSelected({ id: r.id, stageIndex });
            const displayStageLabel = formatStageLabel(c.stageLabel, stageIndex);
            const open = () => onOpenStage(stageIndex, displayStageLabel);
            const cTags = c.tags ?? [];
            const cNote = (c.note ?? '').trim();
            const cFailed = c.state === 'failed';
            const hasMetrics = (c.totalActions ?? 0) > 0;
            return (
              <div
                key={stageIndex}
                role="listitem"
                className={`hp-stage-child${sel ? ' hp-stage-child--selected' : ''}${cFailed ? ' hp-stage-child--failed' : ''}`}
                onClick={open}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    open();
                  }
                }}
                tabIndex={0}
              >
                {/* Col 1: Checkbox */}
                <div className="hp-record-select" onClick={(e) => e.stopPropagation()}>
                  <Checkbox
                    checked={sel}
                    onClick={(e) =>
                      onSelect(
                        {
                          id: r.id,
                          stageIndex,
                          stageLabel: displayStageLabel,
                          name: r.name,
                          starred: c.starred,
                        },
                        e as unknown as React.MouseEvent,
                      )
                    }
                  />
                </div>

                {/* Col 2: Identity — state pill + segment label */}
                <div className="hp-record-identity">
                  <span className={`hp-sc-state hp-sc-state--${cFailed ? 'bad' : 'ok'}`}>{cFailed ? '失败' : '完成'}</span>
                  <span className="hp-stage-status-marker" aria-hidden />
                  <span className="hp-sc-label">{displayStageLabel}</span>
                </div>

                {/* Col 3: Peak bots */}
                <div className="hp-record-time">
                  <span className="hp-record-k">峰值</span>
                  <span className="hp-record-v hp-record-v--strong">{c.totalBots.toLocaleString()}</span>
                </div>

                {/* Col 4: Concurrency */}
                <div className="hp-record-duration">
                  <span className="hp-record-k">并发</span>
                  <span className="hp-record-v hp-record-v--strong">{cfg.concurrency}</span>
                </div>

                {/* Col 5: Actions count + success rate */}
                <div className="hp-record-load">
                  {hasMetrics ? (
                    <>
                      <span>动作 <b>{fmtMetricCount(c.totalActions ?? 0)}</b></span>
                      <span>成功 <b className={fmtRateClass(c.successRate)}>{fmtPercent(c.successRate)}</b></span>
                    </>
                  ) : (
                    <span className="hp-record-empty">—</span>
                  )}
                </div>

                {/* Col 6: RTT metrics */}
                <div className="hp-record-config">
                  {hasMetrics ? (
                    <>
                      <span>RTT <b>{fmtMs(c.avgRttMs)}</b></span>
                      <span>P95 <b>{fmtMs(c.p95RttMs)}</b></span>
                    </>
                  ) : null}
                </div>

                {/* Col 7: Tags */}
                <div className="hp-record-tags">
                  {cTags.length > 0 ? (
                    cTags.slice(0, 3).map((t) => <span key={t} className="hp-tag-pill">{t}</span>)
                  ) : null}
                </div>

                {/* Col 8: Note */}
                <Tooltip title={cNote || undefined} mouseEnterDelay={0.3}>
                  <div className={`hp-record-note${cNote ? '' : ' hp-record-note--empty'}`}>{cNote || ''}</div>
                </Tooltip>

                {/* Col 9: Star */}
                <div className="hp-record-actions" onClick={(e) => e.stopPropagation()}>
                  <Tooltip title={c.starred ? '取消收藏本段' : '收藏本段'}>
                    <Button
                      type="text"
                      size="small"
                      className="hp-record-icon-btn"
                      icon={c.starred ? <StarFilled style={{ color: 'var(--color-warning)' }} /> : <StarOutlined />}
                      onClick={(e) => onStarStage(c, e)}
                    />
                  </Tooltip>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`;
  return `${(sec / 3600).toFixed(1)}h`;
}

/** 格式化大数量：15.2K / 1.3M */
function fmtMetricCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return n.toLocaleString();
}

/** 格式化成功率 0~1 → "99.2%" */
function fmtPercent(v?: number): string {
  if (v == null) return '—';
  return `${(v * 100).toFixed(1)}%`;
}

/** 成功率着色类名 */
function fmtRateClass(v?: number): string {
  if (v == null) return '';
  if (v >= 0.99) return 'hp-sc-ok';
  if (v >= 0.95) return 'hp-sc-warn';
  return 'hp-sc-bad';
}

/** 格式化延迟毫秒 */
function fmtMs(v?: number): string {
  if (v == null || v === 0) return '—';
  if (v < 1) return '<1ms';
  if (v < 100) return `${v.toFixed(1)}ms`;
  return `${Math.round(v)}ms`;
}
