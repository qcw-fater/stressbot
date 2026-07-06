import { describe, expect, it } from 'vitest';
import { jsonToFlow } from './jsonToFlow';
import { flowToJson } from './flowToJson';
import type { FlowJson } from './flowToJson';

const baseFlow: FlowJson = {
  defaultDelayMs: 1000,
  nodes: {
    main: {
      type: 'switch',
      cases: [
        { condition: 'state:level >= 10', next: 'advanced', description: '高等级' },
        { condition: 'lua:has_guild.lua', next: 'guild' },
      ],
      defaultNext: 'normal',
    },
    advanced: { type: 'action', action: 'advanced' },
    guild: { type: 'action', action: 'guild' },
    normal: { type: 'action', action: 'normal' },
  },
  actions: {
    advanced: { pattern: 'clearState', keys: ['a'] },
    guild: { pattern: 'clearState', keys: ['g'] },
    normal: { pattern: 'clearState', keys: ['n'] },
  },
  listens: {},
};

describe('switch node codec', () => {
  it('emits case and default edges', () => {
    const { rfEdges } = jsonToFlow(baseFlow);
    expect(rfEdges.map((e) => ({ sourceHandle: e.sourceHandle, target: e.target, type: e.type, data: e.data }))).toEqual([
      { sourceHandle: 'case-0', target: 'advanced', type: 'branch', data: { branch: 'case', caseIndex: 0, sourceNodeType: 'switch' } },
      { sourceHandle: 'case-1', target: 'guild', type: 'branch', data: { branch: 'case', caseIndex: 1, sourceNodeType: 'switch' } },
      { sourceHandle: 'default', target: 'normal', type: 'branch', data: { branch: 'default', sourceNodeType: 'switch' } },
    ]);
  });

  it('exports switch fields without empty values', () => {
    const exported = flowToJson({
      defaultDelayMs: 1000,
      nodes: {
        main: {
          type: 'switch',
          cases: [
            { condition: 'state:level >= 10', next: 'advanced', description: '高等级' },
            { condition: '', next: '', description: '' },
            { condition: 'state:level >= 20', next: '', description: '' },
            { condition: '', next: 'advanced', description: '' },
            { condition: '', next: '', description: '仅说明' },
          ],
          defaultNext: '',
        },
        advanced: { type: 'action', action: 'advanced' },
      },
      actions: { advanced: { pattern: 'clearState', keys: ['a'] } },
      listens: {},
    });

    expect(exported.nodes.main).toEqual({
      type: 'switch',
      cases: [{ condition: 'state:level >= 10', next: 'advanced', description: '高等级' }],
    });
  });
});
