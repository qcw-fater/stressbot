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
  | 'switch'
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

  // switch 专用
  cases?: SwitchCase[];
  defaultNext?: string;

  // action 专用
  action?: string;
  onError?: OnErrorDef;
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

export type OnErrorStrategy = 'resume' | 'skip' | 'abort';

export interface OnErrorDef {
  ignoreCodes?: number[];
  handler?: string;
  retry?: RetryDef;
  strategy?: OnErrorStrategy;
}

export interface RetryDef {
  maxRetries?: number;
  retryDelayMs?: number;
}

export interface SwitchCase {
  condition: string;
  next: string;
}

/**
 * 监听引用。
 * - route 为不透明结构，字段由对应 codec 的 routeKeyTemplate 决定
 * - server 为 `<protocol>:<service>` 形式，对应协议配置里的连接
 * - listen 为 listens 表的 key；null = 静默丢弃（连 listen {} 都不调用）
 * - queueSize 监听缓存队列容量（镜像 Go ListenRef.QueueSize *int）：
 *   未写（undefined）→ 默认 1；显式 >0 → 按该值；显式 <=0 → 校验报错（不静默 clamp）。
 */
export interface ListenRef {
  route: unknown;
  server: string;
  listen: string | null;
  queueSize?: number;
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
