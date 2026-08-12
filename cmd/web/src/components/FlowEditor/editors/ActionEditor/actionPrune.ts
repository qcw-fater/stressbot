import type { ActionDef, ActionPattern } from '@/types/action';

type ActionKey = keyof ActionDef;

const PATTERN_FIELDS: Record<ActionPattern, ActionKey[]> = {
  tcpSend: ['pattern', 'service', 'route', 'c2sProto', 'bindings'],
  udpSend: ['pattern', 'service', 'route', 'c2sProto', 'bindings'],
  tcpRequest: ['pattern', 'service', 'route', 'c2sProto', 's2cProto', 'bindings', 'store', 'timeout'],
  udpRequest: ['pattern', 'service', 'route', 'c2sProto', 's2cProto', 'bindings', 'store', 'timeout'],
  tcpConnect: ['pattern', 'service', 'address'],
  udpConnect: ['pattern', 'service', 'address'],
  tcpClose: ['pattern', 'service'],
  udpClose: ['pattern', 'service'],
  tcpListen: ['pattern', 'service', 'route', 's2cProto', 'store', 'timeout'],
  udpListen: ['pattern', 'service', 'route', 's2cProto', 'store', 'timeout'],
  httpRequest: ['pattern', 'url', 'method', 'contentType', 'bindings', 'store'],
  setState: ['pattern', 'bindings'],
  clearState: ['pattern', 'keys'],
  lua: ['pattern', 'script'],
};

export function pruneActionByPattern(action: ActionDef): ActionDef {
  const allowed = PATTERN_FIELDS[action.pattern];
  if (!allowed) return { pattern: action.pattern };

  const out: Partial<ActionDef> = { pattern: action.pattern };
  for (const key of allowed) {
    const value = action[key];
    if (value !== undefined) {
      (out as Record<ActionKey, unknown>)[key] = value;
    }
  }
  return out as ActionDef;
}
