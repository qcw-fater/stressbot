/**
 * 节点 ID 生成与重命名工具。
 *
 * 规则：
 *   - 新建节点默认 ID = `${type}_${nanoid(6)}`
 *   - 用户手动改名时校验：必须非空、唯一、不含空白字符
 */

import { nanoid } from 'nanoid';
import type { NodeType } from '@/types/flow';

export function generateNodeId(type: NodeType, taken: Set<string>): string {
  for (let i = 0; i < 20; i++) {
    const id = `${type}_${nanoid(6)}`;
    if (!taken.has(id)) return id;
  }
  return `${type}_${Date.now().toString(36)}`;
}

export function isValidNodeId(id: string): boolean {
  if (!id) return false;
  if (/\s/.test(id)) return false;
  if (id.startsWith('__')) return false; // 保留内部前缀（如 __cb__ 用于 ListenCard）
  return true;
}
