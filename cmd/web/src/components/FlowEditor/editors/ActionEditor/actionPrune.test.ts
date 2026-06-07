import { describe, expect, it } from 'vitest';
import { pruneActionByPattern } from './actionPrune';
import type { ActionDef } from '@/types/action';

describe('pruneActionByPattern', () => {
  it('切换到 tcpConnect 时只保留连接字段', () => {
    const action: ActionDef = {
      pattern: 'tcpConnect',
      service: 'logic',
      address: '127.0.0.1:9000',
      route: { cmd: 1 },
      c2sProto: 'Game.TestC2S',
      s2cProto: 'Game.TestS2C',
      bindings: [{ type: 'fixed', field: 'id', value: 1 }],
      store: [{ field: 'id', setter: 'id' }],
      timeout: 10,
      pollMs: 100,
    };

    expect(pruneActionByPattern(action)).toEqual({
      pattern: 'tcpConnect',
      service: 'logic',
      address: '127.0.0.1:9000',
    });
  });

  it('tcpRequest 保留请求响应字段', () => {
    const action: ActionDef = {
      pattern: 'tcpRequest',
      service: 'logic',
      route: { cmd: 1 },
      c2sProto: 'Game.TestC2S',
      s2cProto: 'Game.TestS2C',
      bindings: [{ type: 'fixed', field: 'id', value: 1 }],
      store: [{ field: 'id', setter: 'id' }],
      timeout: 10,
      address: '127.0.0.1:9000',
      script: 'test.lua',
      keys: ['unused'],
    };

    expect(pruneActionByPattern(action)).toEqual({
      pattern: 'tcpRequest',
      service: 'logic',
      route: { cmd: 1 },
      c2sProto: 'Game.TestC2S',
      s2cProto: 'Game.TestS2C',
      bindings: [{ type: 'fixed', field: 'id', value: 1 }],
      store: [{ field: 'id', setter: 'id' }],
      timeout: 10,
    });
  });

  it('lua 只保留脚本字段', () => {
    const action: ActionDef = {
      pattern: 'lua',
      script: 'scripts/test.lua',
      service: 'logic',
      bindings: [{ type: 'fixed', field: 'id', value: 1 }],
    };

    expect(pruneActionByPattern(action)).toEqual({
      pattern: 'lua',
      script: 'scripts/test.lua',
    });
  });
});
