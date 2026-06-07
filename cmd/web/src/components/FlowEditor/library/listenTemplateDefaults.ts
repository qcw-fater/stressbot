import type { FlowNode } from '@/types/flow';
import type { ListenTemplateDefaultRef } from './templateStore';
import { routeKey } from '../listens/refsGraph';

export interface InferListenDefaultRefResult {
  defaultRef?: ListenTemplateDefaultRef;
  ambiguous: boolean;
}

export function cloneListenDefaultRef(ref?: ListenTemplateDefaultRef): ListenTemplateDefaultRef | undefined {
  if (!ref) return undefined;
  return {
    server: ref.server,
    route: cloneJsonValue(ref.route),
  };
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
      refs.push({ server, route: cloneJsonValue(ref.route) });
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
  return `${ref.server}|${routeKey(ref.route)}`;
}

function cloneJsonValue(value: unknown): unknown {
  if (value === undefined) return undefined;
  return JSON.parse(JSON.stringify(value)) as unknown;
}
