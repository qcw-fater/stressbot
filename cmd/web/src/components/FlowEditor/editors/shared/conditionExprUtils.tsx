/**
 * 条件表达式高亮渲染工具。
 *
 * 从表达式中提取标识符词元（含点分路径和数组下标），
 * 与已知 state key 匹配后渲染为彩色标签。
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
  builtin: 'cyan',
};

/**
 * 匹配点分路径标识符：支持 a.b.c、items[0].name 等形式。
 * 首段为常规标识符，后续可跟 .xxx 或 [N] 的组合。
 */
const PATH_RE = /[a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*|\[\d+\])*/g;

/**
 * 将条件表达式渲染为混合 ReactNode：
 * 已知 state key（含子字段路径）→ 彩色 Tag；其余字符 → 纯文本 span。
 */
export function renderExprWithHighlights(
  expr: string,
  knownKeys: StateKeyInfo[],
): ReactNode[] {
  if (!expr) return [];

  // 构建 key 集合：顶层 key + 可能的子字段路径前缀
  const keyMap = new Map(knownKeys.map((k) => [k.key, k]));
  const nodes: ReactNode[] = [];
  let lastIndex = 0;

  for (const m of expr.matchAll(PATH_RE)) {
    const start = m.index!;
    const end = start + m[0].length;

    // 匹配前的非标识符文本
    if (start > lastIndex) {
      nodes.push(expr.slice(lastIndex, start));
    }

    // 尝试最长匹配：先看完整路径是否是已知 key
    // 如果不是，尝试逐步缩短看是否匹配顶层 key
    let matched = false;
    let path = m[0];

    const info = keyMap.get(path);
    if (info) {
      nodes.push(
        <Tag
          key={start}
          color={SOURCE_TYPE_COLOR[info.sourceType] ?? 'default'}
          style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', margin: 0 }}
        >
          {path}
        </Tag>,
      );
      matched = true;
    }

    // 如果完整路径不匹配，检查是否以某个已知 key 开头（说明是子字段路径）
    if (!matched) {
      // 检查首段是否是已知 key（说明是嵌套路径）
      const dotIdx = path.indexOf('.');
      const bracketIdx = path.indexOf('[');
      let firstSegEnd = path.length;
      if (dotIdx > 0) firstSegEnd = Math.min(firstSegEnd, dotIdx);
      if (bracketIdx > 0) firstSegEnd = Math.min(firstSegEnd, bracketIdx);

      const firstSeg = path.slice(0, firstSegEnd);
      const rootInfo = keyMap.get(firstSeg);
      if (rootInfo) {
        // 首段是已知 key，整条路径高亮为嵌套引用
        nodes.push(
          <Tag
            key={start}
            color={SOURCE_TYPE_COLOR[rootInfo.sourceType] ?? 'default'}
            style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', margin: 0, opacity: 0.85 }}
          >
            {path}
          </Tag>,
        );
        matched = true;
      }
    }

    if (!matched) {
      // nil 关键字高亮
      if (path === 'nil') {
        nodes.push(
          <Tag
            key={start}
            color="red"
            style={{ fontSize: 10, lineHeight: '16px', padding: '0 4px', margin: 0 }}
          >
            nil
          </Tag>,
        );
        matched = true;
      }
    }

    if (!matched) {
      nodes.push(m[0]);
    }

    lastIndex = end;
  }

  if (lastIndex < expr.length) {
    nodes.push(expr.slice(lastIndex));
  }

  return nodes;
}
