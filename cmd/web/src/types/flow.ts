/**
 * 流程节点类型定义。
 *
 * 严格镜像 Go 端 `engine/flow.go` 的 TaskFlow / Node / WeightedOption / ListenRef。
 * 任何字段调整必须同步两侧，避免序列化漂移。
 */

import type { ActionDef } from './action';
import type { ListenDef } from './listen';

export type NodeType =
  | 'sequence'
  | 'action'
  | 'loop'
  | 'boolean'
  | 'weighted'
  | 'wait'
  | 'break'
  | 'continue';

/** 严格按 Go 端字段顺序与命名（json tag）。可选字段为空字符串/0/[]/null 时不写入导出。 */
export interface FlowNode {
  type: NodeType;

  /** 节点说明（人类可读注释，UI 显示，不参与运行时逻辑）。
   *  为空时不导出到 flow.json；Go 端会忽略未知字段。 */
  description?: string;

  // sequence 专用
  next?: string[];

  // loop 专用
  body?: string;
  loopCount?: number;
  condition?: string;
  breakCondition?: string;

  // boolean 专用（condition 与 loop 共享）
  trueNext?: string;
  falseNext?: string;

  // action 专用
  action?: string;
  errorStrategy?: 'ignore' | 'skip' | 'abort';
  listenRefs?: ListenRef[];

  // weighted 专用
  options?: WeightedOption[];

  // wait 专用
  waitMs?: number;
  waitMin?: number;
  waitMax?: number;

  // 通用：action / boolean
  delayMs?: number;
}

export interface WeightedOption {
  node: string;
  weight: number;
}

/**
 * 监听引用。
 * - route 为不透明结构，最常见 `{cmd, act}`，但允许任意 JSON 形态
 * - server 形如 `tcp:logic` / `udp:battle`
 * - listen 为 listens 表的 key；null = 静默丢弃（连 listen {} 都不调用）
 */
export interface ListenRef {
  route: unknown;
  server: string;
  listen: string | null;
}

/** TaskFlow：编辑器主数据模型，1:1 对应 flow.json。 */
export interface TaskFlow {
  defaultDelayMs: number;
  nodes: Record<string, FlowNode>;
  actions: Record<string, ActionDef>;
  listens: Record<string, ListenDef>;
}

/** 创建空 TaskFlow（提供默认值） */
export function emptyTaskFlow(): TaskFlow {
  return {
    defaultDelayMs: 1000,
    nodes: {},
    actions: {},
    listens: {},
  };
}
