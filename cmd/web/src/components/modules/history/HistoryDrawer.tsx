/**
 * 历史压测记录 Drawer：list / detail / compare 三种视图。
 *
 * - 进入：list 视图（搜索/筛选/分页），点击行进 detail；
 * - 多选 2~5 行后点"对比"进 compare；
 * - detail 视图可：编辑备注/标签/收藏，下载完整配置归档，删除（强制），克隆为新任务。
 */

import { App, Button, Drawer, Empty, Input, Space, Spin, Switch, Table, Tag, Tooltip } from 'antd';
import {
  ArrowLeftOutlined,
  DeleteOutlined,
  StarFilled,
  StarOutlined,
  SwapOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';
import dayjs from 'dayjs';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { historyApi, showApiError } from '@/services';
import type { HistoryRecord } from '@/types/api';
import { HistoryDetailView } from './HistoryDetailView';
import { HistoryCompareView } from './HistoryCompareView';

export interface HistoryDrawerProps {
  open: boolean;
  onClose: () => void;
}

type View = { kind: 'list' } | { kind: 'detail'; id: string } | { kind: 'compare'; ids: string[] };

export function HistoryDrawer({ open, onClose }: HistoryDrawerProps) {
  const { message, modal } = App.useApp();
  const [view, setView] = useState<View>({ kind: 'list' });
  const [items, setItems] = useState<HistoryRecord[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [search, setSearch] = useState('');
  const [starredOnly, setStarredOnly] = useState(false);
  const [selectedIds, setSelectedIds] = useState<string[]>([]);

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
    } catch (err) {
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
    async (record: HistoryRecord) => {
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
    (record: HistoryRecord) => {
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

  const columns: ColumnsType<HistoryRecord> = useMemo(
    () => [
      {
        title: '',
        dataIndex: 'starred',
        key: 'starred',
        width: 32,
        render: (_: boolean, r) => (
          <Button
            type="text"
            size="small"
            icon={r.starred ? <StarFilled style={{ color: '#faad14' }} /> : <StarOutlined />}
            onClick={(e) => {
              e.stopPropagation();
              onToggleStar(r);
            }}
          />
        ),
      },
      {
        title: '任务',
        dataIndex: 'name',
        key: 'name',
        render: (v: string, r) => (
          <div>
            <div style={{ fontWeight: 500 }}>{v}</div>
            <div style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>
              <code>{r.id.slice(0, 8)}</code> · {r.totalBots} bots × {r.agentCount} agents
            </div>
          </div>
        ),
      },
      {
        title: '状态',
        dataIndex: 'state',
        key: 'state',
        width: 80,
        render: (v: string, r) =>
          v === 'failed' ? (
            <Tooltip title={r.errorMsg}>
              <Tag color="error">失败</Tag>
            </Tooltip>
          ) : (
            <Tag color="success">完成</Tag>
          ),
      },
      {
        title: '开始',
        dataIndex: 'startedAt',
        key: 'startedAt',
        width: 160,
        render: (v: string | undefined) => (v ? dayjs(v).format('MM-DD HH:mm:ss') : '—'),
      },
      {
        title: '时长',
        dataIndex: 'durationSec',
        key: 'durationSec',
        width: 80,
        render: (v: number) => formatDuration(v),
      },
      {
        title: '标签',
        dataIndex: 'tags',
        key: 'tags',
        render: (tags: string[]) => (
          <Space size={4} wrap>
            {tags.map((t) => (
              <Tag key={t}>{t}</Tag>
            ))}
            {tags.length === 0 && <span style={{ color: 'var(--text-tertiary)' }}>—</span>}
          </Space>
        ),
      },
      {
        title: '',
        key: 'actions',
        width: 60,
        render: (_, r) => (
          <Button
            type="text"
            size="small"
            danger
            icon={<DeleteOutlined />}
            onClick={(e) => {
              e.stopPropagation();
              onDelete(r);
            }}
          />
        ),
      },
    ],
    [onToggleStar, onDelete],
  );

  return (
    <Drawer
      title={
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          {view.kind !== 'list' && (
            <Button
              size="small"
              icon={<ArrowLeftOutlined />}
              onClick={() => setView({ kind: 'list' })}
            >
              返回列表
            </Button>
          )}
          <span>
            历史压测记录
            {view.kind === 'list' && total > 0 && (
              <span style={{ fontSize: 12, color: 'var(--text-tertiary)', marginLeft: 8 }}>
                共 {total} 条
              </span>
            )}
          </span>
        </div>
      }
      open={open}
      onClose={onClose}
      width={view.kind === 'compare' ? 1100 : 920}
      destroyOnHidden
    >
      {view.kind === 'list' && (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Space>
            <Input.Search
              placeholder="按名称 / 标签 / 备注搜索"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onSearch={refresh}
              allowClear
              style={{ width: 320 }}
            />
            <Space>
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
          </Space>
          {loading && items.length === 0 ? (
            <Spin />
          ) : items.length === 0 ? (
            <Empty description="暂无历史记录" />
          ) : (
            <Table<HistoryRecord>
              rowKey="id"
              size="small"
              dataSource={items}
              columns={columns}
              pagination={{ pageSize: 50, showSizeChanger: false }}
              rowSelection={{
                selectedRowKeys: selectedIds,
                onChange: (keys) => setSelectedIds(keys.map(String)),
                getCheckboxProps: (r) => ({
                  disabled: !selectedIds.includes(r.id) && selectedIds.length >= 5,
                }),
              }}
              onRow={(r) => ({
                onClick: () => setView({ kind: 'detail', id: r.id }),
                style: { cursor: 'pointer' },
              })}
            />
          )}
        </Space>
      )}
      {view.kind === 'detail' && (
        <HistoryDetailView id={view.id} onChange={refresh} />
      )}
      {view.kind === 'compare' && <HistoryCompareView ids={view.ids} />}
    </Drawer>
  );
}

function formatDuration(sec: number): string {
  if (sec < 60) return `${sec}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m${sec % 60}s`;
  return `${(sec / 3600).toFixed(1)}h`;
}
