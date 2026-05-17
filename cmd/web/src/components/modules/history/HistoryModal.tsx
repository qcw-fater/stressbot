/**
 * 历史压测记录面板（FloatingWindow）：list / detail / compare 三种视图。
 *
 * - 进入：list 视图（搜索/筛选），点击卡片进 detail；
 * - 多选 2~5 张后点"对比"进 compare；
 * - detail 视图可：编辑备注/标签/收藏，下载完整配置归档，删除（强制），克隆为新任务。
 */

import { App, Button, Checkbox, Empty, Input, Pagination, Space, Spin, Switch, Tooltip } from 'antd';
import {
  ArrowLeftOutlined,
  DeleteOutlined,
  StarFilled,
  StarOutlined,
  SwapOutlined,
} from '@ant-design/icons';
import dayjs from 'dayjs';
import { useCallback, useEffect, useState } from 'react';
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

  const paged = items.slice((page - 1) * pageSize, page * pageSize);

  const isList = view.kind === 'list';

  const titleContent = (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
      {view.kind !== 'list' && (
        <Button size="small" icon={<ArrowLeftOutlined />} onClick={() => setView({ kind: 'list' })}>
          返回
        </Button>
      )}
      <span>历史记录</span>
    </div>
  );

  return (
    <FloatingWindow
      windowId="history"
      title={titleContent}
      defaultSize={{ width: 900, height: 640 }}
      minSize={{ width: 600, height: 400 }}
      open={open}
      onClose={onClose}
      extra={
        view.kind === 'list' && total > 0 ? (
          <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>
            共 {total} 条
          </span>
        ) : undefined
      }
    >
      {view.kind === 'list' && (
        <>
          <div className="hp-list-toolbar">
            <Input.Search
              placeholder="按名称 / 标签 / 备注搜索"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onSearch={refresh}
              allowClear
              style={{ width: 320 }}
            />
            <Space size={4}>
              <span style={{ fontSize: 12 }}>仅收藏</span>
              <Switch checked={starredOnly} onChange={setStarredOnly} size="small" />
            </Space>
            <Button
              icon={<SwapOutlined />}
              disabled={selectedIds.length < 2 || selectedIds.length > 5}
              onClick={() => setView({ kind: 'compare', ids: selectedIds })}
            >
              对比 ({selectedIds.length})
            </Button>
          </div>

          {loading && items.length === 0 ? (
            <Spin style={{ marginTop: 40 }} />
          ) : items.length === 0 ? (
            <Empty description="暂无历史记录" style={{ marginTop: 40 }} />
          ) : (
            <>
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
              {total > pageSize && (
                <div className="hp-list-pagination">
                  <Pagination
                    size="small"
                    current={page}
                    pageSize={pageSize}
                    total={items.length}
                    onChange={setPage}
                    showSizeChanger={false}
                  />
                </div>
              )}
            </>
          )}
        </>
      )}
      {view.kind === 'detail' && <HistoryDetailView id={view.id} onChange={refresh} />}
      {view.kind === 'compare' && <HistoryCompareView ids={view.ids} />}
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
      className={`hp-glass hp-card${selected ? ' hp-card--selected' : ''}`}
      onClick={onClick}
    >
      <Checkbox
        className="hp-card__checkbox"
        checked={selected}
        onClick={(e) => onSelect(r.id, e as unknown as React.MouseEvent)}
      />

      <div className="hp-card__row1">
        <div className="hp-card__name">{r.name}</div>
        <div className="hp-card__actions">
          <span
            className="hp-status-dot"
            style={{ background: failed ? 'var(--color-error)' : 'var(--color-success)' }}
          />
          <span style={{ fontSize: 12, color: failed ? 'var(--color-error)' : 'var(--color-success)', fontWeight: 500 }}>
            {failed ? '失败' : '完成'}
          </span>
          <Tooltip title={r.starred ? '取消收藏' : '收藏'}>
            <Button
              type="text"
              size="small"
              icon={r.starred ? <StarFilled style={{ color: 'var(--color-warning)' }} /> : <StarOutlined />}
              onClick={(e) => onStar(r, e)}
            />
          </Tooltip>
          <Tooltip title="删除">
            <Button
              type="text"
              size="small"
              danger
              icon={<DeleteOutlined />}
              onClick={(e) => onDelete(r, e)}
            />
          </Tooltip>
        </div>
      </div>

      <div className="hp-card__row2">
        <span><code>{r.id.slice(0, 8)}</code></span>
        <span className="hp-card__sep">·</span>
        <span>{formatDuration(r.durationSec)}</span>
        <span className="hp-card__sep">·</span>
        <span>{r.totalBots} 机器人</span>
        <span className="hp-card__sep">·</span>
        <span>{r.agentCount} 节点</span>
        <span className="hp-card__sep">·</span>
        <span>{r.startedAt ? dayjs(r.startedAt).format('MM-DD HH:mm') : '—'}</span>
      </div>

      {tags.length > 0 && (
        <div className="hp-card__tags">
          {tags.map((t) => (
            <span key={t} className="hp-chip">{t}</span>
          ))}
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
