/**
 * 条件表达式高亮渲染工具。
 *
 * 从表达式中提取词元：state 路径（含点分路径与数组下标）、字符串/数字字面量。
 * 已知 state key 渲染为彩色标签，字面量渲染为值标签，用于 ConditionInput 下方预览。
 *
 * 严格类型文法下裸标识符恒为 state 路径、字符串必须带引号：此处把带引号字符串作为
 * 整体字面量高亮，避免其内容被误识别为 path。
 */

import { Tag } from 'antd';
import type { CSSProperties, ReactNode } from 'react';
import type { StateKeyInfo } from '../ActionEditor/stateRegistry';

const SOURCE_TYPE_COLOR: Record<string, string> = {
  store: 'blue',
  listenStore: 'orange',
  stateExtra: 'volcano',
  storeAs: 'green',
  lua: 'purple',
  builtin: 'cyan',
};

const tagStyle: CSSProperties = {
  fontSize: 10,
  lineHeight: '16px',
  padding: '0 4px',
  margin: 0,
};

/**
 * 词元正则：依次匹配 带引号字符串 | 数字 | state 路径。
 * 字符串分支在最前，保证引号内内容不会被 path 分支抢先匹配。
 */
const TOKEN_RE = /"(?:[^"]*)"?|\d+(?:\.\d+)?|[a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*|\[\d+\])*/g;

/**
 * 将条件表达式渲染为混合 ReactNode：
 * 已知 state key（含子字段路径）→ 彩色 Tag；字符串/数字字面量 → 值 Tag；其余字符 → 纯文本。
 */
export function renderExprWithHighlights(
  expr: string,
  knownKeys: StateKeyInfo[],
): ReactNode[] {
  if (!expr) return [];

  const keyMap = new Map(knownKeys.map((k) => [k.key, k]));
  const nodes: ReactNode[] = [];
  let lastIndex = 0;

  for (const m of expr.matchAll(TOKEN_RE)) {
    const text = m[0];
    const start = m.index!;
    const end = start + text.length;

    // 匹配前的非词元文本（运算符、括号、空白）
    if (start > lastIndex) {
      nodes.push(expr.slice(lastIndex, start));
    }

    // ① 带引号字符串字面量 → 整体作为值标签（内容不当 path）
    if (text.startsWith('"')) {
      nodes.push(
        <Tag key={start} color="green" style={tagStyle}>
          {text}
        </Tag>,
      );
      lastIndex = end;
      continue;
    }

    // ② 数字字面量 → 值标签
    if (/^\d/.test(text)) {
      nodes.push(
        <Tag key={start} color="default" style={tagStyle}>
          {text}
        </Tag>,
      );
      lastIndex = end;
      continue;
    }

    // ③ state 路径：先看完整路径是否是已知 key，否则看首段（嵌套路径）
    let matched = false;
    const info = keyMap.get(text);
    if (info) {
      nodes.push(
        <Tag
          key={start}
          color={SOURCE_TYPE_COLOR[info.sourceType] ?? 'default'}
          style={tagStyle}
        >
          {text}
        </Tag>,
      );
      matched = true;
    }

    if (!matched) {
      const dotIdx = text.indexOf('.');
      const bracketIdx = text.indexOf('[');
      let firstSegEnd = text.length;
      if (dotIdx > 0) firstSegEnd = Math.min(firstSegEnd, dotIdx);
      if (bracketIdx > 0) firstSegEnd = Math.min(firstSegEnd, bracketIdx);

      const firstSeg = text.slice(0, firstSegEnd);
      const rootInfo = keyMap.get(firstSeg);
      if (rootInfo) {
        nodes.push(
          <Tag
            key={start}
            color={SOURCE_TYPE_COLOR[rootInfo.sourceType] ?? 'default'}
            style={{ ...tagStyle, opacity: 0.85 }}
          >
            {text}
          </Tag>,
        );
        matched = true;
      }
    }

    if (!matched) {
      nodes.push(text);
    }

    lastIndex = end;
  }

  if (lastIndex < expr.length) {
    nodes.push(expr.slice(lastIndex));
  }

  return nodes;
}
