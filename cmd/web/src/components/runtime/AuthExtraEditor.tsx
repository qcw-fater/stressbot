/**
 * AuthExtra 编辑器：可视化编辑 Record<string, string>。
 *
 * 用途：RobotConfig.authExtra 是 Auth 请求时附带的扩展字段（version/channel/platform 等），
 * 不同游戏字段集差别极大，因此默认不预填任何字段，由用户通过"添加字段"按钮自行配置；
 * suggestedKeys 仅供调用方按需注入推荐 tag（默认空数组）。
 *
 * 关键设计：rows（含空 key 草稿）由内部 useState 管理，外部 value 仅承载已成型的 map。
 * 否则：addRow('') → toMap 把空 key 过滤 → onChange 上的 map 没变 → 看起来"按钮没反应"。
 *
 * 双向同步：
 *   - 用户编辑 → setRows + onChange(toMap(rows))；
 *   - 外部 value 变化（如 EditorPage 启动时从 conf/config.json 异步同步）→ useEffect 比对，
 *     不一致时合并：用外部 map 作为"已确认行"，保留内部"未填 key 的草稿行"。
 *
 * 校验：
 *   - 重复的 key 红边提示但不阻断（提交前由 Modal 决定）；
 *   - 空 key 不导出到 onChange 的 map（行仍保留在 UI 中等待用户填写）。
 */

import { Button, Input, Space, Tag, Tooltip, Typography } from 'antd';
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState } from 'react';

export interface AuthExtraEditorProps {
  value?: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
  /**
   * 常用 key 提示，用户点击会插入空值行。
   * 默认空数组 = 不显示任何推荐；不同游戏的 Auth 字段千差万别，
   * 强行内置 version/channel/platform 反而误导用户。
   */
  suggestedKeys?: string[];
}

const DEFAULT_SUGGESTED: string[] = [];

interface Row {
  key: string;
  value: string;
}

function toRows(v: Record<string, string> | undefined): Row[] {
  if (!v) return [];
  return Object.entries(v).map(([key, value]) => ({ key, value }));
}

function toMap(rows: Row[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const { key, value } of rows) {
    if (key.trim() === '') continue;
    out[key.trim()] = value;
  }
  return out;
}

function shallowEqualMap(a: Record<string, string>, b: Record<string, string>): boolean {
  const ak = Object.keys(a);
  const bk = Object.keys(b);
  if (ak.length !== bk.length) return false;
  for (const k of ak) if (a[k] !== b[k]) return false;
  return true;
}

export function AuthExtraEditor({
  value,
  onChange,
  suggestedKeys = DEFAULT_SUGGESTED,
}: AuthExtraEditorProps) {
  const [rows, setRows] = useState<Row[]>(() => toRows(value));

  // 外部 value 变化时同步（外部主动 reset / 父组件刷新）。
  // 仅当 value（已成型 map）与当前 rows 的成型 map 不一致时才合并；
  // 这样用户中途键入草稿 key 不会因为 onChange 回调引发的 value 同步反复触发 reset。
  // 用 ref 记住最近一次"我们自己 emit 出去的 map"，跳过本次 echo。
  const lastEmittedRef = useRef<Record<string, string>>(toMap(rows));
  useEffect(() => {
    const ext = value ?? {};
    if (shallowEqualMap(ext, lastEmittedRef.current)) return; // 自身回声，忽略
    setRows((prev) => {
      const drafts = prev.filter((r) => r.key.trim() === '');
      const next = [...toRows(ext), ...drafts];
      lastEmittedRef.current = toMap(next);
      return next;
    });
  }, [value]);

  const commit = (next: Row[]) => {
    setRows(next);
    const map = toMap(next);
    lastEmittedRef.current = map;
    onChange(map);
  };

  const addRow = (key = '') => commit([...rows, { key, value: '' }]);
  const removeRow = (idx: number) => commit(rows.filter((_, i) => i !== idx));
  const setKey = (idx: number, k: string) => {
    const next = rows.slice();
    next[idx] = { ...next[idx], key: k };
    commit(next);
  };
  const setVal = (idx: number, v: string) => {
    const next = rows.slice();
    next[idx] = { ...next[idx], value: v };
    commit(next);
  };

  // 重复 key 标记
  const dupKeys = new Set<string>();
  {
    const seen = new Set<string>();
    for (const { key } of rows) {
      const k = key.trim();
      if (!k) continue;
      if (seen.has(k)) dupKeys.add(k);
      seen.add(k);
    }
  }

  // 推荐 key：未出现在当前行里的才显示
  const presentKeys = new Set(rows.map((r) => r.key.trim()).filter(Boolean));
  const remainingSuggestions = suggestedKeys.filter((k) => !presentKeys.has(k));

  return (
    <div>
      {rows.length === 0 && (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          尚未配置任何字段；lua 脚本里 robot.get(key) 将返回 nil。
        </Typography.Text>
      )}

      {rows.map((row, idx) => (
        <Space.Compact key={idx} block style={{ marginBottom: 6 }}>
          <Input
            placeholder="字段名（如 version）"
            value={row.key}
            onChange={(e) => setKey(idx, e.target.value)}
            status={dupKeys.has(row.key.trim()) ? 'error' : undefined}
            style={{ width: '38%' }}
          />
          <Input
            placeholder="值"
            value={row.value}
            onChange={(e) => setVal(idx, e.target.value)}
          />
          <Tooltip title="删除该字段">
            <Button icon={<DeleteOutlined />} danger onClick={() => removeRow(idx)} />
          </Tooltip>
        </Space.Compact>
      ))}

      <Space size={4} wrap style={{ marginTop: 4 }}>
        <Button size="small" icon={<PlusOutlined />} onClick={() => addRow()}>
          添加字段
        </Button>
        {remainingSuggestions.map((k) => (
          <Tag
            key={k}
            color="blue"
            style={{ cursor: 'pointer', userSelect: 'none' }}
            onClick={() => addRow(k)}
          >
            + {k}
          </Tag>
        ))}
      </Space>
    </div>
  );
}
