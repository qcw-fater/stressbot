import type { FlowNode } from '@/types/flow';
import type { ListenTemplateDefaultRef } from './templateStore';
import { routeKey } from '../listens/refsGraph';

export interface InferListenDefaultRefResult {
  defaultRef?: ListenTemplateDefaultRef;
  ambiguous: boolean;
}

export function cloneListenDefaultRef(ref?: ListenTemplateDefaultRef): ListenTemplateDefaultRef | undefined {
  if (!ref) return undefined;
  const cloned: ListenTemplateDefaultRef = {
    server: ref.server,
    route: cloneJsonValue(ref.route),
  };
  if (typeof ref.queueSize === 'number') cloned.queueSize = ref.queueSize;
  return cloned;
}

export function inferListenDefaultRef(
  nodes: Record<string, FlowNode>,
  listenName: string,
): InferListenDefaultRefResult {
  const refs: ListenTemplateDefaultRef[] = [];
  for (const node of Object.values(nodes)) {
    if (node.type !== 'action' || !node.listenRefs?.length) continue;
    for (const ref of node.listenRefs) {
      if (ref.listen !== listenName) continue;
      const server = ref.server?.trim();
      if (!server) continue;
      const defaultRef: ListenTemplateDefaultRef = {
        server,
        route: cloneJsonValue(ref.route),
      };
      if (typeof ref.queueSize === 'number') defaultRef.queueSize = ref.queueSize;
      refs.push(defaultRef);
    }
  }

  if (refs.length === 0) return { ambiguous: false };
  const first = refs[0];
  const firstKey = defaultRefKey(first);
  const ambiguous = refs.some((ref) => defaultRefKey(ref) !== firstKey);
  return { defaultRef: cloneListenDefaultRef(first), ambiguous };
}

export function defaultRefSummary(ref?: ListenTemplateDefaultRef): string | undefined {
  if (!ref) return undefined;
  return `${ref.server} · ${routeKey(ref.route)}`;
}

function defaultRefKey(ref: ListenTemplateDefaultRef): string {
  return `${ref.server}|${routeKey(ref.route)}|${ref.queueSize ?? 1}`;
}

function cloneJsonValue(value: unknown): unknown {
  if (value === undefined) return undefined;
  return JSON.parse(JSON.stringify(value)) as unknown;
}
