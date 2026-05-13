/**
 * 条件表达式高亮渲染工具。
 *
 * 从表达式中提取标识符词元，与已知 state key 匹配后渲染为彩色标签。
 * 用于 ConditionInput 下方预览条的可视化。
 */

import { Tag } from 'antd';
import type { ReactNode } from 'react';
import type { StateKeyInfo } from '../ActionEditor/stateRegistry';

const SOURCE_TYPE_COLOR: Record<string, string> = {
  store: 'blue',
  listenStore: 'orange',
  stateExtra: 'volcano',
  storeAs: 'green',
  lua: 'purple',
};

const IDENT_RE = /[a-zA-Z_][a-zA-Z0-9_]*/g;

/**
 * 将条件表达式渲染为混合 ReactNode：
 * 已知 state key → 彩色 Tag；其余字符 → 纯文本 span。
 */
export function renderExprWithHighlights(
  expr: string,
  knownKeys: StateKeyInfo[],
): ReactNode[] {
  if (!expr) return [];

  const keyMap = new Map(knownKeys.map((k) => [k.key, k]));
  const nodes: ReactNode[] = [];
  let lastIndex = 0;

  for (const m of expr.matchAll(IDENT_RE)) {
    const start = m.index!;
    const end = start + m[0].length;

    // 匹配前的非标识符文本
    if (start > lastIndex) {
      nodes.push(expr.slice(lastIndex, start));
    }

    const info = keyMap.get(m[0]);
    if (info) {
      nodes.push(
        <Tag
          key={start}
          color={SOURCE_TYPE_COLOR[info.sourceType] ?? 'default'}
          style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', margin: 0 }}
        >
          {m[0]}
        </Tag>,
      );
    } else {
      nodes.push(m[0]);
    }

    lastIndex = end;
  }

  if (lastIndex < expr.length) {
    nodes.push(expr.slice(lastIndex));
  }

  return nodes;
}
