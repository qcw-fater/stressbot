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
  StarFilled,
  StarOutlined,
  SwapOutlined,
} from '@ant-design/icons';
import { useCallback, useEffect, useMemo, useState } from 'react';
import dayjs from 'dayjs';
import { ApiError, historyApi, showApiError } from '@/services';
import { useEditorStore } from '@/components/FlowEditor/store/editorStore';
import { FloatingWindow } from '@/components/FlowEditor/panels/FloatingWindow';
import type { HistoryRecord } from '@/types/api';
import { HistoryDetailView } from './HistoryDetailView';
import { HistoryCompareView } from './HistoryCompareView';
import './HistoryPanel.css';

export interface HistoryModalProps {
  open: boolean;
  onClose: () => void;
}

type View = { kind: 'list' } | { kind: 'detail'; id: string } | { kind: 'compare'; ids: string[] };

export function HistoryModal({ open, onClose }: HistoryModalProps) {
  const { message, modal } = App.useApp();
  const [view, setView] = useState<View>({ kind: 'list' });
  const [items, setItems] = useState<HistoryRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const [starredOnly, setStarredOnly] = useState(false);
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [page, setPage] = useState(1);
  const pageSize = 20;

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const resp = await historyApi.listHistory({
        search: search || undefined,
        starred: starredOnly ? true : undefined,
        limit: 100,
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

  const onDelete = useCallback(
    (record: HistoryRecord, e?: React.MouseEvent) => {
      e?.stopPropagation();
      modal.confirm({
        title: '确认删除？',
        content: record.starred ? '该记录已收藏，需要强制删除' : `将删除 ${record.name} 的所有数据`,
        okText: record.starred ? '强制删除' : '删除',
        okButtonProps: { danger: true },
        onOk: async () => {
          try {
            await historyApi.deleteHistory(record.id, record.starred);
            message.success('已删除');
            refresh();
          } catch (err) {
            showApiError(err);
          }
        },
      });
    },
    [modal, message, refresh],
  );

  const toggleSelect = (id: string, e?: React.MouseEvent) => {
    e?.stopPropagation();
    setSelectedIds((prev) => {
      if (prev.includes(id)) return prev.filter((x) => x !== id);
      if (prev.length >= 5) return prev;
      return [...prev, id];
    });
  };

  const selectedRecords = useMemo(
    () => items.filter((r) => selectedIds.includes(r.id)),
    [items, selectedIds],
  );

  const onBatchDelete = useCallback(() => {
    const starredCount = selectedRecords.filter((r) => r.starred).length;
    modal.confirm({
      title: `确认批量删除 ${selectedIds.length} 条记录？`,
      content: starredCount > 0 ? `其中 ${starredCount} 条已收藏，将强制删除` : '此操作不可恢复',
      okText: '删除',
      okButtonProps: { danger: true },
      onOk: async () => {
        try {
          await Promise.all(selectedRecords.map((r) => historyApi.deleteHistory(r.id, r.starred)));
          message.success(`已删除 ${selectedIds.length} 条记录`);
          setSelectedIds([]);
          refresh();
        } catch (err) {
          showApiError(err);
          refresh();
        }
      },
    });
  }, [modal, message, refresh, selectedRecords, selectedIds]);

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

  return (
    <FloatingWindow
      windowId="history"
      title={titleContent}
      defaultSize={{ width: 960, height: 680 }}
      minSize={{ width: 520, height: 380 }}
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
              placeholder="名称、标签或备注"
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
                disabled={selectedIds.length < 2 || selectedIds.length > 5}
                onClick={() => setView({ kind: 'compare', ids: selectedIds })}
              >
                对比 {selectedIds.length > 0 ? `(${selectedIds.length})` : ''}
              </Button>
              <Button
                danger
                icon={<DeleteOutlined />}
                disabled={selectedIds.length === 0}
                onClick={onBatchDelete}
              >
                删除 {selectedIds.length > 0 ? `(${selectedIds.length})` : ''}
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
                {paged.map((r) => (
                  <HistoryCard
                    key={r.id}
                    record={r}
                    selected={selectedIds.includes(r.id)}
                    onSelect={toggleSelect}
                    onStar={onToggleStar}
                    onDelete={onDelete}
                    onClick={() => setView({ kind: 'detail', id: r.id })}
                  />
                ))}
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
          <HistoryDetailView id={view.id} onChange={refresh} />
        </div>
      )}
      {view.kind === 'compare' && (
        <div className="hp-shell hp-shell--compare">
          <HistoryCompareView ids={view.ids} />
        </div>
      )}
    </FloatingWindow>
  );
}

/* ── 列表卡片 ── */

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
  onSelect: (id: string, e?: React.MouseEvent) => void;
  onStar: (r: HistoryRecord, e?: React.MouseEvent) => void;
  onDelete: (r: HistoryRecord, e?: React.MouseEvent) => void;
  onClick: () => void;
}) {
  const tags = r.tags ?? [];
  const failed = r.state === 'failed';

  return (
    <div
      className={`hp-card hp-card--list${selected ? ' hp-card--selected' : ''}${failed ? ' hp-card--failed' : ''}`}
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
      <Checkbox
        className="hp-card__checkbox"
        checked={selected}
        onClick={(e) => onSelect(r.id, e as unknown as React.MouseEvent)}
      />

      <div className="hp-card__main">
        <div className="hp-card__row1">
          <div className="hp-card__title-block">
            <div className="hp-card__name" title={r.name}>
              {r.name}
            </div>
            <span className={`hp-card__status hp-card__status--${failed ? 'bad' : 'ok'}`}>
              <span className="hp-status-dot" />
              {failed ? '失败' : '完成'}
            </span>
          </div>
          <div className="hp-card__actions">
            <Tooltip title={r.starred ? '取消收藏' : '收藏'}>
              <Button
                type="text"
                size="small"
                className="hp-card__icon-btn"
                icon={r.starred ? <StarFilled style={{ color: 'var(--color-warning)' }} /> : <StarOutlined />}
                onClick={(e) => onStar(r, e)}
              />
            </Tooltip>
            <Tooltip title="删除">
              <Button
                type="text"
                size="small"
                className="hp-card__icon-btn"
                danger
                icon={<DeleteOutlined />}
                onClick={(e) => onDelete(r, e)}
              />
            </Tooltip>
          </div>
        </div>

        <div className="hp-card__metrics" aria-hidden>
          <span className="hp-metric" title="记录 ID">
            <span className="hp-metric__k">ID</span>
            <code className="hp-metric__v">{r.id.slice(0, 8)}</code>
          </span>
          <span className="hp-metric">
            <span className="hp-metric__k">时长</span>
            <span className="hp-metric__v">{formatDuration(r.durationSec)}</span>
          </span>
          <span className="hp-metric">
            <span className="hp-metric__k">机器人</span>
            <span className="hp-metric__v">{r.totalBots}</span>
          </span>
          <span className="hp-metric">
            <span className="hp-metric__k">节点</span>
            <span className="hp-metric__v">{r.agentCount}</span>
          </span>
          <span className="hp-metric hp-metric--wide">
            <span className="hp-metric__k">开始</span>
            <span className="hp-metric__v">{r.startedAt ? dayjs(r.startedAt).format('MM-DD HH:mm') : '—'}</span>
          </span>
        </div>

        {tags.length > 0 && (
          <div className="hp-card__tags">
            {tags.map((t) => (
              <span key={t} className="hp-tag-pill">
                {t}
              </span>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`;
  return `${(sec / 3600).toFixed(1)}h`;
}
